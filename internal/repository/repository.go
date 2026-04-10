// Package repository provides SQLite-backed implementations of the domain
// repository interfaces used by CronMon.
package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// rowScanner is satisfied by both *sql.Row and *sql.Rows, allowing a single
// scan helper to work with both query variants.
type rowScanner interface {
	Scan(dest ...any) error
}

const timeLayout = time.RFC3339

// formatTime formats a time.Time as an RFC3339 UTC string for storage.
func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// formatTimePtr converts a *time.Time to a sql.NullString for nullable
// DATETIME columns. A nil pointer produces a NULL value.
func formatTimePtr(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(timeLayout), Valid: true}
}

// parseTime parses an RFC3339 datetime string as returned by SQLite.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parseTime %q: %w", s, err)
	}
	return t, nil
}

// parseTimePtr converts a sql.NullString from a nullable DATETIME column to
// a *time.Time. A NULL value produces a nil pointer.
func parseTimePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid {
		return nil, nil
	}
	t, err := time.Parse(timeLayout, ns.String)
	if err != nil {
		return nil, fmt.Errorf("parseTimePtr %q: %w", ns.String, err)
	}
	return &t, nil
}

// stringPtrToNull converts a *string to a sql.NullString for nullable TEXT
// columns (e.g. checks.slug). A nil pointer produces a NULL value.
func stringPtrToNull(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

// statusPtrToNull converts a *model.Status to a sql.NullString for the
// nullable pre_pause_status column. A nil pointer produces a NULL value.
func statusPtrToNull(s *model.Status) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*s), Valid: true}
}
