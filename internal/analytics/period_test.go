package analytics_test

import (
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/analytics"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := database.ParseDate(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return parsed
}

func formatPeriod(p analytics.Period) string {
	return database.FormatDate(p.From) + " -> " + database.FormatDate(p.To)
}

// TestThirtyDaysAndThePreviousThirty is the example the milestone was specified
// with.
func TestThirtyDaysAndThePreviousThirty(t *testing.T) {
	t.Parallel()

	current, hasPrevious := analytics.PeriodFor(analytics.Preset30D, mustDate(t, "2026-10-05"), time.Time{}, false)
	if !hasPrevious {
		t.Fatal("30D has no previous period")
	}
	if got := formatPeriod(current); got != "2026-09-06 -> 2026-10-05" {
		t.Errorf("current = %s, want 2026-09-06 -> 2026-10-05", got)
	}
	if got := formatPeriod(current.Previous()); got != "2026-08-07 -> 2026-09-05" {
		t.Errorf("previous = %s, want 2026-08-07 -> 2026-09-05", got)
	}
}

func TestEveryFixedPresetHasAnAdjacentEqualPreviousPeriod(t *testing.T) {
	t.Parallel()

	today := mustDate(t, "2028-03-01") // just after a leap day
	wantDays := map[analytics.Preset]int{
		analytics.Preset7D: 7, analytics.Preset30D: 30, analytics.Preset90D: 90, analytics.Preset1Y: 365,
	}
	for preset, days := range wantDays {
		current, hasPrevious := analytics.PeriodFor(preset, today, time.Time{}, false)
		previous := current.Previous()

		if !hasPrevious {
			t.Errorf("%s has no previous period", preset)
		}
		if current.Days() != days || previous.Days() != days {
			t.Errorf("%s: %d and %d days, want %d each", preset, current.Days(), previous.Days(), days)
		}
		if !current.To.Equal(today) {
			t.Errorf("%s ends on %s, want today", preset, database.FormatDate(current.To))
		}
		// No overlap and no gap: the previous period ends the day before.
		if !previous.To.AddDate(0, 0, 1).Equal(current.From) {
			t.Errorf("%s: previous ends %s, current starts %s", preset,
				database.FormatDate(previous.To), database.FormatDate(current.From))
		}
		if previous.Contains(current.From) || current.Contains(previous.To) {
			t.Errorf("%s: the periods overlap", preset)
		}
	}
}

func TestAllStartsAtTheFirstMovementAndHasNoPrevious(t *testing.T) {
	t.Parallel()

	today := mustDate(t, "2026-10-05")

	current, hasPrevious := analytics.PeriodFor(analytics.PresetAll, today, mustDate(t, "2024-01-15"), true)
	if hasPrevious {
		t.Error("ALL has a previous period")
	}
	if got := formatPeriod(current); got != "2024-01-15 -> 2026-10-05" {
		t.Errorf("ALL = %s", got)
	}

	// An empty ledger, and a ledger whose only movements are in the future:
	// both are a single day, today.
	for _, earliest := range []struct {
		date time.Time
		ok   bool
	}{{time.Time{}, false}, {mustDate(t, "2026-12-01"), true}} {
		current, _ := analytics.PeriodFor(analytics.PresetAll, today, earliest.date, earliest.ok)
		if got := formatPeriod(current); got != "2026-10-05 -> 2026-10-05" {
			t.Errorf("ALL with earliest %v = %s, want today only", earliest, got)
		}
	}
}

func TestParsePreset(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]analytics.Preset{
		"7D": analytics.Preset7D, "30d": analytics.Preset30D, " 90D ": analytics.Preset90D,
		"1y": analytics.Preset1Y, "all": analytics.PresetAll,
	} {
		got, ok := analytics.ParsePreset(raw)
		if !ok || got != want {
			t.Errorf("ParsePreset(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "2W", "30", "MAX"} {
		if _, ok := analytics.ParsePreset(raw); ok {
			t.Errorf("ParsePreset(%q) was accepted", raw)
		}
	}
}
