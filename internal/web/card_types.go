package web

import (
	"context"
	"errors"
)

// M8.1: credit cards. A card is an account whose nature is a LIABILITY: its
// balance is money owed, a purchase lowers it and a payment raises it. Every
// figure below was derived by the backend from the ledger and the card's terms
// on this read; nothing is stored, and no template adds anything up.

// AccountTypeCreditCard is the first liability (TD-089). It is NOT offered by
// the generic account form -- AccountTypes lists assets only -- because a card
// is opened with its terms, through /cards/new.
const AccountTypeCreditCard AccountType = "CREDIT_CARD"

// IsLiability reports whether accounts of this type owe money rather than hold
// it. It names the one liability type there is (TD-045).
func (t AccountType) IsLiability() bool { return t == AccountTypeCreditCard }

// TransactionTypeRefund is money given back for an expense (TD-090): one
// positive movement under an EXPENSE category. It is not income. It is created
// and corrected only through the /refunds routes.
const TransactionTypeRefund TransactionType = "REFUND"

// CardService is the M8.1 surface for credit cards.
//
// Opening a card and recording on one are idempotent on a request key the
// server rendered into the form (TD-095): a double submission returns what the
// first one recorded.
type CardService interface {
	List(ctx context.Context, includeInactive bool) ([]CreditCardView, error)
	Get(ctx context.Context, accountID int64) (CreditCardView, error)
	Open(ctx context.Context, in OpenCreditCardInput) (int64, error)
	UpdateTerms(ctx context.Context, accountID int64, in CreditCardTermsInput) error

	// Movements lists what moved the card, newest first, labelled for a
	// card: purchase, charge, payment, refund...
	Movements(ctx context.Context, accountID int64, limit int) ([]CardMovement, error)

	RecordPurchase(ctx context.Context, requestKey string, in RecordExpenseInput) (TransactionView, error)
	RecordCharge(ctx context.Context, requestKey string, kind string, in RecordExpenseInput) (TransactionView, error)
	UpdateCharge(ctx context.Context, id int64, kind string, in UpdateTransactionInput) (TransactionView, error)
	// ChargeKind returns what a charge was charged as, and false when the
	// transaction is not a charge.
	ChargeKind(ctx context.Context, id int64) (string, bool, error)

	// RecordRefund and UpdateRefund are the ONLY way a REFUND is created or
	// corrected, on any account.
	RecordRefund(ctx context.Context, requestKey string, in RecordRefundInput) (TransactionView, error)
	UpdateRefund(ctx context.Context, id int64, in UpdateRefundInput) (TransactionView, error)
}

// CreditCardView is a card with its terms and its standing today.
type CreditCardView struct {
	AccountID    int64
	Name         string
	Institution  *string
	CurrencyCode string
	MinorUnit    int
	IsActive     bool

	ProductName *string
	// CreditLimitFormatted is the limit in the card's currency, or "" when the
	// limit is UNKNOWN -- which is neither zero nor unlimited.
	CreditLimitFormatted string
	StatementDay         int
	// DueDaysAfterStatement is nil when UNKNOWN.
	DueDaysAfterStatement  *int
	PaymentProcessingDays  int
	Denomination           string
	DenominationConfirmed  bool
	DefaultPaymentMethodID *int64

	// The standing, in the card's currency. BalanceFormatted is the ledger's
	// signed figure; Debt and CreditBalance are its two readings, each
	// positive, at most one of them non-zero.
	BalanceMinor           int64
	BalanceFormatted       string
	DebtMinor              int64
	DebtFormatted          string
	CreditBalanceMinor     int64
	CreditBalanceFormatted string

	// AvailableFormatted is the limit minus the debt plus any credit balance,
	// present only when HasLimit. It can be negative: a card over its limit
	// is reported, not clamped.
	HasLimit           bool
	AvailableMinor     int64
	AvailableFormatted string

	FutureMovementCount int
}

// OverLimit reports a card whose debt exceeds its known limit.
func (c CreditCardView) OverLimit() bool { return c.HasLimit && c.AvailableMinor < 0 }

// DenominationLabel names the denomination for a screen.
func (c CreditCardView) DenominationLabel() string {
	switch c.Denomination {
	case "NOMINAL":
		return "Nominal"
	case "INDEXED":
		return "Indexada (UVC)"
	default:
		return c.Denomination
	}
}

