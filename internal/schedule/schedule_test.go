package schedule_test

import (
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/schedule"
)

// fixedLoc returns a fixed-offset location for DST boundary testing.
func fixedLoc(name string, offsetHours int) *time.Location {
	return time.FixedZone(name, offsetHours*3600)
}

// ── Validate ──────────────────────────────────────────────────────────────────

func TestValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		// valid 5-field expressions
		{name: "every minute", expr: "* * * * *", wantErr: false},
		{name: "daily at 2am", expr: "0 2 * * *", wantErr: false},
		{name: "weekdays at noon", expr: "0 12 * * 1-5", wantErr: false},
		{name: "every 15 mins", expr: "*/15 * * * *", wantErr: false},
		{name: "first of month", expr: "0 0 1 * *", wantErr: false},
		{name: "complex step+range", expr: "5,10,30 6 * * 0,6", wantErr: false},

		// invalid expressions
		{name: "empty string", expr: "", wantErr: true},
		{name: "whitespace only", expr: "   ", wantErr: true},
		{name: "6-field (with seconds)", expr: "0 * * * * *", wantErr: true},
		{name: "4-field", expr: "* * * *", wantErr: true},
		{name: "bad minute value", expr: "60 * * * *", wantErr: true},
		{name: "bad hour value", expr: "* 25 * * *", wantErr: true},
		{name: "bad dom value", expr: "* * 32 * *", wantErr: true},
		{name: "bad month value", expr: "* * * 13 *", wantErr: true},
		{name: "bad dow value", expr: "* * * * 8", wantErr: true},
		{name: "non-numeric field", expr: "abc * * * *", wantErr: true},
		{name: "@reboot macro (not 5-field)", expr: "@reboot", wantErr: true},
		{name: "@hourly macro (not 5-field)", expr: "@hourly", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := schedule.Validate(tc.expr)
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tc.expr, err, tc.wantErr)
			}
		})
	}
}

// ── NextAfter ─────────────────────────────────────────────────────────────────

func TestNextAfter(t *testing.T) {
	t.Parallel()

	utc := time.UTC
	// Use a fixed reference time: 2024-01-15 02:00:00 UTC (Monday)
	ref := time.Date(2024, 1, 15, 2, 0, 0, 0, utc)

	cases := []struct {
		name    string
		expr    string
		ref     time.Time
		want    time.Time
		wantErr bool
	}{
		{
			name: "daily 2am — exactly on scheduled time returns next day",
			expr: "0 2 * * *",
			ref:  ref, // exactly 2024-01-15 02:00:00
			want: time.Date(2024, 1, 16, 2, 0, 0, 0, utc),
		},
		{
			name: "daily 2am — one second before returns same day",
			expr: "0 2 * * *",
			ref:  ref.Add(-time.Second),
			want: time.Date(2024, 1, 15, 2, 0, 0, 0, utc),
		},
		{
			name: "every minute — one second past minute boundary",
			expr: "* * * * *",
			ref:  time.Date(2024, 1, 15, 2, 0, 1, 0, utc),
			want: time.Date(2024, 1, 15, 2, 1, 0, 0, utc),
		},
		{
			name: "every minute — exactly on boundary returns next minute",
			expr: "* * * * *",
			ref:  time.Date(2024, 1, 15, 2, 0, 0, 0, utc),
			want: time.Date(2024, 1, 15, 2, 1, 0, 0, utc),
		},
		{
			name: "midnight only — ref is just before midnight returns same night",
			expr: "0 0 * * *",
			ref:  time.Date(2024, 1, 15, 23, 59, 0, 0, utc),
			want: time.Date(2024, 1, 16, 0, 0, 0, 0, utc),
		},
		{
			name: "weekdays only — Saturday ref returns Monday",
			expr: "0 9 * * 1-5",
			ref:  time.Date(2024, 1, 13, 9, 0, 0, 0, utc), // Saturday
			want: time.Date(2024, 1, 15, 9, 0, 0, 0, utc), // Monday
		},
		{
			name: "month boundary — last day of January",
			expr: "0 0 1 * *",
			ref:  time.Date(2024, 1, 31, 0, 0, 0, 0, utc),
			want: time.Date(2024, 2, 1, 0, 0, 0, 0, utc),
		},
		{
			name:    "invalid expression returns error",
			expr:    "bad expression",
			ref:     ref,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schedule.NextAfter(tc.expr, tc.ref)
			if (err != nil) != tc.wantErr {
				t.Errorf("NextAfter(%q, %v) error = %v, wantErr %v", tc.expr, tc.ref, err, tc.wantErr)
				return
			}
			if tc.wantErr {
				return
			}
			if !got.Equal(tc.want) {
				t.Errorf("NextAfter(%q, %v)\n  got  %v\n  want %v", tc.expr, tc.ref, got, tc.want)
			}
			// strictly after: result must be > ref
			if !got.After(tc.ref) {
				t.Errorf("NextAfter(%q, %v) = %v is not strictly after ref", tc.expr, tc.ref, got)
			}
		})
	}
}

