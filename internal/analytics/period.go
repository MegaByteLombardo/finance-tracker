package analytics

import (
	"strings"
	"time"
)

// Preset names a period the dashboard can be looked at over.
type Preset string

const (
	Preset7D  Preset = "7D"
	Preset30D Preset = "30D"
	Preset90D Preset = "90D"
	Preset1Y  Preset = "1Y"
	PresetAll Preset = "ALL"

	// DefaultPreset is what the dashboard shows when nothing, or something
	// unrecognised, was asked for.
	DefaultPreset = Preset30D
)

// Presets lists every preset in the order a selector shows them.
func Presets() []Preset {
	return []Preset{Preset7D, Preset30D, Preset90D, Preset1Y, PresetAll}
}

// ParsePreset reads a preset from a query string, case-insensitively. ok is
// false for anything that is not one; the caller decides whether that means the
// default or an error.
func ParsePreset(raw string) (_ Preset, ok bool) {
	candidate := Preset(strings.ToUpper(strings.TrimSpace(raw)))
	for _, known := range Presets() {
		if candidate == known {
			return known, true
		}
	}
	return "", false
}

// days is how many calendar days a fixed preset covers, and zero for ALL.
//
// 1Y is 365 days rather than "the same date last year", so that every preset
// is a fixed number of days and its previous period is exactly as long
// (TD-069). A leap year moves the start by one day, which is the honest cost of
// comparing equal lengths.
func (p Preset) days() int {
	switch p {
	case Preset7D:
		return 7
	case Preset30D:
		return 30
	case Preset90D:
		return 90
	case Preset1Y:
		return 365
	default:
		return 0
	}
}

// Period is a range of calendar dates, both ends included.
type Period struct {
	From time.Time
	To   time.Time
}

// Days is how many calendar days the period covers, both ends included.
func (p Period) Days() int {
	return int(p.To.Sub(p.From).Hours()/24) + 1
}

// Previous is the period of the same length that ends the day before this one
// starts: no overlap, no gap (TD-069).
//
//	2026-09-06 -> 2026-10-05   (30 days)
//	2026-08-07 -> 2026-09-05   (30 days)
func (p Period) Previous() Period {
	n := p.Days()
	return Period{
		From: p.From.AddDate(0, 0, -n),
		To:   p.From.AddDate(0, 0, -1),
	}
}

// Contains reports whether a date falls inside the period.
func (p Period) Contains(day time.Time) bool {
	return !day.Before(p.From) && !day.After(p.To)
}

// PeriodFor resolves a preset into dates, ending today.
//
// A fixed preset covers its number of days, today included, and has a previous
// period. ALL starts at the earliest movement in the ledger -- or today, when
// there is none or when every movement is still in the future -- and has no
// previous period: there is nothing before "everything" to compare it with.
func PeriodFor(preset Preset, today time.Time, earliest time.Time, hasEarliest bool) (current Period, hasPrevious bool) {
	if n := preset.days(); n > 0 {
		return Period{From: today.AddDate(0, 0, -(n - 1)), To: today}, true
	}
	from := today
	if hasEarliest && earliest.Before(today) {
		from = earliest
	}
	return Period{From: from, To: today}, false
}
