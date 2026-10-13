package analytics

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
)

// topExpenses is how many individual expenses a recap names.
const topExpenses = 5

// RecapRequest names the recap to compute.
type RecapRequest struct {
	Granularity Granularity

	// Anchor is any date inside the wanted period. Without one, the recap is of
	// the last period that has fully ended.
	Anchor    time.Time
	HasAnchor bool
}

// Direction is which way a category moved against the previous period.
type Direction string

const (
	DirectionUp   Direction = "UP"
	DirectionDown Direction = "DOWN"
	DirectionSame Direction = "SAME"

	// DirectionNew is a category with nothing in the previous period.
	DirectionNew Direction = "NEW"
	// DirectionGone is a category with nothing in this period.
	DirectionGone Direction = "GONE"

	// DirectionUnknown is a category that cannot be compared: there is no
	// previous period, or a flow of that kind had no rate on its day.
	DirectionUnknown Direction = "UNKNOWN"
)

// CategoryChange is one category in a recap's period against the previous one.
type CategoryChange struct {
	CategoryID   int64
	CategoryName string

	CurrentMinor      int64
	CurrentFormatted  string
	PreviousMinor     int64
	PreviousFormatted string

	Change    Change
	Direction Direction

	// Share is CurrentMinor as a part of the period's total of the same kind.
	Share Ratio

	// Count is how many transactions the current figure sums.
	Count int
}

// RecapExpense is one of the period's largest expenses.
type RecapExpense struct {
	TransactionID int64
	Date          time.Time
	Description   *string
	CategoryName  string
	AccountName   string

	// AmountMinor is what went out, in the account's own currency.
	CurrencyCode    string
	AmountMinor     int64
	AmountFormatted string

	// BaseMinor is that amount at the reference rate of its own day, rounded
	// once. It is what the ranking is by.
	BaseMinor     int64
	BaseFormatted string
}

// AccountChange is how one account's balance moved over the period.
type AccountChange struct {
	AccountID   int64
	AccountName string
	AccountType string
	IsActive    bool

	CurrencyCode string

	// The balances at the close of the day before the period and at its close,
	// and their difference, in the account's own currency. Exact: no rate is
	// involved.
	OpeningMinor     int64
	OpeningFormatted string
	ClosingMinor     int64
	ClosingFormatted string
	DeltaMinor       int64
	DeltaFormatted   string

	// BaseChange is the difference between the account's two valued lines in
	// the base currency. Unavailable when either end could not be valued.
	BaseChange Change
}

// IndicatorKey names one of the recap's fixed comparisons.
type IndicatorKey string

const (
	IndicatorNetWorth    IndicatorKey = "NET_WORTH"
	IndicatorNetCashFlow IndicatorKey = "NET_CASH_FLOW"
	IndicatorExpenses    IndicatorKey = "EXPENSES"
	IndicatorSavingsRate IndicatorKey = "SAVINGS_RATE"
)

// Verdict is which way an indicator moved, by its fixed rule.
type Verdict string

const (
	VerdictImproved    Verdict = "IMPROVED"
	VerdictWorsened    Verdict = "WORSENED"
	VerdictUnchanged   Verdict = "UNCHANGED"
	VerdictUnavailable Verdict = "UNAVAILABLE"
)

// Indicator is one fixed comparison and which way it went (TD-073).
//
// The rule behind each key is fixed and stated in the contract; nothing here
// weighs one indicator against another, and nothing recommends anything. It is
// a report of four signs.
type Indicator struct {
	Key     IndicatorKey
	Verdict Verdict

	// Change backs an amount indicator; Points backs the savings rate, as a
	// difference in basis points of two rates.
	Change Change
	Points Ratio
}

// IndicatorSummary counts the indicators by verdict.
type IndicatorSummary struct {
	Improved    int
	Worsened    int
	Unchanged   int
	Unavailable int
	Total       int
}

