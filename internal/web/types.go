package web

import (
	"errors"
	"fmt"
	"time"
)

// Domain enumerations and models as defined in docs/m2-contract.md.

type AccountType string

const (
	AccountTypeBank   AccountType = "BANK"
	AccountTypeCash   AccountType = "CASH"
	AccountTypeWallet AccountType = "WALLET"
	AccountTypeOther  AccountType = "OTHER"
)

// AccountTypes lists what the generic account form offers: the ASSET types.
// A credit card is a type too (AccountTypeCreditCard), opened through its own
// form with its terms.
func AccountTypes() []AccountType {
	return []AccountType{AccountTypeBank, AccountTypeCash, AccountTypeWallet, AccountTypeOther}
}

type AccountSubtype string

const (
	AccountSubtypeChecking AccountSubtype = "CHECKING"
	AccountSubtypeSavings  AccountSubtype = "SAVINGS"
)

func AccountSubtypes() []AccountSubtype {
	return []AccountSubtype{AccountSubtypeChecking, AccountSubtypeSavings}
}

func AccountSubtypesFor(t AccountType) []AccountSubtype {
	if t == AccountTypeBank {
		return []AccountSubtype{AccountSubtypeChecking, AccountSubtypeSavings}
	}
	return nil
}

type CategoryType string

const (
	CategoryTypeIncome  CategoryType = "INCOME"
	CategoryTypeExpense CategoryType = "EXPENSE"
)

func CategoryTypes() []CategoryType {
	return []CategoryType{CategoryTypeIncome, CategoryTypeExpense}
}

type TransactionType string

const (
	TransactionTypeIncome         TransactionType = "INCOME"
	TransactionTypeExpense        TransactionType = "EXPENSE"
	TransactionTypeOpeningBalance TransactionType = "OPENING_BALANCE"
	// TransactionTypeTransfer is money moving between two of the user's own
	// accounts. It is neither income nor an expense and has no category and no
	// payment method.
	//
	// It is NOT the payment method named "Bank transfer", which records the
	// channel an operation with the outside world used.
	//
	// Knowing this value does not make it offerable. A transfer has two accounts
	// and two movements, so it is created and edited only through
	// RecordTransfer/UpdateTransfer and the /transfers routes. The
	// income-and-expense form must never present it or accept it; see
	// TransactionTypesForIncomeExpenseForm.
	TransactionTypeTransfer TransactionType = "TRANSFER"
	// TransactionTypeFXExchange is money actually exchanged between two of the
	// user's own accounts in DIFFERENT currencies: bolívares left one and
	// dollars arrived in another.
	//
	// It is not a transfer. A transfer's two movements cancel out; an exchange's
	// do not, and their magnitudes are quantities of two different currencies
	// with no arithmetic relation between them. It has no category and no
	// payment method.
	//
	// There is no BUY and no SELL. "Compra de USD" and "Venta de USD" are labels
	// a screen infers from which account was the destination; the ledger stores
	// one type.
	//
	// Knowing this value does not make it offerable. An exchange has two
	// accounts, two amounts and two currencies, so it is created and edited only
	// through RecordExchange/UpdateExchange and the /exchanges routes.
	TransactionTypeFXExchange TransactionType = "FX_EXCHANGE"
)

// TransactionTypes lists every type a transaction can have, which is not the
// same as the list any one form may offer.
func TransactionTypes() []TransactionType {
	return []TransactionType{
		TransactionTypeIncome,
		TransactionTypeExpense,
		TransactionTypeOpeningBalance,
		TransactionTypeTransfer,
		TransactionTypeFXExchange,
		TransactionTypeRefund,
	}
}

// TransactionTypesForIncomeExpenseForm is what the generic transaction form may
// offer: income and an expense, and nothing else.
//
// An opening balance belongs to the account-opening workflow, and a transfer to
// the transfer flow. Each needs inputs this form does not have — an opening
// balance may be negative and has no category; a transfer has a second account —
// so offering either here would produce a form that cannot express what it
// claims to.
func TransactionTypesForIncomeExpenseForm() []TransactionType {
	return []TransactionType{TransactionTypeIncome, TransactionTypeExpense}
}

