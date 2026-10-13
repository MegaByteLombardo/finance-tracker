package web

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The M6.5 surface: the weekly and monthly recap (docs/m6.5-contract.md).
//
// As on the dashboard, every figure is computed by internal/analytics and
// arrives formatted. What this file adds is wording -- a period's title, an
// indicator's label -- and the links between recaps. It filters lists by a
// direction the backend already decided; it never compares two amounts.

// RecapGranularity is the length of a recap: a calendar week or month.
type RecapGranularity string

const (
	RecapWeek  RecapGranularity = "WEEK"
	RecapMonth RecapGranularity = "MONTH"

	// DefaultRecapGranularity is what an absent or unrecognised ?period= means.
	DefaultRecapGranularity = RecapMonth
)

// RecapGranularities lists the granularities in selector order.
func RecapGranularities() []RecapGranularity { return []RecapGranularity{RecapWeek, RecapMonth} }

// Label is how the selector shows the granularity.
func (g RecapGranularity) Label() string {
	if g == RecapWeek {
		return "Semanal"
	}
	return "Mensual"
}

// Query is the granularity as ?period= spells it.
func (g RecapGranularity) Query() string { return strings.ToLower(string(g)) }

// Noun names one period of this granularity, for "vs. la semana anterior".
func (g RecapGranularity) Noun() string {
	if g == RecapWeek {
		return "semana"
	}
	return "mes"
}

// RecapRequest names the recap to show.
type RecapRequest struct {
	Granularity RecapGranularity

	// Anchor is any date inside the wanted period. Without one, the recap is of
	// the last period that has fully ended.
	Anchor    time.Time
	HasAnchor bool
}

// ParseRecapRequest reads ?period=week|month and ?start=YYYY-MM-DD. Anything
// unrecognised is ignored, so a recap link with a typo still opens: the
// default granularity, the last closed period.
func ParseRecapRequest(q url.Values) RecapRequest {
	req := RecapRequest{Granularity: DefaultRecapGranularity}
	raw := strings.TrimSpace(q.Get("period"))
	for _, g := range RecapGranularities() {
		if strings.EqualFold(raw, string(g)) {
			req.Granularity = g
		}
	}
	if start, err := time.Parse(time.DateOnly, strings.TrimSpace(q.Get("start"))); err == nil {
		req.Anchor, req.HasAnchor = start, true
	}
	return req
}

// RecapURL is the link to a recap. A zero anchor means the default period.
func RecapURL(path string, g RecapGranularity, anchor time.Time) string {
	q := url.Values{}
	q.Set("period", g.Query())
	if !anchor.IsZero() {
		q.Set("start", anchor.Format(time.DateOnly))
	}
	return path + "?" + q.Encode()
}

// CategoryDirection is which way a category moved, as the backend decided.
type CategoryDirection string

const (
	DirectionUp      CategoryDirection = "UP"
	DirectionDown    CategoryDirection = "DOWN"
	DirectionSame    CategoryDirection = "SAME"
	DirectionNew     CategoryDirection = "NEW"
	DirectionGone    CategoryDirection = "GONE"
	DirectionUnknown CategoryDirection = "UNKNOWN"
)

// RecapCategory is one category in the period against the previous one.
type RecapCategory struct {
	CategoryID   int64
	CategoryName string

	CurrentMinor      int64
	CurrentFormatted  string
	PreviousMinor     int64
	PreviousFormatted string

	Change    Change
	Direction CategoryDirection
	Share     Ratio
	Count     int
}

// Rose reports a category that went up, including one that is new.
func (c RecapCategory) Rose() bool { return c.Direction == DirectionUp || c.Direction == DirectionNew }

// Fell reports a category that went down, including one that is gone.
func (c RecapCategory) Fell() bool {
	return c.Direction == DirectionDown || c.Direction == DirectionGone
}

// IsNew and IsGone are for the badge beside the name.
func (c RecapCategory) IsNew() bool  { return c.Direction == DirectionNew }
func (c RecapCategory) IsGone() bool { return c.Direction == DirectionGone }

// RecapExpense is one of the period's largest expenses.
type RecapExpense struct {
	TransactionID int64
	Date          time.Time
	Description   *string
	CategoryName  string
	AccountName   string

	CurrencyCode    string
	AmountMinor     int64
	AmountFormatted string

	BaseMinor     int64
	BaseFormatted string
}

// EditURL opens the expense.
func (e RecapExpense) EditURL() string {
	return "/transactions/" + strconv.FormatInt(e.TransactionID, 10) + "/edit"
}