// Recap is a calendar week or month, against the one before it.
type Recap struct {
	Granularity Granularity

	// Today is the Venezuelan calendar date the recap was computed for.
	Today time.Time

	Period         Period
	FullPeriod     Period
	PreviousPeriod Period
	InProgress     bool

	// HasPrevious is false when the previous period ended before the first
	// movement in the ledger: there is nothing to compare with.
	HasPrevious bool

	// The neighbouring recaps, as a date inside each. HasEarlier is false at
	// the period of the first movement; HasLater is false at the period in
	// progress.
	EarlierAnchor time.Time
	HasEarlier    bool
	LaterAnchor   time.Time
	HasLater      bool

	BaseCurrencyCode string

	// Opening is the net worth at the close of the day before Period, and
	// Closing at the close of its last day.
	Opening valuation.NetWorth
	Closing valuation.NetWorth
	Change  Change

	// PreviousChange is the same movement over PreviousPeriod.
	PreviousChange Change

	CashFlow         CashFlow
	PreviousCashFlow CashFlow

	// IncomeChange is the income against the previous period's, and
	// NetCashFlowChange the net cash flow against the previous period's.
	IncomeChange      Change
	NetCashFlowChange Change

	// SavingsRateChange is the difference between the two savings rates, in
	// basis points: 320 is 3.20 percentage points. Undefined unless both are.
	SavingsRateChange Ratio

	// ExpenseChange compares the totals; the daily averages compare periods of
	// different lengths fairly, and the expenses indicator is read from them.
	ExpenseChange                        Change
	ExpenseDailyAverageMinor             int64
	ExpenseDailyAverageFormatted         string
	PreviousExpenseDailyAverageMinor     int64
	PreviousExpenseDailyAverageFormatted string
	ExpenseDailyAverageChange            Change

	// ExpenseCategories and IncomeCategories hold every category of either
	// period, the largest movement first.
	ExpenseCategories []CategoryChange
	IncomeCategories  []CategoryChange

	// TopExpenses are the period's largest expenses in the base currency.
	// UnvaluedExpenseCount is how many could not be valued on their day and so
	// could not be ranked; they are counted, never estimated (TD-074).
	TopExpenses          []RecapExpense
	UnvaluedExpenseCount int

	Accounts []AccountChange

	Indicators []Indicator
	Summary    IndicatorSummary
}

