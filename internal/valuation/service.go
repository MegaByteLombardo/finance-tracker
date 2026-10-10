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

	// ErrInvalidRange means a series was asked for a range that ends before it
	// starts, runs past the date its rates were loaded for, or is longer than
	// any history this application could hold.
	ErrInvalidRange = errors.New("invalid valuation range")
)

// Service computes the consolidated net worth, for one date or for a range.
//
// It holds the services it reads from rather than their repositories: valuing
// money is a question asked of the domain, not of the tables, and going through
// the services is what keeps the ledger's own rules -- what a balance is, which
// movements count on which day -- and fx's lookup policy in one place each.
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

// NewService wires the domains together.
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
// It is also the date the ledger's current balances are cut at (TD-063), so the
// figure here and the balances on the account screens describe the same day.
func (s *Service) GetCurrentNetWorth(ctx context.Context) (NetWorth, error) {
	return s.GetNetWorthAt(ctx, database.TodayAt(s.now()))
}

// GetNetWorthAt values everything the user held at the close of a date: the
// movements dated on or before it, at the rates in effect on it.
//
// # Rounding
//
// Each currency's balance is summed EXACTLY from the ledger, converted as an
// exact rational, and rounded once -- half to even -- into the base currency's
// minor units. The total is then the integer sum of those rounded components,
// with no further rounding, so the components a screen shows add up to the total
// that screen shows.
//
// # A missing rate is reported, not raised
//
// A currency with no path to the base does not fail this call. It comes back as
// an unvalued component and an entry in Missing, with Complete set to false. An
// error would deny the user the part of their net worth that IS known, and a
// zero would silently shrink the total.
func (s *Service) GetNetWorthAt(ctx context.Context, on time.Time) (NetWorth, error) {
	snapshot, err := s.Load(ctx, on)
	if err != nil {
		return NetWorth{}, err
	}
	return snapshot.NetWorthAt(on)
}

// GetNetWorthSeries values the user's holdings at the close of every day from
// one date to another, inclusive. It is GetNetWorthAt for each day, computed in
// one pass from one load (TD-065).
func (s *Service) GetNetWorthSeries(ctx context.Context, from, to time.Time) (Series, error) {
	snapshot, err := s.Load(ctx, to)
	if err != nil {
		return Series{}, err
	}
	return snapshot.Series(from, to)
}

// Load reads everything a valuation up to a date needs, with a constant number
// of queries, and returns it as a Snapshot that answers every later question in
// memory.
//
// It reads the settings, the currencies, every account (archived ones included,
// TD-064), the daily deltas of the whole ledger, and every active rate
// observation in effect on or before through. Nothing is read inside a database
// transaction: the pool has one connection, and the services this calls each use
// it (TD-065).
func (s *Service) Load(ctx context.Context, through time.Time) (*Snapshot, error) {
	appSettings, err := s.settings.Get(ctx)
	if err != nil {
		if errors.Is(err, settings.ErrMissing) {
			return nil, fmt.Errorf("%w: %s", ErrBaseCurrencyUnavailable, err)
		}
		return nil, fmt.Errorf("reading the valuation base: %w", err)
	}

	currencies, err := s.currencies.List(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("reading currencies: %w", err)
	}
	byCode := make(map[string]currency.Currency, len(currencies))
	for _, c := range currencies {
		byCode[c.Code] = c
	}
	base, ok := byCode[appSettings.BaseCurrencyCode]
	if !ok {
		return nil, fmt.Errorf("%w: currency %q does not exist",
			ErrBaseCurrencyUnavailable, appSettings.BaseCurrencyCode)
	}

	// Every account, archived or not. Only the metadata is used: the balances
	// this listing carries are the ledger's CURRENT ones, and a valuation at
	// another date rebuilds its own from the deltas below.
	accounts, err := s.ledger.ListAccountBalances(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("reading accounts: %w", err)
	}

	deltas, err := s.ledger.ListDailyAccountDeltas(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the ledger's daily deltas: %w", err)
	}

	book, err := s.rates.BookThrough(ctx, through)
	if err != nil {
		return nil, fmt.Errorf("reading exchange rates through %s: %w", database.FormatDate(through), err)
	}

	return newSnapshot(base, byCode, accounts, deltas, book, through)
}
