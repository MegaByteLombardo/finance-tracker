// Package ledger owns the movement of money: what happened, and what it did to
// an account.
//
// It holds two concepts rather than one table:
//
//   - a transaction is the financial EVENT — its type, category, channel,
//     description and effective date;
//   - an account movement is the IMPACT of that event on one account, a signed
//     amount in that account's currency.
//
// They are separate because a transfer between two of the user's own accounts
// is one event with two impacts. That is a later milestone, but a flat
// transactions(account_id, amount) table would have to be migrated away from
// when it arrives, and migrating a ledger is exactly the thing worth avoiding.
// In M2 every transaction has exactly one movement.
//
// This package is the only place monetary amounts exist. Balances are never
// stored: they are SUM(amount_minor) over the movements, computed when read.
package ledger

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Errors this package returns. Callers distinguish them with errors.Is.
//
// The account and category errors are declared here, not re-exported from those
// packages, so that a handler dealing with transactions never has to import
// account or category to interpret what the ledger returned. That this package
// consults them is an implementation detail and must not leak.
var (
	// ErrNotFound means no transaction exists with the given id.
	ErrNotFound = errors.New("transaction not found")
	// ErrInvalidInput means the input failed validation: a malformed date, an
	// unknown payment method, a description that is present but empty.
	ErrInvalidInput = errors.New("invalid transaction")
	// ErrInvalidAmount means the amount could not be read exactly: unparseable,
	// more decimals than the currency has, out of range, or signed where the
	// use case does not allow it.
	//
	// It is separate from ErrInvalidInput so that a form can highlight the
	// amount field specifically. A handler that treats all validation alike
	// should check for both.
	ErrInvalidAmount = errors.New("invalid amount")
	// ErrAccountNotFound means the account the movement would land in does not
	// exist.
	ErrAccountNotFound = errors.New("transaction account not found")
	// ErrAccountInactive means the account exists but has been archived, so no
	// new movement may be recorded against it.
	ErrAccountInactive = errors.New("transaction account is inactive")
	// ErrCategoryNotFound means the requested category does not exist.
	ErrCategoryNotFound = errors.New("transaction category not found")
	// ErrCategoryInactive means the category exists but has been archived, so
	// it cannot classify a new transaction.
	ErrCategoryInactive = errors.New("transaction category is inactive")
	// ErrCategoryTypeMismatch means an expense was filed under an income
	// category, or the reverse.
	ErrCategoryTypeMismatch = errors.New("category type does not match the transaction type")
)

// TransactionType is what kind of financial event a transaction records.
type TransactionType string

const (
	// Income is money arriving from outside. Its movement is positive.
	Income TransactionType = "INCOME"
	// Expense is money leaving. Its movement is negative.
	Expense TransactionType = "EXPENSE"
	// OpeningBalance is where an account started before this application knew
	// about it. It is not income: recording a starting balance as income would
	// make it appear in every "what did I earn" figure forever.
	OpeningBalance TransactionType = "OPENING_BALANCE"
)

// TransactionTypes lists every valid type, in the order a UI would present
// them.
func TransactionTypes() []TransactionType {
	return []TransactionType{Income, Expense, OpeningBalance}
}

// Valid reports whether t is a known transaction type.
func (t TransactionType) Valid() bool {
	for _, known := range TransactionTypes() {
		if t == known {
			return true
		}
	}
	return false
}

// PaymentMethod is the CHANNEL an operation with the outside world used.
//
// It is not a transfer between the user's own accounts. BankTransfer here means
// "this external operation moved through a bank transfer"; a future
// TransactionType of TRANSFER will mean "money moved between two accounts we
// own". Confusing the two would make the ledger unreadable.
//
// The same four values apply to income and to expenses: money can arrive by
// transfer or in cash just as it can leave that way.
type PaymentMethod string

