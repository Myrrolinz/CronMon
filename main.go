package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/config"
	"github.com/myrrolinz/cronmon/internal/db"
	"github.com/myrrolinz/cronmon/internal/handler"
	"github.com/myrrolinz/cronmon/internal/middleware"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/notify"
	"github.com/myrrolinz/cronmon/internal/repository"
	"github.com/myrrolinz/cronmon/internal/scheduler"
)

// version is set at build time via -X main.version=<tag>
var version = "dev"

func main() {
	_ = version

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}

	initLogger(cfg.LogLevel)
	slog.Info("starting CronMon", "version", version, "port", cfg.Port)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

// run opens the database, wires all subsystems, starts the HTTP server, and
// blocks until ctx is cancelled or the HTTP server fails to start.
// It is extracted from main so that integration tests can drive it directly.
func run(ctx context.Context, cfg config.Config) error {
	sqlDB, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			slog.Error("failed to close database", "err", closeErr)
		}
	}()

	// Repos
	checkRepo := repository.NewCheckRepository(sqlDB)
	pingRepo := repository.NewPingRepository(sqlDB)
	chanRepo := repository.NewChannelRepository(sqlDB)
	notifRepo := repository.NewNotificationRepository(sqlDB)

	// Cache: hydrate from DB on every startup so the scheduler and ping handler
	// have an up-to-date view from the first tick.
	stateCache := cache.New(checkRepo)
	if err := stateCache.Hydrate(context.Background()); err != nil {
		return fmt.Errorf("hydrate cache: %w", err)
	}

	// Notifiers
	alertCh := make(chan model.AlertEvent, 64)
	notifiers := buildNotifiers(cfg)
	worker := notify.NewWorker(alertCh, notifiers, notifRepo)
	worker.Start()

	// Scheduler
	interval := time.Duration(cfg.SchedulerInterval) * time.Second
	sched := scheduler.New(stateCache, chanRepo, pingRepo, alertCh, interval)
	sched.Start()

	// HTTP
	deps := muxDeps{
		cfg:        cfg,
		stateCache: stateCache,
		alertCh:    alertCh,
		pingRepo:   pingRepo,
		chanRepo:   chanRepo,
		notifRepo:  notifRepo,
	}
	mux := buildMux(deps)
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// serveErrCh captures a fatal ListenAndServe error (e.g. port already in
	// use).  Without this, a startup failure would only be logged inside the
	// goroutine while main blocked forever on <-ctx.Done().
	serveErrCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErrCh <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining…")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutCtx); err != nil {
			slog.Error("HTTP shutdown error", "err", err)
			// Shutdown timed out: in-flight handlers (e.g. ping) may still be
			// running and could send on alertCh.  Force-close all remaining
			// connections so those handlers return before we close the channel.
			if closeErr := srv.Close(); closeErr != nil {
				slog.Error("HTTP force-close error", "err", closeErr)
			}
		}

	case runErr = <-serveErrCh:
		slog.Error("HTTP server failed to start", "err", runErr)
	}

	sched.Stop()
	close(alertCh)
	worker.Wait()

	slog.Info("shutdown complete")
	return runErr
}

// muxDeps holds all dependencies required by buildMux, grouped for readability
// and to make the function signature easy to swap in tests.
type muxDeps struct {
	cfg        config.Config
	stateCache *cache.StateCache
	alertCh    chan model.AlertEvent
	pingRepo   repository.PingRepository
	chanRepo   repository.ChannelRepository
	notifRepo  repository.NotificationRepository
}

