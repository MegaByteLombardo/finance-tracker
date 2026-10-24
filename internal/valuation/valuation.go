// Package valuation expresses everything the user holds in one currency.
//
// It answers one question -- "what was all of this worth in my base currency at
// the close of a given day?" -- by combining three things it does not own: the
// movements the ledger records, the reference rates fx resolves, and the
// valuation base the user chose in settings. "Right now" is that question asked
// about today; a chart is that question asked about every day of a range, by the
// same code (TD-065).
//
// # Nothing here is stored
//
// There is no current_net_worth column, no converted_balance, no cached total,
// and no in-memory cache either. The net worth is a FUNCTION of the ledger, the
// rates and the base currency, recomputed on every read. A stored copy would be
// a fourth thing that could disagree with the three that produce it, and the
// first time it disagreed nobody would know which one was right.
//
// # Why this is its own package
//
// It reads from ledger, fx and settings, which do not know about each other. It
// is not in internal/application, because that package is for WRITES that must
// span two domains atomically (TD-030) and this writes nothing. It is not inside
// fx, because that would make the package that owns exchange rates depend on the
// ledger.
package valuation

import (
	"sort"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
)

// NetWorth is everything the user holds, expressed in one currency.
type NetWorth struct {
	// BaseCurrencyCode is the valuation base: the currency the total is
	// expressed in. It is the application's setting, not one half of a rate
	// pair, and the two are never called the same thing in this codebase.
	BaseCurrencyCode string
	BaseCurrencyName string
	BaseSymbol       *string
	BaseMinorUnit    int

	// ValuationDate is the date the rates were looked up for. It is a business
	// date in Venezuela, not a UTC instant: at nine in the evening in Caracas,
	// UTC is already tomorrow, and valuing today's balances with tomorrow's date
	// would reach for rates that have not taken effect.
	ValuationDate time.Time

	// TotalMinor is the sum of the components that could be valued, in the base
	// currency's minor units. It is an integer, like every other amount in this
	// application.
	//
	// When Complete is false this is a PARTIAL total, and presenting it as
	// though it were the user's net worth would be a lie. The interface must
	// show the warning alongside it.
	TotalMinor     int64
	TotalFormatted string

	// AssetsMinor and LiabilitiesMinor split the total by the nature of the
	// accounts (M8.1, TD-089): the converted lines of ASSET accounts summed, and
	// the converted lines of LIABILITY accounts summed and negated, so that what
	// is owed is a positive figure. AssetsMinor - LiabilitiesMinor ==
	// TotalMinor, exactly, because both are sums of the same allocated lines
	// (TD-070) -- the total is not computed a second way.
	//
	// An overdrawn bank account lowers AssetsMinor rather than counting as a
	// liability, and a card holding a credit lowers LiabilitiesMinor: what an
	// account IS does not change with the sign of its balance. Lines that could
	// not be valued are in neither, as they are not in the total.
	AssetsMinor          int64
	AssetsFormatted      string
	LiabilitiesMinor     int64
	LiabilitiesFormatted string

	// Complete is false when at least one currency could not be valued.
	//
	// A missing rate is reported rather than treated as an error, so the user
	// sees what IS known plus an explicit statement of what is not, with
	// somewhere to go and fix it. It is never treated as zero: a currency valued
	// at nothing would silently shrink the total, which is the one outcome worse
	// than an incomplete one.
	Complete bool

	// Components is one entry per currency held by an account in Accounts,
	// ordered by currency code.
	Components []Component

	// Missing names the conversions that could not be resolved, so the interface
	// can say "there is no USD rate for the bolivars in Banesco" and offer the
	// form that fixes it.
	Missing []MissingRate

	// Accounts is the same total broken down by account, ordered by currency
	// code, then name, then id. An account appears when it is active or holds a
	// non-zero balance on the valuation date: an archived account is valued like
	// any other (TD-064), and one that is archived AND empty has nothing to say.
	//
	// For each currency, the accounts' converted figures add up exactly to that
	// currency's component, and the components add up to the total (TD-070).
	Accounts []AccountComponent

	// ArchivedWithBalance counts the archived accounts in Accounts whose balance
	// is not zero. They are counted in the total -- archiving is not a financial
	// event -- and a screen should say so, because the user may have archived
	// an account believing it was empty.
	ArchivedWithBalance int

	// FutureMovementCount is how many movements are dated after ValuationDate.
	// They exist in the ledger and are not part of this figure yet (TD-063).
	FutureMovementCount int
}

// AccountComponent is one account's contribution to the total.
type AccountComponent struct {
	AccountID   int64
	AccountName string
	AccountType string
	// Nature is whether the account holds money or owes it, derived from its
	// type (TD-089).
	Nature      account.Nature
	Institution *string

	// IsActive is false for an archived account. It does not change how the
	// account is valued.
	IsActive bool

	CurrencyCode   string
	CurrencySymbol *string
	MinorUnit      int

	// BalanceMinor is the account's balance on the valuation date, in its own
	// currency's minor units: the movements dated on or before that date.
	BalanceMinor     int64
	BalanceFormatted string

	// ConvertedMinor is this account's share of its currency's converted
	// component, allocated by largest remainder so the accounts of one currency
	// add up to it exactly. Meaningful only when Valued is true.
	ConvertedMinor     int64
	ConvertedFormatted string
	Valued             bool

	// Share is ConvertedMinor as a part of the ASSETS (M8.1). It is defined
	// only for an asset account, when the total is complete and the assets are
	// greater than zero; a debt is not a part of what the user holds. See Share.
	Share Share
}

