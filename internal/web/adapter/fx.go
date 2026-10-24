package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/settings"
	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// The M5 half of the translation layer.
//
// internal/web speaks its own vocabulary rather than importing the domain, so
// this is the only place that knows both. It converts values in both directions
// and maps every domain sentinel to the web one a handler matches on.

// --- exchange rates -------------------------------------------------------

type exchangeRateAdapter struct {
	svc *fx.Service

	// now decides whether an observation is still in the future, which is a
	// presentation fact rather than a stored one.
	now func() time.Time
}

func (a *exchangeRateAdapter) List(ctx context.Context, in web.ListExchangeRatesInput) ([]web.ExchangeRate, error) {
	list, err := a.svc.List(ctx, fx.ListInput{
		BaseCurrencyCode:  in.BaseCurrencyCode,
		QuoteCurrencyCode: in.QuoteCurrencyCode,
		IncludeArchived:   in.IncludeArchived,
		Limit:             in.Limit,
	})
	if err != nil {
		return nil, TranslateRateError(err)
	}
	return a.toWebRates(list), nil
}

func (a *exchangeRateAdapter) Get(ctx context.Context, id int64) (web.ExchangeRate, error) {
	rate, err := a.svc.Get(ctx, id)
	if err != nil {
		return web.ExchangeRate{}, TranslateRateError(err)
	}
	return a.toWebRate(rate), nil
}

func (a *exchangeRateAdapter) CreateManual(ctx context.Context, in web.CreateManualRateInput) (web.ExchangeRate, error) {
	// The source is NOT taken from the input, because the input has no field
	// for it. fx.CreateManualInput does not either: MANUAL is set by the
	// service, so nothing arriving over HTTP can claim to be an official
	// publication.
	created, err := a.svc.CreateManual(ctx, fx.CreateManualInput{
		BaseCurrencyCode:  in.BaseCurrencyCode,
		QuoteCurrencyCode: in.QuoteCurrencyCode,
		Rate:              in.Rate,
		EffectiveDate:     in.EffectiveDate,
	})
	if err != nil {
		return web.ExchangeRate{}, TranslateRateError(err)
	}
	return a.toWebRate(created), nil
}

func (a *exchangeRateAdapter) UpdateManual(ctx context.Context, id int64, in web.UpdateManualRateInput) (web.ExchangeRate, error) {
	updated, err := a.svc.UpdateManual(ctx, id, fx.UpdateManualInput{Rate: in.Rate})
	if err != nil {
		return web.ExchangeRate{}, TranslateRateError(err)
	}
	return a.toWebRate(updated), nil
}

func (a *exchangeRateAdapter) SetManualActive(ctx context.Context, id int64, isActive bool) (web.ExchangeRate, error) {
	updated, err := a.svc.SetManualActive(ctx, id, isActive)
	if err != nil {
		return web.ExchangeRate{}, TranslateRateError(err)
	}
	return a.toWebRate(updated), nil
}

func (a *exchangeRateAdapter) RefreshNow(ctx context.Context) (web.RefreshRatesResult, error) {
	result, err := a.svc.RefreshFromProvider(ctx)
	if err != nil {
		return web.RefreshRatesResult{}, TranslateRateError(err)
	}

	return web.RefreshRatesResult{
		Source:        web.RateSource(result.Source),
		EffectiveDate: result.EffectiveDate,
		FetchedAt:     result.FetchedAt,
		Stored:        result.Stored,
		Skipped:       result.Skipped,
		Rates:         a.toWebRates(result.Rates),
	}, nil
}

func (a *exchangeRateAdapter) ProviderEnabled() bool { return a.svc.HasProvider() }

func (a *exchangeRateAdapter) toWebRates(list []fx.Rate) []web.ExchangeRate {
	out := make([]web.ExchangeRate, 0, len(list))
	for _, rate := range list {
		out = append(out, a.toWebRate(rate))
	}
	return out
}

func (a *exchangeRateAdapter) toWebRate(rate fx.Rate) web.ExchangeRate {
	source := web.RateSource(rate.Source)

	return web.ExchangeRate{
		ID:                rate.ID,
		BaseCurrencyCode:  rate.BaseCurrencyCode,
		QuoteCurrencyCode: rate.QuoteCurrencyCode,
		Pair:              rate.Pair(),
		EffectiveDate:     rate.EffectiveDate,
		// The canonical string, at the precision the source published. It is
		// never reformatted on the way out: the trailing zeros of
		// "842.20670000" record how precise the statement was.
		Rate:        rate.Value.String(),
		Source:      source,
		SourceLabel: source.Label(),
		// Only what a person typed may be corrected. An official row records
		// what was published.
		IsEditable: rate.Source == fx.SourceManual,
		IsActive:   rate.IsActive,
		FetchedAt:  rate.FetchedAt,
		IsUpcoming: rate.EffectiveDate.After(database.TodayAt(a.now())),
	}
}

