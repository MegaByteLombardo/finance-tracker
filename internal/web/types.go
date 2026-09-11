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
)

// TransactionTypes lists every type a transaction can have, which is not the
// same as the list any one form may offer.
func TransactionTypes() []TransactionType {
	return []TransactionType{
		TransactionTypeIncome,
		TransactionTypeExpense,
		TransactionTypeOpeningBalance,
		TransactionTypeTransfer,
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

	BalanceMinor     int64
	BalanceFormatted string
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

	// ErrSameAccount means a transfer named one account as both its source and
	// its destination.
	ErrSameAccount = errors.New("a transfer needs two different accounts")
	// ErrCurrencyMismatch means a transfer's two accounts are in different
	// currencies, which M4 cannot represent because it has no exchange rates.
	ErrCurrencyMismatch = errors.New("a transfer needs both accounts in the same currency")
	// ErrInconsistentTransfer means a stored transfer is not two balanced
	// movements in two different accounts. It is an integrity failure, not a
	// validation one: no input can cause it, so it warrants a 500 rather than a
	// message on a form.
	ErrInconsistentTransfer = errors.New("stored transfer is inconsistent")
)

// TransferSide names which end of a transfer an error refers to, so that a form
// can mark the right field.
type TransferSide string

const (
	TransferSideSource      TransferSide = "source"
	TransferSideDestination TransferSide = "destination"
)

// TransferAccountError says which of a transfer's two accounts was rejected.
//
// errors.Is still matches ErrAccountNotFound or ErrAccountInactive; errors.As
// recovers the side.
type TransferAccountError struct {
	Side      TransferSide
	AccountID int64
	Err       error
}

func (e *TransferAccountError) Error() string {
	return fmt.Sprintf("%s account %d: %s", e.Side, e.AccountID, e.Err)
}

func (e *TransferAccountError) Unwrap() error { return e.Err }
