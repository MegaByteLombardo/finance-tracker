package web

import (
	"errors"
	"time"
)

// The M5 vocabulary: reference exchange rates, the valuation base, and the
// consolidated net worth. See docs/m5-contract.md.
//
// Everything here is REFERENCE / VALUATION FX -- rates used to express balances
// the user already holds in one currency. None of it is the rate a particular
// operation happened at. No transaction carries a rate, and nothing in this file
// should ever be attached to one.

// RateSource is where an observation came from.
//
// The presentation layer must never invent or submit a source. The backend sets
// it, which is why CreateManualRateInput has no field for it.
type RateSource string

const (
	// RateSourceManual is a rate a person typed. It wins over an automatic
	// source FOR THE SAME EFFECTIVE DATE, and only for that date.
	RateSourceManual RateSource = "MANUAL"

	// RateSourceBCVOfficial is a rate read from the Banco Central de Venezuela's
	// own publication.
	RateSourceBCVOfficial RateSource = "BCV_OFFICIAL"
)

// Label is the source's name for a screen. It is here rather than in a template
// so that the two interfaces cannot disagree about what to call a rate.
func (s RateSource) Label() string {
	switch s {
	case RateSourceManual:
		return "Manual"
	case RateSourceBCVOfficial:
		return "BCV"
	default:
		return string(s)
	}
}

// ExchangeRate is one stored observation, ready to display.
//
// The orientation is the project's only one: 1 BaseCurrencyCode = Rate
// QuoteCurrencyCode. A template must never reverse it by relabelling; to show
// the other direction, ask the backend.
type ExchangeRate struct {
	ID int64

	BaseCurrencyCode  string
	QuoteCurrencyCode string

	// Pair is "USD/VES", precomputed so every screen spells it the same way.
	Pair string

	// EffectiveDate is the ECONOMIC date the rate applies to -- the BCV's "Fecha
	// Valor" -- not the day it was fetched.
	EffectiveDate time.Time

	// Rate is the canonical decimal string exactly as stored, at the precision
	// the source published. Display it; never parse it into a number in the
	// browser and never round it for storage.
	Rate string

	Source      RateSource
	SourceLabel string

	// IsEditable is true only for a manual observation. An official one records
	// what was published and cannot be edited or archived; the way to disagree
	// with it is a manual rate for the same day.
	IsEditable bool

	// IsActive is false for an archived observation, which valuation ignores.
	// Archiving is how a manual override is withdrawn; nothing is ever deleted.
	IsActive bool

	// FetchedAt is when this application saw the value. It is absent for a
	// manual rate, because nothing was fetched.
	FetchedAt *time.Time

	// IsUpcoming is true when EffectiveDate is still in the future.
	//
	// The BCV publishes forward -- a page read on a Friday can carry a Fecha
	// Valor of the following Tuesday. Such an observation is stored and may be
	// SHOWN, labelled as upcoming, but it takes no part in any valuation before
	// its date.
	IsUpcoming bool
}

// CreateManualRateInput records a rate the user typed.
//
// There is deliberately NO Source field. The backend sets MANUAL, so a form
// submission cannot claim to be the Banco Central, and a handler must not add
// one.
type CreateManualRateInput struct {
	BaseCurrencyCode  string
	QuoteCurrencyCode string

	// Rate is a decimal string in the canonical format: digits, an optional
	// point, digits. No thousands separators and no comma -- a comma is the
	// provider's problem to normalise, not the user's to type.
	Rate string

	// EffectiveDate is YYYY-MM-DD.
	EffectiveDate string
}

// UpdateManualRateInput corrects a manual rate.
//
// Only the value. The pair and the effective date are the identity of an
// observation: changing them names a different observation rather than
// correcting this one, so there is no field for them.
type UpdateManualRateInput struct {
	Rate string
}

// ListExchangeRatesInput selects which observations to list.
type ListExchangeRatesInput struct {
	// BaseCurrencyCode and QuoteCurrencyCode filter to one pair when both are
	// given. Empty means every pair.
	BaseCurrencyCode  string
	QuoteCurrencyCode string

	// IncludeArchived brings back withdrawn manual overrides.
	IncludeArchived bool

	// Limit bounds the result; zero means the service's default.
	Limit int
}

// RefreshRatesResult is what pressing "update rates" produced, in terms a person
// can read.
type RefreshRatesResult struct {
	Source RateSource

	// EffectiveDate is the publication's own date, not the day of the fetch.
	EffectiveDate time.Time
	FetchedAt     time.Time

	// Stored is how many observations were written.
	Stored int

	// Skipped names currencies the source published that this application does
	// not have. They are dropped, never created: which currencies exist is the
	// user's decision, not a scraped document's.
	Skipped []string

	Rates []ExchangeRate
}

// AppSettings is the application's own configuration.
type AppSettings struct {
	// BaseCurrencyCode is the valuation base: the currency the consolidated net
	// worth is expressed in.
	//
	// Changing it rewrites NOTHING. No transaction, no movement and no balance
	// changes; only the presentation of the total does.
	BaseCurrencyCode string

	UpdatedAt time.Time
}

// RateMethod names how a conversion factor was arrived at.
type RateMethod string

const (
	// RateMethodIdentity is a currency into itself: exactly 1, with no
	// observation involved and no stored row.
	RateMethodIdentity RateMethod = "IDENTITY"
	// RateMethodDirect used one observation as published.
	RateMethodDirect RateMethod = "DIRECT"
	// RateMethodInverted used one observation, inverted exactly.
	RateMethodInverted RateMethod = "INVERTED"
	// RateMethodCross used two observations sharing a pivot currency.
	RateMethodCross RateMethod = "CROSS"
)

