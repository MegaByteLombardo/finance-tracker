package database_test

import (
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

func TestParseDateIsStrict(t *testing.T) {
	t.Parallel()

	valid := []string{"2026-09-11", "2024-02-29", "1999-12-31", "2026-01-01"}
	for _, in := range valid {
		t.Run("accepts "+in, func(t *testing.T) {
			t.Parallel()

			got, err := database.ParseDate(in)
			if err != nil {
				t.Fatalf("ParseDate(%q): %v", in, err)
			}
			if database.FormatDate(got) != in {
				t.Errorf("round trip of %q produced %q", in, database.FormatDate(got))
			}
			if got.Location() != time.UTC {
				t.Errorf("ParseDate(%q) returned a time in %v, want UTC", in, got.Location())
			}
		})
	}

	invalid := []string{"2026-9-11", "2026-02-31", "26-09-11", "2026/09/11", "", "abcd-ef-gh",
		"2026-09-11T00:00:00Z", "2026-13-01", "2026-00-10"}
	for _, in := range invalid {
		t.Run("refuses "+in, func(t *testing.T) {
			t.Parallel()

			if got, err := database.ParseDate(in); err == nil {
				t.Errorf("ParseDate(%q) = %v, want an error", in, got)
			}
		})
	}
}

// TestTodayIsTheVenezuelanCalendarDate is the regression guard for the bug this
// function used to have: between 20:00 and 24:00 UTC-04, UTC is already on the
// next day, so a UTC "today" names a date the user has not reached.
//
// That is not cosmetic. It would make a valuation look for an exchange rate
// effective tomorrow, and make a transaction form default to tomorrow's date.
func TestTodayIsTheVenezuelanCalendarDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		now  time.Time
		want string
	}{
		{
			name: "late evening in Caracas, when UTC has already rolled over",
			now:  time.Date(2026, 9, 12, 1, 59, 52, 0, time.UTC), // 2026-09-11 21:59:52 -04:00
			want: "2026-09-11",
		},
		{
			name: "one minute before midnight in Caracas",
			now:  time.Date(2026, 9, 12, 3, 59, 0, 0, time.UTC),
			want: "2026-09-11",
		},
		{
			name: "midnight in Caracas",
			now:  time.Date(2026, 9, 12, 4, 0, 0, 0, time.UTC),
			want: "2026-09-12",
		},
		{
			name: "midday in Caracas, where UTC and local agree on the date",
			now:  time.Date(2026, 9, 11, 16, 0, 0, 0, time.UTC),
			want: "2026-09-11",
		},
		{
			name: "the instant is read, not its own zone",
			now: time.Date(2026, 9, 11, 22, 30, 0, 0,
				time.FixedZone("+09:00", 9*60*60)), // 2026-09-11 09:30 -04:00
			want: "2026-09-11",
		},
		{
			name: "a year boundary",
			now:  time.Date(2027, 1, 1, 2, 0, 0, 0, time.UTC), // 2026-12-31 22:00 -04:00
			want: "2026-12-31",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := database.TodayAt(tc.now)
			if database.FormatDate(got) != tc.want {
				t.Errorf("TodayAt(%s) = %s, want %s",
					tc.now.Format(time.RFC3339), database.FormatDate(got), tc.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("TodayAt returned a time in %v, want UTC midnight", got.Location())
			}
			if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
				t.Errorf("TodayAt returned %v, want midnight", got)
			}
		})
	}
}

func TestTodayUsesTheEconomicLocation(t *testing.T) {
	t.Parallel()

	_, offset := time.Now().In(database.EconomicLocation).Zone()
	if offset != -4*60*60 {
		t.Errorf("EconomicLocation offset = %d seconds, want -14400 (UTC-04:00)", offset)
	}

	// Today must agree with TodayAt for the same instant, so that the tested
	// function and the one actually called cannot drift apart.
	now := time.Now()
	if database.FormatDate(database.Today()) != database.FormatDate(database.TodayAt(now)) {
		// Tolerated only if the clock crossed midnight between the two calls,
		// which a second call resolves.
		if database.FormatDate(database.Today()) != database.FormatDate(database.TodayAt(time.Now())) {
			t.Error("Today and TodayAt disagree for the same instant")
		}
	}
}