// Share is a part of the total, in basis points: 10,000 is the whole.
//
// It is defined only when the total is complete and greater than zero. A share
// of a partial total would be a share of the wrong number, and a share of zero
// or of a negative net worth means nothing a percentage can say. Shares that
// are defined add up to exactly 10,000 (TD-070).
type Share struct {
	BasisPoints int64
	Defined     bool
}

// Point is the net worth at the close of one day.
type Point struct {
	Date time.Time

	TotalMinor     int64
	TotalFormatted string

	// Complete is false when some currency held that day could not be valued.
	// The total is then partial, and a chart must draw a GAP here rather than
	// a point (TD-067).
	Complete bool
	Missing  []MissingRate
}

// Series is the net worth at the close of every day in a range, inclusive.
type Series struct {
	BaseCurrencyCode string
	BaseCurrencyName string
	BaseSymbol       *string
	BaseMinorUnit    int

	From time.Time
	To   time.Time

	// Points holds one entry per calendar day from From to To, in order.
	Points []Point

	// IncompleteDays counts the points whose Complete is false, so a screen
	// can say "faltan tasas para 40 días" without counting them itself.
	IncompleteDays int
}

// Gap is a run of consecutive days on which one currency could not be valued
// into the base. It answers "which rates are missing", where IncompleteDays
// only says how many days are affected.
type Gap struct {
	// FromCurrencyCode is the currency held and ToCurrencyCode the valuation
	// base, as in MissingRate.
	FromCurrencyCode string
	ToCurrencyCode   string

	// From and To are the first and last day of the run, inclusive.
	From time.Time
	To   time.Time
	Days int
}

// Gaps groups the series' missing rates into runs of consecutive days, one
// per currency, ordered by first day and then by currency. Two currencies
// missing on the same days are two gaps: each needs its own rate.
//
// Under the lookup rule (latest effective_date <= day), an observation dated
// a gap's From covers that whole run.
func (s Series) Gaps() []Gap {
	var gaps []Gap
	open := make(map[string]int) // currency -> index into gaps of its open run

	for _, point := range s.Points {
		seen := make(map[string]bool, len(point.Missing))
		for _, missing := range point.Missing {
			code := missing.FromCurrencyCode
			seen[code] = true
			if i, ok := open[code]; ok {
				gaps[i].To = point.Date
				gaps[i].Days++
				continue
			}
			open[code] = len(gaps)
			gaps = append(gaps, Gap{
				FromCurrencyCode: code,
				ToCurrencyCode:   missing.ToCurrencyCode,
				From:             point.Date,
				To:               point.Date,
				Days:             1,
			})
		}
		for code := range open {
			if !seen[code] {
				delete(open, code)
			}
		}
	}

	sort.SliceStable(gaps, func(i, j int) bool {
		if !gaps[i].From.Equal(gaps[j].From) {
			return gaps[i].From.Before(gaps[j].From)
		}
		return gaps[i].FromCurrencyCode < gaps[j].FromCurrencyCode
	})
	return gaps
}

// Component is one currency's contribution to the total.
type Component struct {
	CurrencyCode   string
	CurrencyName   string
	CurrencySymbol *string
	MinorUnit      int

	// AccountCount is how many accounts make up this currency's balance.
	AccountCount int

	// BalanceMinor is the ledger's own figure, untouched: the exact sum of the
	// movements in this currency, in ITS minor units.
	BalanceMinor     int64
	BalanceFormatted string

	// ConvertedMinor is that balance in the base currency's minor units, and is
	// meaningful only when Valued is true.
	//
	// It is rounded exactly ONCE, here, from the exact per-currency total --
	// never per account and then summed, which would accumulate a half-unit of
	// error per account and produce a screen whose lines do not add up to its
	// own total.
	ConvertedMinor     int64
	ConvertedFormatted string

	// Valued is false when no rate could be found. The component is still
	// listed, with its own balance intact, because the user holds that money
	// whether or not this application can price it.
	Valued bool

	// Resolution is how the conversion factor was arrived at: identity, a direct
	// observation, an inverted one, or a cross through a pivot, with every
	// observation it used. It is nil when Valued is false.
	//
	// This is what makes a consolidated figure checkable. Nobody can verify a
	// net worth by hand, so every component has to be able to say which source
	// said what, effective on which day.
	//
	// A zero balance is valued at exactly zero without needing a rate
	// (TD-067), so it may be Valued with a nil Resolution when no rate exists.
	Resolution *fx.Resolution

	// Share is ConvertedMinor as a part of the total.
	Share Share
}

// MissingRate names a conversion that could not be resolved.
type MissingRate struct {
	// FromCurrencyCode is the currency that could not be valued, and
	// ToCurrencyCode is the valuation base.
	FromCurrencyCode string
	ToCurrencyCode   string

	// BalanceMinor is what is sitting unvalued, so the interface can say how
	// much is at stake rather than only that something is.
	BalanceMinor     int64
	BalanceFormatted string
}
