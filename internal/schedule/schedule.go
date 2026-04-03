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

// parser is a reusable cron parser restricted to exactly 5 fields
// (minute, hour, dom, month, dow). Omitting cron.Descriptor means the
// parser rejects @-macros and enforces the correct field count natively —
// no pre-validation needed.
var parser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// parseSchedule validates expr and returns the parsed cron.Schedule.
// It is the single parse path shared by Validate and NextAfter.
func parseSchedule(expr string) (cron.Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("schedule: expression must not be empty")
	}
	sched, err := parser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("schedule: invalid cron expression %q: %w", expr, err)
	}
	return sched, nil
}

// Validate returns nil if expr is a valid 5-field cron expression, or a
// descriptive error otherwise.
//
// Valid examples: "* * * * *", "0 2 * * *", "*/15 6-20 * * 1-5"
// Invalid examples: "@hourly", "0 * * * * *" (6-field), ""
func Validate(expr string) error {
	_, err := parseSchedule(expr)
	return err
}

// NextAfter returns the next scheduled time strictly after t for the given
// cron expression. Returns an error if expr is invalid.
func NextAfter(expr string, t time.Time) (time.Time, error) {
	sched, err := parseSchedule(expr)
	if err != nil {
		return time.Time{}, err
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
