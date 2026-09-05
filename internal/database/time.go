package database

import (
	"fmt"
	"time"
)

// TimeLayout is how every timestamp in this database is stored: RFC 3339, UTC,
// with exactly three fractional digits.
//
// Timestamps are TEXT rather than Unix integers for two reasons. The database
// is one file that its owner will open with the sqlite3 CLI to look at their
// own finances, and "2026-08-29T22:37:55.123Z" reads where "1787012275" does
// not. And business dates arriving later are calendar dates, so ISO-8601 keeps
// system timestamps and business dates on one convention, where date(),
// strftime() and BETWEEN behave identically over both.
//
// The fixed width is what makes lexicographic order chronological, which is
// why the layout is ".000" and not time.RFC3339Nano: RFC3339Nano strips
// trailing zeros, so "…:05.1Z" would sort after "…:05.09Z" as text. The
// matching CHECK constraint in each table enforces this shape in the database
// itself.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// FormatTime renders t for storage. The value is converted to UTC and
// truncated to the stored precision, so what a caller keeps in memory matches
// what a later read returns.
func FormatTime(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format(TimeLayout)
}

// Now returns the current time already reduced to the precision the database
// stores, so a value written and then read back compares equal.
func Now() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}

// ParseTime reads a stored timestamp.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing timestamp %q: %w", s, err)
	}
	return t, nil
}
