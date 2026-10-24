package web

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	webassets "github.com/MegaByteLombardo/finance-tracker/web"
)

// Config holds web presentation configuration
type Config struct {
	TemplatesFS  fs.FS
	StaticFS     fs.FS
	TemplatesDir string
	StaticDir    string
}

// DefaultConfig returns reasonable defaults using embedded assets.
func DefaultConfig() Config {
	tmplFS, _ := fs.Sub(webassets.FS, "templates")
	staticFS, _ := fs.Sub(webassets.FS, "static")
	return Config{
		TemplatesFS: tmplFS,
		StaticFS:    staticFS,
	}
}

// NewRouter constructs and configures the presentation layer HTTP router.
func NewRouter(logger *slog.Logger, svcs *Services, cfg Config) http.Handler {
	var templateFS fs.FS
	if cfg.TemplatesFS != nil {
		templateFS = cfg.TemplatesFS
	} else if cfg.TemplatesDir != "" {
		templateFS = os.DirFS(cfg.TemplatesDir)
	} else {
		templateFS, _ = fs.Sub(webassets.FS, "templates")
	}

	renderer := NewRenderer(templateFS)
	mux := http.NewServeMux()

	// Static files
	if cfg.StaticFS != nil {
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(cfg.StaticFS))))
	} else if cfg.StaticDir != "" {
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(cfg.StaticDir))))
	} else {
		staticFS, _ := fs.Sub(webassets.FS, "static")
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	}

	// Dashboard & Mobile Views
	mux.HandleFunc("GET /{$}", handleDashboard(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile", handleMobileDashboard(logger, renderer, svcs))
	mux.HandleFunc("GET /recap", handleRecap(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/recap", handleMobileRecap(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/movimientos", handleMobileTransactions(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/transactions", handleMobileTransactions(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/cuentas", handleMobileAccounts(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/accounts", handleMobileAccounts(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/operacion/nueva", handleMobileNewOperation(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/nueva-operacion", handleMobileNewOperation(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/categorias", handleMobileCategories(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/metodos-pago", handleMobilePaymentMethods(logger, renderer, svcs))

	// Savings (M7.1): the policy and the suggestion each eligible income
	// produces. A suggestion is a target, never money. Actions on a suggestion
	// name the INCOME and act on its live revision; the mobile pages post to the
	// same endpoints with a "return" path.
	mux.HandleFunc("GET /savings", handleSavings(logger, renderer, svcs, false))
	mux.HandleFunc("GET /savings/policy", handleSavingsPolicy(logger, renderer, svcs, false))
	mux.HandleFunc("POST /savings/policy", handleSetSavingsPolicy(logger, renderer, svcs))
	mux.HandleFunc("POST /savings/categories/{id}/eligibility", handleSetCategoryEligibility(logger, svcs))
	mux.HandleFunc("GET /savings/sync", handleSavingsSync(logger, renderer, svcs, false))
	mux.HandleFunc("POST /savings/sync", handleApplySavingsSync(logger, svcs))
	mux.HandleFunc("POST /savings/value-pending", handleValuePending(logger, svcs))
	mux.HandleFunc("GET /savings/incomes/{id}", handleSavingsIncome(logger, renderer, svcs, false))
	mux.HandleFunc("POST /savings/incomes/{id}/override", handleSetIncomeOverride(logger, svcs))
	mux.HandleFunc("POST /savings/incomes/{id}/value", handleSuggestionAction(logger, svcs, "value"))
	mux.HandleFunc("POST /savings/incomes/{id}/revalue", handleSuggestionAction(logger, svcs, "revalue"))
	mux.HandleFunc("POST /savings/incomes/{id}/dismiss", handleSuggestionAction(logger, svcs, "dismiss"))
	mux.HandleFunc("GET /savings/fund", handleSavingsFund(logger, renderer, svcs, false))
	mux.HandleFunc("POST /savings/reserve", handleSavingsReserve(logger, svcs))
	mux.HandleFunc("POST /savings/release", handleSavingsRelease(logger, svcs))
	mux.HandleFunc("GET /mobile/ahorro/fondo", handleSavingsFund(logger, renderer, svcs, true))
	mux.HandleFunc("GET /savings/goals", handleSavingsGoals(logger, renderer, svcs, false))
	mux.HandleFunc("POST /savings/goals", handleCreateGoal(logger, renderer, svcs))
	mux.HandleFunc("GET /savings/goals/{id}", handleSavingsGoal(logger, renderer, svcs, false))
	mux.HandleFunc("POST /savings/goals/{id}", handleUpdateGoal(logger, svcs))
	mux.HandleFunc("POST /savings/goals/{id}/pause", handleGoalAction(logger, svcs, "pause"))
	mux.HandleFunc("POST /savings/goals/{id}/resume", handleGoalAction(logger, svcs, "resume"))
	mux.HandleFunc("POST /savings/goals/{id}/complete", handleGoalAction(logger, svcs, "complete"))
	mux.HandleFunc("POST /savings/goals/{id}/cancel", handleGoalAction(logger, svcs, "cancel"))
	mux.HandleFunc("POST /savings/reallocate", handleReallocate(logger, svcs))
	mux.HandleFunc("GET /mobile/ahorro/metas", handleSavingsGoals(logger, renderer, svcs, true))
	mux.HandleFunc("GET /mobile/ahorro/metas/{id}", handleSavingsGoal(logger, renderer, svcs, true))
	mux.HandleFunc("GET /savings/impact", handleSavingsImpact(logger, renderer, svcs))
	mux.HandleFunc("GET /savings/issues", handleSavingsIssues(logger, renderer, svcs, false))
	mux.HandleFunc("POST /savings/issues/acknowledge", handleAcknowledgeIssue(logger, svcs))
	mux.HandleFunc("GET /mobile/ahorro/incidencias", handleSavingsIssues(logger, renderer, svcs, true))
	mux.HandleFunc("GET /mobile/ahorro", handleSavings(logger, renderer, svcs, true))
	mux.HandleFunc("GET /mobile/ahorro/politica", handleSavingsPolicy(logger, renderer, svcs, true))
	mux.HandleFunc("GET /mobile/ahorro/sincronizar", handleSavingsSync(logger, renderer, svcs, true))
	mux.HandleFunc("GET /mobile/ahorro/ingresos/{id}", handleSavingsIncome(logger, renderer, svcs, true))

	// Accounts
	mux.HandleFunc("GET /accounts", handleAccountsList(logger, renderer, svcs))
	mux.HandleFunc("GET /accounts/new", handleNewAccountForm(logger, renderer, svcs))
	mux.HandleFunc("POST /accounts", handleCreateAccount(logger, renderer, svcs))
	mux.HandleFunc("GET /accounts/{id}/edit", handleEditAccountForm(logger, renderer, svcs))
	mux.HandleFunc("POST /accounts/{id}", handleUpdateAccount(logger, renderer, svcs))
	mux.HandleFunc("POST /accounts/{id}/archive", handleSetAccountActive(logger, svcs, false))
	mux.HandleFunc("POST /accounts/{id}/restore", handleSetAccountActive(logger, svcs, true))
	mux.HandleFunc("GET /accounts/subtype-options", handleSubtypeOptions(logger, renderer))

	// Credit cards (M8.1). A card is a liability account with the issuer's
	// terms. A purchase and a charge are expenses on it; a payment is a
	// transfer or an exchange into it, recorded on those forms. Every form
	// carries a request key, so a double submission records once.
	mux.HandleFunc("GET /cards", handleCards(logger, renderer, svcs))
	mux.HandleFunc("GET /cards/new", handleNewCardForm(logger, renderer, svcs))
	mux.HandleFunc("POST /cards", handleOpenCard(logger, renderer, svcs))
	mux.HandleFunc("GET /cards/{id}", handleCard(logger, renderer, svcs))
	mux.HandleFunc("GET /cards/{id}/edit", handleEditCardTerms(logger, renderer, svcs))
	mux.HandleFunc("POST /cards/{id}/edit", handleUpdateCardTerms(logger, renderer, svcs))
	mux.HandleFunc("GET /cards/{id}/purchases/new", handleNewCardEntry(logger, renderer, svcs, "purchase"))
	mux.HandleFunc("POST /cards/{id}/purchases", handleRecordCardEntry(logger, renderer, svcs, "purchase"))
	mux.HandleFunc("GET /cards/{id}/charges/new", handleNewCardEntry(logger, renderer, svcs, "charge"))
	mux.HandleFunc("POST /cards/{id}/charges", handleRecordCardEntry(logger, renderer, svcs, "charge"))
	mux.HandleFunc("GET /cards/{id}/charges/{tid}/edit", handleEditCardCharge(logger, renderer, svcs))
	mux.HandleFunc("POST /cards/{id}/charges/{tid}", handleUpdateCardCharge(logger, renderer, svcs))

	// Refunds (M8.1): money given back for an expense, on any account. Never
	// income. Recorded and corrected only here.
	mux.HandleFunc("GET /refunds/new", handleNewRefund(logger, renderer, svcs))
	mux.HandleFunc("POST /refunds", handleRecordRefund(logger, renderer, svcs))
	mux.HandleFunc("GET /refunds/{id}/edit", handleEditRefund(logger, renderer, svcs))
	mux.HandleFunc("POST /refunds/{id}", handleUpdateRefund(logger, renderer, svcs))

	// Transactions
	mux.HandleFunc("GET /transactions", handleTransactionsList(logger, renderer, svcs))
	mux.HandleFunc("GET /transactions/new", handleNewTransactionForm(logger, renderer, svcs))
	mux.HandleFunc("POST /transactions", handleCreateTransaction(logger, renderer, svcs))
	mux.HandleFunc("GET /transactions/{id}/edit", handleEditTransactionForm(logger, renderer, svcs))
	mux.HandleFunc("POST /transactions/{id}", handleUpdateTransaction(logger, renderer, svcs))
	mux.HandleFunc("GET /transactions/account-info", handleAccountInfo(logger, renderer, svcs))
	mux.HandleFunc("GET /transactions/category-options", handleCategoryOptions(logger, renderer, svcs))

	// Transfers
	mux.HandleFunc("GET /transfers/new", handleNewTransferForm(logger, renderer, svcs))
	mux.HandleFunc("POST /transfers", handleCreateTransfer(logger, renderer, svcs))
	mux.HandleFunc("GET /transfers/{id}/edit", handleEditTransferForm(logger, renderer, svcs))
	mux.HandleFunc("POST /transfers/{id}", handleUpdateTransfer(logger, renderer, svcs))

	// Currency exchanges (M5.5). An FX_EXCHANGE is a real operation between two
	// accounts in DIFFERENT currencies, with two amounts. It is not a transfer,
	// and it has nothing to do with the reference rates administered under
	// /settings/exchange-rates.
	mux.HandleFunc("GET /exchanges/new", handleNewExchangeForm(logger, renderer, svcs))
	mux.HandleFunc("POST /exchanges", handleCreateExchange(logger, renderer, svcs))
	mux.HandleFunc("GET /exchanges/{id}/edit", handleEditExchangeForm(logger, renderer, svcs))
	mux.HandleFunc("POST /exchanges/{id}", handleUpdateExchange(logger, renderer, svcs))

	// Categories
	mux.HandleFunc("GET /categories", handleCategoriesList(logger, renderer, svcs))
	mux.HandleFunc("GET /categories/new", handleNewCategoryForm(logger, renderer, svcs))
	mux.HandleFunc("POST /categories", handleCreateCategory(logger, renderer, svcs))
	mux.HandleFunc("GET /categories/{id}/edit", handleEditCategoryForm(logger, renderer, svcs))
	mux.HandleFunc("POST /categories/{id}", handleUpdateCategory(logger, renderer, svcs))
	mux.HandleFunc("POST /categories/{id}/toggle", handleToggleCategory(logger, svcs))

	// Payment Methods
	mux.HandleFunc("GET /payment-methods", handlePaymentMethodsList(logger, renderer, svcs))
	mux.HandleFunc("GET /payment-methods/new", handleNewPaymentMethodForm(logger, renderer, svcs))
	mux.HandleFunc("POST /payment-methods", handleCreatePaymentMethod(logger, renderer, svcs))
	mux.HandleFunc("GET /payment-methods/{id}/edit", handleEditPaymentMethodForm(logger, renderer, svcs))
	mux.HandleFunc("POST /payment-methods/{id}", handleUpdatePaymentMethod(logger, renderer, svcs))
	mux.HandleFunc("POST /payment-methods/{id}/toggle", handleTogglePaymentMethod(logger, svcs))

	// Settings & Data / Backups
	mux.HandleFunc("GET /settings/data", handleBackupsList(logger, renderer, svcs))
	mux.HandleFunc("POST /settings/data", handleCreateBackup(logger, svcs))
	mux.HandleFunc("POST /settings/data/backup", handleCreateBackup(logger, svcs))

	// Settings: the valuation base currency (M5)
	// "GET /settings" matches that exact path only; "/settings/{$}" would match
	// "/settings/" and make the bare path a redirect.
	mux.HandleFunc("GET /settings", handleSettingsIndex(logger, renderer, svcs))
	mux.HandleFunc("GET /settings/{$}", handleSettingsIndex(logger, renderer, svcs))
	mux.HandleFunc("POST /settings/base-currency", handleSetBaseCurrency(logger, renderer, svcs))

	// The ticker tape under the header (TD-096). /ticker is the fragment htmx
	// loads into it; what it shows is chosen in the browser, so the settings
	// screen has no POST.
	mux.HandleFunc("GET /ticker", handleTicker(logger, renderer, svcs))
	mux.HandleFunc("GET /settings/ticker", handleTickerSettings(logger, renderer))

	// Exchange rates (M5). These are REFERENCE rates, used to value balances;
	// no transaction carries a rate.
	mux.HandleFunc("GET /settings/exchange-rates", handleExchangeRatesList(logger, renderer, svcs))
	mux.HandleFunc("POST /settings/exchange-rates", handleCreateManualRate(logger, renderer, svcs))
	mux.HandleFunc("POST /settings/exchange-rates/refresh", handleRefreshRates(logger, renderer, svcs))
	mux.HandleFunc("POST /settings/exchange-rates/{id}", handleUpdateManualRate(logger, renderer, svcs))
	mux.HandleFunc("POST /settings/exchange-rates/{id}/toggle", handleToggleManualRate(logger, svcs))

	// Mobile equivalents, rendered with the mobile layout against the same
	// handlers' data.
	mux.HandleFunc("GET /mobile/configuracion", handleMobileSettings(logger, renderer, svcs))
	mux.HandleFunc("GET /mobile/tasas", handleMobileExchangeRates(logger, renderer, svcs))

	return mux
}
