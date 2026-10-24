package web

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// The M6 surface: the historical net worth and the dashboard's analytics.
//
// Every figure here is computed by internal/analytics and internal/valuation and
// arrives already formatted. A handler passes it to a template; a template
// shows it. Neither adds, subtracts, divides or converts anything (see
// docs/m6-contract.md). The one computation this package does with these values
// is GEOMETRY -- where on a chart a point sits -- and it does that with
// integers, in chart.go.

// AnalyticsService computes the dashboard for a period.
type AnalyticsService interface {
	// Dashboard computes everything the dashboard shows for a preset, as of
	// today in Venezuela. An unknown preset is ErrInvalidInput; a handler
	// normalises the query string with ParseDashboardPreset before calling.
	Dashboard(ctx context.Context, preset DashboardPreset) (DashboardAnalytics, error)

	// MonthToDateCashFlow is income and expenses from the first of the current
	// month to today, in the valuation base, by the dashboard's rules. It is
	// what a screen titled with the month's name shows; no handler adds up the
	// rows it lists.
	MonthToDateCashFlow(ctx context.Context) (CashFlow, error)

	// Recap computes the recap of a calendar week or month against the one
	// before it (docs/m6.5-contract.md). An unknown granularity is
	// ErrInvalidInput; a handler normalises the query with ParseRecapRequest.
	Recap(ctx context.Context, req RecapRequest) (Recap, error)
}

// DashboardPreset names a dashboard period.
type DashboardPreset string

const (
	DashboardPreset7D  DashboardPreset = "7D"
	DashboardPreset30D DashboardPreset = "30D"
	DashboardPreset90D DashboardPreset = "90D"
	DashboardPreset1Y  DashboardPreset = "1Y"
	DashboardPresetAll DashboardPreset = "ALL"

	// DefaultDashboardPreset is what an absent or unrecognised ?period= means.
	DefaultDashboardPreset = DashboardPreset30D
)

// DashboardPresets lists the presets in selector order.
func DashboardPresets() []DashboardPreset {
	return []DashboardPreset{
		DashboardPreset7D, DashboardPreset30D, DashboardPreset90D, DashboardPreset1Y, DashboardPresetAll,
	}
}

// Label is how the selector shows the preset.
func (p DashboardPreset) Label() string {
	switch p {
	case DashboardPresetAll:
		return "Todo"
	default:
		return string(p)
	}
}

// Description is the period in words, for the phrase under a figure.
func (p DashboardPreset) Description() string {
	switch p {
	case DashboardPreset7D:
		return "últimos 7 días"
	case DashboardPreset30D:
		return "últimos 30 días"
	case DashboardPreset90D:
		return "últimos 90 días"
	case DashboardPreset1Y:
		return "último año"
	case DashboardPresetAll:
		return "todo el histórico"
	default:
		return string(p)
	}
}

// ParseDashboardPreset reads ?period=. Anything that is not a preset, including
// nothing, is the default: a dashboard link with a typo still opens.
func ParseDashboardPreset(raw string) DashboardPreset {
	for _, preset := range DashboardPresets() {
		if strings.EqualFold(strings.TrimSpace(raw), string(preset)) {
			return preset
		}
	}
	return DefaultDashboardPreset
}

// Ratio is a percentage, already formatted: Formatted is "7.24" for 7.24%.
//
// When Defined is false there is no number at all -- a savings rate with no
// income, a change from zero -- and a template shows "—".
type Ratio struct {
	BasisPoints int64
	Defined     bool
	Formatted   string
}

// IsNegative reports a defined ratio below zero, for colouring.
func (r Ratio) IsNegative() bool { return r.Defined && r.BasisPoints < 0 }

// IsPositive reports a defined ratio above zero, for colouring and the "+".
func (r Ratio) IsPositive() bool { return r.Defined && r.BasisPoints > 0 }

// ChangeReason says why a change is not available.
type ChangeReason string

const (
	ChangeReasonNone             ChangeReason = ""
	ChangeReasonIncomplete       ChangeReason = "INCOMPLETE"
	ChangeReasonNoPreviousPeriod ChangeReason = "NO_PREVIOUS_PERIOD"
)

// Change is a difference between two figures, computed by the backend.
type Change struct {
	Defined bool
	Reason  ChangeReason

	AmountMinor int64
	// AmountFormatted carries a "-" when negative and no sign otherwise; the
	// template adds the "+" from IsPositive.
	AmountFormatted string

	Percent Ratio
}

