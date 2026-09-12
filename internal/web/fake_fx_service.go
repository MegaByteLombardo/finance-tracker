package web

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// In-memory stand-ins for the M5 services, used by the presentation tests.
//
// They exist for the same reason the other fakes in this package do: the
// handlers are tested without a database, so that a template or a status code
// failing is not reported as a SQL error. They are not a substitute for the
// domain tests -- every rule they appear to implement is tested for real against
// SQLite in internal/fx and internal/valuation.

// FakeRateStore holds exchange rates, settings and a net worth in memory.
type FakeRateStore struct {
	mu sync.Mutex

	rates  map[int64]ExchangeRate
	nextID int64

	baseCurrency string
	updatedAt    time.Time

	// providerEnabled and providerErr let a test drive the refresh button's
	// failure paths, which are most of what is interesting about it.
	providerEnabled bool
	providerErr     error
	providerRates   []ExchangeRate
	effectiveDate   time.Time
	skipped         []string

	// netWorth is returned verbatim, because the arithmetic behind it is
	// internal/valuation's business and is tested there.
	netWorth    NetWorth
	netWorthErr error
}

// NewFakeRateStore returns a store with USD as the valuation base, which is what
// migration 00012 seeds.
func NewFakeRateStore() *FakeRateStore {
	return &FakeRateStore{
		rates:           make(map[int64]ExchangeRate),
		nextID:          1,
		baseCurrency:    "USD",
		updatedAt:       time.Now().UTC(),
		providerEnabled: true,
		effectiveDate:   time.Now().In(economicLocation).Truncate(24 * time.Hour),
		netWorth: NetWorth{
			BaseCurrencyCode: "USD",
			BaseMinorUnit:    2,
			TotalFormatted:   "0.00",
			Complete:         true,
		},
	}
}

// SetProviderEnabled switches the automatic source on or off.
func (f *FakeRateStore) SetProviderEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providerEnabled = enabled
}

// SetProviderError makes the next refresh fail with err.
func (f *FakeRateStore) SetProviderError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providerErr = err
}

// SetNetWorth replaces what the valuation service returns.
func (f *FakeRateStore) SetNetWorth(worth NetWorth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netWorth = worth
}

// SetNetWorthError makes the valuation fail.
func (f *FakeRateStore) SetNetWorthError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netWorthErr = err
}

// --- ExchangeRateService --------------------------------------------------

