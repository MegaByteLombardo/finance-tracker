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

// EconomicLocation is the timezone this application's business calendar runs on.
//
// It is Venezuela, UTC-04:00, because that is where the money is and where the
// source of every exchange rate publishes. It exists as a named value rather
// than as a bare offset in one function because "what day is it, financially?"
// must have exactly one answer across the whole application: the valuation date,
// the day a form defaults to, and the day a refresh decides it has already run
// on all have to agree, or a rate fetched on Friday evening lands on a date the
// user has not reached yet.
//
// It is a FIXED zone rather than time.LoadLocation("America/Caracas"), and that
// is a deliberate trade rather than a shortcut:
//
//   - The runtime image is gcr.io/distroless/static, which ships no tzdata at
//     all, so LoadLocation would fail at runtime in production while working
//     perfectly in development. A failure that only happens in the container is
//     the worst kind.
//   - Importing _ "time/tzdata" would embed the database in the binary and fix
//     that, at about 450 KB on an image of 5.8 MB. It was rejected because it
//     buys no freshness: an embedded tzdata is frozen at build time exactly like
//     a constant is, so a change to Venezuela's offset would need a rebuild
//     either way.
//   - Venezuela observes no daylight saving, so there is no RULE here to get
//     wrong within a year -- only a permanent offset, which has changed twice in
//     twenty years (2007 and 2016) and would be a code change in both designs.
//
// If Venezuela changes its offset again, this constant is where it changes.
var EconomicLocation = time.FixedZone("-04:00", -4*60*60)

// Today returns the current business date: the calendar date it is right now in
// EconomicLocation, as a time.Time at UTC midnight like every other date here.
//
// It is NOT time.Now().UTC() truncated to a day, which is what it used to be.
// That version was wrong for four hours out of every twenty-four: at 21:00 in
// Caracas, UTC has already rolled over, so a valuation would have been computed
// for a day the user had not reached and a form would have defaulted to
// tomorrow. Nothing caught it because nothing called this function yet.
func Today() time.Time {
	return TodayAt(time.Now())
}

// TodayAt is Today with the instant supplied, so that a test can state what time
// it is instead of waiting for it.
func TodayAt(now time.Time) time.Time {
	year, month, day := now.In(EconomicLocation).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
