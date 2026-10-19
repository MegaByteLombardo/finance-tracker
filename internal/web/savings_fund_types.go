package web

import (
	"errors"
	"time"
)

// M7.2: the savings fund as the presentation layer sees it. Amounts arrive
// formatted, and everything a screen may offer arrives decided.

var (
	// ErrSavingsInsufficientFree: the account's free balance does not cover it.
	ErrSavingsInsufficientFree = errors.New("the account's free balance does not cover this")
	// ErrSavingsInsufficientReserved: less is reserved there than requested.
	ErrSavingsInsufficientReserved = errors.New("less is reserved there than requested")
	// ErrSavingsExceedsSuggestion: confirming more than remains, without an
	// exception.
	ErrSavingsExceedsSuggestion = errors.New("this would confirm more than the suggestion")
	// ErrSavingsNotUSDAccount: savings are backed by USD accounts only.
	ErrSavingsNotUSDAccount = errors.New("savings can only be backed by a USD account")
	// ErrSavingsInvalidRequestKey: the form's idempotency token is missing.
	ErrSavingsInvalidRequestKey = errors.New("invalid request key")
)

// SavingsAccountAvailability is one USD account seen from the fund.
type SavingsAccountAvailability struct {
	AccountID   int64
	AccountName string
	IsActive    bool

	BalanceFormatted       string
	ReservedFormatted      string
	FreeFormatted          string
	OverallocatedFormatted string

	// HasReservation, HasFree and Overallocated are decided by the backend so
	// a template compares no amount.
	HasReservation bool
	HasFree        bool
	Overallocated  bool
}

// SavingsFund is the fund today.
type SavingsFund struct {
	Accounts []SavingsAccountAvailability

	BalanceFormatted       string
	ReservedFormatted      string
	FreeFormatted          string
	OverallocatedFormatted string
	AllocatedFormatted     string
	UnallocatedFormatted   string

	Overallocated bool
	// CanReserve: some active account has a free balance.
	CanReserve bool
	// CanRelease: something is reserved.
	CanRelease bool
}

// SavingsEntry is one impact of an operation.
type SavingsEntry struct {
	AccountName string
	GoalName    string
	// AmountFormatted is signed: "-300.00" for money given back.
	AmountFormatted string
}

// SavingsOperation is one thing the user did with savings.
type SavingsOperation struct {
	ID        int64
	Kind      string
	KindLabel string

	IncomeTransactionID *int64
	TransactionID       *int64
	Reason              *string
	IsException         bool

	EffectiveDate time.Time
	CreatedAt     time.Time

	Entries []SavingsEntry
	// TotalFormatted is signed.
	TotalFormatted string
	IsOutflow      bool
}

// SavingsReserveInput sets money aside, optionally confirming an income's
// suggestion.
type SavingsReserveInput struct {
	RequestKey          string
	IncomeTransactionID *int64
	AccountID           int64
	GoalID              *int64
	Amount              string
	Exception           bool
	Reason              string
}

// SavingsReleaseInput gives reserved money back, with a reason.
type SavingsReleaseInput struct {
	RequestKey string
	AccountID  int64
	GoalID     *int64
	Amount     string
	Reason     string
}

// SavingsFundView is the fund page.
type SavingsFundView struct {
	Available bool
	Error     string

	Fund       SavingsFund
	Operations []SavingsOperation

	// ReserveKey and ReleaseKey are fresh idempotency tokens for the forms.
	ReserveKey string
	ReleaseKey string

	Return     string
	Alert      *FlashAlert
	Navigation MobileNavView
}

// Help explains a savings term.
func (SavingsFundView) Help(key string) RecapHelp { return SavingsHelp(key) }