const (
	// BankTransfer is money moved through the banking system.
	BankTransfer PaymentMethod = "BANK_TRANSFER"
	// POS is a card payment at a point of sale.
	POS PaymentMethod = "POS"
	// CashPayment is physical money changing hands.
	CashPayment PaymentMethod = "CASH"
	// OtherMethod is any channel the four above do not describe.
	OtherMethod PaymentMethod = "OTHER"
)

// PaymentMethods lists every valid method, in the order a UI would present
// them.
func PaymentMethods() []PaymentMethod {
	return []PaymentMethod{BankTransfer, POS, CashPayment, OtherMethod}
}

// Valid reports whether m is a known payment method.
func (m PaymentMethod) Valid() bool {
	for _, known := range PaymentMethods() {
		if m == known {
			return true
		}
	}
	return false
}

const (
	maxDescriptionLength = 255

	// defaultListLimit is what a "recent activity" screen shows. maxListLimit
	// bounds what a caller may ask for: this is a listing, not an export, and
	// an unbounded query over a growing ledger is a page that gets slower every
	// month without anyone noticing.
	defaultListLimit = 50
	maxListLimit     = 500
)

// Transaction is a financial event as stored. It carries no amount and names no
// account; its movements do.
type Transaction struct {
	ID   int64
	Type TransactionType

	// CategoryID and PaymentMethod are set for income and expenses, and absent
	// for an opening balance. The database enforces that pairing.
	CategoryID    *int64
	PaymentMethod *PaymentMethod

	Description *string

	// EffectiveDate is when the event happened financially, as a calendar date
	// at UTC midnight. It is not CreatedAt: a purchase made on the 3rd and
	// entered on the 5th is dated the 3rd.
	EffectiveDate time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Movement is the impact of a transaction on one account.
type Movement struct {
	ID            int64
	TransactionID int64
	AccountID     int64

	// AmountMinor is signed and never zero. Its currency is the account's, by
	// construction: there is no currency column here to disagree with it.
	AmountMinor int64

	CreatedAt time.Time
}

// TransactionView is one movement joined to its transaction, its account and
// its currency: everything a table row needs, in one query.
//
// It is movement-centric rather than transaction-centric. In M2 that is a
// distinction without a difference, because every transaction has exactly one
// movement. When transfers arrive, one transaction will produce two rows here,
// and that change belongs to the milestone that introduces them rather than
// happening quietly now.
type TransactionView struct {
	// The event.
	ID            int64
	Type          TransactionType
	EffectiveDate time.Time
	Description   *string
	PaymentMethod *PaymentMethod

	// The category. Absent for an opening balance.
	CategoryID   *int64
	CategoryName *string
	CategoryType *category.Type

	// The account the money moved in.
	MovementID     int64
	AccountID      int64
	AccountName    string
	AccountType    account.Type
	AccountSubtype *account.Subtype
	Institution    *string

	// The currency, which is the account's currency, always.
	CurrencyCode   string
	CurrencySymbol *string
	MinorUnit      int

	// The amount, signed: income positive, expenses negative.
	AmountMinor int64
	// AmountFormatted is AmountMinor rendered exactly at the currency's scale,
	// sign included. Display this rather than computing it.
	AmountFormatted string

	// CreatedAt is when the row was recorded, not when the event happened.
	CreatedAt time.Time
}

// AccountBalance is one account with its balance derived from its movements.
type AccountBalance struct {
	AccountID      int64
	AccountName    string
	AccountType    account.Type
	AccountSubtype *account.Subtype
	Institution    *string
	IsActive       bool

	CurrencyCode   string
	CurrencySymbol *string
	MinorUnit      int

	// BalanceMinor is the sum of the account's movements. An account with none
	// has a balance of zero.
	BalanceMinor     int64
	BalanceFormatted string
}

// CurrencyTotal is the sum of every account balance in one currency.
//
// Currencies are never converted into one another: a dashboard shows USD, VES
// and EUR side by side. A single consolidated figure needs exchange rates and
// belongs to the FX milestone.
type CurrencyTotal struct {
	CurrencyCode   string
	CurrencyName   string
	CurrencySymbol *string
	MinorUnit      int

	AccountCount   int
	TotalMinor     int64
	TotalFormatted string
}

// RecordIncomeInput is the input to Service.RecordIncome.
type RecordIncomeInput struct {
	AccountID int64

	// CategoryID must name an active INCOME category.
	CategoryID int64

	// Amount is a positive decimal string such as "1500.00". The sign is
	// applied by the use case, never taken from here.
	Amount string

	PaymentMethod PaymentMethod

	// Description is optional. nil means none; an empty string is a caller
	// mistake and is rejected.
	Description *string

	// EffectiveDate is "YYYY-MM-DD" and is required.
	EffectiveDate string
}

// RecordExpenseInput is the input to Service.RecordExpense.
//
// Amount is positive here too. Sending "-25.99" is an error rather than a
// synonym: the sign belongs to the operation, which is what makes an expense
// that increases a balance impossible to express.
type RecordExpenseInput struct {
	AccountID     int64
	CategoryID    int64
	Amount        string
	PaymentMethod PaymentMethod
	Description   *string
	EffectiveDate string
}

// RecordOpeningBalanceInput is the input to Service.RecordOpeningBalance.
//
// It has no category and no payment method — not as fields left empty, but
// absent from the struct, so an opening balance cannot be given either.
type RecordOpeningBalanceInput struct {
	AccountID int64

	// Amount may be negative, for an account that starts overdrawn, and may be
	// zero, which records nothing at all.
	Amount string

	EffectiveDate string
}

// ListTransactionsInput filters Service.ListTransactions.
type ListTransactionsInput struct {
	// Limit caps the result. Zero or less uses the default of 50; anything
	// above 500 is capped at 500.
	Limit int
}

func (in ListTransactionsInput) limit() int {
	switch {
	case in.Limit <= 0:
		return defaultListLimit
	case in.Limit > maxListLimit:
		return maxListLimit
	default:
		return in.Limit
	}
}

// normalizeDescription trims the value behind a pointer. nil stays nil; a value
// that trims to empty stays empty so validation can reject it rather than
// quietly turning it into an absent description.
func normalizeDescription(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	return &trimmed
}

// validateDescription rejects a present-but-empty description.
//
// An empty string is not silently treated as "absent": a caller who sends ""
// believed they were sending a description, and swallowing that would hide
// their bug. Absence is expressed with nil, explicitly — the same rule as
// account.Institution and currency.Symbol.
func validateDescription(description *string) error {
	if description == nil {
		return nil
	}
	if *description == "" {
		return fmt.Errorf("%w: description must not be blank; omit it entirely instead", ErrInvalidInput)
	}
	if len([]rune(*description)) > maxDescriptionLength {
		return fmt.Errorf("%w: description must be at most %d characters",
			ErrInvalidInput, maxDescriptionLength)
	}
	return nil
}

// validateEffectiveDate parses the calendar date a transaction happened on.
//
// The layout demands zero-padded components, so "2026-9-5" is refused, and
// time.Parse validates the calendar itself, so "2026-02-31" is refused too. The
// same two rules are enforced by a CHECK constraint on the column.
func validateEffectiveDate(s string) (time.Time, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("%w: an effective date is required, as YYYY-MM-DD", ErrInvalidInput)
	}

	date, err := database.ParseDate(trimmed)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: effective date %q is not a date in the form YYYY-MM-DD",
			ErrInvalidInput, trimmed)
	}
	return date, nil
}

// validatePaymentMethod normalises and checks a required payment method.
func validatePaymentMethod(m PaymentMethod) (PaymentMethod, error) {
	normalized := PaymentMethod(strings.ToUpper(strings.TrimSpace(string(m))))
	if !normalized.Valid() {
		return "", fmt.Errorf("%w: payment method %q must be one of %v",
			ErrInvalidInput, m, PaymentMethods())
	}
	return normalized, nil
}
