package web

import (
	"context"
	"sync"
	"time"
)

// FakeAnalytics is the in-memory stand-in for the M6 dashboard service.
//
// It computes nothing financial. By default it echoes the net worth the
// FakeRateStore holds -- so the M5 tests that set a net worth there still drive
// the dashboard -- flat across the period, with the fake ledger's accounts as
// an unvalued breakdown. A test that needs particular analytics sets them with
// Set. Every rule the real service applies is tested against SQLite in
// internal/analytics and internal/valuation.
type FakeAnalytics struct {
	mu sync.Mutex

	services *FakeServices

	dashboard     *DashboardAnalytics
	monthCashFlow *CashFlow
	err           error

	// Requested records the presets asked for, so a test can check what a
	// handler passed through.
	Requested []DashboardPreset
}

// Set makes Dashboard return this value, with the requested preset filled in.
func (f *FakeAnalytics) Set(d DashboardAnalytics) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dashboard = &d
}

// SetError makes Dashboard fail.
func (f *FakeAnalytics) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *FakeAnalytics) Dashboard(ctx context.Context, preset DashboardPreset) (DashboardAnalytics, error) {
	f.mu.Lock()
	f.Requested = append(f.Requested, preset)
	override, err := f.dashboard, f.err
	f.mu.Unlock()

	if err != nil {
		return DashboardAnalytics{}, err
	}
	if override != nil {
		out := *override
		out.Preset = preset
		return out, nil
	}

	worth, err := f.services.Rates.GetCurrentNetWorth(ctx)
	if err != nil {
		return DashboardAnalytics{}, err
	}
	if len(worth.Components) == 0 {
		totals, err := f.services.ListCurrencyTotals(ctx, true)
		if err != nil {
			return DashboardAnalytics{}, err
		}
		for _, total := range totals {
			worth.Components = append(worth.Components, NetWorthComponent{
				CurrencyCode:     total.CurrencyCode,
				CurrencyName:     total.CurrencyName,
				CurrencySymbol:   total.CurrencySymbol,
				MinorUnit:        total.MinorUnit,
				AccountCount:     total.AccountCount,
				BalanceMinor:     total.TotalMinor,
				BalanceFormatted: total.TotalFormatted,
			})
		}
	}
	if len(worth.Accounts) == 0 {
		balances, err := f.services.ListAccountBalances(ctx, true)
		if err != nil {
			return DashboardAnalytics{}, err
		}
		for _, b := range balances {
			if !b.IsActive && b.BalanceMinor == 0 {
				continue
			}
			worth.Accounts = append(worth.Accounts, NetWorthAccount{
				AccountID:        b.AccountID,
				AccountName:      b.AccountName,
				AccountType:      string(b.AccountType),
				Institution:      b.Institution,
				IsActive:         b.IsActive,
				CurrencyCode:     b.CurrencyCode,
				CurrencySymbol:   b.CurrencySymbol,
				BalanceMinor:     b.BalanceMinor,
				BalanceFormatted: b.BalanceFormatted,
			})
		}
	}

	today := time.Now().In(economicLocation)
	to := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	days := map[DashboardPreset]int{
		DashboardPreset7D: 7, DashboardPreset30D: 30, DashboardPreset90D: 90, DashboardPreset1Y: 365,
	}[preset]
	if days == 0 {
		days = 1
	}
	from := to.AddDate(0, 0, -(days - 1))

	out := DashboardAnalytics{
		Preset:      preset,
		Today:       to,
		PeriodFrom:  from,
		PeriodTo:    to,
		HasPrevious: preset != DashboardPresetAll,
		NetWorth:    worth,
		Opening: NetWorthPoint{
			Date: from.AddDate(0, 0, -1), TotalMinor: worth.TotalMinor,
			TotalFormatted: worth.TotalFormatted, Complete: worth.Complete,
		},
		Change: Change{Defined: true, AmountFormatted: "0.00"},
		CashFlow: CashFlow{
			From: from, To: to, Complete: true,
			IncomeFormatted: "0.00", ExpenseFormatted: "0.00", NetFormatted: "0.00",
		},
		Spending: Spending{TotalFormatted: "0.00", DailyAverageFormatted: "0.00", PreviousTotalFormatted: "0.00"},
	}
	if out.HasPrevious {
		out.PreviousFrom = from.AddDate(0, 0, -days)
		out.PreviousTo = from.AddDate(0, 0, -1)
	}
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		out.Points = append(out.Points, NetWorthPoint{
			Date: day, TotalMinor: worth.TotalMinor, TotalFormatted: worth.TotalFormatted, Complete: worth.Complete,
		})
	}
	return out, nil
}

// SetMonthCashFlow makes MonthToDateCashFlow return this value.
func (f *FakeAnalytics) SetMonthCashFlow(cf CashFlow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.monthCashFlow = &cf
}

// MonthToDateCashFlow returns what a test set, or an empty, complete month. It
// never adds up the fake ledger: that arithmetic is internal/analytics' and is
// tested there against SQLite.
func (f *FakeAnalytics) MonthToDateCashFlow(_ context.Context) (CashFlow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return CashFlow{}, f.err
	}
	if f.monthCashFlow != nil {
		return *f.monthCashFlow, nil
	}
	return CashFlow{
		CurrencyCode: "USD", Complete: true,
		IncomeFormatted: "0.00", ExpenseFormatted: "0.00", NetFormatted: "0.00",
	}, nil
}

var _ AnalyticsService = (*FakeAnalytics)(nil)
