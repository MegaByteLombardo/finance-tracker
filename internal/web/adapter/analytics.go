package adapter

import (
	"context"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/analytics"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// --- analytics --------------------------------------------------------------

type analyticsAdapter struct {
	svc *analytics.Service
}

func (a *analyticsAdapter) Dashboard(ctx context.Context, preset web.DashboardPreset) (web.DashboardAnalytics, error) {
	parsed, ok := analytics.ParsePreset(string(preset))
	if !ok {
		return web.DashboardAnalytics{}, fmt.Errorf("%w: unknown dashboard period %q", web.ErrInvalidInput, preset)
	}

	d, err := a.svc.Dashboard(ctx, parsed)
	if err != nil {
		return web.DashboardAnalytics{}, TranslateValuationError(err)
	}

	out := web.DashboardAnalytics{
		Preset:           web.DashboardPreset(d.Preset),
		Today:            d.Today,
		PeriodFrom:       d.Period.From,
		PeriodTo:         d.Period.To,
		HasPrevious:      d.HasPrevious,
		PreviousFrom:     d.PreviousPeriod.From,
		PreviousTo:       d.PreviousPeriod.To,
		NetWorth:         toWebNetWorth(d.NetWorth),
		Points:           make([]web.NetWorthPoint, 0, len(d.Series.Points)),
		IncompleteDays:   d.Series.IncompleteDays,
		Change:           toWebChange(d.Change),
		CashFlow:         toWebCashFlow(d.CashFlow),
		PreviousCashFlow: toWebCashFlow(d.PreviousCashFlow),
		Spending:         toWebSpending(d.Spending),
		Opening: web.NetWorthPoint{
			Date:           d.Opening.Date,
			TotalMinor:     d.Opening.TotalMinor,
			TotalFormatted: d.Opening.TotalFormatted,
			Complete:       d.Opening.Complete,
		},
	}
	for _, gap := range d.Gaps {
		out.Gaps = append(out.Gaps, web.RateGap{
			CurrencyCode:     gap.FromCurrencyCode,
			BaseCurrencyCode: gap.ToCurrencyCode,
			From:             gap.From,
			To:               gap.To,
			Days:             gap.Days,
		})
	}
	for _, point := range d.Series.Points {
		out.Points = append(out.Points, web.NetWorthPoint{
			Date:           point.Date,
			TotalMinor:     point.TotalMinor,
			TotalFormatted: point.TotalFormatted,
			Complete:       point.Complete,
		})
	}
	return out, nil
}

func (a *analyticsAdapter) MonthToDateCashFlow(ctx context.Context) (web.CashFlow, error) {
	cf, err := a.svc.MonthToDateCashFlow(ctx)
	if err != nil {
		return web.CashFlow{}, TranslateValuationError(err)
	}
	return toWebCashFlow(cf), nil
}

// toWebRatio formats basis points as a percentage with two decimals. Formatting
// only: the number was computed, and rounded, by internal/money.
func toWebRatio(r analytics.Ratio) web.Ratio {
	if !r.Defined {
		return web.Ratio{}
	}
	return web.Ratio{BasisPoints: r.BasisPoints, Defined: true, Formatted: money.Format(r.BasisPoints, 2)}
}

func toWebChange(c analytics.Change) web.Change {
	return web.Change{
		Defined:         c.Defined,
		Reason:          web.ChangeReason(c.Reason),
		AmountMinor:     c.AmountMinor,
		AmountFormatted: c.AmountFormatted,
		Percent:         toWebRatio(c.Percent),
	}
}

func toWebCategoryAmounts(lines []analytics.CategoryAmount) []web.CategoryAmount {
	out := make([]web.CategoryAmount, 0, len(lines))
	for _, line := range lines {
		out = append(out, web.CategoryAmount{
			CategoryID:      line.CategoryID,
			CategoryName:    line.CategoryName,
			AmountMinor:     line.AmountMinor,
			AmountFormatted: line.AmountFormatted,
			Count:           line.Count,
		})
	}
	return out
}

func toWebCashFlow(cf analytics.CashFlow) web.CashFlow {
	out := web.CashFlow{
		From:              cf.Period.From,
		CurrencyCode:      cf.BaseCurrencyCode,
		To:                cf.Period.To,
		IncomeMinor:       cf.IncomeMinor,
		IncomeFormatted:   cf.IncomeFormatted,
		ExpenseMinor:      cf.ExpenseMinor,
		ExpenseFormatted:  cf.ExpenseFormatted,
		NetMinor:          cf.NetMinor,
		NetFormatted:      cf.NetFormatted,
		SavingsRate:       toWebRatio(cf.SavingsRate),
		Complete:          cf.Complete,
		IncomeCategories:  toWebCategoryAmounts(cf.IncomeCategories),
		ExpenseCategories: toWebCategoryAmounts(cf.ExpenseCategories),
		TransactionCount:  cf.TransactionCount,
	}
	for _, missing := range cf.Missing {
		out.Missing = append(out.Missing, web.MissingFlow{
			CurrencyCode:     missing.CurrencyCode,
			IncomeMinor:      missing.IncomeMinor,
			IncomeFormatted:  missing.IncomeFormatted,
			ExpenseMinor:     missing.ExpenseMinor,
			ExpenseFormatted: missing.ExpenseFormatted,
			Count:            missing.Count,
			ExpenseFrom:      missing.ExpenseFrom,
			ExpenseTo:        missing.ExpenseTo,
		})
	}
	return out
}

func toWebSpending(s analytics.Spending) web.Spending {
	out := web.Spending{
		TotalMinor:             s.TotalMinor,
		TotalFormatted:         s.TotalFormatted,
		DailyAverageMinor:      s.DailyAverageMinor,
		DailyAverageFormatted:  s.DailyAverageFormatted,
		PreviousTotalMinor:     s.PreviousTotalMinor,
		PreviousTotalFormatted: s.PreviousTotalFormatted,
		Change:                 toWebChange(s.Change),
		Categories:             make([]web.CategoryComparison, 0, len(s.Categories)),
	}
	for _, c := range s.Categories {
		out.Categories = append(out.Categories, web.CategoryComparison{
			CategoryID:        c.CategoryID,
			CategoryName:      c.CategoryName,
			Other:             c.Other,
			CurrentMinor:      c.CurrentMinor,
			CurrentFormatted:  c.CurrentFormatted,
			PreviousMinor:     c.PreviousMinor,
			PreviousFormatted: c.PreviousFormatted,
			Change:            toWebChange(c.Change),
			Share:             toWebRatio(c.Share),
		})
	}
	return out
}

func (a *analyticsAdapter) Recap(ctx context.Context, req web.RecapRequest) (web.Recap, error) {
	g, ok := analytics.ParseGranularity(string(req.Granularity))
	if !ok {
		return web.Recap{}, fmt.Errorf("%w: unknown recap period %q", web.ErrInvalidInput, req.Granularity)
	}

	r, err := a.svc.Recap(ctx, analytics.RecapRequest{Granularity: g, Anchor: req.Anchor, HasAnchor: req.HasAnchor})
	if err != nil {
		return web.Recap{}, TranslateValuationError(err)
	}

	out := web.Recap{
		Granularity:   web.RecapGranularity(r.Granularity),
		Today:         r.Today,
		PeriodFrom:    r.Period.From,
		PeriodTo:      r.Period.To,
		FullTo:        r.FullPeriod.To,
		PreviousFrom:  r.PreviousPeriod.From,
		PreviousTo:    r.PreviousPeriod.To,
		InProgress:    r.InProgress,
		HasPrevious:   r.HasPrevious,
		EarlierAnchor: r.EarlierAnchor,
		HasEarlier:    r.HasEarlier,
		LaterAnchor:   r.LaterAnchor,
		HasLater:      r.HasLater,

		BaseCurrencyCode: r.BaseCurrencyCode,
		Opening:          toWebNetWorth(r.Opening),
		Closing:          toWebNetWorth(r.Closing),
		Change:           toWebChange(r.Change),
		PreviousChange:   toWebChange(r.PreviousChange),

		CashFlow:          toWebCashFlow(r.CashFlow),
		PreviousCashFlow:  toWebCashFlow(r.PreviousCashFlow),
		IncomeChange:      toWebChange(r.IncomeChange),
		NetCashFlowChange: toWebChange(r.NetCashFlowChange),
		SavingsRateChange: toWebRatio(r.SavingsRateChange),

		ExpenseChange:                        toWebChange(r.ExpenseChange),
		ExpenseDailyAverageFormatted:         r.ExpenseDailyAverageFormatted,
		PreviousExpenseDailyAverageFormatted: r.PreviousExpenseDailyAverageFormatted,
		ExpenseDailyAverageChange:            toWebChange(r.ExpenseDailyAverageChange),

		ExpenseCategories:    toWebRecapCategories(r.ExpenseCategories),
		IncomeCategories:     toWebRecapCategories(r.IncomeCategories),
		UnvaluedExpenseCount: r.UnvaluedExpenseCount,

		Summary: web.IndicatorSummary{
			Improved: r.Summary.Improved, Worsened: r.Summary.Worsened,
			Unchanged: r.Summary.Unchanged, Unavailable: r.Summary.Unavailable, Total: r.Summary.Total,
		},
	}
	for _, e := range r.TopExpenses {
		out.TopExpenses = append(out.TopExpenses, web.RecapExpense{
			TransactionID:   e.TransactionID,
			Date:            e.Date,
			Description:     e.Description,
			CategoryName:    e.CategoryName,
			AccountName:     e.AccountName,
			CurrencyCode:    e.CurrencyCode,
			AmountMinor:     e.AmountMinor,
			AmountFormatted: e.AmountFormatted,
			BaseMinor:       e.BaseMinor,
			BaseFormatted:   e.BaseFormatted,
		})
	}
	for _, acc := range r.Accounts {
		out.Accounts = append(out.Accounts, web.RecapAccount{
			AccountID:        acc.AccountID,
			AccountName:      acc.AccountName,
			AccountType:      acc.AccountType,
			IsActive:         acc.IsActive,
			CurrencyCode:     acc.CurrencyCode,
			OpeningMinor:     acc.OpeningMinor,
			OpeningFormatted: acc.OpeningFormatted,
			ClosingMinor:     acc.ClosingMinor,
			ClosingFormatted: acc.ClosingFormatted,
			DeltaMinor:       acc.DeltaMinor,
			DeltaFormatted:   acc.DeltaFormatted,
			BaseChange:       toWebChange(acc.BaseChange),
		})
	}
	for _, i := range r.Indicators {
		out.Indicators = append(out.Indicators, web.Indicator{
			Key:     web.IndicatorKey(i.Key),
			Verdict: web.Verdict(i.Verdict),
			Change:  toWebChange(i.Change),
			Points:  toWebRatio(i.Points),
		})
	}
	return out, nil
}

func toWebRecapCategories(lines []analytics.CategoryChange) []web.RecapCategory {
	out := make([]web.RecapCategory, 0, len(lines))
	for _, c := range lines {
		out = append(out, web.RecapCategory{
			CategoryID:        c.CategoryID,
			CategoryName:      c.CategoryName,
			CurrentMinor:      c.CurrentMinor,
			CurrentFormatted:  c.CurrentFormatted,
			PreviousMinor:     c.PreviousMinor,
			PreviousFormatted: c.PreviousFormatted,
			Change:            toWebChange(c.Change),
			Direction:         web.CategoryDirection(c.Direction),
			Share:             toWebRatio(c.Share),
			Count:             c.Count,
		})
	}
	return out
}
