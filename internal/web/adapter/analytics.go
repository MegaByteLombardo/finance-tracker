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
	for _, gap := range d.Series.Gaps() {
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
