// Package analytics answers the dashboard's questions about a period: how the
// net worth moved, what came in, what went out, and where it went.
//
// It computes nothing about money that another package already owns. Balances
// and their history come from internal/valuation, which rebuilds them from the
// ledger (TD-065); flows come from the ledger's own query, which accepts INCOME
// and EXPENSE and nothing else (TD-068); every conversion goes through the same
// snapshot, at the reference rate of the flow's own day, with fx's lookup
// policy. What this package adds is the arithmetic OF a period: totals,
// differences and ratios, all exact, all rounded once, half to even, by
// internal/money.
//
// # Why this is its own package
//
// Like internal/valuation it is a cross-domain READ and writes nothing, so it
// is not in internal/application (TD-030). It is not inside valuation because
// valuation answers "what is it worth on a day", and a cash flow is a different
// question: what happened between two days.
//
// # Nothing here is stored
//
// Every figure is derived on every request. There is no table, no cache and no
// background job behind any of it.
//
// # No floats
//
// Amounts are int64 minor units, intermediate values are big.Rat, and every
// percentage is an int64 number of basis points. A screen that needs a number
// to draw gets a formatted string or an integer.
package analytics

import (
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
)

// Ratio is a percentage in basis points: 724 is 7.24%.
//
// Defined is false when the ratio has no value -- a savings rate with no
// income, a change from zero, a comparison involving an incomplete figure. A
// caller handed false shows "—"; there is no float in which a NaN or an
// infinity could hide.
type Ratio struct {
	BasisPoints int64
	Defined     bool
}

// Reason says why a figure is not available.
type Reason string

const (
	// ReasonNone means the figure is available.
	ReasonNone Reason = ""

	// ReasonIncomplete means a rate was missing for one of the figures the
	// result depends on, so it would have been computed from a partial total
	// (TD-067).
	ReasonIncomplete Reason = "INCOMPLETE"

	// ReasonNoPreviousPeriod means there is nothing to compare with: the ALL
	// preset has no period before it.
	ReasonNoPreviousPeriod Reason = "NO_PREVIOUS_PERIOD"
)

// Change is the difference between two figures, and that difference relative to
// the first.
type Change struct {
	// Defined is false when the absolute change cannot be stated; Reason says
	// why.
	Defined bool
	Reason  Reason

	AmountMinor     int64
	AmountFormatted string

	// Percent is the change over the absolute value of the starting figure. It
	// is undefined when the change is, and when the starting figure is zero.
	Percent Ratio
}

// CategoryAmount is one category's total over a period, in the base currency.
type CategoryAmount struct {
	// CategoryID is zero only for a flow with no category, which the schema
	// makes unreachable for income and expenses.
	CategoryID   int64
	CategoryName string

	// AmountMinor is POSITIVE for income and for expenses alike: it is how much
	// came in, or how much went out.
	AmountMinor     int64
	AmountFormatted string

	// Count is how many transactions the line sums.
	Count int
}

// MissingFlow is money that came in or went out in a currency with no rate on
// its day, stated in that currency because it cannot be stated in the base.
type MissingFlow struct {
	CurrencyCode string
	MinorUnit    int

	IncomeMinor      int64
	IncomeFormatted  string
	ExpenseMinor     int64
	ExpenseFormatted string

	// ExpenseFrom and ExpenseTo are the first and last day of an unvalued
	// expense in this currency; zero when there is none. A rate dated
	// ExpenseFrom values every one of them (latest effective_date <= day).
	ExpenseFrom time.Time
	ExpenseTo   time.Time

	Count int
}

// CashFlow is what came in and what went out over one period, in the valuation
// base.
//
// Only INCOME and EXPENSE count. A transfer between two of the user's own
// accounts and a currency exchange are neither, and an opening balance states
// where an account started (TD-068).
type CashFlow struct {
	Period Period

	// BaseCurrencyCode is the valuation base every total is expressed in.
	BaseCurrencyCode string

	IncomeMinor      int64
	IncomeFormatted  string
	ExpenseMinor     int64
	ExpenseFormatted string

	// NetMinor is IncomeMinor - ExpenseMinor, and may be negative.
	NetMinor     int64
	NetFormatted string

	// SavingsRate is NetMinor / IncomeMinor. It is undefined when there was no
	// income, and when the figures are incomplete.
	SavingsRate Ratio

	// Complete is false when some flow could not be converted. The totals then
	// leave it out and Missing says what it was.
	Complete bool
	Missing  []MissingFlow

	// IncomeCategories and ExpenseCategories add up exactly to IncomeMinor and
	// ExpenseMinor: each line is rounded once, and the totals are their sums.
	// Largest first.
	IncomeCategories  []CategoryAmount
	ExpenseCategories []CategoryAmount

	TransactionCount int
}

// CategoryComparison is one spending category in the current period against
// the previous one.
type CategoryComparison struct {
	CategoryID   int64
	CategoryName string

	// Other is true for the line that gathers every category below the top
	// ones, so the lines still add up to the total.
	Other bool

	CurrentMinor      int64
	CurrentFormatted  string
	PreviousMinor     int64
	PreviousFormatted string

	Change Change

	// Share is CurrentMinor as a part of the period's total spending.
	Share Ratio
}

// Spending is the expense side of a period, looked at more closely.
type Spending struct {
	TotalMinor     int64
	TotalFormatted string

	// DailyAverageMinor is TotalMinor over the period's days, rounded once,
	// half to even.
	DailyAverageMinor     int64
	DailyAverageFormatted string

	// Categories is the top spending categories, largest first, plus an
	// "other" line when there are more. Their CurrentMinor add up to TotalMinor.
	Categories []CategoryComparison

	PreviousTotalMinor     int64
	PreviousTotalFormatted string

	// Change is the total against the previous period's.
	Change Change
}

// Dashboard is everything the dashboard shows, computed.
//
// A handler renders it; a template formats nothing that is not already a
// string here, and adds nothing up.
type Dashboard struct {
	Preset Preset

	// Today is the Venezuelan calendar date the dashboard was computed for.
	Today time.Time

	Period         Period
	PreviousPeriod Period
	HasPrevious    bool

	// NetWorth is the net worth today, with its breakdown by currency and by
	// account. It is the same figure as the last point of Series.
	NetWorth valuation.NetWorth

	// Series is the net worth at the close of every day of Period.
	Series valuation.Series

	// Opening is the net worth at the close of the day before Period starts:
	// the baseline Change is measured from.
	Opening valuation.Point

	// Change is NetWorth against Opening (TD-069).
	Change Change

	// Gaps are the missing rates of Opening and of Series together. Opening is
	// not drawn, but a rate missing on that day is what makes Change
	// unavailable, so a screen that lists what to load must list it too.
	Gaps []valuation.Gap

	CashFlow         CashFlow
	PreviousCashFlow CashFlow

	Spending Spending
}
