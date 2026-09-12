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
	mux.HandleFunc("GET /mobile", handleMobileDashboard(logger, renderer))
	mux.HandleFunc("GET /mobile/movimientos", handleMobileTransactions(logger, renderer))
	mux.HandleFunc("GET /mobile/transactions", handleMobileTransactions(logger, renderer))
	mux.HandleFunc("GET /mobile/cuentas", handleMobileAccounts(logger, renderer))
	mux.HandleFunc("GET /mobile/accounts", handleMobileAccounts(logger, renderer))
	mux.HandleFunc("GET /mobile/operacion/nueva", handleMobileNewOperation(logger, renderer))
	mux.HandleFunc("GET /mobile/nueva-operacion", handleMobileNewOperation(logger, renderer))
	mux.HandleFunc("GET /mobile/categorias", handleMobileCategories(logger, renderer))
	mux.HandleFunc("GET /mobile/metodos-pago", handleMobilePaymentMethods(logger, renderer))

	// Accounts
	mux.HandleFunc("GET /accounts", handleAccountsList(logger, renderer, svcs))
	mux.HandleFunc("GET /accounts/new", handleNewAccountForm(logger, renderer, svcs))
	mux.HandleFunc("POST /accounts", handleCreateAccount(logger, renderer, svcs))
	mux.HandleFunc("GET /accounts/{id}/edit", handleEditAccountForm(logger, renderer, svcs))
	mux.HandleFunc("POST /accounts/{id}", handleUpdateAccount(logger, renderer, svcs))
	mux.HandleFunc("GET /accounts/subtype-options", handleSubtypeOptions(logger, renderer))

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

	return mux
}