// TestNextAfterDST checks NextAfter behaviour around timezone boundaries.
func TestNextAfterDST(t *testing.T) {
	t.Parallel()

	// ── fixed-offset zones (no DST rules) ─────────────────────────────────
	// Fixed zones have no transitions, so no hours are ever skipped or
	// repeated. These cases verify basic timezone arithmetic.
	winter := fixedLoc("EST", -5) // UTC-5, always
	summer := fixedLoc("EDT", -4) // UTC-4, always

	// 01:59 EST → next 02:00 is 02:00 the same day (fixed zones never skip hours)
	ref := time.Date(2024, 3, 10, 1, 59, 0, 0, winter)
	got, err := schedule.NextAfter("0 2 * * *", ref)
	if err != nil {
		t.Fatalf("fixed-zone winter: unexpected error: %v", err)
	}
	want := time.Date(2024, 3, 10, 2, 0, 0, 0, winter)
	if !got.Equal(want) {
		t.Errorf("fixed-zone winter:\n  got  %v\n  want %v", got, want)
	}

	// 03:30 EDT → next 04:00 is 04:00 the same day
	refSummer := time.Date(2024, 3, 10, 3, 30, 0, 0, summer)
	got2, err := schedule.NextAfter("0 4 * * *", refSummer)
	if err != nil {
		t.Fatalf("fixed-zone summer: unexpected error: %v", err)
	}
	want2 := time.Date(2024, 3, 10, 4, 0, 0, 0, summer)
	if !got2.Equal(want2) {
		t.Errorf("fixed-zone summer:\n  got  %v\n  want %v", got2, want2)
	}

	// ── real DST: spring-forward "skipped hour" (America/New_York) ────────
	// On 2024-03-10, New York clocks spring from 02:00 EST → 03:00 EDT.
	// "0 2 * * *" just after 01:59 EST: adding one minute lands at 03:00 EDT
	// (the UTC instant that was 02:00 EST). Hour is now 3, not 2, so cron
	// advances to the next eligible slot: 02:00 EDT the following day.
	nyLoc, locErr := time.LoadLocation("America/New_York")
	if locErr != nil {
		t.Skipf("America/New_York timezone data unavailable: %v", locErr)
	}
	refNY := time.Date(2024, 3, 10, 1, 59, 0, 0, nyLoc)
	gotNY, err := schedule.NextAfter("0 2 * * *", refNY)
	if err != nil {
		t.Fatalf("spring-forward: unexpected error: %v", err)
	}
	// 06:00 UTC = 02:00 EDT (2024-03-11 is fully in EDT, UTC-4)
	wantNY := time.Date(2024, 3, 11, 6, 0, 0, 0, time.UTC).In(nyLoc)
	if !gotNY.Equal(wantNY) {
		t.Errorf("spring-forward skipped hour:\n  got  %v\n  want %v", gotNY, wantNY)
	}

	// ── real DST: fall-back "repeated hour" (America/New_York) ───────────
	// On 2024-11-03, New York clocks fall back from 02:00 EDT → 01:00 EST.
	// The hour 01:00–01:59 occurs twice that day.
	// "0 1 * * *" at 01:30 EDT (first pass, UTC-4 = 05:30 UTC): the next
	// eligible 01:00 is the second occurrence — 01:00 EST (UTC-5 = 06:00 UTC)
	// on the same calendar day, not the following day.
	//
	// Go's time.Date resolves the ambiguous wall time to the first occurrence
	// (pre-transition, EDT), giving us the UTC instant 05:30 UTC.
	// robfig/cron then finds 06:00 UTC, which maps to 01:00 EST in New York.
	refFB := time.Date(2024, 11, 3, 1, 30, 0, 0, nyLoc) // 01:30 EDT = 05:30 UTC
	gotFB, err := schedule.NextAfter("0 1 * * *", refFB)
	if err != nil {
		t.Fatalf("fall-back: unexpected error: %v", err)
	}
	// 06:00 UTC = 01:00 EST (the second occurrence of 1am on 2024-11-03)
	wantFB := time.Date(2024, 11, 3, 6, 0, 0, 0, time.UTC).In(nyLoc)
	if !gotFB.Equal(wantFB) {
		t.Errorf("fall-back repeated hour:\n  got  %v\n  want %v", gotFB, wantFB)
	}
}

