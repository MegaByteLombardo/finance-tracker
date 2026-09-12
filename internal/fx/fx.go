// Package fx owns reference exchange rates: what a source said one currency was
// worth in another, on a given day, and how to turn those observations into an
// exact conversion factor.
//
// # Reference FX is not transaction FX
//
// Everything here is REFERENCE / VALUATION FX: a rate used to express balances
// the user already holds in a single currency, for presentation. It is never the
// rate a particular operation happened at. A card's rate on a purchase, a
// merchant's rate, the rate a cross-currency transfer settled at -- none of that
// exists in this application, nothing here implies it, and transactions gained
// no exchange_rate_id in M5. The two are different facts about different things,
// and conflating them would make it impossible to say later which one a stored
// number meant.
//
// # Orientation
//
// Every rate in this project, in the database and in memory, means:
//
//	1 BaseCurrencyCode = Rate QuoteCurrencyCode
//
// base = USD, quote = VES, rate = 842.20670000 is "1 USD = 842.2067 VES".
//
// The columns are base/quote rather than from/to because from/to invites a
// reader to assume a direction of conversion that the row does not state.
// Direction is the caller's question, answered by Resolve, which inverts
// exactly rather than reading a second stored row that could disagree.
//
// Careful: the "base currency" of a pair is not the application's base currency
// of valuation. That one lives in internal/settings and is called the valuation
// base everywhere.
//
// # No floats
//
// A rate is a money.Decimal, stored as canonical TEXT and computed on
// math/big.Rat. There is no float32, float64 or SQLite REAL anywhere in this
// package or anywhere a value from it travels.
package fx

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// Errors this package returns. Callers distinguish them with errors.Is.
var (
	// ErrNotFound means no observation exists with the given id.
	ErrNotFound = errors.New("exchange rate not found")

	// ErrInvalidInput means the input failed validation: a malformed currency
	// code, a malformed or impossible date, a source the caller may not choose.
	ErrInvalidInput = errors.New("invalid exchange rate")

	// ErrInvalidRate means the rate itself could not be read exactly, or is not
	// greater than zero.
	//
	// It is separate from ErrInvalidInput so that a form can highlight the rate
	// field rather than the whole submission -- the same split ledger makes
	// between ErrInvalidInput and ErrInvalidAmount.
	ErrInvalidRate = errors.New("invalid rate")

	// ErrConflict means an observation already exists for that pair, date and
	// source.
	//
	// It is raised for a manual creation and NOT for a provider ingest: a person
	// typing a rate that already exists should be told and offered the edit,
	// whereas a second run of the scraper is the same publication being seen
	// again and updates in place.
	ErrConflict = errors.New("an exchange rate already exists for that pair, date and source")

	// ErrSameCurrency means base and quote were the same currency. A currency is
	// worth exactly one of itself; that is arithmetic, and storing it would
	// create a row that could be edited into being false.
	ErrSameCurrency = errors.New("an exchange rate needs two different currencies")

	// ErrCurrencyNotFound means one of the two currencies does not exist.
	//
	// There is deliberately no ErrCurrencyInactive here, unlike account and
	// ledger. An ACTIVE account denominated in an ARCHIVED currency is a
	// reachable, documented state, and it still has to be valued; refusing to
	// record the rate that values it would make that state permanently
	// unvaluable. Archiving a currency retires it from new ACCOUNTS, not from
	// arithmetic about money that already exists.
	ErrCurrencyNotFound = errors.New("exchange rate currency not found")

	// ErrNotEditable means an edit or an archive was attempted on an observation
	// that did not come from a person.
	//
	// A BCV_OFFICIAL row is a record of what was published. Editing one would
	// make the application claim the source said something it did not, and the
	// user already has a way to override it: a MANUAL observation for the same
	// day, which wins by precedence and can itself be corrected or archived.
	ErrNotEditable = errors.New("only a manual exchange rate can be edited")

	// ErrRateUnavailable means no usable path exists between two currencies on
	// the valuation date: no direct observation, no reverse one, and no single
	// pivot connecting them.
	//
	// It is how a missing rate reaches the valuation layer, which turns it into
	// an explicitly incomplete result rather than a total that silently left
	// something out.
	ErrRateUnavailable = errors.New("no exchange rate is available for that pair")

	// ErrProviderUnavailable means the external source could not be reached or
	// did not answer properly: DNS, TLS, a timeout, a non-200 status.
	//
	// It is an EXTERNAL failure. Nothing local is wrong, the application keeps
	// working, and manual entry remains available. It must never end a startup
	// and must never become a 500.
	ErrProviderUnavailable = errors.New("the rate provider could not be reached")

	// ErrProviderInvalidResponse means the source was reached and answered, but
	// what it said could not be trusted: the document changed shape, a value was
	// not a number, the publication date was missing or ambiguous.
	//
	// It is kept separate from ErrProviderUnavailable because the two call for
	// different actions. Unreachable is weather; unparseable means the parser
	// needs updating, and the log line is the only warning anyone will get.
	ErrProviderInvalidResponse = errors.New("the rate provider returned something unexpected")
)