// PaymentMethod is a channel money moved through, as administered by the user.
//
// M3 replaced M2's fixed enumeration with rows in a table: real use produced
// "Pago móvil", "Zelle" and "Débito", and adding one should not need a
// deployment. The id is the identifier; the name is display text the user can
// change, so it must never be cached or compared as a key.
type PaymentMethod struct {
	ID        int64
	Name      string
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Models

type Currency struct {
	Code      string
	Name      string
	Symbol    *string
	MinorUnit int
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Account struct {
	ID           int64
	Name         string
	Type         AccountType
	Subtype      *AccountSubtype
	CurrencyCode string
	Institution  *string
	IsActive     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Category struct {
	ID        int64
	Name      string
	Type      CategoryType
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TransferDetail is a whole transfer: where the money left, where it arrived,
// and how much moved.
//
// For a TRANSFER this is the ONLY thing a template should read. The
// movement-centric fields of TransactionView describe one of its two halves, so
// AmountMinor is negative there and AccountID names only the source; rendering
// either would show half of a transfer without saying so.
type TransferDetail struct {
	SourceMovementID  int64
	SourceAccountID   int64
	SourceAccountName string

	DestinationMovementID  int64
	DestinationAccountID   int64
	DestinationAccountName string

	// AmountMinor is the amount that moved, always positive.
	AmountMinor int64
	// AmountFormatted is it rendered at the currency's scale, unsigned. Display
	// this rather than computing it.
	AmountFormatted string
}

// ExecutionRate is the rate an FX_EXCHANGE actually happened at, derived by the
// backend from its two amounts.
//
// It is NOT a reference rate. Nothing here was published by anybody, nothing
// here values a balance, and nothing here comes from or goes to the exchange
// rates screen. See docs/m5.5-contract.md.
//
// Orientation is the project's, always: 1 BaseCurrencyCode = RateFormatted
// QuoteCurrencyCode. Which side is the base was decided by the backend; a
// template must never swap the two codes to show the other direction, because
// the inverse is a different number, not the same string relabelled.
type ExecutionRate struct {
	BaseCurrencyCode  string
	QuoteCurrencyCode string

	// RateFormatted is the canonical decimal string. Display it; never parse it
	// into a JavaScript number and never reformat it.
	RateFormatted string

	// Exact is false when the rate has no finite decimal expansion, so
	// RateFormatted is a rounded approximation. A screen showing one must say
	// so.
	Exact bool
}

// ExchangeDetail is a whole FX_EXCHANGE: what left, what arrived, in which two
// currencies, and at what rate.
//
// For an FX_EXCHANGE this is the ONLY thing a template should read. The
// movement-centric fields of TransactionView describe the source side alone, so
// its CurrencyCode, MinorUnit and AmountFormatted all name the currency the
// money LEFT — rendering them would show "-85,000.00 VES" with no way to
// discover that 100.00 USD arrived.
type ExchangeDetail struct {
	SourceMovementID     int64
	SourceAccountID      int64
	SourceAccountName    string
	SourceCurrencyCode   string
	SourceCurrencySymbol *string
	SourceMinorUnit      int
	// SourceAmountMinor is the amount delivered, always positive.
	SourceAmountMinor int64
	// SourceAmountFormatted is it at the source currency's scale, unsigned.
	SourceAmountFormatted string

	DestinationMovementID     int64
	DestinationAccountID      int64
	DestinationAccountName    string
	DestinationCurrencyCode   string
	DestinationCurrencySymbol *string
	DestinationMinorUnit      int
	// DestinationAmountMinor is the amount received, always positive.
	DestinationAmountMinor int64
	// DestinationAmountFormatted is it at the destination currency's scale,
	// unsigned.
	DestinationAmountFormatted string

	ExecutionRate ExecutionRate
}

// IsPurchaseOf reports whether this exchange bought the given currency, which is
// true when that currency is the destination.
//
// It is a presentation convenience and nothing more: the ledger has one type,
// and "Compra de USD" is a label, not a fact stored anywhere.
func (d ExchangeDetail) IsPurchaseOf(code string) bool {
	return d.DestinationCurrencyCode == code
}

// IsSaleOf reports whether this exchange sold the given currency, which is true
// when that currency is the source. Same caveat as IsPurchaseOf.
func (d ExchangeDetail) IsSaleOf(code string) bool {
	return d.SourceCurrencyCode == code
}

type TransactionView struct {
	ID            int64
	Type          TransactionType
	EffectiveDate time.Time
	Description   *string

	// Absent for an opening balance. The name is for display only.
	PaymentMethodID   *int64
	PaymentMethodName *string

	CategoryID   *int64
	CategoryName *string
	CategoryType *CategoryType

	// Transfer is set if and only if Type is TransactionTypeTransfer, and is
	// the complete, self-sufficient representation of one. Read it instead of
	// the movement-centric fields below whenever the type is TRANSFER.
	Transfer *TransferDetail

	// Exchange is set if and only if Type is TransactionTypeFXExchange, and is
	// the complete, self-sufficient representation of one — both currencies
	// included. Read it instead of the movement-centric fields below whenever
	// the type is FX_EXCHANGE.
	Exchange *ExchangeDetail

	// RefundOf is the expense a REFUND gives money back for, when linked.
	RefundOf *int64

	MovementID     int64
	AccountID      int64
	AccountName    string
	AccountType    AccountType
	AccountSubtype *AccountSubtype
	Institution    *string

	CurrencyCode   string
	CurrencySymbol *string
	MinorUnit      int

	AmountMinor     int64
	AmountFormatted string

	CreatedAt time.Time
}

type AccountBalance struct {
	AccountID      int64
	AccountName    string
	AccountType    AccountType
	AccountSubtype *AccountSubtype
	Institution    *string
	IsActive       bool

	CurrencyCode   string
	CurrencySymbol *string
	MinorUnit      int

	// BalanceMinor is the CURRENT balance: the movements dated on or before
	// today (TD-063).
	BalanceMinor     int64
	BalanceFormatted string

	// FutureMovementCount is how many movements are dated after today. They
	// are listed, and not yet in the balance.
	FutureMovementCount int
}

type CurrencyTotal struct {
	CurrencyCode   string
	CurrencyName   string
	CurrencySymbol *string
	MinorUnit      int

	AccountCount   int
	TotalMinor     int64
	TotalFormatted string
}

// Service inputs

type CreateAccountInput struct {
	Name         string
	Type         AccountType
	Subtype      *AccountSubtype
	CurrencyCode string
	Institution  *string
}

type UpdateAccountMetadataInput struct {
	Name        string
	Type        AccountType
	Subtype     *AccountSubtype
	Institution *string
}

type CreateCategoryInput struct {
	Name string
	Type CategoryType
}

type UpdateCategoryMetadataInput struct {
	Name string
}

type ListCategoriesInput struct {
	Type            *CategoryType
	IncludeInactive bool
}

type RecordIncomeInput struct {
	AccountID       int64
	CategoryID      int64
	Amount          string
	PaymentMethodID int64
	Description     *string
	EffectiveDate   string
}

type RecordExpenseInput struct {
	AccountID       int64
	CategoryID      int64
	Amount          string
	PaymentMethodID int64
	Description     *string
	EffectiveDate   string

	// Savings, when set, is what the expense does to savings, committed in
	// the same transaction (M7.4). The expense is never refused because of
	// savings; without a plan, an uncovered expense is reported as an issue.
	Savings *SavingsExpensePlan
}

type CreatePaymentMethodInput struct {
	Name string
}

type UpdatePaymentMethodMetadataInput struct {
	Name string
}

type ListPaymentMethodsInput struct {
	IncludeInactive bool
}

type RecordOpeningBalanceInput struct {
	AccountID     int64
	Amount        string
	EffectiveDate string
}

type ListTransactionsInput struct {
	Limit int
}

type UpdateTransactionInput struct {
	AccountID           int64
	Type                TransactionType
	CategoryID          int64
	PaymentMethodID     int64
	Amount              string
	Description         *string
	EffectiveDate       string
	AllowCurrencyChange bool
}

type UpdateOpeningBalanceInput struct {
	Amount        string
	EffectiveDate string
}

// RecordTransferInput moves money between two of the user's own accounts.
//
// Amount is a positive decimal string in the canonical format. The direction is
// which account was named as the source, never the sign.
type RecordTransferInput struct {
	SourceAccountID      int64
	DestinationAccountID int64
	Amount               string
	Description          *string
	EffectiveDate        string

	// Savings, when set, moves the backing of reserved money to the
	// destination in the same transaction (M7.4).
	Savings *SavingsTransferPlan
}

// UpdateTransferInput replaces every editable field of a transfer.
//
// There is no Type: a transfer cannot become anything else, and nothing else can
// become a transfer.
type UpdateTransferInput struct {
	SourceAccountID      int64
	DestinationAccountID int64
	Amount               string
	Description          *string
	EffectiveDate        string

	// AllowCurrencyChange must be true to move a transfer to a pair of accounts
	// in a different currency, because the amount is then read at a different
	// scale rather than converted. It does not permit a cross-currency
	// transfer: the two accounts must still share a currency.
	AllowCurrencyChange bool
}

// RecordExchangeInput exchanges money between two of the user's own accounts in
// two DIFFERENT currencies.
//
// Both amounts are positive decimal strings, each in its own account's currency.
// The direction is which account was named as the source, never a sign, and
// whether this was a purchase or a sale of dollars follows from that.
//
// There is no rate field and there must never be one. The backend derives the
// execution rate from the two amounts; supplying one instead would make the
// application compute an amount the bank already decided.
type RecordExchangeInput struct {
	SourceAccountID      int64
	SourceAmount         string
	DestinationAccountID int64
	DestinationAmount    string
	Description          *string
	EffectiveDate        string
}

// UpdateExchangeInput replaces every editable field of an exchange.
//
// There is no Type: an exchange cannot become anything else, and nothing else
// can become an exchange. There is no AllowCurrencyChange either — both
// currencies and both amounts are named explicitly, so changing an account
// reinterprets nothing.
type UpdateExchangeInput struct {
	SourceAccountID      int64
	SourceAmount         string
	DestinationAccountID int64
	DestinationAmount    string
	Description          *string
	EffectiveDate        string
}

type Backup struct {
	Name      string
	Reason    string
	CreatedAt time.Time
	SizeBytes int64
}

type OpenAccountInput struct {
	Account            CreateAccountInput
	OpeningBalance     *string
	OpeningBalanceDate string
}

type OpenAccountResult struct {
	Account        Account
	OpeningBalance *TransactionView
}

// Errors as specified in docs/m2-contract.md and docs/m3-contract.md
var (
	ErrNotFound             = errors.New("not found")
	ErrInvalidInput         = errors.New("invalid input")
	ErrConflict             = errors.New("conflict: record already exists")
	ErrCurrencyNotFound     = errors.New("currency not found")
	ErrCurrencyInactive     = errors.New("currency is inactive")
	ErrInvalidAmount        = errors.New("invalid amount")
	ErrAccountNotFound      = errors.New("account not found")
	ErrAccountInactive      = errors.New("account is inactive")
	ErrCategoryNotFound     = errors.New("category not found")
	ErrCategoryInactive     = errors.New("category is inactive")
	ErrCategoryTypeMismatch = errors.New("category type mismatch")

	ErrPaymentMethodNotFound = errors.New("payment method not found")
	ErrPaymentMethodInactive = errors.New("payment method is inactive")

	ErrTypeChangeNotAllowed       = errors.New("type change not allowed")
	ErrCurrencyChangeNotConfirmed = errors.New("currency change not confirmed")

	// ErrSameAccount means a transfer or an exchange named one account as both
	// its source and its destination.
	ErrSameAccount = errors.New("the source and the destination must be two different accounts")
	// ErrCurrencyMismatch means a TRANSFER's two accounts are in different
	// currencies. A transfer moves one sum of money; two currencies mean two
	// amounts, which is an FX_EXCHANGE and belongs on the exchange form.
	ErrCurrencyMismatch = errors.New("a transfer needs both accounts in the same currency")
	// ErrSameCurrency means an FX_EXCHANGE's two accounts are in the SAME
	// currency, which is a transfer rather than an exchange. It is
	// ErrCurrencyMismatch's mirror image.
	ErrSameCurrency = errors.New("an exchange needs the two accounts in different currencies")
	// ErrInconsistentTransfer means a stored transfer is not two balanced
	// movements in two different accounts. It is an integrity failure, not a
	// validation one: no input can cause it, so it warrants a 500 rather than a
	// message on a form.
	ErrInconsistentTransfer = errors.New("stored transfer is inconsistent")
	// ErrInconsistentExchange means a stored exchange is not two opposite
	// movements in two different accounts of two different currencies. Like
	// ErrInconsistentTransfer it is an integrity failure and warrants a 500.
	ErrInconsistentExchange = errors.New("stored exchange is inconsistent")
)

// MovementSide names which end of a two-sided transaction — a transfer or an
// exchange — an error refers to, so that a form can mark the right field.
type MovementSide string

const (
	MovementSideSource      MovementSide = "source"
	MovementSideDestination MovementSide = "destination"
)

// TransferSide is the name docs/m4-contract.md froze for MovementSide. It is the
// SAME type, so nothing has to convert between the two spellings.
type TransferSide = MovementSide

const (
	TransferSideSource      = MovementSideSource
	TransferSideDestination = MovementSideDestination
)

// AccountSideError says which of a two-sided transaction's accounts was
// rejected.
//
// errors.Is still matches ErrAccountNotFound or ErrAccountInactive; errors.As
// recovers the side.
type AccountSideError struct {
	Side      MovementSide
	AccountID int64
	Err       error
}

func (e *AccountSideError) Error() string {
	return fmt.Sprintf("%s account %d: %s", e.Side, e.AccountID, e.Err)
}

func (e *AccountSideError) Unwrap() error { return e.Err }

// TransferAccountError is the name docs/m4-contract.md froze for
// AccountSideError, kept as an alias so errors.As with either spelling matches
// the same value.
type TransferAccountError = AccountSideError
