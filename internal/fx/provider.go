package fx

import (
	"context"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// RateProvider is an external source of published exchange rates.
//
// It is the only interface in this package, and it exists because there is
// genuinely more than one implementation: the Banco Central de Venezuela's
// website, and the fixture-backed one the tests drive. It is NOT a plugin
// framework, and it has exactly one method because one is all a source has to
// do.
//
// Manual entry deliberately does not implement it. A provider fetches -- it has
// a network, a document and a publication date of its own -- and a person typing
// a rate has none of those. Forcing the two through one interface would mean
// inventing a fetch for the manual case and a form for the automatic one.
type RateProvider interface {
	// Name is the source observations from this provider are stored under. It
	// must be one of Sources().
	Name() Source

	// Fetch retrieves the current publication.
	//
	// It returns ErrProviderUnavailable when the source could not be reached or
	// did not answer properly, and ErrProviderInvalidResponse when it answered
	// with something that could not be trusted. Those two are kept apart because
	// they call for different actions: unreachable is weather, unparseable means
	// the parser needs updating.
	//
	// It must honour ctx, and it must never return a partially understood
	// publication.
	Fetch(ctx context.Context) (Publication, error)
}

// Publication is one document from a source: a set of rates that share a single
// economic date.
//
// The unit matters. The rates in a publication belong together, which is why
// they are stored together or not at all -- mixing one currency's rate from
// today's publication with another's from last week's produces a total that
// looks entirely ordinary and is a blend of two days.
type Publication struct {
	Source Source

	// EffectiveDate is the date the SOURCE published for this document -- the
	// BCV's "Fecha Valor". It is a calendar date, never a timestamp, and it is
	// never derived from the clock.
	//
	// A provider that cannot determine it reliably must FAIL rather than fall
	// back to today. Inventing this date would attach real numbers to a day
	// nobody published them for, and nothing downstream could ever detect it.
	EffectiveDate time.Time

	// FetchedAt is when this application saw the document. It is a different
	// fact from EffectiveDate and the two disagree in both directions: the BCV
	// holds one Fecha Valor across a weekend, and publishes on a Friday a rate
	// whose Fecha Valor is the following week.
	FetchedAt time.Time

	// Observations are the rates the document stated, oriented as the whole
	// application orients rates: 1 Base = Rate Quote.
	Observations []Observation
}

// Observation is one rate as published.
type Observation struct {
	BaseCurrencyCode  string
	QuoteCurrencyCode string

	// Rate is exact, at the scale the source published it at. The BCV publishes
	// eight decimal places and all eight are kept, including trailing zeros:
	// they record the precision of the statement.
	Rate money.Decimal
}