// Source is where an observation came from.
//
// It is an enumeration rather than an is_manual boolean because provenance is
// the point: two sources can state different values for the same day, and the
// application has to know which to prefer and be able to tell the user which one
// it used.
type Source string

const (
	// SourceManual is a rate a person typed. It always wins over an automatic
	// source FOR THE SAME DAY; see Precedence.
	SourceManual Source = "MANUAL"

	// SourceBCVOfficial is a rate read from the Banco Central de Venezuela's own
	// publication.
	SourceBCVOfficial Source = "BCV_OFFICIAL"
)

// Sources lists every source this binary can store, highest precedence first.
//
// There is no BCV_MIRROR. A third-party mirror was evaluated for M5 and left
// out: the historical one no longer resolves at all, and the reachable one
// publishes four decimals where the BCV publishes eight, under a date field that
// is not the Fecha Valor. A value is added here when a source is actually
// implemented, not in anticipation of one.
func Sources() []Source { return []Source{SourceManual, SourceBCVOfficial} }

// Valid reports whether s is a source this binary knows how to store.
func (s Source) Valid() bool {
	for _, known := range Sources() {
		if s == known {
			return true
		}
	}
	return false
}

// Precedence orders sources when more than one has spoken about THE SAME DAY.
// Lower wins.
//
// MANUAL above BCV_OFFICIAL: if the user has taken the trouble to state a rate
// for a day, they are correcting or overriding what was published, and the
// application must not quietly prefer the publication.
//
// This ordering is applied ONLY within one effective_date, and that restriction
// is the whole decision. Applied across dates, a manual rate typed once in
// January would shadow every publication for the rest of the year and freeze the
// user's net worth with no symptom at all. Scoped to its own day, a manual rate
// wins exactly where it claims to apply and nowhere else.
func (s Source) Precedence() int {
	for i, known := range Sources() {
		if s == known {
			return i
		}
	}
	// An unknown source sorts last. Unreachable through the schema, which
	// whitelists the column, and handled rather than assumed so that a row
	// written by hand cannot silently outrank a real one.
	return len(Sources())
}

// IsAutomatic reports whether this source is fetched rather than typed. It is
// what decides both whether fetched_at is required and whether the row may be
// edited.
func (s Source) IsAutomatic() bool { return s != SourceManual }

