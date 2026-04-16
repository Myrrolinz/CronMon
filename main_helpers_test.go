package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/config"
	"github.com/myrrolinz/cronmon/internal/db"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// openTestDB opens an in-memory SQLite database and runs migrations.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			t.Fatalf("openTestDB cleanup: %v", closeErr)
		}
	})
	return sqlDB
}

// buildTestCfg loads a Config from the current environment (must call setEnv first).
func buildTestCfg(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("buildTestCfg: %v", err)
	}
	return cfg
}

// buildTestDeps wires all dependencies needed by buildMux from a test database.
func buildTestDeps(t *testing.T, sqlDB *sql.DB) muxDeps {
	t.Helper()

	cfg := buildTestCfg(t)

	checkRepo := repository.NewCheckRepository(sqlDB)
	pingRepo := repository.NewPingRepository(sqlDB)
	chanRepo := repository.NewChannelRepository(sqlDB)
	notifRepo := repository.NewNotificationRepository(sqlDB)

	sc := cache.New(checkRepo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("buildTestDeps: hydrate: %v", err)
	}

	alertCh := make(chan model.AlertEvent, 64)
	t.Cleanup(func() {
		// Drain so nothing blocks.
		for len(alertCh) > 0 {
			<-alertCh
		}
		close(alertCh)
	})

	return muxDeps{
		cfg:        cfg,
		stateCache: sc,
		alertCh:    alertCh,
		pingRepo:   pingRepo,
		chanRepo:   chanRepo,
		notifRepo:  notifRepo,
	}
}
