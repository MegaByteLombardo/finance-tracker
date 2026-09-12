package web

import (
	"strings"
	"time"
)

// NavItem represents the active navigation item
type NavItem string

const (
	NavDashboard      NavItem = "dashboard"
	NavAccounts       NavItem = "accounts"
	NavTransactions   NavItem = "transactions"
	NavCategories     NavItem = "categories"
	NavPaymentMethods NavItem = "payment-methods"
	NavSettings       NavItem = "settings"
)

// FlashAlert represents an alert message displayed to the user
type FlashAlert struct {
	Type    string // info, warning, danger, success
	Message string
}

// PageData is the common structure passed into layout templates
type PageData struct {
	Title     string
	ActiveNav NavItem
	Alert     *FlashAlert
	Data      any
}

// DashboardView holds all data needed for the Dashboard page
type DashboardView struct {
	CurrencySections   []CurrencySection
	CurrencyTotals     []CurrencyTotal
	RecentTransactions []TransactionView
	HasAccounts        bool
	HasCategories      bool
	HasTransactions    bool
}

type CurrencySection struct {
	CurrencyCode   string
	CurrencyName   string
	CurrencySymbol *string
	TotalFormatted string
	Accounts       []AccountBalance
}

// AccountForm holds state and validation for the new account form
type AccountForm struct {
	Name               string
	Type               AccountType
	Subtype            string // "CHECKING", "SAVINGS", or ""
	Institution        string
	CurrencyCode       string
	InitialBalance     string
	InitialBalanceDate string

	// MinorUnit of the selected currency; see TransactionForm for why this is
	// not assumed to be 2.
	MinorUnit int

	Currencies []Currency
	Errors     map[string]string
}

func (f AccountForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f AccountForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// TransactionForm holds state and validation for the new transaction form
type TransactionForm struct {
	Type            TransactionType // INCOME or EXPENSE
	AccountID       int64
	CategoryID      int64
	Amount          string
	PaymentMethodID int64
	Description     string
	EffectiveDate   string

	SelectedCurrencyCode   string
	SelectedCurrencySymbol *string

	// SelectedCurrencyMinorUnit drives how many decimals the amount field
	// accepts and displays. It is read from the currency rather than assumed to
	// be 2: JPY has 0 and KWD has 3, and an interface that hard-codes two would
	// quietly mangle either.
	SelectedCurrencyMinorUnit int

	Accounts   []Account
	Categories []Category
	Methods    []PaymentMethod
	Errors     map[string]string
}

func (f TransactionForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f TransactionForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// CategoryForm holds state and validation for category creation
type CategoryForm struct {
	Name   string
	Type   CategoryType
	Errors map[string]string
}

func (f CategoryForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f CategoryForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// CategoryGroupView groups categories by type
type CategoryGroupView struct {
	IncomeCategories  []Category
	ExpenseCategories []Category
}

// Helper methods

func TodayDate() string {
	return time.Now().UTC().Format("2006-01-02")
}

func OptionalString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// EditAccountForm holds state and validation for editing an existing account
type EditAccountForm struct {
	ID           int64
	Name         string
	Type         AccountType
	Subtype      string // "CHECKING", "SAVINGS", or ""
	Institution  string
	CurrencyCode string
	Errors       map[string]string
}

func (f EditAccountForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f EditAccountForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// EditCategoryForm holds state and validation for editing a category
type EditCategoryForm struct {
	ID     int64
	Name   string
	Type   CategoryType
	Errors map[string]string
}

func (f EditCategoryForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f EditCategoryForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// PaymentMethodsView holds data for the payment methods index page
type PaymentMethodsView struct {
	Methods []PaymentMethod
}

// PaymentMethodForm holds state and validation for creating or editing a payment method
type PaymentMethodForm struct {
	ID     int64
	Name   string
	Errors map[string]string
}

func (f PaymentMethodForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f PaymentMethodForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// EditTransactionForm holds state and validation for editing a regular transaction
type EditTransactionForm struct {
	ID                  int64
	AccountID           int64
	Type                TransactionType // INCOME or EXPENSE
	CategoryID          int64
	PaymentMethodID     int64
	Amount              string
	Description         string
	EffectiveDate       string
	AllowCurrencyChange bool

	SelectedCurrencyCode      string
	SelectedCurrencySymbol    *string
	SelectedCurrencyMinorUnit int

	Accounts   []Account
	Categories []Category
	Methods    []PaymentMethod
	Errors     map[string]string

	// Currency change confirmation
	NeedsCurrencyConfirmation bool
	OriginalCurrencyCode      string
	TargetCurrencyCode        string
}

func (f EditTransactionForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f EditTransactionForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// EditOpeningBalanceForm holds state and validation for editing an opening balance transaction
type EditOpeningBalanceForm struct {
	ID                int64
	AccountID         int64
	AccountName       string
	CurrencyCode      string
	CurrencySymbol    *string
	CurrencyMinorUnit int
	Amount            string
	EffectiveDate     string
	Errors            map[string]string
}

func (f EditOpeningBalanceForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f EditOpeningBalanceForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// BackupsView holds data for the settings/data snapshots page
type BackupsView struct {
	Backups []Backup
}

// TransferForm holds state and validation for the new internal transfer form
type TransferForm struct {
	SourceAccountID      int64
	DestinationAccountID int64
	Amount               string
	Description          string
	EffectiveDate        string

	SelectedCurrencyCode      string
	SelectedCurrencySymbol    *string
	SelectedCurrencyMinorUnit int

	Accounts []Account
	Errors   map[string]string
}

func (f TransferForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f TransferForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// EditTransferForm holds state and validation for editing an internal transfer
type EditTransferForm struct {
	ID                   int64
	SourceAccountID      int64
	DestinationAccountID int64
	Amount               string
	Description          string
	EffectiveDate        string
	AllowCurrencyChange  bool

	SelectedCurrencyCode      string
	SelectedCurrencySymbol    *string
	SelectedCurrencyMinorUnit int

	Accounts []Account
	Errors   map[string]string

	// Currency change confirmation
	NeedsCurrencyConfirmation bool
	OriginalCurrencyCode      string
	TargetCurrencyCode        string
}

func (f EditTransferForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f EditTransferForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}