// Rate is one stored observation: what a source said a pair was worth on a day.
//
// It is an OBSERVATION, not a truth. Several can exist for one day, and the
// application chooses between them by precedence rather than by overwriting, so
// that evidence is never destroyed.
type Rate struct {
	ID int64

	// BaseCurrencyCode and QuoteCurrencyCode carry the orientation:
	// 1 Base = Value Quote.
	BaseCurrencyCode  string
	QuoteCurrencyCode string

	// EffectiveDate is the ECONOMIC date: the BCV's Fecha Valor, or the day a
	// manually entered rate applies to. It is not the day the rate was fetched,
	// and the two disagree in both directions -- the BCV keeps one Fecha Valor
	// across a weekend, and publishes on a Friday a rate whose Fecha Valor is
	// the following week.
	EffectiveDate time.Time

	// Value is the rate itself, exact, at the scale the source published.
	Value money.Decimal

	Source Source

	// IsActive is false for an archived observation. Archiving is how a manual
	// correction is undone: valuation then falls back to whatever an automatic
	// source published for that day. Nothing is ever deleted.
	IsActive bool

	// FetchedAt is when this application saw the value. Present for an automatic
	// source, absent for a manual one, and the database enforces both halves.
	FetchedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Pair is the unordered identity of the two currencies an observation relates,
// used to group candidates when a cross rate is being built.
func (r Rate) Pair() string { return r.BaseCurrencyCode + "/" + r.QuoteCurrencyCode }

// Method names how a conversion factor was arrived at, so that a screen can say
// it rather than presenting every number as though it had been published.
type Method string

const (
	// MethodIdentity is a currency into itself: exactly 1, with no observation
	// involved. No row for USD/USD exists or ever should.
	MethodIdentity Method = "IDENTITY"

	// MethodDirect used one observation, in the orientation it was published in.
	MethodDirect Method = "DIRECT"

	// MethodInverted used one observation, mathematically inverted. The inverse
	// is computed, never stored: 1/842.2067 is not a terminating decimal, so a
	// stored inverse would be a rounded second copy of a fact that could then
	// disagree with the first.
	MethodInverted Method = "INVERTED"

	// MethodCross used two observations sharing a pivot currency, divided. The
	// result is not stored either, for the same reason and one more: it is a
	// function of two rows, so a stored copy could contradict both.
	MethodCross Method = "CROSS"
)

// Leg is one observation as used in a resolution, and whether it was inverted to
// point the way the conversion needed.
type Leg struct {
	Observation Rate
	Inverted    bool
}

// Resolution is a conversion factor together with the complete account of where
// it came from.
//
// The traceability is not decoration. A consolidated net worth is a number the
// user cannot check by hand, so every component has to be able to say which
// observations produced it, from which source, effective on which day, and
// whether anything was inverted or divided along the way.
type Resolution struct {
	FromCurrencyCode string
	ToCurrencyCode   string
	Method           Method

	// Legs is empty for MethodIdentity, holds one observation for MethodDirect
	// and MethodInverted, and two for MethodCross.
	Legs []Leg

	// rate is unexported and handed out only as a copy, for the same reason
	// money.Decimal hides its big.Int: a *big.Rat is mutable, and the math/big
	// API mutates in place, so returning the field would let one caller's
	// arithmetic rewrite the factor every other caller is about to use.
	rate *big.Rat
}

// Rate returns the conversion factor as a NEW *big.Rat: multiply an amount in
// FromCurrencyCode by this to get the amount in ToCurrencyCode.
func (r Resolution) Rate() *big.Rat {
	if r.rate == nil {
		return big.NewRat(1, 1)
	}
	return new(big.Rat).Set(r.rate)
}

// EffectiveDate is the date the resolution is anchored at: the leg's date, or
// for a cross the OLDER of the two, because a chain is only as current as its
// stalest link. It is the zero time for an identity, which involves no
// observation at all.
func (r Resolution) EffectiveDate() time.Time {
	var oldest time.Time
	for _, leg := range r.Legs {
		if oldest.IsZero() || leg.Observation.EffectiveDate.Before(oldest) {
			oldest = leg.Observation.EffectiveDate
		}
	}
	return oldest
}

// UsesManualOverride reports whether any leg came from a person.
//
// The interface shows this, because a valuation that silently preferred a typed
// rate over the published one would be impossible to debug from the screen.
func (r Resolution) UsesManualOverride() bool {
	for _, leg := range r.Legs {
		if leg.Observation.Source == SourceManual {
			return true
		}
	}
	return false
}

// CreateManualInput is the input to Service.CreateManual.
//
// There is deliberately NO Source field. The backend sets MANUAL, so a form
// submission cannot claim to be the Banco Central. The same reasoning that keeps
// a movement's sign out of ledger's inputs: an invalid combination should have
// no signature to arrive through.
type CreateManualInput struct {
	BaseCurrencyCode  string
	QuoteCurrencyCode string

	// Rate is a decimal string in the canonical format, the same way every
	// amount crosses this boundary. It is parsed exactly and never through a
	// float.
	Rate string

	// EffectiveDate is YYYY-MM-DD: the day this rate applies to, which is a
	// calendar date and not an instant.
	EffectiveDate string
}

// UpdateManualInput is the input to Service.UpdateManual.
//
// It carries only the rate. The pair and the effective date are the IDENTITY of
// an observation -- changing them does not correct this observation, it names a
// different one -- so there is no signature through which they could be
// modified. The same treatment currencies.code and categories.category_type get.
type UpdateManualInput struct {
	Rate string
}

// ListInput selects which observations to return.
type ListInput struct {
	// BaseCurrencyCode and QuoteCurrencyCode filter to one pair when both are
	// set. Empty means every pair.
	BaseCurrencyCode  string
	QuoteCurrencyCode string

	// IncludeArchived brings back observations whose IsActive is false, which
	// valuation ignores.
	IncludeArchived bool

	// Limit bounds the result. Zero means the default.
	Limit int
}

// RefreshResult is what a fetch reports back, in terms a person can read.
type RefreshResult struct {
	Source Source

	// EffectiveDate is the publication's own date -- its Fecha Valor -- not the
	// day the fetch happened.
	EffectiveDate time.Time

	FetchedAt time.Time

	// Stored counts the observations written. Skipped counts the ones the
	// provider returned for currencies this application does not have, which are
	// dropped rather than silently creating a currency.
	Stored  int
	Skipped []string

	// Rates is what was stored, so the caller can show it without a second
	// query.
	Rates []Rate
}

const (
	codeLength      = 3
	defaultListSize = 200
	maxListSize     = 1000
)

// normalizeCode puts a currency code into canonical form, so what is stored is
// canonical regardless of how it was typed.
func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func validateCode(code string) error {
	if len(code) != codeLength {
		return fmt.Errorf("%w: currency code %q must be exactly %d letters",
			ErrInvalidInput, code, codeLength)
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return fmt.Errorf("%w: currency code %q must contain only letters",
				ErrInvalidInput, code)
		}
	}
	return nil
}

