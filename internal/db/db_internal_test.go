// Package db internal tests exercise unexported helpers directly.
package db

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestIsCommentOnly(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty string", "", true},
		{"single comment", "-- this is a comment", true},
		{"multiple comments", "-- line 1\n-- line 2", true},
		{"comment with surrounding whitespace", "  -- comment  \n  -- another  ", true},
		{"blank lines only", "   \n\n   \n", true},
		{"code only", "CREATE TABLE t (id INTEGER)", false},
		{"comment then code", "-- comment\nCREATE TABLE t (id INTEGER)", false},
		{"code then comment", "CREATE TABLE t (id INTEGER)\n-- comment", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isCommentOnly(tc.input)
			if got != tc.want {
				t.Errorf("isCommentOnly(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestExecStatements_InvalidSQL(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer database.Close() //nolint:errcheck

	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	err = execStatements(tx, "THIS IS NOT VALID SQL")
	if err == nil {
		t.Error("expected error for invalid SQL, got nil")
	}
}

func TestExecStatements_LongInvalidSQL(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer database.Close() //nolint:errcheck

	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Statement longer than 60 characters to exercise the preview truncation path.
	longInvalid := "INVALID_KEYWORD " + strings.Repeat("x", 60)
	err = execStatements(tx, longInvalid)
	if err == nil {
		t.Error("expected error for long invalid SQL, got nil")
	}
}

func TestExecStatements_CommentOnly(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer database.Close() //nolint:errcheck

	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// A content string of only comments must be a no-op (isCommentOnly fast-path).
	err = execStatements(tx, "-- comment one\n-- comment two")
	if err != nil {
		t.Errorf("execStatements with comment-only SQL: %v", err)
	}
}

func TestExecStatements_EmptyStatements(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer database.Close() //nolint:errcheck

	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Semicolons with only whitespace between them must be a no-op.
	err = execStatements(tx, "  ;  ;  ")
	if err != nil {
		t.Errorf("execStatements with empty statements: %v", err)
	}
}