func (f *FakeRateStore) List(_ context.Context, in ListExchangeRatesInput) ([]ExchangeRate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]ExchangeRate, 0, len(f.rates))
	for _, rate := range f.rates {
		if !in.IncludeArchived && !rate.IsActive {
			continue
		}
		if in.BaseCurrencyCode != "" && in.QuoteCurrencyCode != "" &&
			(rate.BaseCurrencyCode != in.BaseCurrencyCode || rate.QuoteCurrencyCode != in.QuoteCurrencyCode) {
			continue
		}
		out = append(out, rate)
	}

	sort.Slice(out, func(i, j int) bool {
		if !out[i].EffectiveDate.Equal(out[j].EffectiveDate) {
			return out[i].EffectiveDate.After(out[j].EffectiveDate)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (f *FakeRateStore) Get(_ context.Context, id int64) (ExchangeRate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	rate, ok := f.rates[id]
	if !ok {
		return ExchangeRate{}, fmt.Errorf("exchange rate %d: %w", id, ErrRateNotFound)
	}
	return rate, nil
}

func (f *FakeRateStore) CreateManual(_ context.Context, in CreateManualRateInput) (ExchangeRate, error) {
	base := strings.ToUpper(strings.TrimSpace(in.BaseCurrencyCode))
	quote := strings.ToUpper(strings.TrimSpace(in.QuoteCurrencyCode))

	if len(base) != 3 || len(quote) != 3 {
		return ExchangeRate{}, fmt.Errorf("%w: a currency code is three letters", ErrInvalidInput)
	}
	if base == quote {
		return ExchangeRate{}, fmt.Errorf("%w: %s", ErrSameCurrencyPair, base)
	}
	if err := fakeValidateRate(in.Rate); err != nil {
		return ExchangeRate{}, err
	}
	effectiveDate, err := time.Parse("2006-01-02", strings.TrimSpace(in.EffectiveDate))
	if err != nil {
		return ExchangeRate{}, fmt.Errorf("%w: %q is not a date", ErrInvalidInput, in.EffectiveDate)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, existing := range f.rates {
		if existing.BaseCurrencyCode == base && existing.QuoteCurrencyCode == quote &&
			existing.EffectiveDate.Equal(effectiveDate) && existing.Source == RateSourceManual {
			return ExchangeRate{}, fmt.Errorf("%w: %s/%s", ErrRateConflict, base, quote)
		}
	}

	rate := ExchangeRate{
		ID:                f.nextID,
		BaseCurrencyCode:  base,
		QuoteCurrencyCode: quote,
		Pair:              base + "/" + quote,
		EffectiveDate:     effectiveDate,
		Rate:              strings.TrimSpace(in.Rate),
		// Set here, never taken from input: the same rule the real service
		// follows.
		Source:      RateSourceManual,
		SourceLabel: RateSourceManual.Label(),
		IsEditable:  true,
		IsActive:    true,
		IsUpcoming:  effectiveDate.After(time.Now().In(economicLocation).Truncate(24 * time.Hour)),
	}
	f.rates[rate.ID] = rate
	f.nextID++
	return rate, nil
}

func (f *FakeRateStore) UpdateManual(_ context.Context, id int64, in UpdateManualRateInput) (ExchangeRate, error) {
	if err := fakeValidateRate(in.Rate); err != nil {
		return ExchangeRate{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	rate, ok := f.rates[id]
	if !ok {
		return ExchangeRate{}, fmt.Errorf("exchange rate %d: %w", id, ErrRateNotFound)
	}
	if rate.Source != RateSourceManual {
		return ExchangeRate{}, fmt.Errorf("exchange rate %d: %w", id, ErrRateNotEditable)
	}

	rate.Rate = strings.TrimSpace(in.Rate)
	f.rates[id] = rate
	return rate, nil
}

func (f *FakeRateStore) SetManualActive(_ context.Context, id int64, isActive bool) (ExchangeRate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	rate, ok := f.rates[id]
	if !ok {
		return ExchangeRate{}, fmt.Errorf("exchange rate %d: %w", id, ErrRateNotFound)
	}
	if rate.Source != RateSourceManual {
		return ExchangeRate{}, fmt.Errorf("exchange rate %d: %w", id, ErrRateNotEditable)
	}

	rate.IsActive = isActive
	f.rates[id] = rate
	return rate, nil
}

func (f *FakeRateStore) RefreshNow(_ context.Context) (RefreshRatesResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.providerEnabled {
		return RefreshRatesResult{}, fmt.Errorf("%w: no provider", ErrProviderUnavailable)
	}
	if f.providerErr != nil {
		return RefreshRatesResult{}, f.providerErr
	}

	stored := make([]ExchangeRate, 0, len(f.providerRates))
	for _, rate := range f.providerRates {
		rate.ID = f.nextID
		rate.Source = RateSourceBCVOfficial
		rate.SourceLabel = RateSourceBCVOfficial.Label()
		rate.IsEditable = false
		rate.IsActive = true
		rate.EffectiveDate = f.effectiveDate
		rate.Pair = rate.BaseCurrencyCode + "/" + rate.QuoteCurrencyCode
		f.rates[rate.ID] = rate
		f.nextID++
		stored = append(stored, rate)
	}

	return RefreshRatesResult{
		Source:        RateSourceBCVOfficial,
		EffectiveDate: f.effectiveDate,
		FetchedAt:     time.Now().UTC(),
		Stored:        len(stored),
		Skipped:       f.skipped,
		Rates:         stored,
	}, nil
}

func (f *FakeRateStore) ProviderEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.providerEnabled
}

// fakeValidateRate mirrors the real service's refusals closely enough that a
// handler test exercising a bad rate gets the same error a real one would.
func fakeValidateRate(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%w: a rate is required", ErrInvalidRate)
	}
	if strings.ContainsAny(trimmed, ",+-eE ") {
		return fmt.Errorf("%w: %q is not a canonical decimal", ErrInvalidRate, raw)
	}

	var digits, points int
	for _, r := range trimmed {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			points++
		default:
			return fmt.Errorf("%w: %q is not a decimal", ErrInvalidRate, raw)
		}
	}
	if digits == 0 || points > 1 {
		return fmt.Errorf("%w: %q is not a decimal", ErrInvalidRate, raw)
	}
	if strings.Trim(strings.ReplaceAll(trimmed, ".", ""), "0") == "" {
		return fmt.Errorf("%w: a rate must be greater than zero", ErrInvalidRate)
	}
	return nil
}

// --- SettingsService ------------------------------------------------------

func (f *FakeRateStore) GetSettings(_ context.Context) (AppSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return AppSettings{BaseCurrencyCode: f.baseCurrency, UpdatedAt: f.updatedAt}, nil
}

func (f *FakeRateStore) SetBaseCurrency(_ context.Context, code string) (AppSettings, error) {
	normalised := strings.ToUpper(strings.TrimSpace(code))
	if len(normalised) != 3 {
		return AppSettings{}, fmt.Errorf("%w: a currency code is three letters", ErrInvalidInput)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.baseCurrency = normalised
	f.updatedAt = time.Now().UTC()
	f.netWorth.BaseCurrencyCode = normalised
	return AppSettings{BaseCurrencyCode: f.baseCurrency, UpdatedAt: f.updatedAt}, nil
}

// --- ValuationService -----------------------------------------------------

func (f *FakeRateStore) GetCurrentNetWorth(_ context.Context) (NetWorth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.netWorthErr != nil {
		return NetWorth{}, f.netWorthErr
	}
	return f.netWorth, nil
}

// fakeSettingsService adapts the store to SettingsService, whose Get collides
// with ExchangeRateService's.
type fakeSettingsService struct{ *FakeRateStore }

func (s fakeSettingsService) Get(ctx context.Context) (AppSettings, error) {
	return s.FakeRateStore.GetSettings(ctx)
}

var (
	_ ExchangeRateService = (*FakeRateStore)(nil)
	_ ValuationService    = (*FakeRateStore)(nil)
	_ SettingsService     = fakeSettingsService{}
)