// CreditCardTermsInput is what the user states about a card.
type CreditCardTermsInput struct {
	ProductName            *string
	CreditLimit            *string
	StatementDay           int
	DueDaysAfterStatement  *int
	PaymentProcessingDays  int
	Denomination           string
	DenominationConfirmed  bool
	DefaultPaymentMethodID *int64
}

// OpenCreditCardInput opens a card: the account, its terms and any previous
// debt, atomically.
type OpenCreditCardInput struct {
	RequestKey   string
	Name         string
	Institution  *string
	CurrencyCode string
	Terms        CreditCardTermsInput

	// OpeningDebt is what was owed before, as a POSITIVE amount, recorded as a
	// negative opening balance. nil means nothing was owed.
	OpeningDebt     *string
	OpeningDebtDate string
}

// RecordRefundInput gives money back for an expense, on any account.
type RecordRefundInput struct {
	AccountID       int64
	CategoryID      int64
	Amount          string
	PaymentMethodID int64
	Description     *string
	EffectiveDate   string

	// OriginalTransactionID names the expense it refunds, when the user says.
	OriginalTransactionID *int64
}

// UpdateRefundInput replaces every editable field of a refund, the link
// included.
type UpdateRefundInput struct {
	AccountID             int64
	CategoryID            int64
	Amount                string
	PaymentMethodID       int64
	Description           *string
	EffectiveDate         string
	OriginalTransactionID *int64
	AllowCurrencyChange   bool
}

// CardMovement is one transaction as a card's statement of activity shows it.
type CardMovement struct {
	Transaction TransactionView

	// Kind is what the movement is FOR THE CARD, inferred structurally from
	// its type, its direction and whether it is a charge -- never from a
	// category or a description (TD-044). See the CardMovement* constants.
	Kind      string
	KindLabel string

	// ChargeKind is INTEREST, FEE or PENALTY for a charge, "" otherwise.
	ChargeKind string

	// AmountFormatted is the movement's effect on the card, signed: negative
	// raises the debt, positive lowers it. For an exchange into the card it
	// is the amount that arrived, in the card's currency.
	AmountFormatted string
	Credit          bool
}

// What a movement is for a card. Inferred, never stored (TD-091).
const (
	CardMovementPurchase     = "PURCHASE"
	CardMovementCharge       = "CHARGE"
	CardMovementPayment      = "PAYMENT"
	CardMovementRefund       = "REFUND"
	CardMovementCashAdvance  = "CASH_ADVANCE"
	CardMovementOpening      = "OPENING_BALANCE"
	CardMovementOtherCredit  = "OTHER_CREDIT"
	CardMovementOtherOutflow = "OTHER_OUTFLOW"
)

// ChargeKinds lists what the issuer can charge as, in the order a form offers
// them.
func ChargeKinds() []string { return []string{"INTEREST", "FEE", "PENALTY"} }

// ChargeKindLabel names a charge kind for a screen.
func ChargeKindLabel(kind string) string {
	switch kind {
	case "INTEREST":
		return "Interés"
	case "FEE":
		return "Comisión"
	case "PENALTY":
		return "Penalización"
	default:
		return kind
	}
}

// Errors M8.1 adds.
var (
	// ErrNotACreditCard means a card operation named an account that is not
	// one.
	ErrNotACreditCard = errors.New("the account is not a credit card")
	// ErrNatureChange means an account edit tried to turn an asset into a
	// liability or the reverse.
	ErrNatureChange = errors.New("an account cannot change between an asset and a liability")
	// ErrNotACharge means a charge edit named something that is not a charge,
	// or would leave it something a charge cannot be.
	ErrNotACharge = errors.New("the transaction is not a card charge")
	// ErrChargeEditedAsCharge means a generic edit reached a card charge,
	// which is corrected through its own form.
	ErrChargeEditedAsCharge = errors.New("this expense is a card charge and is corrected as one")
	// ErrRefundOriginal means a refund named an expense it cannot refund.
	ErrRefundOriginal = errors.New("a refund can only name an expense of the same account")
	// ErrRefundExceedsOriginal means the refunds of one expense would add up
	// to more than it.
	ErrRefundExceedsOriginal = errors.New("the refunds of an expense cannot add up to more than it")
	// ErrInvalidRequestKey means the form's idempotency token was missing or
	// malformed; the form must be reloaded.
	ErrInvalidRequestKey = errors.New("invalid request key")
	// ErrSavingsLiabilityAccount means savings were asked of a liability,
	// which never backs reserved money (TD-094).
	ErrSavingsLiabilityAccount = errors.New("savings can only be backed by an asset account")
)
