package web

import (
	"strings"
	"time"
)

// NavItem represents the active navigation item
type NavItem string

const (
	NavDashboard    NavItem = "dashboard"
	NavAccounts     NavItem = "accounts"
	NavTransactions NavItem = "transactions"
	NavCategories   NavItem = "categories"
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
	Type          TransactionType // INCOME or EXPENSE
	AccountID     int64
	CategoryID    int64
	Amount        string
	PaymentMethod PaymentMethod
	Description   string
	EffectiveDate string

	SelectedCurrencyCode   string
	SelectedCurrencySymbol *string

	Accounts   []Account
	Categories []Category
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
