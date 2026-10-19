// Package savings holds M7: the savings policy, the suggestion it produces for
// each eligible income, and -- in later submilestones -- the money reserved
// against those suggestions and the goals it is assigned to.
//
// It is a layer OVER the ledger, never a second ledger. It reads incomes,
// accounts and reference exchange rates; it writes only its own tables; and
// nothing in it moves a balance or takes part in the net worth. Four things are
// kept apart, in the code as in the schema (docs/m7-contract.md):
//
//	the income recorded         transactions, owned by internal/ledger
//	the saving suggested        savings_suggestions, here (M7.1)
//	the money reserved          M7.2
//	the money assigned to goals M7.3
//
// A suggestion is computed when an eligible income is recorded or corrected, in
// the SAME transaction (internal/application). Every Tx method here therefore
// works through the executor it is handed and never through the pool: the pool
// has exactly one connection, and asking it for a second one from inside an
// open transaction would wait forever.
package savings

import (
	"errors"
	"math/big"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// FundCurrency is the currency every M7 figure is denominated in.
//
// It is NOT the valuation base of app_settings, which the user may change: the
// savings fund is held in dollars whatever the net worth is expressed in. An
// income in another currency is converted into this one at the reference rate
// in force on the income's date.
const FundCurrency = "USD"

var (
	// ErrInvalidInput means the request is malformed: a rate that is not a
	// percentage, a date that is not a date, a blank reason.
	ErrInvalidInput = errors.New("invalid savings input")

	// ErrNotFound means no suggestion exists for what was asked.
	ErrNotFound = errors.New("savings suggestion not found")

	// ErrIncomeNotFound means the transaction does not exist or is not an
	// INCOME. Translated from the ledger, so a handler need not import it.
	ErrIncomeNotFound = errors.New("income not found")

	// ErrCategoryNotFound means no category exists with that id.
	ErrCategoryNotFound = errors.New("category not found")

	// ErrCategoryNotIncome means eligibility was set on an EXPENSE category.
	// Only income is ever saved from.
	ErrCategoryNotIncome = errors.New("only an income category can be eligible for savings")

	// ErrRetroactivePolicy means a version after the first would start before
	// today, or before the version already in force. Only the FIRST version may
	// reach back (TD-077): a later one would change the rate applied to incomes
	// that already have suggestions.
	ErrRetroactivePolicy = errors.New("a later savings policy version cannot start in the past")

	// ErrPolicyVersionInUse means a version starting on that date already
	// governs at least one suggestion, so its rate can no longer change.
	ErrPolicyVersionInUse = errors.New("that savings policy version already governs suggestions")

	// ErrInvalidState means the suggestion is not in a status that allows the
	// action: dismissing one that is waiting for a rate, valuing one that is
	// already valued.
	ErrInvalidState = errors.New("the savings suggestion does not allow this")

	// ErrRateUnavailable means no reference rate connects the income's
	// currency with USD on the income's date. The suggestion stays pending; it
	// is never valued at zero or at a later rate.
	ErrRateUnavailable = errors.New("no exchange rate is available for the income's date")

	// ErrNotYetEffective means the income is dated in the future, so the rate
	// that will be in force on its date cannot be known yet (TD-081).
	ErrNotYetEffective = errors.New("the income's date has not arrived yet")

	// ErrRateUnchanged means a revaluation was asked for but the rate the
	// suggestion used is still the one in force on its date.
	ErrRateUnchanged = errors.New("the exchange rate of the income's date has not changed")

	// ErrReasonRequired means a decision that must explain itself -- a release,
	// an exception, an override -- came without a reason. It is always wrapped
	// together with ErrInvalidInput.
	ErrReasonRequired = errors.New("a reason is required")

	// ErrExceedsMovement means more savings were assigned to a movement than
	// the movement's own amount. Wrapped together with ErrInvalidInput.
	ErrExceedsMovement = errors.New("the savings exceed the movement")
)

// PolicyVersion is one rate of the savings policy and the day it starts.
type PolicyVersion struct {
	ID int64

	// RateBP is basis points of each eligible income: 2000 is 20%.
	RateBP int64

	// EffectiveFrom is the first income date this version governs. The version
	// applied to an income is the one with the latest EffectiveFrom on or
	// before the income's effective date -- never chosen by when anything was
	// recorded.
	EffectiveFrom time.Time

	// SuggestionCount is how many suggestions, of any status, this version
	// governs. A version with suggestions never changes its rate.
	SuggestionCount int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Policy is the whole history of the savings rate.
type Policy struct {
	// Versions are newest first.
	Versions []PolicyVersion

	// Current is the version in force today, if any.
	Current *PolicyVersion

	// Scheduled is a version that starts after today, if any.
	Scheduled *PolicyVersion

	// Start is the first version's EffectiveFrom: incomes dated before it are
	// never suggested from. Nil when no policy was ever set.
	Start *time.Time
}

// SetPolicyInput is the input to Service.SetPolicy.
type SetPolicyInput struct {
	// Rate is a percentage as a decimal string with at most two decimals:
	// "20", "12.5", "7.25". It is read as basis points without a float.
	Rate string

	// EffectiveFrom is "YYYY-MM-DD".
	EffectiveFrom string
}

// CategoryEligibility is one INCOME category and whether its income is saved
// from.
type CategoryEligibility struct {
	CategoryID int64
	Name       string
	IsActive   bool

	// Eligible is the decision. Explicit says whether it comes from a rule the
	// user set rather than from the default, which is eligible (TD-076).
	Eligible bool
	Explicit bool
}

// IncomeOverride is an explicit exception for one income.
type IncomeOverride struct {
	Eligible  bool
	Reason    string
	UpdatedAt time.Time
}

// Status is where a suggestion revision stands.
//
// The confirmation states -- pending, partially confirmed, confirmed -- are not
// here. They are what an OPEN suggestion looks like against the money reserved
// for it, which is derived on read (M7.2) and never stored.
type Status string

const (
	// StatusPendingValuation is live and waiting for a rate, or for its date.
	StatusPendingValuation Status = "PENDING_VALUATION"
	// StatusOpen is live and valued.
	StatusOpen Status = "OPEN"
	// StatusDismissed is live, valued, and declined by the user.
	StatusDismissed Status = "DISMISSED"
	// StatusSuperseded is closed: the next revision replaced it.
	StatusSuperseded Status = "SUPERSEDED"
	// StatusInvalidated is closed: the income stopped being eligible.
	StatusInvalidated Status = "INVALIDATED"
)

// Live reports whether the revision is the current one for its income. Exactly
// these three statuses are live; the partial unique index in 00015 names the
// same three.
func (s Status) Live() bool {
	return s == StatusPendingValuation || s == StatusOpen || s == StatusDismissed
}

// PendingReason says why a live revision has no valuation yet.
type PendingReason string

const (
	// PendingNoRate: no reference rate connects the currency with USD on the
	// income's date.
	PendingNoRate PendingReason = "NO_RATE"
	// PendingFutureDate: the income's date has not arrived, so the rate in
	// force on it is not known yet (TD-081).
	PendingFutureDate PendingReason = "FUTURE_DATE"
)

// RateLeg is one reference observation a valuation used, as it was when the
// suggestion was made (TD-078).
type RateLeg struct {
	ExchangeRateID    int64
	BaseCurrencyCode  string
	QuoteCurrencyCode string
	Rate              money.Decimal
	EffectiveDate     time.Time
	Source            fx.Source
	Inverted          bool
}

// Suggestion is one revision of what the policy says to save from an income.
type Suggestion struct {
	ID                  int64
	IncomeTransactionID int64
	Revision            int

	PolicyVersionID     int64
	RateBP              int64
	PolicyEffectiveFrom time.Time

	// The income as it was when this revision was made.
	IncomeAccountID     int64
	IncomeAccountName   string
	IncomeCategoryID    int64
	IncomeCategoryName  string
	IncomeAmountMinor   int64
	IncomeCurrencyCode  string
	IncomeMinorUnit     int
	IncomeEffectiveDate time.Time

	// IncomeDescription is the income's description NOW: it is not part of
	// what the suggestion is computed from, so it is read, not recorded.
	IncomeDescription *string

	// The valuation in USD cents. Nil while pending.
	IncomeUSDMinor    *int64
	SuggestedUSDMinor *int64
	FXMethod          *fx.Method
	ValuedAt          *time.Time
	Legs              []RateLeg

	Status       Status
	StatusReason *string

	CreatedAt time.Time
	UpdatedAt time.Time

	// Derived on read, never stored.

	// Pending says why a PENDING_VALUATION revision has no amount.
	Pending PendingReason

	// RateChanged is true when the rate in force on the income's date is no
	// longer the one this valuation used -- a corrected publication, a manual
	// override added or archived. It is reported; revaluing is explicit.
	RateChanged bool

	// ConfirmedUSDMinor is what reservations confirmed against THIS revision
	// (M7.2).
	ConfirmedUSDMinor int64

	// For a live revision: what is confirmed for the income across all of its
	// revisions, what remains of the suggestion, any excess confirmed above it
	// (an exception, or a revision that lowered the figure), and the derived
	// confirmation state. Reservations are never moved by a revision.
	IncomeConfirmedUSDMinor int64
	RemainingUSDMinor       int64
	OverConfirmedUSDMinor   int64
	Confirmation            Confirmation
}

// Confirmation is what an open suggestion looks like against the money
// reserved for it. It is derived on read and never stored (TD-079).
type Confirmation string

const (
	// ConfirmationPending: nothing confirmed yet.
	ConfirmationPending Confirmation = "PENDING_CONFIRMATION"
	// ConfirmationPartial: part of the suggestion confirmed.
	ConfirmationPartial Confirmation = "PARTIALLY_CONFIRMED"
	// ConfirmationFull: the suggestion confirmed in full, or beyond.
	ConfirmationFull Confirmation = "CONFIRMED"
)

// Overview is the savings policy's figures for a period.
//
// A suggestion belongs to the period that contains its income's EFFECTIVE
// date, never to the day it was created or valued.
type Overview struct {
	From time.Time
	To   time.Time

	Policy Policy

	// USD cents, each summed over the live, valued revisions of the period
	// (by the income's effective date).
	EligibleIncomeUSDMinor int64 // the incomes, converted
	SuggestedUSDMinor      int64 // OPEN and DISMISSED: everything the policy suggested
	ConfirmedUSDMinor      int64 // reserved against those suggestions, whenever
	OpenUSDMinor           int64 // OPEN: what remains to confirm
	DismissedUSDMinor      int64 // DISMISSED: what was declined
	OverConfirmedUSDMinor  int64 // confirmed above the live suggestion

	// ConfirmationProgress is ConfirmedUSDMinor over SuggestedUSDMinor: how
	// much of what was suggested has been honoured. It is NOT the growth of
	// the fund, which the figures below measure.
	ConfirmationProgress Ratio

	// The fund's own movement in the period, by the operations' dates (the
	// confirmation date, not the income's): gross contributions, withdrawals
	// (releases and spending), and their difference. Reallocations between
	// goals move nothing in or out.
	GrossContributionsMinor int64
	WithdrawalsMinor        int64
	NetChangeMinor          int64

	// Fund is the reserved balance today.
	Fund Fund

	OpenCount      int
	DismissedCount int

	// Pending valuations are counted and their amounts stated in their own
	// currencies, never estimated in USD.
	PendingValuationCount int
	PendingByCurrency     []CurrencyAmount

	// RateChangedCount is how many open suggestions were valued at a rate that
	// has since changed.
	RateChangedCount int

	// UnsyncedCount is how many incomes the sync preview would change.
	UnsyncedCount int

	// IssueCount is how many reconciliation issues are not acknowledged, and
	// IssueTotal how many there are (M7.4).
	IssueCount int
	IssueTotal int
}

// Ratio is basis points, with Defined false for a ratio over nothing.
type Ratio struct {
	BasisPoints int64
	Defined     bool
}

// CurrencyAmount is an amount in its own currency.
type CurrencyAmount struct {
	CurrencyCode string
	MinorUnit    int
	AmountMinor  int64
}

// IncomeDetail is one income seen from the savings side: what it is now,
// whether it is eligible and why, and every suggestion revision it has had.
type IncomeDetail struct {
	TransactionID int64
	Description   *string
	Date          time.Time
	AccountName   string
	CategoryID    int64
	CategoryName  string
	CurrencyCode  string
	MinorUnit     int
	AmountMinor   int64

	Eligible bool
	// Reason explains the decision in words, eligible or not.
	Reason string

	Override *IncomeOverride

	// Revisions are oldest first.
	Revisions []Suggestion
}

// SyncAction is what the sync would do to one income.
type SyncAction string

const (
	// SyncCreate: an eligible income has no live suggestion.
	SyncCreate SyncAction = "CREATE"
	// SyncRevise: the income changed since its live suggestion was made.
	SyncRevise SyncAction = "REVISE"
	// SyncInvalidate: the income is no longer eligible.
	SyncInvalidate SyncAction = "INVALIDATE"
)

// SyncItem is one income the sync would change.
type SyncItem struct {
	Action        SyncAction
	TransactionID int64
	Date          time.Time
	Description   *string
	AccountName   string
	CategoryName  string
	CurrencyCode  string
	MinorUnit     int
	AmountMinor   int64

	// RateBP is the policy rate that would apply, for CREATE and REVISE.
	RateBP int64

	// Reason explains the action in words.
	Reason string
}

// SyncPlan is the preview of a sync: every income whose suggestions would
// change, and how. Nothing is written until it is applied.
type SyncPlan struct {
	Items []SyncItem
}

// SyncResult counts what a sync did.
type SyncResult struct {
	Created     int
	Revised     int
	Invalidated int
}

// valuation is a computed suggestion amount and where its rate came from.
type valuation struct {
	incomeUSDMinor    int64
	suggestedUSDMinor int64
	method            fx.Method
	legs              []fx.Leg
}

// bpDenominator is BasisPointsPerUnit as a big.Int, for exact arithmetic.
var bpDenominator = big.NewInt(money.BasisPointsPerUnit)
