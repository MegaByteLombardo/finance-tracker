// Package creditcard owns what a credit card is beyond being an account (M8.1).
//
// A card IS an account -- account_type CREDIT_CARD, a liability (TD-089) -- and
// everything the ledger does with an account it does with a card: a purchase is
// an EXPENSE on it, a payment a TRANSFER or an FX_EXCHANGE into it, a previous
// debt a negative opening balance. Nothing here duplicates the account's name,
// institution, currency or status.
//
// What this package adds is the issuer's terms (credit_cards) and the one fact a
// card's debit can carry that its type cannot: that the issuer charged it, as
// interest, a fee or a penalty (credit_charges).
//
// # Nothing calculated is stored
//
// The debt is the account's balance, read from the ledger as of today. The
// available credit is the limit minus that debt, derived on every read. Which
// statement a movement belongs to, what a payment settles and what is overdue
// are derived from the movements and the statements (M8.2, TD-092). This
// package stores terms and assertions, never results.
package creditcard

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors this package returns. Callers distinguish them with errors.Is.
var (
	// ErrNotFound means the account has no card terms: it is not a card.
	ErrNotFound = errors.New("credit card not found")
	// ErrInvalidInput means the terms failed validation.
	ErrInvalidInput = errors.New("invalid credit card terms")
	// ErrNotACreditCard means an operation reserved for cards named an account
	// that is not one: terms for a bank account, a card purchase on cash.
	ErrNotACreditCard = errors.New("the account is not a credit card")
	// ErrAccountNotFound means the account does not exist.
	ErrAccountNotFound = errors.New("credit card account not found")
	// ErrPaymentMethodNotFound and ErrPaymentMethodInactive concern the card's
	// default payment method.
	ErrPaymentMethodNotFound = errors.New("default payment method not found")
	ErrPaymentMethodInactive = errors.New("default payment method is inactive")
	// ErrNotACharge means a charge was asked of a transaction that cannot be
	// one -- it is not an EXPENSE, or not on a liability -- or a charge edit
	// named a transaction that is not a charge.
	ErrNotACharge = errors.New("the transaction is not a card charge")
	// ErrChargeEditedAsCharge means a generic edit reached a card charge. A
	// charge is corrected through the charge's own use case, which keeps its
	// kind and its account in step (TD-045): a generic edit could turn it into
	// an income, or move it to a bank account, and leave the row that says
	// "the issuer charged this" pointing at something else.
	ErrChargeEditedAsCharge = errors.New("this expense is a card charge and is corrected as one")

	// M8.2: statements and the assertions that tie movements to them.

	// ErrStatementNotFound means no statement of this card has that id.
	ErrStatementNotFound = errors.New("statement not found")
	// ErrInvalidStatement means a statement's figures or dates failed
	// validation.
	ErrInvalidStatement = errors.New("invalid statement")
	// ErrStatementExists means the card already has a statement closing that
	// day, with different figures. Submitting the same statement twice is not
	// this error: it returns the one already recorded.
	ErrStatementExists = errors.New("the card already has a statement closing that day")
	// ErrStatementOverlap means a statement's period would overlap another's
	// of the same card: one day is billed by one statement at most.
	ErrStatementOverlap = errors.New("the statement's period overlaps another statement of the card")
	// ErrAssertionAfterClosing means a movement would belong to a statement
	// that closed before it happened -- asserted that way, or left that way by
	// moving a statement's closing date. Nothing is billed before it happens.
	ErrAssertionAfterClosing = errors.New("a movement cannot belong to a statement that closed before it happened")
	// ErrNotACardMovement means an assertion named a transaction that does not
	// move the statement's card.
	ErrNotACardMovement = errors.New("the transaction does not move this card")
)

// Denomination is what the card's debt is a number of.
type Denomination string

const (
	// Nominal is a debt of a fixed number of units of the card's currency.
	Nominal Denomination = "NOMINAL"
	// Indexed is a debt re-expressed against an index such as the UVC. M8 can
	// record such a card but its analytics assume Nominal and refuse it.
	Indexed Denomination = "INDEXED"
)

// Denominations lists every valid denomination, in the order a UI shows them.
func Denominations() []Denomination { return []Denomination{Nominal, Indexed} }

// Valid reports whether d is a known denomination.
func (d Denomination) Valid() bool { return d == Nominal || d == Indexed }

// RefundPolicy is what a refund dated after a closing does to what that
// statement asks for (R2.5). A refund ALWAYS lowers the debt and the next
// statement; the policy says whether the issuer also lowers the billed balance
// of the statement already emitted. It never touches the minimum or anything
// overdue: a refund is not a payment.
type RefundPolicy string

const (
	// RefundNextStatement: the billed balance stands; the refund appears on
	// the next statement. The default.
	RefundNextStatement RefundPolicy = "NEXT_STATEMENT"
	// RefundReducesBilled: the billed balance falls by the refund.
	RefundReducesBilled RefundPolicy = "REDUCES_BILLED"
)

// RefundPolicies lists every policy, in the order a UI shows them.
func RefundPolicies() []RefundPolicy { return []RefundPolicy{RefundNextStatement, RefundReducesBilled} }

// Valid reports whether p is a known policy.
func (p RefundPolicy) Valid() bool { return p == RefundNextStatement || p == RefundReducesBilled }

// ChargeKind is what the issuer charged as.
type ChargeKind string

const (
	Interest ChargeKind = "INTEREST"
	Fee      ChargeKind = "FEE"
	Penalty  ChargeKind = "PENALTY"
)

// ChargeKinds lists every kind, in the order a UI shows them.
func ChargeKinds() []ChargeKind { return []ChargeKind{Interest, Fee, Penalty} }