// Recap computes the recap of a calendar week or month, as of today in
// Venezuela.
//
// It issues seven queries whatever the period: the valuation snapshot's five,
// the flows of both periods together, and the period's expenses one by one.
func (s *Service) Recap(ctx context.Context, req RecapRequest) (Recap, error) {
	g, ok := ParseGranularity(string(req.Granularity))
	if !ok {
		return Recap{}, fmt.Errorf("unknown recap granularity %q", req.Granularity)
	}

	today := database.TodayAt(s.now())
	snapshot, err := s.valuation.Load(ctx, today)
	if err != nil {
		return Recap{}, err
	}
	base := snapshot.Base()
	earliest, hasEarliest := snapshot.EarliestActivity()

	// The range a recap can be asked for: from the period of the first
	// movement to the period in progress. Anything outside is clamped to it.
	latest := CalendarPeriod(g, today)
	first := latest
	if hasEarliest && earliest.Before(today) {
		first = CalendarPeriod(g, earliest)
	}
	anchor := LastClosed(g, today).From
	if req.HasAnchor {
		anchor = dateOnly(req.Anchor)
	}
	if anchor.After(latest.To) {
		anchor = latest.From
	}
	if anchor.Before(first.From) {
		anchor = first.From
	}

	periods := RecapPeriodsFor(g, anchor, today)
	result := Recap{
		Granularity:      g,
		Today:            today,
		Period:           periods.Current,
		FullPeriod:       periods.Full,
		PreviousPeriod:   periods.Previous,
		InProgress:       periods.InProgress,
		HasPrevious:      hasEarliest && !periods.Previous.To.Before(earliest),
		BaseCurrencyCode: base.Code,
	}
	if periods.Full.From.After(first.From) {
		result.HasEarlier = true
		result.EarlierAnchor = StepCalendarPeriod(g, anchor, -1).From
	}
	if periods.Full.To.Before(latest.From) {
		result.HasLater = true
		result.LaterAnchor = StepCalendarPeriod(g, anchor, 1).From
	}

	if result.Opening, err = snapshot.NetWorthAt(result.Period.From.AddDate(0, 0, -1)); err != nil {
		return Recap{}, err
	}
	if result.Closing, err = snapshot.NetWorthAt(result.Period.To); err != nil {
		return Recap{}, err
	}
	if result.Change, err = compare(result.Opening.TotalMinor, result.Closing.TotalMinor,
		result.Opening.Complete && result.Closing.Complete, true, base.MinorUnit); err != nil {
		return Recap{}, err
	}
	if result.PreviousChange, err = netWorthChange(snapshot, result.PreviousPeriod, result.HasPrevious, base.MinorUnit); err != nil {
		return Recap{}, err
	}

	flows, err := s.ledger.ListFlows(ctx, result.PreviousPeriod.From, result.Period.To)
	if err != nil {
		return Recap{}, fmt.Errorf("reading the recap's flows: %w", err)
	}
	convert := converter(snapshot.ConvertToBaseExact)
	if result.CashFlow, err = buildCashFlow(result.Period, flows, convert, base.Code, base.MinorUnit); err != nil {
		return Recap{}, err
	}
	if result.HasPrevious {
		if result.PreviousCashFlow, err = buildCashFlow(result.PreviousPeriod, flows, convert, base.Code, base.MinorUnit); err != nil {
			return Recap{}, err
		}
	}
	if err := result.compareCashFlows(base.MinorUnit); err != nil {
		return Recap{}, err
	}

	expenses, err := s.ledger.ListExpenses(ctx, result.Period.From, result.Period.To)
	if err != nil {
		return Recap{}, fmt.Errorf("reading the recap's expenses: %w", err)
	}
	if result.TopExpenses, result.UnvaluedExpenseCount, err = rankExpenses(expenses, convert, base.MinorUnit); err != nil {
		return Recap{}, err
	}

	if result.Accounts, err = accountChanges(result.Opening, result.Closing, base.MinorUnit); err != nil {
		return Recap{}, err
	}

	result.Indicators, result.Summary = indicators(result)
	return result, nil
}

// netWorthChange is the net worth's movement over a period: from the close of
// the day before it to the close of its last day.
func netWorthChange(snapshot *valuation.Snapshot, period Period, hasPrevious bool, minorUnit int) (Change, error) {
	if !hasPrevious {
		return Change{Reason: ReasonNoPreviousPeriod}, nil
	}
	opening, err := snapshot.NetWorthAt(period.From.AddDate(0, 0, -1))
	if err != nil {
		return Change{}, err
	}
	closing, err := snapshot.NetWorthAt(period.To)
	if err != nil {
		return Change{}, err
	}
	return compare(opening.TotalMinor, closing.TotalMinor, opening.Complete && closing.Complete, true, minorUnit)
}

