package analytics_test

import (
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/analytics"
)

func TestAWeekRunsMondayToSunday(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"2026-10-05": "2026-10-05 -> 2026-10-11", // a Monday
		"2026-10-11": "2026-10-05 -> 2026-10-11", // its Sunday
		"2026-10-01": "2026-09-28 -> 2026-10-04", // across a month
		"2027-01-01": "2026-12-28 -> 2027-01-03", // across a year
	}
	for day, want := range cases {
		if got := formatPeriod(analytics.WeekContaining(mustDate(t, day))); got != want {
			t.Errorf("week of %s = %s, want %s", day, got, want)
		}
	}
}

func TestAMonthRunsFromItsFirstToItsLastDay(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"2026-10-12": "2026-10-01 -> 2026-10-31",
		"2026-02-14": "2026-02-01 -> 2026-02-28",
		"2028-02-29": "2028-02-01 -> 2028-02-29", // leap year
		"2026-12-31": "2026-12-01 -> 2026-12-31",
	}
	for day, want := range cases {
		if got := formatPeriod(analytics.MonthContaining(mustDate(t, day))); got != want {
			t.Errorf("month of %s = %s, want %s", day, got, want)
		}
	}
}

func TestStepping(t *testing.T) {
	t.Parallel()

	// From the 31st, the month before is February whole: stepping goes through
	// the 1st, so it never overflows into March.
	if got := formatPeriod(analytics.StepCalendarPeriod(analytics.GranularityMonth, mustDate(t, "2026-03-31"), -1)); got != "2026-02-01 -> 2026-02-28" {
		t.Errorf("month before March = %s", got)
	}
	if got := formatPeriod(analytics.StepCalendarPeriod(analytics.GranularityMonth, mustDate(t, "2026-12-15"), 1)); got != "2027-01-01 -> 2027-01-31" {
		t.Errorf("month after December = %s", got)
	}
	if got := formatPeriod(analytics.StepCalendarPeriod(analytics.GranularityWeek, mustDate(t, "2026-10-01"), -1)); got != "2026-09-21 -> 2026-09-27" {
		t.Errorf("week before = %s", got)
	}
	if got := formatPeriod(analytics.LastClosed(analytics.GranularityMonth, mustDate(t, "2026-10-12"))); got != "2026-09-01 -> 2026-09-30" {
		t.Errorf("last closed month = %s", got)
	}
	if got := formatPeriod(analytics.LastClosed(analytics.GranularityWeek, mustDate(t, "2026-10-12"))); got != "2026-10-05 -> 2026-10-11" {
		t.Errorf("last closed week = %s", got)
	}
}

// TestAClosedMonthIsComparedWithTheWholePreviousOne: different lengths are the
// months a person means, and the daily average is what makes them comparable.
func TestAClosedMonthIsComparedWithTheWholePreviousOne(t *testing.T) {
	t.Parallel()

	p := analytics.RecapPeriodsFor(analytics.GranularityMonth, mustDate(t, "2026-03-10"), mustDate(t, "2026-10-12"))
	if p.InProgress {
		t.Error("March is in progress in October")
	}
	if got := formatPeriod(p.Current); got != "2026-03-01 -> 2026-03-31" {
		t.Errorf("current = %s", got)
	}
	if got := formatPeriod(p.Previous); got != "2026-02-01 -> 2026-02-28" {
		t.Errorf("previous = %s", got)
	}
}

// TestAPeriodInProgressIsComparedLikeForLike is TD-072.
func TestAPeriodInProgressIsComparedLikeForLike(t *testing.T) {
	t.Parallel()

	cases := []struct {
		g                        analytics.Granularity
		today, current, previous string
	}{
		{analytics.GranularityMonth, "2026-10-12", "2026-10-01 -> 2026-10-12", "2026-09-01 -> 2026-09-12"},
		// The cut never runs past the previous month's end.
		{analytics.GranularityMonth, "2026-03-31", "2026-03-01 -> 2026-03-31", "2026-02-01 -> 2026-02-28"},
		{analytics.GranularityMonth, "2026-03-29", "2026-03-01 -> 2026-03-29", "2026-02-01 -> 2026-02-28"},
		{analytics.GranularityMonth, "2026-10-01", "2026-10-01 -> 2026-10-01", "2026-09-01 -> 2026-09-01"},
		{analytics.GranularityWeek, "2026-10-14", "2026-10-12 -> 2026-10-14", "2026-10-05 -> 2026-10-07"},
	}
	for _, c := range cases {
		today := mustDate(t, c.today)
		p := analytics.RecapPeriodsFor(c.g, today, today)
		if !p.InProgress {
			t.Errorf("%s %s: not in progress", c.g, c.today)
		}
		if got := formatPeriod(p.Current); got != c.current {
			t.Errorf("%s %s: current = %s, want %s", c.g, c.today, got, c.current)
		}
		if got := formatPeriod(p.Previous); got != c.previous {
			t.Errorf("%s %s: previous = %s, want %s", c.g, c.today, got, c.previous)
		}
	}
}

func TestParseGranularity(t *testing.T) {
	t.Parallel()

	if g, ok := analytics.ParseGranularity(" week "); !ok || g != analytics.GranularityWeek {
		t.Errorf("week = %q, %v", g, ok)
	}
	if _, ok := analytics.ParseGranularity("year"); ok {
		t.Error("year was accepted")
	}
}
