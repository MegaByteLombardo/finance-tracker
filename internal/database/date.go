package database

import (
	"fmt"
	"time"
)

// DateLayout is how a business date is stored: ISO 8601, YYYY-MM-DD.
//
// A business date is a calendar date, not an instant. "The purchase happened on
// 2026-09-03" is true regardless of the timezone it is read in, whereas a
// timestamp names a moment that different zones render as different days. The
// two are stored differently for that reason: TimeLayout carries a time and a
// zone, this does not.
//
// The layout is fixed-width, so lexicographic order is chronological here for
// the same reason it is for timestamps, and date(), strftime() and BETWEEN
// behave identically over both conventions.
const DateLayout = "2006-01-02"

// FormatDate renders a calendar date for storage.
//
// The value is read in UTC, which is where ParseDate puts it. Passing a time
// that carries a local zone would shift the date across midnight, so dates that
// reach here should have come from ParseDate or from a UTC construction.
func FormatDate(t time.Time) string {
	return t.UTC().Format(DateLayout)
}

// ParseDate reads a calendar date and returns it at UTC midnight.
//
// It is strict: the layout demands zero-padded components, so "2026-9-5" is
// refused, and time.Parse validates the calendar itself, so "2026-02-31" is
// refused too. The same two rules are enforced by a CHECK constraint on every
// date column, so a hand-written INSERT cannot get past them either.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing date %q: %w", s, err)
	}
	return t, nil
}

// Today returns the current date in UTC, as a sensible default for a form.
func Today() time.Time {
	return time.Now().UTC().Truncate(24 * time.Hour)
}