// compareCashFlows fills every comparison between the two cash flows.
func (r *Recap) compareCashFlows(minorUnit int) error {
	current, previous := r.CashFlow, r.PreviousCashFlow
	var err error

	if r.NetCashFlowChange, err = compare(previous.NetMinor, current.NetMinor,
		current.Complete && previous.Complete, r.HasPrevious, minorUnit); err != nil {
		return err
	}
	if r.HasPrevious && current.SavingsRate.Defined && previous.SavingsRate.Defined {
		r.SavingsRateChange = Ratio{
			BasisPoints: current.SavingsRate.BasisPoints - previous.SavingsRate.BasisPoints,
			Defined:     true,
		}
	}

	incomeComparable := incomeComplete(current) && incomeComplete(previous)
	if r.IncomeChange, err = compare(previous.IncomeMinor, current.IncomeMinor,
		incomeComparable, r.HasPrevious, minorUnit); err != nil {
		return err
	}

	expensesComparable := expensesComplete(current) && expensesComplete(previous)
	if r.ExpenseChange, err = compare(previous.ExpenseMinor, current.ExpenseMinor,
		expensesComparable, r.HasPrevious, minorUnit); err != nil {
		return err
	}
	if r.ExpenseDailyAverageMinor, err = dailyAverage(current.ExpenseMinor, r.Period); err != nil {
		return err
	}
	r.ExpenseDailyAverageFormatted = money.Format(r.ExpenseDailyAverageMinor, minorUnit)
	if r.HasPrevious {
		if r.PreviousExpenseDailyAverageMinor, err = dailyAverage(previous.ExpenseMinor, r.PreviousPeriod); err != nil {
			return err
		}
	}
	r.PreviousExpenseDailyAverageFormatted = money.Format(r.PreviousExpenseDailyAverageMinor, minorUnit)
	if r.ExpenseDailyAverageChange, err = compare(r.PreviousExpenseDailyAverageMinor, r.ExpenseDailyAverageMinor,
		expensesComparable, r.HasPrevious, minorUnit); err != nil {
		return err
	}

	if r.ExpenseCategories, err = compareCategories(current.ExpenseCategories, previous.ExpenseCategories,
		current.ExpenseMinor, expensesComparable, expensesComplete(current), r.HasPrevious, minorUnit); err != nil {
		return fmt.Errorf("comparing expense categories: %w", err)
	}
	if r.IncomeCategories, err = compareCategories(current.IncomeCategories, previous.IncomeCategories,
		current.IncomeMinor, incomeComparable, incomeComplete(current), r.HasPrevious, minorUnit); err != nil {
		return fmt.Errorf("comparing income categories: %w", err)
	}
	return nil
}

// dailyAverage is a total over a period's days, rounded once, half to even.
func dailyAverage(total int64, period Period) (int64, error) {
	average, err := money.RoundHalfEven(big.NewRat(total, int64(period.Days())))
	if err != nil {
		return 0, fmt.Errorf("computing a daily average: %w", err)
	}
	return average, nil
}

// incomeComplete reports a cash flow whose every income was valued.
func incomeComplete(cf CashFlow) bool {
	for _, missing := range cf.Missing {
		if missing.IncomeMinor != 0 {
			return false
		}
	}
	return true
}

