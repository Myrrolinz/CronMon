// Package notify defines the Notifier interface, its implementations, and the
// Worker that consumes AlertEvents and dispatches them to the correct backend.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// defaultSendTimeout is the per-send context deadline used in production.
const defaultSendTimeout = 15 * time.Second

// WorkerOption configures a Worker at construction time.
type WorkerOption func(*Worker)

// WithSendTimeout overrides the per-send deadline.
// Intended for tests that want deterministic timeout behaviour without
// waiting 15 seconds.
func WithSendTimeout(d time.Duration) WorkerOption {
	return func(w *Worker) { w.sendTimeout = d }
}

// Worker consumes AlertEvents from alertCh, dispatches each event to the
// Notifier registered for that channel type, and records the outcome in the
// notifications table.  One Worker must be created per process; it owns the
// notifications table writes (the Scheduler intentionally writes none).
//
// Lifecycle:
//
//	w.Start()         // launch background goroutine
//	close(alertCh)    // signal shutdown (called by graceful-shutdown logic)
//	w.Wait()          // block until all queued events are processed
type Worker struct {
	alertCh     <-chan model.AlertEvent
	notifiers   map[string]Notifier // keyed by model.Channel.Type
	notifRepo   repository.NotificationRepository
	sendTimeout time.Duration
	wg          sync.WaitGroup
	startOnce   sync.Once
}

// NewWorker constructs a Worker.  Call Start to begin processing.
// notifiers maps channel type strings (e.g. "email", "slack") to their
// Notifier implementation.
func NewWorker(
	alertCh <-chan model.AlertEvent,
	notifiers map[string]Notifier,
	notifRepo repository.NotificationRepository,
	opts ...WorkerOption,
) *Worker {
	w := &Worker{
		alertCh:     alertCh,
		notifiers:   notifiers,
		notifRepo:   notifRepo,
		sendTimeout: defaultSendTimeout,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start launches the background goroutine that ranges over alertCh.
// Safe to call multiple times; only the first call has any effect.
func (w *Worker) Start() {
	w.startOnce.Do(func() {
		w.wg.Add(1)
		go w.run()
	})
}

// Wait blocks until alertCh has been closed and all queued events have been
// processed.  Call this after closing alertCh during graceful shutdown.
func (w *Worker) Wait() {
	w.wg.Wait()
}

// run is the background goroutine body.  It drains alertCh until it is closed.
func (w *Worker) run() {
	defer w.wg.Done()
	for event := range w.alertCh {
		w.dispatch(event)
	}
}

// dispatch sends a single AlertEvent to the appropriate notifier and writes
// the outcome to the notifications table.  Errors from notifier.Send and
// repository writes are logged and captured in the notification record.
func (w *Worker) dispatch(event model.AlertEvent) {
	sendCtx, cancel := context.WithTimeout(context.Background(), w.sendTimeout)
	defer cancel()

	channelID := event.Channel.ID
	n := &model.Notification{
		CheckID:   event.Check.ID,
		ChannelID: &channelID,
		Type:      event.AlertType,
		SentAt:    time.Now().UTC(),
	}

	notifier, ok := w.notifiers[event.Channel.Type]
	if !ok {
		errMsg := fmt.Sprintf("no notifier registered for channel type %q", event.Channel.Type)
		n.Error = &errMsg
		slog.Warn("notifier worker: unknown channel type",
			"type", event.Channel.Type,
			"check_id", event.Check.ID,
		)
	} else {
		if err := notifier.Send(sendCtx, event); err != nil {
			errMsg := err.Error()
			n.Error = &errMsg
			slog.Error("notifier worker: send failed",
				"type", event.Channel.Type,
				"check_id", event.Check.ID,
				"alert_type", string(event.AlertType),
				"error", err,
			)
		} else {
			slog.Info("notifier worker: alert delivered",
				"type", event.Channel.Type,
				"check_id", event.Check.ID,
				"alert_type", string(event.AlertType),
			)
		}
	}

	const repoTimeout = 5 * time.Second
	repoCtx, repoCancel := context.WithTimeout(context.Background(), repoTimeout)
	defer repoCancel()

	if err := w.notifRepo.Create(repoCtx, n); err != nil {
		// Possible FK violation: the channel may have been deleted between
		// the scheduler enqueuing the event and this write.  The schema
		// explicitly supports NULL channel_id (ON DELETE SET NULL) to
		// preserve the audit record — retry with a nil ChannelID.
		n.ChannelID = nil
		retryCtx, retryCancel := context.WithTimeout(context.Background(), repoTimeout)
		defer retryCancel()
		if retryErr := w.notifRepo.Create(retryCtx, n); retryErr != nil {
			slog.Error("notifier worker: failed to record notification",
				"check_id", event.Check.ID,
				"error", retryErr,
			)
		}
	}
}