// --- settings -------------------------------------------------------------

type settingsAdapter struct {
	svc *settings.Service
}

func (a *settingsAdapter) Get(ctx context.Context) (web.AppSettings, error) {
	current, err := a.svc.Get(ctx)
	if err != nil {
		return web.AppSettings{}, TranslateSettingsError(err)
	}
	return web.AppSettings{
		BaseCurrencyCode: current.BaseCurrencyCode,
		UpdatedAt:        current.UpdatedAt,
	}, nil
}

func (a *settingsAdapter) SetBaseCurrency(ctx context.Context, code string) (web.AppSettings, error) {
	updated, err := a.svc.SetBaseCurrency(ctx, code)
	if err != nil {
		return web.AppSettings{}, TranslateSettingsError(err)
	}
	return web.AppSettings{
		BaseCurrencyCode: updated.BaseCurrencyCode,
		UpdatedAt:        updated.UpdatedAt,
	}, nil
}

// --- valuation ------------------------------------------------------------

type valuationAdapter struct {
	svc *valuation.Service
}

func (a *valuationAdapter) GetCurrentNetWorth(ctx context.Context) (web.NetWorth, error) {
	worth, err := a.svc.GetCurrentNetWorth(ctx)
	if err != nil {
		return web.NetWorth{}, TranslateValuationError(err)
	}
	return toWebNetWorth(worth), nil
}

// toWebNetWorth carries a valuation across unchanged. Every figure in it was
// computed by internal/valuation; nothing here adds anything up.
func toWebNetWorth(worth valuation.NetWorth) web.NetWorth {
	out := web.NetWorth{
		BaseCurrencyCode:     worth.BaseCurrencyCode,
		BaseCurrencyName:     worth.BaseCurrencyName,
		BaseSymbol:           worth.BaseSymbol,
		BaseMinorUnit:        worth.BaseMinorUnit,
		ValuationDate:        worth.ValuationDate,
		TotalMinor:           worth.TotalMinor,
		TotalFormatted:       worth.TotalFormatted,
		AssetsMinor:          worth.AssetsMinor,
		AssetsFormatted:      worth.AssetsFormatted,
		LiabilitiesMinor:     worth.LiabilitiesMinor,
		LiabilitiesFormatted: worth.LiabilitiesFormatted,
		Complete:             worth.Complete,
		Components:           make([]web.NetWorthComponent, 0, len(worth.Components)),
		Accounts:             make([]web.NetWorthAccount, 0, len(worth.Accounts)),
		ArchivedWithBalance:  worth.ArchivedWithBalance,
		FutureMovementCount:  worth.FutureMovementCount,
	}

	for _, component := range worth.Components {
		converted := web.NetWorthComponent{
			CurrencyCode:       component.CurrencyCode,
			CurrencyName:       component.CurrencyName,
			CurrencySymbol:     component.CurrencySymbol,
			MinorUnit:          component.MinorUnit,
			AccountCount:       component.AccountCount,
			BalanceMinor:       component.BalanceMinor,
			BalanceFormatted:   component.BalanceFormatted,
			ConvertedMinor:     component.ConvertedMinor,
			ConvertedFormatted: component.ConvertedFormatted,
			Valued:             component.Valued,
			Share:              toWebShare(component.Share),
		}

		if component.Resolution != nil {
			resolution := toWebResolution(*component.Resolution)
			converted.Resolution = &resolution

			// "Rates as of" is the OLDEST observation behind any component, so
			// the phrase on the screen is true of the whole figure rather than
			// of its freshest part.
			if anchor := component.Resolution.EffectiveDate(); !anchor.IsZero() {
				if out.RatesAsOf.IsZero() || anchor.Before(out.RatesAsOf) {
					out.RatesAsOf = anchor
				}
			}
		}

		out.Components = append(out.Components, converted)
	}

	for _, line := range worth.Accounts {
		out.Accounts = append(out.Accounts, web.NetWorthAccount{
			AccountID:          line.AccountID,
			AccountName:        line.AccountName,
			AccountType:        line.AccountType,
			Nature:             string(line.Nature),
			Institution:        line.Institution,
			IsActive:           line.IsActive,
			CurrencyCode:       line.CurrencyCode,
			CurrencySymbol:     line.CurrencySymbol,
			BalanceMinor:       line.BalanceMinor,
			BalanceFormatted:   line.BalanceFormatted,
			ConvertedMinor:     line.ConvertedMinor,
			ConvertedFormatted: line.ConvertedFormatted,
			Valued:             line.Valued,
			Share:              toWebShare(line.Share),
		})
	}

	for _, missing := range worth.Missing {
		out.Missing = append(out.Missing, web.MissingRateView{
			FromCurrencyCode: missing.FromCurrencyCode,
			ToCurrencyCode:   missing.ToCurrencyCode,
			BalanceFormatted: missing.BalanceFormatted,
		})
	}

	return out
}