// compareCategories lines up every category of either period, matched by id.
//
// Unlike the dashboard's spending card, nothing is folded into an "other" line:
// a recap is about which categories moved, and the one that moved most may be
// small. A category present in only one period is NEW or GONE, with zero on the
// other side. Shares are of the current total and are allocated only when it is
// complete and positive.
func compareCategories(current, previous []CategoryAmount, total int64,
	comparable, currentComplete, hasPrevious bool, minorUnit int) ([]CategoryChange, error) {
	byID := make(map[int64]int, len(current)+len(previous))
	var out []CategoryChange
	for _, line := range current {
		byID[line.CategoryID] = len(out)
		out = append(out, CategoryChange{
			CategoryID: line.CategoryID, CategoryName: line.CategoryName,
			CurrentMinor: line.AmountMinor, Count: line.Count,
		})
	}
	if hasPrevious {
		for _, line := range previous {
			if i, ok := byID[line.CategoryID]; ok {
				out[i].PreviousMinor = line.AmountMinor
				continue
			}
			byID[line.CategoryID] = len(out)
			out = append(out, CategoryChange{
				CategoryID: line.CategoryID, CategoryName: line.CategoryName,
				PreviousMinor: line.AmountMinor,
			})
		}
	}

	var shares []int64
	if currentComplete && total > 0 {
		amounts := make([]int64, len(out))
		for i, c := range out {
			amounts[i] = c.CurrentMinor
		}
		var err error
		if shares, err = allocateShares(amounts, total); err != nil {
			return nil, err
		}
	}

	for i := range out {
		c := &out[i]
		c.CurrentFormatted = money.Format(c.CurrentMinor, minorUnit)
		c.PreviousFormatted = money.Format(c.PreviousMinor, minorUnit)
		var err error
		if c.Change, err = compare(c.PreviousMinor, c.CurrentMinor, comparable, hasPrevious, minorUnit); err != nil {
			return nil, err
		}
		c.Direction = direction(c.Change, c.PreviousMinor, c.CurrentMinor)
		if shares != nil {
			c.Share = Ratio{BasisPoints: shares[i], Defined: true}
		}
	}

	// The largest movement first, whichever way; then the larger current
	// amount, the name and the id, so the same data always lists the same way.
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := abs(out[i].CurrentMinor-out[i].PreviousMinor), abs(out[j].CurrentMinor-out[j].PreviousMinor)
		if di != dj {
			return di > dj
		}
		if out[i].CurrentMinor != out[j].CurrentMinor {
			return out[i].CurrentMinor > out[j].CurrentMinor
		}
		if out[i].CategoryName != out[j].CategoryName {
			return out[i].CategoryName < out[j].CategoryName
		}
		return out[i].CategoryID < out[j].CategoryID
	})
	return out, nil
}

// direction names which way a defined change went. An undefined change has no
// direction: its figures may be missing a flow that had no rate.
func direction(change Change, previous, current int64) Direction {
	switch {
	case !change.Defined:
		return DirectionUnknown
	case previous == 0 && current > 0:
		return DirectionNew
	case current == 0 && previous > 0:
		return DirectionGone
	case current > previous:
		return DirectionUp
	case current < previous:
		return DirectionDown
	default:
		return DirectionSame
	}
}

// rankExpenses values every expense at the rate of its own day and keeps the
// largest. An expense with no rate cannot be placed in the ranking and is
// counted instead; no amount is invented for it (TD-074).
func rankExpenses(items []ledger.ExpenseItem, convert converter, baseMinorUnit int) ([]RecapExpense, int, error) {
	var ranked []RecapExpense
	unvalued := 0
	for _, item := range items {
		exact, ok, err := convert(item.AmountMinor, item.CurrencyCode, item.Date)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			unvalued++
			continue
		}
		rounded, err := money.RoundHalfEven(exact)
		if err != nil {
			return nil, 0, fmt.Errorf("rounding expense %d: %w", item.TransactionID, err)
		}
		name := item.CategoryName
		if name == "" {
			name = uncategorised
		}
		ranked = append(ranked, RecapExpense{
			TransactionID:   item.TransactionID,
			Date:            item.Date,
			Description:     item.Description,
			CategoryName:    name,
			AccountName:     item.AccountName,
			CurrencyCode:    item.CurrencyCode,
			AmountMinor:     item.AmountMinor,
			AmountFormatted: money.Format(item.AmountMinor, item.MinorUnit),
			BaseMinor:       rounded,
			BaseFormatted:   money.Format(rounded, baseMinorUnit),
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].BaseMinor != ranked[j].BaseMinor {
			return ranked[i].BaseMinor > ranked[j].BaseMinor
		}
		if !ranked[i].Date.Equal(ranked[j].Date) {
			return ranked[i].Date.After(ranked[j].Date)
		}
		return ranked[i].TransactionID > ranked[j].TransactionID
	})
	if len(ranked) > topExpenses {
		ranked = ranked[:topExpenses]
	}
	return ranked, unvalued, nil
}