// Valid reports whether k is a known kind.
func (k ChargeKind) Valid() bool {
	for _, known := range ChargeKinds() {
		if k == known {
			return true
		}
	}
	return false
}

const (
	maxProductLength      = 120
	maxDueDays            = 60
	maxProcessingDays     = 10
	lastPossibleStatement = 31
)

// Card is a card's terms as stored.
type Card struct {
	AccountID int64

	// ProductName is the issuer's name for the product. nil when not given.
	ProductName *string

	// CreditLimitMinor is the limit in the card's currency's minor units. nil
	// means UNKNOWN, which is neither zero nor "unlimited".
	CreditLimitMinor *int64

	// StatementDay is the day of the month the statement closes, 1 to 31; in a
	// month without that day the cycle closes on the month's last day.
	StatementDay int

	// DueDaysAfterStatement is how long after the closing date payment is due.
	// nil means UNKNOWN, and an unknown due date never makes anything overdue.
	DueDaysAfterStatement *int

	// PaymentProcessingDays is how many business days the issuer takes to
	// apply a payment.
	PaymentProcessingDays int

	Denomination          Denomination
	DenominationConfirmed bool

	// DefaultPaymentMethodID is what a purchase form preselects.
	DefaultPaymentMethodID *int64

	// RefundPolicy is what a refund after a closing does to that statement's
	// billed balance (M8.2).
	RefundPolicy RefundPolicy

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TermsInput is what the user states about a card, on opening it and on
// editing it. It is a REPLACEMENT on edit: every field is sent every time.
type TermsInput struct {
	ProductName *string

	// CreditLimit is a positive decimal string read at the card currency's
	// scale ("5000000.00"), or nil / blank for UNKNOWN.
	CreditLimit *string

	StatementDay          int
	DueDaysAfterStatement *int
	PaymentProcessingDays int

	Denomination          Denomination
	DenominationConfirmed bool

	DefaultPaymentMethodID *int64

	// RefundPolicy defaults to RefundNextStatement when empty.
	RefundPolicy RefundPolicy
}

func (in *TermsInput) normalize() {
	if in.ProductName != nil {
		trimmed := strings.TrimSpace(*in.ProductName)
		if trimmed == "" {
			in.ProductName = nil
		} else {
			in.ProductName = &trimmed
		}
	}
	if in.CreditLimit != nil {
		trimmed := strings.TrimSpace(*in.CreditLimit)
		if trimmed == "" {
			in.CreditLimit = nil
		} else {
			in.CreditLimit = &trimmed
		}
	}
	in.Denomination = Denomination(strings.ToUpper(strings.TrimSpace(string(in.Denomination))))
	if in.Denomination == "" {
		in.Denomination = Nominal
	}
	in.RefundPolicy = RefundPolicy(strings.ToUpper(strings.TrimSpace(string(in.RefundPolicy))))
	if in.RefundPolicy == "" {
		in.RefundPolicy = RefundNextStatement
	}
}

// validate checks everything that does not need the database. The limit is read
// later, at the currency's scale.
func (in TermsInput) validate() error {
	if in.ProductName != nil && len([]rune(*in.ProductName)) > maxProductLength {
		return fmt.Errorf("%w: the product name must be at most %d characters", ErrInvalidInput, maxProductLength)
	}
	if in.StatementDay < 1 || in.StatementDay > lastPossibleStatement {
		return fmt.Errorf("%w: the statement day must be between 1 and %d", ErrInvalidInput, lastPossibleStatement)
	}
	if in.DueDaysAfterStatement != nil && (*in.DueDaysAfterStatement < 1 || *in.DueDaysAfterStatement > maxDueDays) {
		return fmt.Errorf("%w: payment must be due between 1 and %d days after the statement", ErrInvalidInput, maxDueDays)
	}
	if in.PaymentProcessingDays < 0 || in.PaymentProcessingDays > maxProcessingDays {
		return fmt.Errorf("%w: processing days must be between 0 and %d", ErrInvalidInput, maxProcessingDays)
	}
	if !in.Denomination.Valid() {
		return fmt.Errorf("%w: denomination %q must be one of %v", ErrInvalidInput, in.Denomination, Denominations())
	}
	if !in.RefundPolicy.Valid() {
		return fmt.Errorf("%w: refund policy %q must be one of %v", ErrInvalidInput, in.RefundPolicy, RefundPolicies())
	}
	return nil
}

// Position is a card's standing today, derived from the ledger and the terms on
// every read. Every amount is in the card's currency's minor units.
type Position struct {
	// BalanceMinor is the account's balance as the ledger states it: negative
	// when money is owed.
	BalanceMinor int64

	// DebtMinor is what is owed, as a positive number: max(0, -BalanceMinor).
	DebtMinor int64

	// CreditBalanceMinor is what the issuer owes the user -- an overpayment, a
	// refund larger than the debt -- as a positive number: max(0, BalanceMinor).
	CreditBalanceMinor int64

	// AvailableMinor is the limit minus the debt plus any credit balance, and
	// is meaningful only when HasLimit. It can be negative: a card over its
	// limit is reported as such, not clamped.
	AvailableMinor int64
	HasLimit       bool

	// FutureMovementCount is how many of the card's movements are dated after
	// today. They are listed but not yet part of the balance (TD-063).
	FutureMovementCount int
}

// position derives the standing from a balance and the terms.
func position(balanceMinor int64, futureCount int, card Card) Position {
	p := Position{
		BalanceMinor:        balanceMinor,
		DebtMinor:           max(0, -balanceMinor),
		CreditBalanceMinor:  max(0, balanceMinor),
		FutureMovementCount: futureCount,
	}
	if card.CreditLimitMinor != nil {
		p.HasLimit = true
		p.AvailableMinor = *card.CreditLimitMinor + balanceMinor
	}
	return p
}
