// Package repository_test provides integration tests for the repository layer.
// All tests run against an in-memory SQLite database opened via db.Open so that
// PRAGMA foreign_keys=ON is active and migrations are applied.
package repository_test

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/db"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// testRepos groups all four repositories sharing a single in-memory database.
type testRepos struct {
	db          *sql.DB
	checkRepo   repository.CheckRepository
	pingRepo    repository.PingRepository
	channelRepo repository.ChannelRepository
	notifRepo   repository.NotificationRepository
}

// newTestRepos opens an in-memory SQLite database (with migrations applied) and
// constructs repository instances backed by it. The database is closed via
// t.Cleanup when the test finishes.
func newTestRepos(t *testing.T) testRepos {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() }) //nolint:errcheck
	return testRepos{
		db:          database,
		checkRepo:   repository.NewCheckRepository(database),
		pingRepo:    repository.NewPingRepository(database),
		channelRepo: repository.NewChannelRepository(database),
		notifRepo:   repository.NewNotificationRepository(database),
	}
}

// truncSec truncates a time to second precision, matching SQLite's RFC3339
// storage which discards sub-second components.
func truncSec(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// mustMarshal marshals v to JSON for use as a Channel.Config.
func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// makeCheck returns a Check with sensible defaults. The caller may override
// individual fields after the call.
func makeCheck(id, name string) model.Check {
	now := truncSec(time.Now())
	return model.Check{
		ID:        id,
		Name:      name,
		Schedule:  "0 2 * * *",
		Grace:     10,
		Status:    model.StatusNew,
		CreatedAt: now,
		UpdatedAt: now,
		Tags:      "",
	}
}

// makeChannel returns a Channel with sensible defaults.
func makeChannel(t *testing.T, channelType, name string) model.Channel {
	t.Helper()
	return model.Channel{
		Type:      channelType,
		Name:      name,
		Config:    mustMarshal(t, map[string]string{"address": "ops@example.com"}),
		CreatedAt: truncSec(time.Now()),
	}
}
