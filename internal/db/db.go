// Package db provides SQLite database initialization and schema migration.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // register the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (or creates) a SQLite database at path, applies essential PRAGMAs,
// and runs any pending schema migrations. Returns a ready-to-use *sql.DB.
func Open(path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("db.Open: %w", err)
	}

	// SQLite is single-writer; limit to one open connection to prevent
	// "database is locked" errors under concurrent access.
	database.SetMaxOpenConns(1)
	database.SetConnMaxLifetime(0)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := database.Exec(pragma); err != nil {
			_ = database.Close()
			return nil, fmt.Errorf("db.Open %s: %w", pragma, err)
		}
	}

	if err := Migrate(database); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("db.Open migrate: %w", err)
	}

	return database, nil
}

// Migrate creates the schema_migrations tracking table if absent and runs all
// embedded *.sql files (in sorted order) that have not yet been applied.
// It is safe to call multiple times; already-applied migrations are skipped.
func Migrate(database *sql.DB) error {
	const createTracking = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT     PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
)`
	if _, err := database.Exec(createTracking); err != nil {
		return fmt.Errorf("db.Migrate create tracking table: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("db.Migrate read migrations dir: %w", err)
	}

	// Ensure deterministic application order even if ReadDir returns unsorted.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version := entry.Name()

		// Check whether this migration has already been applied.
		var existing string
		err := database.QueryRow(
			`SELECT version FROM schema_migrations WHERE version = ?`, version,
		).Scan(&existing)
		if err == nil {
			continue // already applied
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("db.Migrate check %s: %w", version, err)
		}

		content, err := fs.ReadFile(migrationsFS, "migrations/"+version)
		if err != nil {
			return fmt.Errorf("db.Migrate read %s: %w", version, err)
		}

		tx, err := database.Begin()
		if err != nil {
			return fmt.Errorf("db.Migrate begin tx for %s: %w", version, err)
		}

		if err := execStatements(tx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("db.Migrate exec %s: %w", version, err)
		}

		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version) VALUES(?)`, version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("db.Migrate record %s: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("db.Migrate commit %s: %w", version, err)
		}
	}

	return nil
}

// execStatements executes each semicolon-separated SQL statement from content
// within the given transaction. Blank chunks and pure-comment chunks are skipped.
func execStatements(tx *sql.Tx, content string) error {
	for _, stmt := range strings.Split(content, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || isCommentOnly(stmt) {
			continue
		}
		if _, err := tx.Exec(stmt); err != nil {
			preview := stmt
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}
			return fmt.Errorf("exec %q: %w", preview, err)
		}
	}
	return nil
}

// isCommentOnly reports whether every non-empty line in s begins with "--".
func isCommentOnly(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			return false
		}
	}
	return true
}