// RecapAccount is how one account's balance moved over the period.
type RecapAccount struct {
	AccountID   int64
	AccountName string
	AccountType string
	IsActive    bool

	CurrencyCode     string
	OpeningMinor     int64
	OpeningFormatted string
	ClosingMinor     int64
	ClosingFormatted string
	DeltaMinor       int64
	DeltaFormatted   string

	BaseChange Change
}

// DeltaChange is the account's own-currency difference as a Change, so a
// template signs it with signedChange like every other.
func (a RecapAccount) DeltaChange() Change {
	return Change{Defined: true, AmountMinor: a.DeltaMinor, AmountFormatted: a.DeltaFormatted}
}

// IndicatorKey names one of the recap's fixed comparisons.
type IndicatorKey string

const (
	IndicatorNetWorth    IndicatorKey = "NET_WORTH"
	IndicatorNetCashFlow IndicatorKey = "NET_CASH_FLOW"
	IndicatorExpenses    IndicatorKey = "EXPENSES"
	IndicatorSavingsRate IndicatorKey = "SAVINGS_RATE"
)

// Verdict is which way an indicator moved, by its fixed rule (TD-073).
type Verdict string

const (
	VerdictImproved    Verdict = "IMPROVED"
	VerdictWorsened    Verdict = "WORSENED"
	VerdictUnchanged   Verdict = "UNCHANGED"
	VerdictUnavailable Verdict = "UNAVAILABLE"
)

// Indicator is one fixed comparison and which way it went.
type Indicator struct {
	Key     IndicatorKey
	Verdict Verdict

	// Change backs an amount indicator; Points backs the savings rate.
	Change Change
	Points Ratio
}

// Label names the indicator.
func (i Indicator) Label() string {
	switch i.Key {
	case IndicatorNetWorth:
		return "Patrimonio neto"
	case IndicatorNetCashFlow:
		return "Flujo neto"
	case IndicatorExpenses:
		return "Gasto diario promedio"
	case IndicatorSavingsRate:
		return "Tasa de ahorro"
	default:
		return string(i.Key)
	}
}

// Rule states, in words, what counts as better -- so the verdict can be traced.
func (i Indicator) Rule() string {
	switch i.Key {
	case IndicatorNetWorth:
		return "mejora si crece en el período"
	case IndicatorNetCashFlow:
		return "mejora si supera al período anterior"
	case IndicatorExpenses:
		return "mejora si baja frente al período anterior"
	case IndicatorSavingsRate:
		return "mejora si sube frente al período anterior"
	default:
		return ""
	}
}

// IsPoints reports the indicator that is read in percentage points.
func (i Indicator) IsPoints() bool { return i.Key == IndicatorSavingsRate }

// VerdictLabel is the verdict in words.
func (i Indicator) VerdictLabel() string {
	switch i.Verdict {
	case VerdictImproved:
		return "Mejoró"
	case VerdictWorsened:
		return "Empeoró"
	case VerdictUnchanged:
		return "Sin cambio"
	default:
		return "Sin datos"
	}
}

// Improved and Worsened choose a CSS class.
func (i Indicator) Improved() bool { return i.Verdict == VerdictImproved }
func (i Indicator) Worsened() bool { return i.Verdict == VerdictWorsened }

// IndicatorSummary counts the indicators by verdict.
type IndicatorSummary struct {
	Improved    int
	Worsened    int
	Unchanged   int
	Unavailable int
	Total       int
}

// RecapMissingRate is a currency that could not be valued at one end of the
// period: the reason the net worth's change is unavailable.
type RecapMissingRate struct {
	CurrencyCode     string
	BaseCurrencyCode string
	On               time.Time
	BalanceFormatted string
}

// PairLabel is the pair to load, as "USD/VES".
func (m RecapMissingRate) PairLabel() string {
	base, quote := ratePair(m.CurrencyCode, m.BaseCurrencyCode)
	return base + "/" + quote
}

// FormURL opens the manual-rate form for that day.
func (m RecapMissingRate) FormURL() string {
	return rateFormURL(m.CurrencyCode, m.BaseCurrencyCode, m.On)
}