// accountChanges lines up each account's balance at the two ends of a period.
//
// The valuation lists an account when it is active or holds a balance, so an
// account can be on one side only: it held nothing on the other, which needs no
// rate and is valued at exactly zero.
func accountChanges(opening, closing valuation.NetWorth, minorUnit int) ([]AccountChange, error) {
	type end struct {
		balance, converted int64
		valued             bool
	}
	before := make(map[int64]end, len(opening.Accounts))
	for _, a := range opening.Accounts {
		before[a.AccountID] = end{a.BalanceMinor, a.ConvertedMinor, a.Valued}
	}

	var out []AccountChange
	seen := make(map[int64]bool, len(closing.Accounts))
	add := func(a valuation.AccountComponent, open, shut end) error {
		change := AccountChange{
			AccountID: a.AccountID, AccountName: a.AccountName, AccountType: a.AccountType,
			IsActive: a.IsActive, CurrencyCode: a.CurrencyCode,
			OpeningMinor: open.balance, ClosingMinor: shut.balance,
			DeltaMinor: shut.balance - open.balance,
		}
		change.OpeningFormatted = money.Format(change.OpeningMinor, a.MinorUnit)
		change.ClosingFormatted = money.Format(change.ClosingMinor, a.MinorUnit)
		change.DeltaFormatted = money.Format(change.DeltaMinor, a.MinorUnit)
		var err error
		if change.BaseChange, err = compare(open.converted, shut.converted,
			open.valued && shut.valued, true, minorUnit); err != nil {
			return err
		}
		out = append(out, change)
		return nil
	}

	for _, a := range closing.Accounts {
		seen[a.AccountID] = true
		open, ok := before[a.AccountID]
		if !ok {
			open = end{valued: true}
		}
		if err := add(a, open, end{a.BalanceMinor, a.ConvertedMinor, a.Valued}); err != nil {
			return nil, err
		}
	}
	for _, a := range opening.Accounts {
		if seen[a.AccountID] {
			continue
		}
		if err := add(a, before[a.AccountID], end{valued: true}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// indicators reads the four fixed comparisons (TD-073):
//
//	NET_WORTH      improved when the net worth grew over the period
//	NET_CASH_FLOW  improved when it is higher than the previous period's
//	EXPENSES       improved when the daily average is lower
//	SAVINGS_RATE   improved when it is higher
func indicators(r Recap) ([]Indicator, IndicatorSummary) {
	list := []Indicator{
		{Key: IndicatorNetWorth, Change: r.Change, Verdict: verdictOf(r.Change, 1)},
		{Key: IndicatorNetCashFlow, Change: r.NetCashFlowChange, Verdict: verdictOf(r.NetCashFlowChange, 1)},
		{Key: IndicatorExpenses, Change: r.ExpenseDailyAverageChange, Verdict: verdictOf(r.ExpenseDailyAverageChange, -1)},
		{Key: IndicatorSavingsRate, Points: r.SavingsRateChange, Verdict: verdictOfPoints(r.SavingsRateChange)},
	}
	var summary IndicatorSummary
	for _, indicator := range list {
		summary.Total++
		switch indicator.Verdict {
		case VerdictImproved:
			summary.Improved++
		case VerdictWorsened:
			summary.Worsened++
		case VerdictUnchanged:
			summary.Unchanged++
		case VerdictUnavailable:
			summary.Unavailable++
		}
	}
	return list, summary
}

// verdictOf reads a change by the sign that counts as better: +1 when more is
// better, -1 when less is.
func verdictOf(c Change, better int64) Verdict {
	if !c.Defined {
		return VerdictUnavailable
	}
	return verdictOfSign(c.AmountMinor * better)
}

func verdictOfPoints(r Ratio) Verdict {
	if !r.Defined {
		return VerdictUnavailable
	}
	return verdictOfSign(r.BasisPoints)
}

func verdictOfSign(n int64) Verdict {
	switch {
	case n > 0:
		return VerdictImproved
	case n < 0:
		return VerdictWorsened
	default:
		return VerdictUnchanged
	}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