// toWebShare formats basis points as a percentage with two decimals. It is
// formatting, not arithmetic: 724 bp is the string "7.24".
func toWebShare(share valuation.Share) web.Ratio {
	if !share.Defined {
		return web.Ratio{}
	}
	return web.Ratio{BasisPoints: share.BasisPoints, Defined: true, Formatted: money.Format(share.BasisPoints, 2)}
}

func toWebResolution(resolution fx.Resolution) web.RateResolutionView {
	method := web.RateMethod(resolution.Method)

	out := web.RateResolutionView{
		Method:             method,
		MethodLabel:        method.Label(),
		EffectiveDate:      resolution.EffectiveDate(),
		UsesManualOverride: resolution.UsesManualOverride(),
		Legs:               make([]web.RateLegView, 0, len(resolution.Legs)),
	}

	for _, leg := range resolution.Legs {
		source := web.RateSource(leg.Observation.Source)
		out.Legs = append(out.Legs, web.RateLegView{
			BaseCurrencyCode:  leg.Observation.BaseCurrencyCode,
			QuoteCurrencyCode: leg.Observation.QuoteCurrencyCode,
			Pair:              leg.Observation.Pair(),
			Rate:              leg.Observation.Value.String(),
			EffectiveDate:     leg.Observation.EffectiveDate,
			Source:            source,
			SourceLabel:       source.Label(),
			Inverted:          leg.Inverted,
		})
	}
	return out
}

// --- error translation ----------------------------------------------------

// TranslateRateError maps fx's sentinels onto the web's.
//
// A handler matches on web errors only, so that the presentation layer never has
// to import a domain package to work out what it was told.
func TranslateRateError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, fx.ErrNotFound):
		return fmt.Errorf("%w: %s", web.ErrRateNotFound, err)
	case errors.Is(err, fx.ErrConflict):
		return fmt.Errorf("%w: %s", web.ErrRateConflict, err)
	case errors.Is(err, fx.ErrSameCurrency):
		return fmt.Errorf("%w: %s", web.ErrSameCurrencyPair, err)
	case errors.Is(err, fx.ErrNotEditable):
		return fmt.Errorf("%w: %s", web.ErrRateNotEditable, err)
	case errors.Is(err, fx.ErrCurrencyNotFound):
		return fmt.Errorf("%w: %s", web.ErrCurrencyNotFound, err)
	case errors.Is(err, fx.ErrInvalidRate):
		return fmt.Errorf("%w: %s", web.ErrInvalidRate, err)
	case errors.Is(err, fx.ErrInvalidInput):
		return fmt.Errorf("%w: %s", web.ErrInvalidInput, err)
	// The two provider failures stay distinct all the way to the handler,
	// because they call for different words on the screen: one says the site is
	// down, the other says this application no longer understands it.
	case errors.Is(err, fx.ErrProviderInvalidResponse):
		return fmt.Errorf("%w: %s", web.ErrProviderInvalidResponse, err)
	case errors.Is(err, fx.ErrProviderUnavailable):
		return fmt.Errorf("%w: %s", web.ErrProviderUnavailable, err)
	case errors.Is(err, fx.ErrRateUnavailable):
		return fmt.Errorf("%w: %s", web.ErrRateNotFound, err)
	default:
		return err
	}
}

// TranslateSettingsError maps settings' sentinels onto the web's.
func TranslateSettingsError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, settings.ErrCurrencyNotFound):
		return fmt.Errorf("%w: %s", web.ErrCurrencyNotFound, err)
	case errors.Is(err, settings.ErrCurrencyInactive):
		return fmt.Errorf("%w: %s", web.ErrCurrencyInactive, err)
	case errors.Is(err, settings.ErrInvalidInput):
		return fmt.Errorf("%w: %s", web.ErrInvalidInput, err)
	case errors.Is(err, settings.ErrMissing):
		// Not a validation failure: migration 00012 creates this row and a
		// CHECK makes a second impossible, so its absence means the database
		// was edited by hand. It deserves a 500, not a message on a form.
		return fmt.Errorf("%w: %s", web.ErrBaseCurrencyUnavailable, err)
	default:
		return err
	}
}

// TranslateValuationError maps valuation's sentinels onto the web's.
func TranslateValuationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, valuation.ErrBaseCurrencyUnavailable) || errors.Is(err, currency.ErrNotFound) {
		return fmt.Errorf("%w: %s", web.ErrBaseCurrencyUnavailable, err)
	}
	return err
}
