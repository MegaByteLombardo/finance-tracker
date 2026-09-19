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
	// NetWorth is the consolidated figure, DERIVED on every request from the
	// ledger, the stored rates and the base currency. It is never stored.
	//
	// When NetWorthError is set the rest of the page still renders: the
	// per-currency balances below are the ledger's own figures and do not depend
	// on any exchange rate.
	NetWorth      NetWorth
	NetWorthError string

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

// ExchangeForm holds state and validation for the new currency exchange form.
//
// It carries TWO amounts and TWO currencies, which is the shape of the
// operation. There is no shared-currency field and no rate field: the rate is
// derived by the backend from the two amounts, and the form only ever previews
// it.
type ExchangeForm struct {
	SourceAccountID      int64
	SourceAmount         string
	DestinationAccountID int64
	DestinationAmount    string
	Description          string
	EffectiveDate        string

	// The currency of each side, resolved from the chosen accounts so that the
	// form can show the right code and the right number of decimals. Empty
	// until an account is chosen.
	SourceCurrencyCode           string
	SourceCurrencySymbol         *string
	SourceCurrencyMinorUnit      int
	DestinationCurrencyCode      string
	DestinationCurrencySymbol    *string
	DestinationCurrencyMinorUnit int

	Accounts []Account
	Errors   map[string]string
}

func (f ExchangeForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f ExchangeForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}

// EditExchangeForm holds state and validation for editing a currency exchange.
//
// There is no NeedsCurrencyConfirmation and no AllowCurrencyChange, unlike the
// transfer and transaction edit forms. Those exist because those operations
// carry ONE amount, so moving them to another currency re-reads the same digits
// at another scale. An exchange names both currencies and both amounts, so
// changing an account reinterprets nothing — it corrects which currency that
// side was in, and the amount beside it says how much.
type EditExchangeForm struct {
	ID                   int64
	SourceAccountID      int64
	SourceAmount         string
	DestinationAccountID int64
	DestinationAmount    string
	Description          string
	EffectiveDate        string

	SourceCurrencyCode           string
	SourceCurrencySymbol         *string
	SourceCurrencyMinorUnit      int
	DestinationCurrencyCode      string
	DestinationCurrencySymbol    *string
	DestinationCurrencyMinorUnit int

	// ExecutionRate is what the exchange currently reads as, derived by the
	// backend. It is shown for orientation while editing and is never posted
	// back: changing an amount changes it, with nothing stored to keep in step.
	ExecutionRate ExecutionRate

	Accounts []Account
	Errors   map[string]string
}

func (f EditExchangeForm) HasError(field string) bool {
	if f.Errors == nil {
		return false
	}
	_, ok := f.Errors[field]
	return ok
}

func (f EditExchangeForm) Error(field string) string {
	if f.Errors == nil {
		return ""
	}
	return f.Errors[field]
}
