// Package schedule provides helpers for parsing and evaluating 5-field cron
// expressions. It is a thin, opinionated wrapper around robfig/cron/v3 that
// restricts the accepted syntax to standard 5-field expressions (no seconds,
// no @macros) and exposes only the three functions needed by CronMon.
package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// parser is a reusable cron parser restricted to 5-field expressions.
// robfig/cron defaults to 6-field (with seconds); using NewParser with
// Minute as the first field enforces the standard 5-field format and
// disables @-macro support when no DescriptorParsers are supplied.
var parser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Validate returns nil if expr is a valid 5-field cron expression, or a
// descriptive error otherwise.
//
// Valid examples: "* * * * *", "0 2 * * *", "*/15 6-20 * * 1-5"
// Invalid examples: "@hourly", "0 * * * * *" (6-field), ""
func Validate(expr string) error {
	if expr == "" {
		return fmt.Errorf("schedule: expression must not be empty")
	}
	// We restrict to 5-field only by counting space-separated tokens before
	// parsing, because robfig/cron with the Descriptor flag will silently
	// accept @-macros as valid single-token schedules.
	if err := rejectNonFiveField(expr); err != nil {
		return err
	}
	if _, err := parser.Parse(expr); err != nil {
		return fmt.Errorf("schedule: invalid cron expression %q: %w", expr, err)
	}
	return nil
}

// NextAfter returns the next scheduled time strictly after t for the given
// cron expression. Returns an error if expr is invalid.
func NextAfter(expr string, t time.Time) (time.Time, error) {
	if err := Validate(expr); err != nil {
		return time.Time{}, err
	}
	sched, err := parser.Parse(expr)
	if err != nil {
		// unreachable after Validate, but satisfies errcheck
		return time.Time{}, fmt.Errorf("schedule: parse %q: %w", expr, err)
	}
	return sched.Next(t), nil
}

// NextExpectedAt returns the next scheduled time after ref, plus grace minutes.
// It is the canonical way to compute the deadline stored in next_expected_at.
//
//	NextExpectedAt(expr, grace, ref) == NextAfter(expr, ref) + grace*time.Minute
func NextExpectedAt(expr string, grace int, ref time.Time) (time.Time, error) {
	next, err := NextAfter(expr, ref)
	if err != nil {
		return time.Time{}, err
	}
	return next.Add(time.Duration(grace) * time.Minute), nil
}

// rejectNonFiveField returns an error when expr is not a whitespace-separated
// sequence of exactly 5 tokens. This catches @-macros (single token) and
// 6-field expressions before they reach the parser.
func rejectNonFiveField(expr string) error {
	if fields := strings.Fields(expr); len(fields) != 5 {
		return fmt.Errorf("schedule: expression %q must have exactly 5 fields (got %d)", expr, len(fields))
	}
	return nil
}
