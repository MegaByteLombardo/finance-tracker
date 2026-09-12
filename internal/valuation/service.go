package valuation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/settings"
)

// Errors this package returns. Callers distinguish them with errors.Is.
var (
	// ErrBaseCurrencyUnavailable means the configured valuation base does not
	// exist as a currency, or the settings row that names it is gone.
	//
	// It is an integrity failure rather than a validation one: settings refuses
	// to store a base currency that does not exist, and a foreign key backs that
	// up, so reaching this means the database was edited by hand. It warrants a
	// 500, not a message on a form.
	ErrBaseCurrencyUnavailable = errors.New("the valuation base currency is unavailable")
)

// Service computes the consolidated net worth.
//
// It holds the three services it reads from rather than their repositories:
// valuing money is a question asked of the domain, not of the tables, and going
// through the services is what keeps the ledger's own rules -- which accounts
// count, how a balance is derived -- in one place.
type Service struct {
	ledger   *ledger.Service
	rates    *fx.Service
	settings *settings.Service

	// currencies resolves the base currency's name, symbol and minor unit, which
	// the total has to be formatted at and which no other input carries: the
	// user may hold nothing at all in their own base currency.
	currencies *currency.Service

	// now is injected so that a test can state the valuation date rather than
	// depend on the day it runs on.
	now func() time.Time
}

// NewService wires the three domains together.
func NewService(
	ledgerService *ledger.Service,
	rateService *fx.Service,
	settingsService *settings.Service,
	currencyService *currency.Service,
) *Service {
	return &Service{
		ledger:     ledgerService,
		rates:      rateService,
		settings:   settingsService,
		currencies: currencyService,
		now:        time.Now,
	}
}

// GetCurrentNetWorth values everything the user holds as of today.
//
// Today is the Venezuelan calendar date, which is the same business day the rate
// provider publishes against. Using a UTC date would, for four hours out of
// every twenty-four, ask for rates effective on a day that has not happened yet.
func (s *Service) GetCurrentNetWorth(ctx context.Context) (NetWorth, error) {
	return s.GetNetWorthAt(ctx, database.TodayAt(s.now()))
}

// GetNetWorthAt values everything the user holds as of a given date.
//
// The date parameter exists because it is the honest shape of this computation:
// a valuation is always "as of" something, and writing that into the signature
// now costs nothing. M5 calls it with today and draws no history; what a chart
// would need is a loop around this function, and that belongs to its own
// milestone with its own decisions about resolution and storage.
//
// # Rounding
//
// Each currency's balance is summed EXACTLY by the ledger, converted as an exact
// rational, and rounded once -- half to even -- into the base currency's minor
// units. The total is then the integer sum of those rounded components, with no
// further rounding, so the components a screen shows add up to the total that
// screen shows.
//
// # A missing rate is reported, not raised
//
// A currency with no path to the base does not fail this call. It comes back as
// an unvalued component and an entry in Missing, with Complete set to false. An
// error would deny the user the part of their net worth that IS known, and a
// zero would silently shrink the total.
func (s *Service) GetNetWorthAt(ctx context.Context, on time.Time) (NetWorth, error) {
	appSettings, err := s.settings.Get(ctx)
	if err != nil {
		if errors.Is(err, settings.ErrMissing) {
			return NetWorth{}, fmt.Errorf("%w: %s", ErrBaseCurrencyUnavailable, err)
		}
		return NetWorth{}, fmt.Errorf("reading the valuation base: %w", err)
	}

	base, err := s.currencies.Get(ctx, appSettings.BaseCurrencyCode)
	if err != nil {
		if errors.Is(err, currency.ErrNotFound) {
			return NetWorth{}, fmt.Errorf("%w: currency %q does not exist",
				ErrBaseCurrencyUnavailable, appSettings.BaseCurrencyCode)
		}
		return NetWorth{}, fmt.Errorf("reading the valuation base currency: %w", err)
	}

	// Active accounts only, which is what the dashboard has always shown. An
	// archived account is money the user has told this application to stop
	// counting.
	//
	// An active account in an ARCHIVED CURRENCY still counts, and its rate is
	// looked up normally. Archiving a currency retires it from new accounts; it
	// does not make the money in existing ones stop existing.
	totals, err := s.ledger.ListCurrencyTotals(ctx, false)
	if err != nil {
		return NetWorth{}, fmt.Errorf("reading currency totals: %w", err)
	}

	result := NetWorth{
		BaseCurrencyCode: base.Code,
		BaseCurrencyName: base.Name,
		BaseSymbol:       base.Symbol,
		BaseMinorUnit:    base.MinorUnit,
		ValuationDate:    on,
		Complete:         true,
		Components:       make([]Component, 0, len(totals)),
	}

	for _, total := range totals {
		component := Component{
			CurrencyCode:     total.CurrencyCode,
			CurrencyName:     total.CurrencyName,
			CurrencySymbol:   total.CurrencySymbol,
			MinorUnit:        total.MinorUnit,
			AccountCount:     total.AccountCount,
			BalanceMinor:     total.TotalMinor,
			BalanceFormatted: total.TotalFormatted,
		}

		resolution, err := s.rates.Resolve(ctx, total.CurrencyCode, base.Code, on)
		switch {
		case err == nil:
			// Resolved.
		case errors.Is(err, fx.ErrRateUnavailable), errors.Is(err, fx.ErrNotFound):
			result.Complete = false
			result.Missing = append(result.Missing, MissingRate{
				FromCurrencyCode: total.CurrencyCode,
				ToCurrencyCode:   base.Code,
				BalanceMinor:     total.TotalMinor,
				BalanceFormatted: total.TotalFormatted,
			})
			result.Components = append(result.Components, component)
			continue
		default:
			return NetWorth{}, fmt.Errorf("resolving %s to %s: %w",
				total.CurrencyCode, base.Code, err)
		}

		converted, err := money.ConvertMinor(
			total.TotalMinor, total.MinorUnit, base.MinorUnit, resolution.Rate())
		if err != nil {
			// Overflow, and nothing else: the rate is positive by construction
			// and both minor units come from the currencies table. A balance
			// this large is a real failure and is reported as one rather than
			// being folded in with "no rate", which would send the user to a
			// form that cannot help them.
			return NetWorth{}, fmt.Errorf("converting %s %s into %s: %w",
				total.TotalFormatted, total.CurrencyCode, base.Code, err)
		}

		component.Valued = true
		component.ConvertedMinor = converted
		component.ConvertedFormatted = money.Format(converted, base.MinorUnit)
		component.Resolution = &resolution

		result.TotalMinor += converted
		result.Components = append(result.Components, component)
	}

	result.TotalFormatted = money.Format(result.TotalMinor, base.MinorUnit)
	return result, nil
}