// buildMux constructs the application's ServeMux with all routes registered:
//
//   - GET  /ping/{uuid}            → no auth
//   - GET  /ping/{uuid}/start      → no auth
//   - GET  /ping/{uuid}/fail       → no auth
//   - All other routes             → BasicAuth protected
//   - POST mutation routes         → additionally wrapped with MethodOverride
//
// Request logging is applied to all routes.
func buildMux(deps muxDeps) http.Handler {
	cfg := deps.cfg

	pingH := handler.NewPingHandler(
		deps.stateCache,
		deps.pingRepo,
		deps.chanRepo,
		deps.alertCh,
		cfg.TrustedProxy,
	)

	checkH := handler.NewCheckHandler(deps.stateCache)

	chanH := handler.NewChannelHandler(deps.chanRepo, deps.stateCache)

	dashH, err := handler.NewDashboardHandler(
		deps.stateCache,
		deps.pingRepo,
		deps.chanRepo,
		deps.notifRepo,
		cfg.BaseURL,
	)
	if err != nil {
		// Template parse errors are programming mistakes, not runtime errors.
		log.Fatalf("buildMux: %v", err)
	}

	auth := middleware.BasicAuth(cfg.AdminUser, cfg.AdminPass)
	methodOverride := middleware.MethodOverride

	// Unprotected mux: ping endpoints only.
	pingMux := http.NewServeMux()
	pingMux.HandleFunc("GET /ping/{uuid}", pingH.HandleSuccess)
	pingMux.HandleFunc("GET /ping/{uuid}/start", pingH.HandleStart)
	pingMux.HandleFunc("GET /ping/{uuid}/fail", pingH.HandleFail)

	// Protected mux: all other routes, all wrapped in BasicAuth.
	// POST mutation routes also get MethodOverride so HTML forms can simulate DELETE.
	authMux := http.NewServeMux()

	// Dashboard / read routes
	authMux.HandleFunc("GET /", dashH.HandleIndex)
	authMux.HandleFunc("GET /checks", dashH.HandleCheckList)
	authMux.HandleFunc("GET /checks/{id}", dashH.HandleCheckDetail)
	authMux.HandleFunc("GET /channels", dashH.HandleChannelList)

	// Static assets (no additional auth beyond the parent wrapper).
	// Must use "GET /static/" (method-qualified) to avoid conflict with "GET /".
	authMux.Handle("GET /static/", dashH.StaticHandler())

	// Check mutations (POST only from HTML forms; MethodOverride enables DELETE-like ops)
	authMux.HandleFunc("POST /checks", checkH.HandleCreate)
	authMux.HandleFunc("POST /checks/{id}", checkH.HandleUpdate)
	authMux.HandleFunc("POST /checks/{id}/delete", checkH.HandleDelete)
	authMux.HandleFunc("POST /checks/{id}/pause", checkH.HandlePause)
	authMux.HandleFunc("POST /checks/{id}/channels", chanH.HandleAttachDetach)

	// Channel mutations
	authMux.HandleFunc("POST /channels", chanH.HandleCreate)
	authMux.HandleFunc("POST /channels/{id}/delete", chanH.HandleDelete)

	// Apply MethodOverride to the entire auth mux (ping routes are excluded by being
	// on a separate mux registered below), then wrap in BasicAuth.
	protectedHandler := auth(methodOverride(authMux))

	// Top-level mux: ping routes take priority; everything else goes to the
	// auth-protected mux.
	root := http.NewServeMux()
	root.Handle("/ping/", pingMux)
	root.Handle("/", protectedHandler)

	return middleware.RequestLogging(root)
}

// buildNotifiers constructs the map of Notifier implementations to register
// with the Worker. Slack and webhook notifiers are always included; an email
// notifier is added only when SMTP_HOST and SMTP_FROM are both configured.
func buildNotifiers(cfg config.Config) map[string]notify.Notifier {
	notifiers := make(map[string]notify.Notifier, 3)

	if cfg.SMTPHost != "" && cfg.SMTPFrom != "" {
		notifiers["email"] = notify.NewEmailNotifier(notify.EmailConfig{
			Host:    cfg.SMTPHost,
			Port:    cfg.SMTPPort,
			User:    cfg.SMTPUser,
			Pass:    cfg.SMTPPass,
			From:    cfg.SMTPFrom,
			TLS:     cfg.SMTPTLS,
			BaseURL: cfg.BaseURL,
		})
	}

	notifiers["slack"] = notify.NewSlackNotifier()
	notifiers["webhook"] = notify.NewWebhookNotifier()

	return notifiers
}

// initLogger configures the global slog logger based on the log level string.
// Unrecognised level strings default to Info.
func initLogger(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
}
