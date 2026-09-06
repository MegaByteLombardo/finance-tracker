package web

import (
	"errors"
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
)

func TransactionTypes() []TransactionType {
	return []TransactionType{TransactionTypeIncome, TransactionTypeExpense, TransactionTypeOpeningBalance}
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
)
