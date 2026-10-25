package creditcard

import (
	"sort"
	"time"
)

// M8.2: the calendar a card's cycles follow (plan §5.4).
//
// A card closes its statement on the same day every month; in a month without
// that day it closes on the month's last day. A movement belongs to the first
// cycle that closes on or after its date. That membership is DERIVED and never
// stored: a registered statement replaces the calendar's closing nearest to it,
// and an assertion (credit_statement_assignments) replaces the rule for one
// movement (R7).
//
// There is no table of holidays. The calendar is the issuer's rule as stated in
// the terms, not a prediction of when the bank's systems will run.

// ClosingDate returns the day the cycle of a month closes, for a card that
// closes on statementDay: min(statementDay, the month's last day).
//
//	ClosingDate(31, 2026, time.February) -> 2026-02-28
//	ClosingDate(31, 2028, time.February) -> 2028-02-29
func ClosingDate(statementDay int, year int, month time.Month) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(statementDay, last), 0, 0, 0, 0, time.UTC)
}

// CalendarClosingFor returns the closing of the cycle a date belongs to by the
// calendar alone: this month's closing if the date is on or before it, next
// month's otherwise. December's late days belong to January's cycle.
func CalendarClosingFor(statementDay int, date time.Time) time.Time {
	closing := ClosingDate(statementDay, date.Year(), date.Month())
	if !date.After(closing) {
		return closing
	}
	return ClosingDate(statementDay, date.Year(), date.Month()+1)
}

// bound is one cycle's closing: the calendar's, or a registered statement's.
type bound struct {
	closing   time.Time
	statement *Statement
}

// schedule returns every cycle closing from the one that contains from to the
// first one on or after through, oldest first.
//
// Registered statements are taken as they are. A calendar closing is left out
// when a registered statement covers it (period_start <= closing <= its
// closing) or when it is the calendar closing nearest to a registered one: the
// bank closed THAT cycle on another day -- a weekend, a holiday -- and the
// calendar's date must not open a second, one-day cycle beside it.
func schedule(statementDay int, statements []Statement, from, through time.Time) []bound {
	if through.Before(from) {
		through = from
	}
	for _, s := range statements {
		if s.PeriodStart.Before(from) {
			from = s.PeriodStart
		}
		if s.ClosingDate.After(through) {
			through = s.ClosingDate
		}
	}

	var calendar []time.Time
	first := CalendarClosingFor(statementDay, from)
	for m := 0; ; m++ {
		c := ClosingDate(statementDay, first.Year(), first.Month()+time.Month(m))
		calendar = append(calendar, c)
		if !c.Before(through) {
			break
		}
	}
	// One more month on each side, so that the statement nearest to an edge
	// still has its calendar twin to replace.
	calendar = append([]time.Time{ClosingDate(statementDay, first.Year(), first.Month()-1)}, calendar...)
	last := calendar[len(calendar)-1]
	calendar = append(calendar, ClosingDate(statementDay, last.Year(), last.Month()+1))

	suppressed := make(map[time.Time]bool)
	for _, s := range statements {
		nearest, distance := time.Time{}, time.Duration(-1)
		for _, c := range calendar {
			if !c.Before(s.PeriodStart) && !c.After(s.ClosingDate) {
				suppressed[c] = true
			}
			d := c.Sub(s.ClosingDate)
			if d < 0 {
				d = -d
			}
			if distance < 0 || d < distance {
				nearest, distance = c, d
			}
		}
		suppressed[nearest] = true
	}

	bounds := make([]bound, 0, len(calendar)+len(statements))
	for i := range statements {
		bounds = append(bounds, bound{closing: statements[i].ClosingDate, statement: &statements[i]})
	}
	for _, c := range calendar {
		if !suppressed[c] {
			bounds = append(bounds, bound{closing: c})
		}
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i].closing.Before(bounds[j].closing) })

	// Trim to the cycles that matter: the one containing from, through the
	// first closing on or after through.
	start := sort.Search(len(bounds), func(i int) bool { return !bounds[i].closing.Before(from) })
	end := sort.Search(len(bounds), func(i int) bool { return !bounds[i].closing.Before(through) })
	if end >= len(bounds) {
		end = len(bounds) - 1
	}
	return bounds[start : end+1]
}

// cycleOf returns the index of the first bound closing on or after date, or -1
// when every bound closes before it.
func cycleOf(bounds []bound, date time.Time) int {
	i := sort.Search(len(bounds), func(i int) bool { return !bounds[i].closing.Before(date) })
	if i == len(bounds) {
		return -1
	}
	return i
}
