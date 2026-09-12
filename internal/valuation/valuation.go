// Package valuation expresses everything the user holds in one currency.
//
// It answers one question -- "what is all of this worth in my base currency
// right now?" -- by combining three things it does not own: the balances the
// ledger derives from movements, the reference rates fx resolves, and the
// valuation base the user chose in settings.
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
	"time"

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

	// Complete is false when at least one currency could not be valued.
	//
	// A missing rate is reported rather than treated as an error, so the user
	// sees what IS known plus an explicit statement of what is not, with
	// somewhere to go and fix it. It is never treated as zero: a currency valued
	// at nothing would silently shrink the total, which is the one outcome worse
	// than an incomplete one.
	Complete bool

	// Components is one entry per currency the user holds, in the order the
	// ledger returns them, which is by currency code.
	Components []Component

	// Missing names the conversions that could not be resolved, so the interface
	// can say "there is no USD rate for the bolivars in Banesco" and offer the
	// form that fixes it.
	Missing []MissingRate
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
	// observation it used. It is nil only when Valued is false.
	//
	// This is what makes a consolidated figure checkable. Nobody can verify a
	// net worth by hand, so every component has to be able to say which source
	// said what, effective on which day.
	Resolution *fx.Resolution
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