// IsPositive reports a defined change above zero.
func (c Change) IsPositive() bool { return c.Defined && c.AmountMinor > 0 }

// IsNegative reports a defined change below zero.
func (c Change) IsNegative() bool { return c.Defined && c.AmountMinor < 0 }

// UnavailableMessage says, in a sentence, why the change is not shown.
func (c Change) UnavailableMessage() string {
	switch c.Reason {
	case ChangeReasonIncomplete:
		return "No disponible: faltan tasas de cambio para alguno de los extremos del período."
	case ChangeReasonNoPreviousPeriod:
		return "Sin período anterior con el que comparar."
	default:
		return ""
	}
}

// NetWorthAccount is one account's line in the breakdown.
type NetWorthAccount struct {
	AccountID   int64
	AccountName string
	AccountType string
	// Nature is ASSET or LIABILITY, derived by the backend from the type.
	Nature      string
	Institution *string
	IsActive    bool

	CurrencyCode   string
	CurrencySymbol *string

	// BalanceFormatted is the account's own balance, in its own currency.
	BalanceMinor     int64
	BalanceFormatted string

	// ConvertedFormatted is that balance in the base currency, allocated so the
	// lines add up to their currency's figure. Meaningful when Valued.
	ConvertedMinor     int64
	ConvertedFormatted string
	Valued             bool

	Share Ratio
}

// IsLiability reports an account whose balance is money owed.
func (a NetWorthAccount) IsLiability() bool { return a.Nature == "LIABILITY" }

// ArchivedWithBalance reports the case a screen warns about: an archived account
// still holding money, which is counted in the net worth (TD-064).
func (a NetWorthAccount) ArchivedWithBalance() bool { return !a.IsActive && a.BalanceMinor != 0 }

// NetWorthPoint is the net worth at the close of one day.
type NetWorthPoint struct {
	Date           time.Time
	TotalMinor     int64
	TotalFormatted string

	// Complete is false when a rate was missing that day. The chart draws a gap.
	Complete bool
}

// RateGap is a run of consecutive days on which CurrencyCode could not be
// valued into BaseCurrencyCode, the valuation base.
type RateGap struct {
	CurrencyCode     string
	BaseCurrencyCode string

	From time.Time
	To   time.Time
	Days int
}

// SingleDay reports a run of one day, which a template names by its date alone.
func (g RateGap) SingleDay() bool { return g.Days == 1 }

// Pair is the pair the missing rate is written in, "USD/VES": USD is the base
// of the pair when exactly one side is USD, otherwise the valuation base is.
// Either orientation resolves the gap (the inverse is computed); this is only
// the one a person would read off a published rate.
func (g RateGap) Pair() (base, quote string) { return ratePair(g.CurrencyCode, g.BaseCurrencyCode) }

// PairLabel is Pair as "USD/VES".
func (g RateGap) PairLabel() string {
	base, quote := g.Pair()
	return base + "/" + quote
}

// FormURL opens the manual-rate form with the pair and the run's first day
// filled in. An observation dated From covers the whole run, because lookup
// takes the latest effective_date on or before each day.
func (g RateGap) FormURL() string { return rateFormURL(g.CurrencyCode, g.BaseCurrencyCode, g.From) }

func ratePair(currency, valuationBase string) (base, quote string) {
	if currency == "USD" && valuationBase != "USD" {
		return currency, valuationBase
	}
	return valuationBase, currency
}

func rateFormURL(currency, valuationBase string, on time.Time) string {
	base, quote := ratePair(currency, valuationBase)
	q := url.Values{}
	q.Set("base", base)
	q.Set("quote", quote)
	q.Set("date", on.Format(time.DateOnly))
	return "/settings/exchange-rates?" + q.Encode() + "#manual-rate"
}

// MissingExpense is spending in one currency, in one of the two compared
// periods, that has no rate on its day -- the reason a spending change is
// unavailable.
type MissingExpense struct {
	// Previous is true for the previous period, false for the current one.
	Previous bool

	CurrencyCode     string
	BaseCurrencyCode string
	AmountFormatted  string

	// From and To are the first and last day of those expenses.
	From time.Time
	To   time.Time
}

// SingleDay reports expenses all on one day.
func (m MissingExpense) SingleDay() bool { return m.From.Equal(m.To) }

// PairLabel is the pair to load, as "USD/VES".
func (m MissingExpense) PairLabel() string {
	base, quote := ratePair(m.CurrencyCode, m.BaseCurrencyCode)
	return base + "/" + quote
}

