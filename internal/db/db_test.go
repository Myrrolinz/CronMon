package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/myrrolinz/cronmon/internal/db"
	_ "modernc.org/sqlite"
)

// openTestDB is a helper that opens an in-memory SQLite database for testing.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestOpen_AllTablesExist(t *testing.T) {
	database := openTestDB(t)

	wantTables := []string{
		"schema_migrations",
		"checks",
		"pings",
		"channels",
		"check_channels",
		"notifications",
	}
	for _, tbl := range wantTables {
		var name string
		err := database.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", tbl, err)
		}
	}
}

func TestOpen_AllIndexesExist(t *testing.T) {
	database := openTestDB(t)

	wantIndexes := []string{
		"idx_checks_status",
		"idx_pings_check_created",
		"idx_notifications_check",
	}
	for _, idx := range wantIndexes {
		var name string
		err := database.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&name)
		if err != nil {
			t.Errorf("index %q missing: %v", idx, err)
		}
	}
}

func TestOpen_ForeignKeysEnabled(t *testing.T) {
	database := openTestDB(t)

	var on int
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if on != 1 {
		t.Errorf("foreign_keys = %d, want 1", on)
	}
}

func TestOpen_WALModeSet(t *testing.T) {
	// WAL mode only applies to file-backed databases; in-memory always uses "memory".
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "wal.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	var mode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestOpen_MigrationIdempotent(t *testing.T) {
	database := openTestDB(t)

	// Calling Migrate again on the same already-migrated DB must be a no-op.
	if err := db.Migrate(database); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	// The version table must contain exactly one entry for the initial migration.
	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations`,
	).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_migrations rows = %d, want 1", count)
	}
}

func TestOpen_ForeignKeyConstraintEnforced(t *testing.T) {
	database := openTestDB(t)

	// Inserting a ping with a non-existent check_id must fail (FK violation).
	_, err := database.Exec(
		`INSERT INTO pings(check_id, type, created_at) VALUES(?, ?, ?)`,
		"no-such-uuid", "success", "2026-01-01T00:00:00Z",
	)
	if err == nil {
		t.Error("expected FK violation error inserting ping with unknown check_id, got nil")
	}
}

func TestOpen_CheckStatusConstraint(t *testing.T) {
	database := openTestDB(t)

	// Inserting a check with an invalid status must fail (CHECK constraint).
	_, err := database.Exec(
		`INSERT INTO checks(id, name, schedule, status, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		"uuid-1", "test", "* * * * *", "invalid_status",
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	)
	if err == nil {
		t.Error("expected CHECK constraint error for invalid status, got nil")
	}
}

func TestOpen_ValidCheckInsert(t *testing.T) {
	database := openTestDB(t)

	// A valid check insert must succeed.
	_, err := database.Exec(
		`INSERT INTO checks(id, name, schedule, status, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		"uuid-1", "Daily Backup", "0 2 * * *", "new",
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert valid check: %v", err)
	}
}

func TestOpen_NotificationsPreservedWhenChannelDeleted(t *testing.T) {
	database := openTestDB(t)

	// Insert a check.
	_, err := database.Exec(
		`INSERT INTO checks(id, name, schedule, status, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		"uuid-1", "test", "* * * * *", "new",
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert check: %v", err)
	}

	// Insert a channel.
	res, err := database.Exec(
		`INSERT INTO channels(type, name, config, created_at) VALUES(?, ?, ?, ?)`,
		"email", "Ops Team", `{"address":"ops@example.com"}`, "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	channelID, _ := res.LastInsertId()

	// Insert a notification referencing both.
	_, err = database.Exec(
		`INSERT INTO notifications(check_id, channel_id, type, sent_at) VALUES(?, ?, ?, ?)`,
		"uuid-1", channelID, "down", "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert notification: %v", err)
	}

	// Delete the channel.
	if _, err := database.Exec(`DELETE FROM channels WHERE id=?`, channelID); err != nil {
		t.Fatalf("delete channel: %v", err)
	}

	// Notification must still exist with channel_id = NULL.
	var nullChannelID sql.NullInt64
	if err := database.QueryRow(
		`SELECT channel_id FROM notifications WHERE check_id=?`, "uuid-1",
	).Scan(&nullChannelID); err != nil {
		t.Fatalf("query notification: %v", err)
	}
	if nullChannelID.Valid {
		t.Errorf("channel_id = %d, want NULL after channel deletion", nullChannelID.Int64)
	}
}

func TestOpen_PingsCascadeDeletedWithCheck(t *testing.T) {
	database := openTestDB(t)

	// Insert a check.
	_, err := database.Exec(
		`INSERT INTO checks(id, name, schedule, status, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		"uuid-2", "hourly", "0 * * * *", "new",
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert check: %v", err)
	}

	// Insert a ping.
	_, err = database.Exec(
		`INSERT INTO pings(check_id, type, created_at) VALUES(?, ?, ?)`,
		"uuid-2", "success", "2026-01-01T00:01:00Z",
	)
	if err != nil {
		t.Fatalf("insert ping: %v", err)
	}

	// Delete the check.
	if _, err := database.Exec(`DELETE FROM checks WHERE id=?`, "uuid-2"); err != nil {
		t.Fatalf("delete check: %v", err)
	}

	// Pings must be gone (ON DELETE CASCADE).
	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM pings WHERE check_id=?`, "uuid-2",
	).Scan(&count); err != nil {
		t.Fatalf("count pings: %v", err)
	}
	if count != 0 {
		t.Errorf("pings count = %d, want 0 after check deletion", count)
	}
}

func TestOpen_OpenError(t *testing.T) {
	// A path in a non-existent directory must fail for a file-backed DB.
	// (SQLite will create the file, but not the directory.)
	_, err := db.Open("/nonexistent/dir/cronmon.db")
	if err == nil {
		t.Error("expected error opening DB in non-existent directory, got nil")
	}
}

func TestOpen_MigrateError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "broken.db")

	// Pre-create a schema_migrations table with the wrong schema so that
	// db.Open's Migrate call cannot query the "version" column.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open raw: %v", err)
	}
	if _, err := rawDB.Exec(`CREATE TABLE schema_migrations (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create wrong schema_migrations: %v", err)
	}
	rawDB.Close()

	// db.Open should fail because Migrate cannot query the missing 'version' column.
	_, err = db.Open(dbPath)
	if err == nil {
		t.Error("expected error when schema_migrations has wrong schema, got nil")
	}
}
