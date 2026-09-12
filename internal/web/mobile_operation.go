package web

import (
	"log/slog"
	"net/http"
	"strings"
)

// MobileAccountOption provides account info with current balance for the mobile picker.
type MobileAccountOption struct {
	ID               int64
	Name             string
	CurrencyCode     string
	BalanceFormatted string
}

// MobileOperationFormView holds initial state and live lookup data for the mobile new operation screen.
type MobileOperationFormView struct {
	SelectedType string // "EXPENSE", "INCOME", "TRANSFER"
	CurrentDate  string
	Accounts     []MobileAccountOption
	Categories   []MobileCategoryItemView
	Methods      []PaymentMethod
	Currencies   []Currency
}

// handleMobileNewOperation serves the mobile new operation form with real options from the database.
func handleMobileNewOperation(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		opType := strings.ToUpper(r.URL.Query().Get("type"))
		if opType != "INCOME" && opType != "TRANSFER" {
			opType = "EXPENSE"
		}

		accounts, err := svcs.Accounts.List(ctx, false)
		if err != nil {
			logger.Error("listing accounts for mobile new operation", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		balances, err := svcs.Ledger.ListAccountBalances(ctx, false)
		if err != nil {
			logger.Error("listing balances for mobile new operation", slog.Any("error", err))
			http.Error(w, "Failed to load balances", http.StatusInternalServerError)
			return
		}

		balMap := make(map[int64]string, len(balances))
		for _, b := range balances {
			balMap[b.AccountID] = b.BalanceFormatted
		}

		var accountOptions []MobileAccountOption
		for _, acct := range accounts {
			balStr := "0.00"
			if b, ok := balMap[acct.ID]; ok {
				balStr = b
			}
			accountOptions = append(accountOptions, MobileAccountOption{
				ID:               acct.ID,
				Name:             acct.Name,
				CurrencyCode:     acct.CurrencyCode,
				BalanceFormatted: balStr,
			})
		}

		categories, err := svcs.Categories.List(ctx, ListCategoriesInput{IncludeInactive: false})
		if err != nil {
			logger.Error("listing categories for mobile new operation", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		var catItems []MobileCategoryItemView
		for _, c := range categories {
			icon, emoji := inferCategoryIconAndEmoji(c.Name)
			catItems = append(catItems, MobileCategoryItemView{
				ID:        c.ID,
				Name:      c.Name,
				Icon:      icon,
				IconEmoji: emoji,
				IsActive:  c.IsActive,
				Type:      c.Type,
			})
		}

		methods, err := svcs.Methods.List(ctx, ListPaymentMethodsInput{IncludeInactive: false})
		if err != nil {
			logger.Error("listing payment methods for mobile new operation", slog.Any("error", err))
			http.Error(w, "Failed to load payment methods", http.StatusInternalServerError)
			return
		}

		currencies, err := svcs.Currencies.List(ctx, false)
		if err != nil {
			logger.Error("listing currencies for mobile new operation", slog.Any("error", err))
			http.Error(w, "Failed to load currencies", http.StatusInternalServerError)
			return
		}

		viewData := MobileOperationFormView{
			SelectedType: opType,
			// The business date in Venezuela, not the server's local date and not
			// UTC. This is a FINANCIAL date on a form that records money moving,
			// and after 20:00 in Caracas a UTC "today" is already tomorrow.
			CurrentDate: todayForForms(),
			Accounts:    accountOptions,
			Categories:  catItems,
			Methods:     methods,
			Currencies:  currencies,
		}

		pageData := PageData{
			Title: "Nueva Operación",
			Data:  viewData,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/new_operation.html", pageData); err != nil {
			logger.Error("rendering mobile new operation form", slog.Any("error", err))
		}
	}
}
