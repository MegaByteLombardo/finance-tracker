package web

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// NavItem represents the active navigation item
type NavItem string

const (
	NavDashboard      NavItem = "dashboard"
	NavRecap          NavItem = "recap"
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

// DashboardView holds all data needed for the Dashboard page.
//
// Every financial figure on it comes from Analytics, which internal/analytics
// and internal/valuation computed and formatted. The handler adds nothing up and
// the template formats nothing that is not already a string (docs/m6-contract.md).
type DashboardView struct {
	// Analytics is the M6 dashboard: today's net worth and its breakdown, its
	// history over the period, the change, the cash flow and the spending.
	Analytics DashboardAnalytics

	// NetWorth is Analytics.NetWorth, kept as its own field because the M5
	// panel renders it.
	NetWorth NetWorth

	// NetWorthError is set when the analytics could not be computed. The rest
	// of the page still renders: recent transactions need no exchange rate.
	NetWorthError string

	// History is the net-worth chart card. It has its own period (?chart=),
	// so moving the chart does not change the figures around it.
	History NetWorthHistoryView

	// Periods is the selector, with the active preset marked.
	Periods []PeriodOption

	RecentTransactions []TransactionView
	HasAccounts        bool
	HasCategories      bool
	HasTransactions    bool
}

// PeriodOption is one button of the period selector.
type PeriodOption struct {
	Preset DashboardPreset
	Label  string
	URL    string
	Active bool
}

// periodOptions builds the page's period selector. chart is the ?chart= the
// page was opened with, kept on every link so changing the period does not
// reset a chart the user moved; empty when it was not given.
func periodOptions(path string, active DashboardPreset, chart string) []PeriodOption {
	options := make([]PeriodOption, 0, len(DashboardPresets()))
	for _, preset := range DashboardPresets() {
		url := path + "?period=" + string(preset)
		if chart != "" {
			url += "&chart=" + chart
		}
		options = append(options, PeriodOption{
			Preset: preset,
			Label:  preset.Label(),
			URL:    url,
			Active: preset == active,
		})
	}
	return options
}

// chartOptions builds the chart card's own selector. Each link keeps the
// page's period and changes only ?chart=.
func chartOptions(path string, period, active DashboardPreset) []PeriodOption {
	options := make([]PeriodOption, 0, len(DashboardPresets()))
	for _, preset := range DashboardPresets() {
		options = append(options, PeriodOption{
			Preset: preset,
			Label:  preset.Label(),
			URL:    path + "?period=" + string(period) + "&chart=" + string(preset),
			Active: preset == active,
		})
	}
	return options
}

// NetWorthHistoryView is the net-worth chart card: the chart over its own
// period, the change over that period, and what rates it is missing.
type NetWorthHistoryView struct {
	// Available is false when the chart's period could not be computed; the
	// card then shows ErrorMessage instead of a chart.
	Available    bool
	ErrorMessage string

	Preset       DashboardPreset
	From         time.Time
	To           time.Time
	CurrencyCode string

	Chart ChartView

	// Change is the net worth at the end of the chart's period against the
	// close of the day before it (TD-069).
	Change Change

	IncompleteDays int
	Gaps           []RateGap

	// OpeningIncomplete is the day before the period missing a rate, which
	// is what makes Change unavailable even when the chart has no hole.
	OpeningIncomplete bool
	OpeningDate       time.Time

	Periods []PeriodOption
}

// newHistoryView lays out the chart card from a computed dashboard.
func newHistoryView(dash DashboardAnalytics, periods []PeriodOption) NetWorthHistoryView {
	base := dash.NetWorth.BaseCurrencyCode
	return NetWorthHistoryView{
		Available:         true,
		Preset:            dash.Preset,
		From:              dash.PeriodFrom,
		To:                dash.PeriodTo,
		CurrencyCode:      base,
		Chart:             BuildNetWorthChart(dash.Points, base, dash.NetWorth.BaseMinorUnit),
		Change:            dash.Change,
		IncompleteDays:    dash.IncompleteDays,
		Gaps:              dash.Gaps,
		OpeningIncomplete: !dash.Opening.Complete,
		OpeningDate:       dash.Opening.Date,
		Periods:           periods,
	}
}

// loadHistory computes the chart card for chart. When chart is the page's own
// period, the dashboard already computed is reused; otherwise the chart's
// period is computed on its own, and a failure there costs only the card.
func loadHistory(r *http.Request, logger *slog.Logger, svcs *Services, path string,
	dash DashboardAnalytics, period, chart DashboardPreset) NetWorthHistoryView {
	periods := chartOptions(path, period, chart)
	if chart == period {
		return newHistoryView(dash, periods)
	}
	own, err := svcs.Analytics.Dashboard(r.Context(), chart)
	if err != nil {
		logger.Error("computing the net worth chart", slog.String("chart", string(chart)), slog.Any("error", err))
		return NetWorthHistoryView{
			Preset:       chart,
			ErrorMessage: "No se pudo calcular la evolución para este período.",
			Periods:      periods,
		}
	}
	return newHistoryView(own, periods)
}

// parseChartPreset reads ?chart=. Absent or unrecognised, the chart follows
// the page's period. It also returns the raw value to keep on the page's
// other links, empty when there is none worth keeping.
func parseChartPreset(raw string, period DashboardPreset) (DashboardPreset, string) {
	raw = strings.TrimSpace(raw)
	for _, preset := range DashboardPresets() {
		if strings.EqualFold(raw, string(preset)) {
			return preset, string(preset)
		}
	}
	return period, ""
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

// TodayDate is the default effective date a form offers: today on the
// Venezuelan calendar (TD-051).
//
// It was the UTC date until M6, which for four hours every evening offered
// tomorrow. Once current balances began excluding future-dated movements
// (TD-063), that default would have recorded an evening purchase as not yet
// having happened.
func TodayDate() string {
	return time.Now().In(economicLocation).Format("2006-01-02")
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

	// IsActive and the current balance drive the archive / restore section.
	// The balance is shown because archiving does not take it out of the net
	// worth (TD-064), and the page says so before the user decides.
	IsActive         bool
	BalanceMinor     int64
	BalanceFormatted string
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
