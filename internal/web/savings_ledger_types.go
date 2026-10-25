package web

import (
	"errors"
	"time"
)

// M7.4: the ledger meets the fund, as the presentation layer sees it.

var (
	// ErrSavingsNotLinkable: the transaction cannot carry that savings
	// operation (a VES expense spending USD savings, for one).
	ErrSavingsNotLinkable = errors.New("the transaction cannot be linked to savings this way")
	// ErrSavingsReasonRequired: a release or exception came without a reason.
	// It also matches ErrInvalidInput.
	ErrSavingsReasonRequired = errors.New("a reason is required")
	// ErrSavingsExceedsMovement: more savings than the movement's amount. It
	// also matches ErrInvalidInput.
	ErrSavingsExceedsMovement = errors.New("the savings exceed the movement")
)

// SavingsUse is an amount of one reservation slice, consumed or moved. A nil
// goal is the unallocated part.
type SavingsUse struct {
	GoalID *int64
	Amount string
}

// SavingsExpensePlan is what an expense does to savings. Spend is money the
// expense was for (a goal's own purpose); Release is reserved money given back
// to cover it, which needs Reason.
type SavingsExpensePlan struct {
	RequestKey string
	Spend      []SavingsUse
	Release    []SavingsUse
	Reason     string
}

// SavingsTransferPlan moves the backing of reserved money from a transfer's
// source to its destination.
type SavingsTransferPlan struct {
	RequestKey string
	Move       []SavingsUse
}

// SavingsSlice is the money reserved on one account for one goal, or
// unallocated on it.
type SavingsSlice struct {
	GoalID            *int64
	GoalName          string
	ReservedFormatted string
}

// SavingsAccountImpact is what an outflow would do to an account's
// reservations. Applies is false for an account outside the fund (not USD), in
// which case nothing is shown.
type SavingsAccountImpact struct {
	Applies     bool
	AccountName string

	BalanceFormatted   string
	ReservedFormatted  string
	FreeFormatted      string
	ShortfallFormatted string

	HasShortfall    bool
	HasReservations bool
	Slices          []SavingsSlice
}

// SavingsIssue is one reconciliation issue.
type SavingsIssue struct {
	Key       string
	Kind      string
	KindLabel string

	AccountName         string
	IncomeTransactionID *int64
	TransactionID       *int64
	AmountFormatted     string
	Detail              string

	Acknowledged bool
	AckNote      string
	AckAt        time.Time
}

// SavingsFundMonth is one month of the fund.
type SavingsFundMonth struct {
	Month                  time.Time
	ContributionsFormatted string
	WithdrawalsFormatted   string
	NetFormatted           string
	ClosingFormatted       string
}

// SavingsGoalMonth is one month of a goal.
type SavingsGoalMonth struct {
	Month            time.Time
	InFormatted      string
	OutFormatted     string
	ClosingFormatted string
}

// SavingsLinkedExpense is an expense that spent a goal's money.
type SavingsLinkedExpense struct {
	TransactionID   int64
	Date            time.Time
	Description     *string
	AmountFormatted string
	SpentFormatted  string
}

// SavingsImpactView is the partial a movement form loads beside its amount.
type SavingsImpactView struct {
	Impact SavingsAccountImpact
	// Mode is "expense" or "transfer": which fields the partial renders.
	Mode string
	// Key is the idempotency token of the plan this partial posts.
	Key string
}

// SavingsIssuesView is the reconciliation page.
type SavingsIssuesView struct {
	Available  bool
	Error      string
	Issues     []SavingsIssue
	Return     string
	Alert      *FlashAlert
	Navigation MobileNavView
}
