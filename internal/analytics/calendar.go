package analytics

import (
	"strings"
	"time"
)

// Granularity is the length of a recap: a calendar week or a calendar month.
//
// A recap's periods are CALENDAR periods, unlike the dashboard's presets, which
// are a fixed number of days ending today (TD-069). "September" is the 1st to
// the 30th whatever day it is now, and a week runs Monday to Sunday (TD-072).
type Granularity string

const (
	GranularityWeek  Granularity = "WEEK"
	GranularityMonth Granularity = "MONTH"

	// DefaultGranularity is what a recap shows when nothing, or something
	// unrecognised, was asked for.
	DefaultGranularity = GranularityMonth
)

// Granularities lists every granularity in the order a selector shows them.
func Granularities() []Granularity {
	return []Granularity{GranularityWeek, GranularityMonth}
}

// ParseGranularity reads a granularity case-insensitively. ok is false for
// anything that is not one.
func ParseGranularity(raw string) (_ Granularity, ok bool) {
	candidate := Granularity(strings.ToUpper(strings.TrimSpace(raw)))
	for _, known := range Granularities() {
		if candidate == known {
			return known, true
		}
	}
	return "", false
}

// WeekContaining is the Monday-to-Sunday week a date falls in.
func WeekContaining(day time.Time) Period {
	day = dateOnly(day)
	// time.Weekday counts from Sunday = 0; the week here starts on Monday.
	back := (int(day.Weekday()) + 6) % 7
	from := day.AddDate(0, 0, -back)
	return Period{From: from, To: from.AddDate(0, 0, 6)}
}

// MonthContaining is the calendar month a date falls in, from its 1st to its
// last day.
func MonthContaining(day time.Time) Period {
	from := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	return Period{From: from, To: from.AddDate(0, 1, -1)}
}

// CalendarPeriod is the whole week or month a date falls in.
func CalendarPeriod(g Granularity, day time.Time) Period {
	if g == GranularityWeek {
		return WeekContaining(day)
	}
	return MonthContaining(day)
}

// StepCalendarPeriod is the calendar period n steps away from the one a date
// falls in: -1 is the one before, +1 the one after. Each has its own length; a
// month is never cut to the length of its neighbour.
func StepCalendarPeriod(g Granularity, day time.Time, n int) Period {
	start := CalendarPeriod(g, day).From
	if g == GranularityWeek {
		return WeekContaining(start.AddDate(0, 0, 7*n))
	}
	// start is the 1st, so adding months never overflows into the next one.
	return MonthContaining(start.AddDate(0, n, 0))
}

// LastClosed is the most recent calendar period that has fully ended by today:
// the one before the period today falls in.
func LastClosed(g Granularity, today time.Time) Period {
	return StepCalendarPeriod(g, today, -1)
}

// RecapPeriods are the two ranges a recap compares.
type RecapPeriods struct {
	// Full is the whole calendar period the recap is about.
	Full Period

	// Current is Full, cut at today when Full has not ended yet.
	Current Period

	// Previous is the calendar period before Full -- cut, when Current is, to
	// the same number of elapsed days and never beyond its own end.
	Previous Period

	// InProgress is true when today falls inside Full.
	InProgress bool
}

// RecapPeriodsFor resolves the calendar period a date falls in, and the one it
// is compared with, as of today.
//
// A period that has ended is compared with the WHOLE previous one, even when
// the two differ in length (31 days against 30): those are the months a person
// means. A period still in progress is compared LIKE FOR LIKE: October 1-12
// against September 1-12, because twelve days against thirty would read as a
// fall in spending that did not happen (TD-072). The previous cut never runs
// past the previous period's end: on March 31 it is all of February.
func RecapPeriodsFor(g Granularity, day, today time.Time) RecapPeriods {
	full := CalendarPeriod(g, day)
	previous := StepCalendarPeriod(g, day, -1)
	result := RecapPeriods{Full: full, Current: full, Previous: previous}

	today = dateOnly(today)
	if full.Contains(today) {
		result.InProgress = true
		result.Current.To = today
		cut := previous.From.AddDate(0, 0, result.Current.Days()-1)
		if cut.Before(previous.To) {
			result.Previous.To = cut
		}
	}
	return result
}

// dateOnly drops the time of day, keeping the calendar date as UTC midnight --
// the form every financial date in this application takes.
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