// Recap is a calendar week or month against the one before it, computed.
type Recap struct {
	Granularity RecapGranularity
	Today       time.Time

	PeriodFrom   time.Time
	PeriodTo     time.Time
	FullTo       time.Time
	PreviousFrom time.Time
	PreviousTo   time.Time
	InProgress   bool
	HasPrevious  bool

	EarlierAnchor time.Time
	HasEarlier    bool
	LaterAnchor   time.Time
	HasLater      bool

	BaseCurrencyCode string

	Opening        NetWorth
	Closing        NetWorth
	Change         Change
	PreviousChange Change

	CashFlow          CashFlow
	PreviousCashFlow  CashFlow
	IncomeChange      Change
	NetCashFlowChange Change
	SavingsRateChange Ratio

	ExpenseChange                        Change
	ExpenseDailyAverageFormatted         string
	PreviousExpenseDailyAverageFormatted string
	ExpenseDailyAverageChange            Change

	ExpenseCategories []RecapCategory
	IncomeCategories  []RecapCategory

	TopExpenses          []RecapExpense
	UnvaluedExpenseCount int

	Accounts []RecapAccount

	Indicators []Indicator
	Summary    IndicatorSummary
}

// Title names the period: "Septiembre 2026", or "Semana del 28 sep al 4 oct
// 2026".
func (r Recap) Title() string {
	if r.Granularity == RecapWeek {
		return "Semana del " + shortSpanishDate(r.PeriodFrom) + " al " + shortSpanishDate(r.FullTo)
	}
	month := spanishMonths[r.PeriodFrom.Month()]
	return strings.ToUpper(month[:1]) + month[1:] + " " + strconv.Itoa(r.PeriodFrom.Year())
}

// ExpensesUp and ExpensesDown split the expense categories by the direction
// the backend gave them; IncomeUp and IncomeDown do the same for income.
func (r Recap) ExpensesUp() []RecapCategory {
	return filterCategories(r.ExpenseCategories, RecapCategory.Rose)
}
func (r Recap) ExpensesDown() []RecapCategory {
	return filterCategories(r.ExpenseCategories, RecapCategory.Fell)
}
func (r Recap) IncomeUp() []RecapCategory {
	return filterCategories(r.IncomeCategories, RecapCategory.Rose)
}
func (r Recap) IncomeDown() []RecapCategory {
	return filterCategories(r.IncomeCategories, RecapCategory.Fell)
}

// CategoriesComparable reports whether any category has a direction at all.
func (r Recap) CategoriesComparable() bool {
	for _, c := range r.ExpenseCategories {
		if c.Direction != DirectionUnknown {
			return true
		}
	}
	for _, c := range r.IncomeCategories {
		if c.Direction != DirectionUnknown {
			return true
		}
	}
	return false
}

func filterCategories(lines []RecapCategory, keep func(RecapCategory) bool) []RecapCategory {
	var out []RecapCategory
	for _, line := range lines {
		if keep(line) {
			out = append(out, line)
		}
	}
	return out
}

// MissingRates lists the currencies that could not be valued at either end of
// the period, opening first.
func (r Recap) MissingRates() []RecapMissingRate {
	var out []RecapMissingRate
	for _, worth := range []NetWorth{r.Opening, r.Closing} {
		for _, m := range worth.Missing {
			out = append(out, RecapMissingRate{
				CurrencyCode: m.FromCurrencyCode, BaseCurrencyCode: r.BaseCurrencyCode,
				On: worth.ValuationDate, BalanceFormatted: m.BalanceFormatted,
			})
		}
	}
	return out
}

// MissingExpenses lists the unvalued expenses of the period and of the
// previous one, current first.
func (r Recap) MissingExpenses() []MissingExpense {
	return missingExpenses(r.BaseCurrencyCode, r.CashFlow, r.PreviousCashFlow, r.HasPrevious)
}

// RecapNav is a recap's selector and arrows.
type RecapNav struct {
	Granularities []RecapOption

	EarlierURL string
	LaterURL   string
	LatestURL  string
}

// RecapOption is one entry of the granularity selector.
type RecapOption struct {
	Label  string
	URL    string
	Active bool
}

// newRecapNav builds the links of a recap page. Switching granularity keeps the
// period's start, so "this month" becomes its first week rather than jumping.
func newRecapNav(path string, r Recap) RecapNav {
	nav := RecapNav{LatestURL: RecapURL(path, r.Granularity, r.Today)}
	for _, g := range RecapGranularities() {
		nav.Granularities = append(nav.Granularities, RecapOption{
			Label:  g.Label(),
			URL:    RecapURL(path, g, r.PeriodFrom),
			Active: g == r.Granularity,
		})
	}
	if r.HasEarlier {
		nav.EarlierURL = RecapURL(path, r.Granularity, r.EarlierAnchor)
	}
	if r.HasLater {
		nav.LaterURL = RecapURL(path, r.Granularity, r.LaterAnchor)
	}
	return nav
}