// ── NextExpectedAt ────────────────────────────────────────────────────────────

func TestNextExpectedAt(t *testing.T) {
	t.Parallel()

	utc := time.UTC
	ref := time.Date(2024, 1, 15, 2, 0, 0, 0, utc)

	cases := []struct {
		name    string
		expr    string
		grace   int
		ref     time.Time
		want    time.Time
		wantErr bool
	}{
		{
			name:  "daily 2am + 10 min grace",
			expr:  "0 2 * * *",
			grace: 10,
			ref:   ref,
			// next after ref (exactly at 2am) is next day 2am + 10 min
			want: time.Date(2024, 1, 16, 2, 10, 0, 0, utc),
		},
		{
			name:  "every minute + 5 min grace",
			expr:  "* * * * *",
			grace: 5,
			ref:   time.Date(2024, 1, 15, 12, 0, 0, 0, utc),
			// next after :00 is :01, plus 5 min = :06
			want: time.Date(2024, 1, 15, 12, 6, 0, 0, utc),
		},
		{
			name:  "grace of 1 minute (minimum)",
			expr:  "0 6 * * *",
			grace: 1,
			ref:   time.Date(2024, 1, 15, 5, 59, 0, 0, utc),
			want:  time.Date(2024, 1, 15, 6, 1, 0, 0, utc),
		},
		{
			name:  "large grace period",
			expr:  "0 0 * * *",
			grace: 120,
			ref:   time.Date(2024, 1, 14, 23, 0, 0, 0, utc),
			want:  time.Date(2024, 1, 15, 2, 0, 0, 0, utc), // midnight + 120 min
		},
		{
			name:    "invalid expression returns error",
			expr:    "not a cron",
			grace:   5,
			ref:     ref,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schedule.NextExpectedAt(tc.expr, tc.grace, tc.ref)
			if (err != nil) != tc.wantErr {
				t.Errorf("NextExpectedAt(%q, %d, %v) error = %v, wantErr %v",
					tc.expr, tc.grace, tc.ref, err, tc.wantErr)
				return
			}
			if tc.wantErr {
				return
			}
			if !got.Equal(tc.want) {
				t.Errorf("NextExpectedAt(%q, %d, %v)\n  got  %v\n  want %v",
					tc.expr, tc.grace, tc.ref, got, tc.want)
			}
		})
	}
}