// validatePair checks both codes and that they differ.
func validatePair(base, quote string) error {
	if err := validateCode(base); err != nil {
		return err
	}
	if err := validateCode(quote); err != nil {
		return err
	}
	if base == quote {
		return fmt.Errorf("%w: %s", ErrSameCurrency, base)
	}
	return nil
}

// parseRate reads the decimal a caller supplied and insists it is a rate.
//
// Positivity is checked HERE, in Go, rather than only by the database: the
// column is TEXT, and the only way SQLite could compare it numerically is a CAST
// to REAL, which would put a float in the one place this project refuses to have
// one. The CHECK in the schema catches zero honestly by stripping the point and
// the leading zeros; everything beyond that -- canonical form, scale bounds -- is
// this function's job.
func parseRate(raw string) (money.Decimal, error) {
	value, err := money.ParseDecimal(raw)
	if err != nil {
		return money.Decimal{}, fmt.Errorf("%w: %s", ErrInvalidRate, err)
	}
	if !value.IsPositive() {
		return money.Decimal{}, fmt.Errorf("%w: a rate must be greater than zero, got %s",
			ErrInvalidRate, value)
	}
	return value, nil
}

// parseEffectiveDate reads the calendar date an observation applies to.
func parseEffectiveDate(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("%w: an effective date is required, as YYYY-MM-DD",
			ErrInvalidInput)
	}
	date, err := database.ParseDate(trimmed)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q is not a date, expected YYYY-MM-DD",
			ErrInvalidInput, trimmed)
	}
	return date, nil
}
