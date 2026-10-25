package web

import (
	"context"
	"errors"
	"time"
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

	CardStatementService
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
	// RefundPolicy is NEXT_STATEMENT or REDUCES_BILLED (M8.2).
	RefundPolicy string

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
	RefundPolicy           string
}

// RefundPolicyLabel names a refund policy for a screen.
func RefundPolicyLabel(policy string) string {
	switch policy {
	case "NEXT_STATEMENT":
		return "En el próximo estado de cuenta"
	case "REDUCES_BILLED":
		return "Reduce lo facturado"
	default:
		return policy
	}
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

// M8.2: statements, obligations and payments. Every figure below was derived
// by internal/creditcard on this read, from the statements the bank emitted,
// the card's movements and the user's assertions (TD-092). Nothing is stored
// and no template compares two amounts: a status, a label and a formatted
// figure arrive ready to show.

// CardStatementService is the M8.2 surface: what a card owes and what each
// payment settled, the statements, their reconciliation and the payment form.
type CardStatementService interface {
	Standing(ctx context.Context, accountID int64) (CardStandingView, error)

	// RecordPayment records a payment: a TRANSFER from an account in the
	// card's currency, an FX_EXCHANGE from another one, with an optional
	// savings plan for a USD source (D-12). Idempotent on its request key.
	RecordPayment(ctx context.Context, in RecordCardPaymentInput) (TransactionView, error)

	// NextStatementDates proposes the period and closing of the next
	// statement to record, as YYYY-MM-DD.
	NextStatementDates(ctx context.Context, accountID int64) (periodStart, closing string, err error)
	// Statement returns a recorded statement as its form holds it.
	Statement(ctx context.Context, accountID, statementID int64) (CardStatementInput, error)
	// RecordStatement records what the bank emitted; the same statement twice
	// returns the one already recorded (its closing day is its key).
	RecordStatement(ctx context.Context, accountID int64, in CardStatementInput) (int64, error)
	UpdateStatement(ctx context.Context, accountID, statementID int64, in CardStatementInput) error
	Reconcile(ctx context.Context, accountID, statementID int64) (CardReconciliationView, error)
	// AssertMembership says the bank billed a movement in a statement; a nil
	// statement withdraws the assertion.
	AssertMembership(ctx context.Context, accountID, transactionID int64, statementID *int64) error
}

// RecordCardPaymentInput pays a card from an asset account.
type RecordCardPaymentInput struct {
	RequestKey      string
	CardAccountID   int64
	SourceAccountID int64
	// Amount is what the card receives, in its currency. SourceAmount is
	// what left the source, read only when the currencies differ.
	Amount        string
	SourceAmount  string
	Description   *string
	EffectiveDate string
	// Savings is what the payment does to the reservations of a USD source.
	Savings *SavingsExpensePlan
}

// CardStatementInput is a statement as typed from the bank's document:
// balances owed positive (a credit balance negative), the rest magnitudes, an
// empty optional field meaning "the statement does not say".
type CardStatementInput struct {
	PeriodStart    string
	ClosingDate    string
	DueDate        string
	ClosingOwed    string
	MinimumPayment string
	PreviousOwed   string
	Payments       string
	Debits         string
	PastDue        string
}

// CardStandingView is a card with its obligations as of today.
type CardStandingView struct {
	Card CreditCardView

	// HasCurrent is false until a cycle has closed: then nothing is billed and
	// every payment is prepaid or a credit balance.
	HasCurrent bool
	Current    CardCycleView
	Open       CardCycleView

	RefundPolicy string

	BilledPendingMinor          int64
	BilledPendingFormatted      string
	BilledNetOfRefundsFormatted string
	// HasRefundsAfterClosing says refunds were made after the closing: the
	// billed balance is shown net of them as well.
	HasRefundsAfterClosing       bool
	RefundsAfterClosingFormatted string

	// MinimumKnown is false when the statement in force states no minimum.
	MinimumKnown            bool
	MinimumPendingMinor     int64
	MinimumPendingFormatted string

	// PastDue is the largest unmet minimum among cycles already due: the
	// amount that brings every one of them up to date at once.
	PastDueMinor     int64
	PastDueFormatted string
	PastDueCycles    []time.Time

	PostClosingDebitsFormatted string
	PrepaidMinor               int64
	PrepaidFormatted           string

	// Cycles are newest first, from the one in progress back.
	Cycles []CardCycleView
	// Payments are newest first, each with what it settled.
	Payments []CardPaymentAllocationView
	Warnings []CardWarningView
}

// CardCycleView is one billing cycle.
type CardCycleView struct {
	// StatementID is 0 for a cycle computed from the calendar and the ledger.
	StatementID int64
	Registered  bool

	PeriodStart time.Time
	ClosingDate time.Time
	// DueDate is nil when unknown; DueEstimated when derived from the terms.
	DueDate      *time.Time
	DueEstimated bool

	// OwedFormatted is the bank's figure when registered, the ledger's
	// otherwise; LedgerOwedFormatted is always the ledger's.
	OwedFormatted       string
	LedgerOwedFormatted string
	BilledFormatted     string
	// MinimumFormatted is "" when unknown.
	MinimumFormatted   string
	PaidAfterFormatted string

	DebitsFormatted   string
	PaymentsFormatted string
	CreditsFormatted  string
	MovementCount     int

	Status      string
	StatusLabel string

	// For a cycle past its due date: what was missing then. "" when it does
	// not apply or is unknown.
	MissedMinimumFormatted string
	UnpaidAtDueFormatted   string

	// Reconciled is true when a registered statement and the ledger agree;
	// DifferenceFormatted is bank minus ledger.
	Reconciled          bool
	DifferenceFormatted string
}

// Closed reports a cycle whose closing date has passed.
func (c CardCycleView) Closed() bool { return c.Status != CardCycleOpen }

// What a cycle's status can be, derived and never stored.
const (
	CardCycleOpen          = "OPEN"
	CardCyclePaid          = "PAID"
	CardCycleOverdue       = "OVERDUE"
	CardCycleMinimumMet    = "MINIMUM_MET"
	CardCycleDueUnknown    = "DUE_UNKNOWN"
	CardCyclePartiallyPaid = "PARTIALLY_PAID"
	CardCycleBilled        = "BILLED"
)

// CardCycleStatusLabel names a cycle status for a screen.
func CardCycleStatusLabel(status string) string {
	switch status {
	case CardCycleOpen:
		return "En curso"
	case CardCyclePaid:
		return "Pagado"
	case CardCycleOverdue:
		return "Vencido"
	case CardCycleMinimumMet:
		return "Mínimo cubierto"
	case CardCycleDueUnknown:
		return "Vencimiento desconocido"
	case CardCyclePartiallyPaid:
		return "Pago parcial"
	case CardCycleBilled:
		return "Facturado"
	default:
		return status
	}
}

// CardPaymentAllocationView is what one payment settled.
type CardPaymentAllocationView struct {
	TransactionID   int64
	Date            time.Time
	AmountFormatted string
	// AgainstClosing is the closing of the statement in force when it was
	// made; nil when none had closed.
	AgainstClosing *time.Time
	Lines          []CardAllocationLineView
	// Clamped says the bank's figures had to be bounded to place it.
	Clamped bool

	// The trace: the line the payment was placed on.
	PaidBeforeFormatted string
	OverdueFormatted    string
	MinimumFormatted    string
	BilledFormatted     string
	UnbilledFormatted   string
}

// CardAllocationLineView is one slice of a payment.
type CardAllocationLineView struct {
	Target          string
	Label           string
	AmountFormatted string
	// Cycles are the overdue statements an OVERDUE slice brought up to date.
	Cycles []time.Time
}

// CardAllocationLabel names what a slice of a payment settled.
func CardAllocationLabel(target string) string {
	switch target {
	case "OVERDUE":
		return "Vencido"
	case "MINIMUM":
		return "Mínimo"
	case "BILLED":
		return "Resto facturado"
	case "PREPAID":
		return "Anticipado"
	case "CREDIT_BALANCE":
		return "Saldo a favor"
	default:
		return target
	}
}

// CardWarningView is something the engine worked around, in words.
type CardWarningView struct {
	Kind          string
	TransactionID int64
	Text          string
}

// CardReconciliationView compares one statement with the ledger. It informs;
// nothing on it corrects anything by itself.
type CardReconciliationView struct {
	Card      CreditCardView
	Cycle     CardCycleView
	Statement CardStatementInput

	BankOwedFormatted   string
	LedgerOwedFormatted string
	DifferenceFormatted string
	Reconciled          bool

	// The breakdown, present when the bank reported the figure.
	Previous    *CardReconComponent
	Payments    *CardReconComponent
	Debits      *CardReconComponent
	PastDue     *CardReconComponent
	Unexplained string

	Members    []CardPlacedView
	Candidates []CardPlacedView
	Warnings   []CardWarningView
}

// CardReconComponent is one line of the breakdown.
type CardReconComponent struct {
	BankFormatted       string
	LedgerFormatted     string
	DifferenceFormatted string
	Matches             bool
}

// CardPlacedView is a movement with the statement it belongs to.
type CardPlacedView struct {
	TransactionID   int64
	Date            time.Time
	Description     string
	RoleLabel       string
	AmountFormatted string
	Credit          bool
	ClosingDate     time.Time
	Asserted        bool
	// CanAssert says the movement may be asserted into the statement shown:
	// it happened on or before its closing.
	CanAssert bool
}

// Errors M8.2 adds.
var (
	ErrStatementNotFound     = errors.New("statement not found")
	ErrInvalidStatement      = errors.New("invalid statement")
	ErrStatementExists       = errors.New("the card already has a statement closing that day")
	ErrStatementOverlap      = errors.New("the statement's period overlaps another statement of the card")
	ErrAssertionAfterClosing = errors.New("a movement cannot belong to a statement that closed before it happened")
	ErrNotACardMovement      = errors.New("the transaction does not move this card")
)