// FormURL opens the manual-rate form on the first of those days; a rate dated
// then values all of them.
func (m MissingExpense) FormURL() string {
	return rateFormURL(m.CurrencyCode, m.BaseCurrencyCode, m.From)
}

// MissingExpenses lists the unvalued expenses of the period and of the
// previous one, current first.
func (d DashboardAnalytics) MissingExpenses() []MissingExpense {
	return missingExpenses(d.NetWorth.BaseCurrencyCode, d.CashFlow, d.PreviousCashFlow, d.HasPrevious)
}

func missingExpenses(baseCode string, current, previous CashFlow, hasPrevious bool) []MissingExpense {
	var out []MissingExpense
	collect := func(cf CashFlow, isPrevious bool) {
		for _, m := range cf.Missing {
			if m.ExpenseMinor == 0 {
				continue
			}
			out = append(out, MissingExpense{
				Previous: isPrevious, CurrencyCode: m.CurrencyCode, BaseCurrencyCode: baseCode,
				AmountFormatted: m.ExpenseFormatted, From: m.ExpenseFrom, To: m.ExpenseTo,
			})
		}
	}
	collect(current, false)
	if hasPrevious {
		collect(previous, true)
	}
	return out
}

// CategoryAmount is one category's total over a period, in the base currency,
// always positive.
type CategoryAmount struct {
	CategoryID      int64
	CategoryName    string
	AmountMinor     int64
	AmountFormatted string
	Count           int
}

// MissingFlow is money that could not be converted, in its own currency.
type MissingFlow struct {
	CurrencyCode     string
	IncomeMinor      int64
	IncomeFormatted  string
	ExpenseMinor     int64
	ExpenseFormatted string
	Count            int

	// ExpenseFrom and ExpenseTo are the first and last day of those expenses.
	ExpenseFrom time.Time
	ExpenseTo   time.Time
}

// CashFlow is income and expenses over a period. Transfers and currency
// exchanges are neither, and are not in it.
type CashFlow struct {
	From time.Time
	To   time.Time

	// CurrencyCode is the valuation base the totals are expressed in.
	CurrencyCode string

	IncomeMinor      int64
	IncomeFormatted  string
	ExpenseMinor     int64
	ExpenseFormatted string
	NetMinor         int64
	NetFormatted     string

	SavingsRate Ratio

	Complete bool
	Missing  []MissingFlow

	IncomeCategories  []CategoryAmount
	ExpenseCategories []CategoryAmount

	TransactionCount int
}

// CategoryComparison is one spending category now and in the previous period.
type CategoryComparison struct {
	CategoryID   int64
	CategoryName string
	Other        bool

	CurrentMinor      int64
	CurrentFormatted  string
	PreviousMinor     int64
	PreviousFormatted string

	Change Change
	Share  Ratio
}

// Spending is the expense side of a period.
type Spending struct {
	TotalMinor            int64
	TotalFormatted        string
	DailyAverageMinor     int64
	DailyAverageFormatted string

	Categories []CategoryComparison

	PreviousTotalMinor     int64
	PreviousTotalFormatted string
	Change                 Change
}

// ChangeUnavailableMessage says why spending cannot be compared. Its
// "incomplete" is about expenses with no rate on their day, not about the
// ends of the period as for the net worth.
func (s Spending) ChangeUnavailableMessage() string {
	if s.Change.Reason == ChangeReasonIncomplete {
		return "No disponible: hay gastos sin tasa de cambio en su fecha, en este período o en el anterior."
	}
	return s.Change.UnavailableMessage()
}

// DashboardAnalytics is everything the dashboard shows, computed.
type DashboardAnalytics struct {
	Preset DashboardPreset
	Today  time.Time

	PeriodFrom time.Time
	PeriodTo   time.Time

	HasPrevious  bool
	PreviousFrom time.Time
	PreviousTo   time.Time

	// NetWorth is today's, with its breakdown. It equals the last point.
	NetWorth NetWorth

	// Points is the net worth at the close of every day of the period.
	Points         []NetWorthPoint
	IncompleteDays int

	// Gaps says which rates those incomplete days are missing, as runs of
	// consecutive days per currency.
	Gaps []RateGap

	// Opening is the net worth at the close of the day before the period, the
	// baseline Change is measured from.
	Opening NetWorthPoint
	Change  Change

	CashFlow         CashFlow
	PreviousCashFlow CashFlow

	Spending Spending
}