// Label is the method's name for a screen.
func (m RateMethod) Label() string {
	switch m {
	case RateMethodIdentity:
		return "misma moneda"
	case RateMethodDirect:
		return "tasa directa"
	case RateMethodInverted:
		return "tasa invertida"
	case RateMethodCross:
		return "tasa cruzada"
	default:
		return string(m)
	}
}

// RateLegView is one observation as it was used in a conversion.
type RateLegView struct {
	BaseCurrencyCode  string
	QuoteCurrencyCode string
	Pair              string
	Rate              string
	EffectiveDate     time.Time
	Source            RateSource
	SourceLabel       string

	// Inverted is true when the observation was read backwards, because the
	// application stores what a source published and never its mirror.
	Inverted bool
}

// RateResolutionView is the complete account of where a conversion factor came
// from.
//
// It exists so a consolidated figure is checkable. Nobody can verify a net worth
// by hand, so every component has to be able to say which source said what,
// effective on which day, and whether anything was inverted or divided.
type RateResolutionView struct {
	Method      RateMethod
	MethodLabel string

	// Legs is empty for an identity, one long for a direct or inverted rate, and
	// two long for a cross.
	Legs []RateLegView

	// EffectiveDate is the date the resolution is anchored at -- for a cross,
	// the older of its two legs, because a chain is only as current as its
	// stalest link.
	EffectiveDate time.Time

	// UsesManualOverride is true when a leg came from a person rather than from
	// the publication.
	//
	// The interface MUST be able to show this. A valuation that silently
	// preferred a typed rate over the published one would be impossible to debug
	// from the screen.
	UsesManualOverride bool
}

// NetWorthComponent is one currency's contribution to the consolidated total.
type NetWorthComponent struct {
	CurrencyCode   string
	CurrencyName   string
	CurrencySymbol *string
	MinorUnit      int
	AccountCount   int

	// BalanceMinor and BalanceFormatted are the ledger's own figure, in THIS
	// currency, untouched by any conversion.
	BalanceMinor     int64
	BalanceFormatted string

	// ConvertedMinor and ConvertedFormatted are that balance in the base
	// currency, and are meaningful only when Valued is true.
	ConvertedMinor     int64
	ConvertedFormatted string

	// Valued is false when no rate could be found. The component is still shown,
	// with its own balance: the user holds that money whether or not this
	// application can price it.
	Valued bool

	// Resolution is nil exactly when Valued is false.
	Resolution *RateResolutionView
}

// MissingRateView names a conversion that could not be resolved, so a screen can
// say what is missing and offer the form that fixes it.
type MissingRateView struct {
	FromCurrencyCode string
	ToCurrencyCode   string

	// BalanceFormatted is how much is sitting unvalued, so the message can say
	// how much is at stake rather than only that something is.
	BalanceFormatted string
}

// NetWorth is everything the user holds, expressed in the valuation base.
//
// It is DERIVED on every read from the ledger, the stored rates and the base
// currency. It is never stored, never cached, and there is no table behind it.
type NetWorth struct {
	BaseCurrencyCode string
	BaseCurrencyName string
	BaseSymbol       *string
	BaseMinorUnit    int

	// ValuationDate is the business date in Venezuela the rates were looked up
	// for, which is also the date the source publishes against.
	ValuationDate time.Time

	TotalMinor     int64
	TotalFormatted string

	// Complete is false when at least one currency could not be valued.
	//
	// When it is false the total is PARTIAL, and a screen must say so. It must
	// never be presented as the user's net worth, and the missing currencies
	// must never be treated as worth zero.
	Complete bool

	Components []NetWorthComponent
	Missing    []MissingRateView

	// RatesAsOf is the oldest observation date behind any valued component, so a
	// screen can say "rates as of ...". It is the zero time when nothing needed
	// a rate.
	RatesAsOf time.Time
}

// Errors M5 adds. The M2, M3 and M4 sentinels remain in force.
var (
	// ErrInvalidRate means the rate could not be read exactly, or is not greater
	// than zero. It is separate from ErrInvalidInput so a form can highlight the
	// rate field rather than the whole submission.
	ErrInvalidRate = errors.New("invalid rate")

	// ErrRateNotFound means no exchange rate exists with that id.
	ErrRateNotFound = errors.New("exchange rate not found")

	// ErrRateConflict means an observation already exists for that pair, date
	// and source. Offer the edit rather than retrying the creation.
	ErrRateConflict = errors.New("an exchange rate already exists for that pair and date")

	// ErrSameCurrencyPair means base and quote were the same currency.
	ErrSameCurrencyPair = errors.New("an exchange rate needs two different currencies")

	// ErrRateNotEditable means an edit or archive was attempted on an
	// observation that did not come from a person. It warrants 409, not 422:
	// nothing about the input was wrong.
	ErrRateNotEditable = errors.New("only a manual exchange rate can be edited")

	// ErrProviderUnavailable means the external source could not be reached.
	//
	// It is EXTERNAL. It must never produce a 500 and must never prevent
	// anything else on the page from working: re-render with a message and leave
	// manual entry available.
	ErrProviderUnavailable = errors.New("the rate provider could not be reached")

	// ErrProviderInvalidResponse means the source answered with something that
	// could not be trusted, which usually means its page changed shape.
	ErrProviderInvalidResponse = errors.New("the rate provider returned something unexpected")

	// ErrBaseCurrencyUnavailable means the configured valuation base is missing
	// from the currencies table, or the settings row is gone. It is an integrity
	// failure and warrants a 500.
	ErrBaseCurrencyUnavailable = errors.New("the valuation base currency is unavailable")
)
