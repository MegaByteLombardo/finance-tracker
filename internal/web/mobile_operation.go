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

// MobileOperationFormView holds initial state and live lookup data for the
// mobile new operation screen.
//
// The four fields below SelectedType exist because this one form posts to four
// different endpoints with four different field names, and the browser is not
// the only thing that decides which. Before M5.5 the template hard-coded the
// income-and-expense shape and a script rewrote it on the first tab click — so
// opening /mobile/operacion/nueva?type=TRANSFER directly gave a form already
// showing "Transferencia" that would have posted to /transactions. The server
// now renders the shape it was asked for, and the script keeps it in step from
// there.
type MobileOperationFormView struct {
	// SelectedType is "EXPENSE", "INCOME", "TRANSFER" or "FX_EXCHANGE".
	SelectedType string

	// Shape is how the form must be rendered for SelectedType.
	Shape MobileOperationShape

	CurrentDate string
	Accounts    []MobileAccountOption
	Categories  []MobileCategoryItemView
	Methods     []PaymentMethod
	Currencies  []Currency
}

// MobileOperationShape is everything about the form that depends on which
// operation is being recorded.
type MobileOperationShape struct {
	// Action is the endpoint to post to.
	Action string
	// SourceFieldName is what the first account select is called: "account_id"
	// for income and expenses, "source_account_id" for the two-sided types.
	SourceFieldName string
	// AmountFieldName is what the hero amount input is called: "amount", or
	// "source_amount" for an exchange, which has a second one.
	AmountFieldName string
	// OriginLabel names the first account select.
	OriginLabel string
	// SubmitLabel is the button's text.
	SubmitLabel string
	// SubmitClass is the button's modifier class.
	SubmitClass string

	// Which groups are visible. An exchange is the only one that shows a second
	// amount, because it is the only operation with two.
	ShowCategory          bool
	ShowPaymentMethod     bool
	ShowDestination       bool
	ShowDestinationAmount bool
}

// mobileOperationShapes is the one place the four shapes are stated.
//
// The map NAMES every type it serves; a type not in it falls back to EXPENSE at
// the one call site, which is the same rule the query parameter already applied.
func mobileOperationShapes() map[string]MobileOperationShape {
	return map[string]MobileOperationShape{
		"EXPENSE": {
			Action:          "/transactions",
			SourceFieldName: "account_id",
			AmountFieldName: "amount",
			OriginLabel:     "Cuenta",
			SubmitLabel:     "Registrar Gasto",
			SubmitClass:     "btn-expense",
			ShowCategory:    true, ShowPaymentMethod: true,
		},
		"INCOME": {
			Action:          "/transactions",
			SourceFieldName: "account_id",
			AmountFieldName: "amount",
			OriginLabel:     "Cuenta de abono",
			SubmitLabel:     "Registrar Ingreso",
			SubmitClass:     "btn-income",
			ShowCategory:    true, ShowPaymentMethod: true,
		},
		"TRANSFER": {
			Action:          "/transfers",
			SourceFieldName: "source_account_id",
			AmountFieldName: "amount",
			OriginLabel:     "Cuenta de origen",
			SubmitLabel:     "Registrar Transferencia",
			SubmitClass:     "btn-transfer",
			ShowDestination: true,
		},
		"FX_EXCHANGE": {
			Action:          "/exchanges",
			SourceFieldName: "source_account_id",
			AmountFieldName: "source_amount",
			OriginLabel:     "Cuenta de origen (entregado)",
			SubmitLabel:     "Registrar Cambio",
			SubmitClass:     "btn-exchange",
			ShowDestination: true, ShowDestinationAmount: true,
		},
	}
}

// handleMobileNewOperation serves the mobile new operation form with real options from the database.
func handleMobileNewOperation(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		shapes := mobileOperationShapes()
		opType := strings.ToUpper(r.URL.Query().Get("type"))
		shape, known := shapes[opType]
		if !known {
			// Anything unrecognised records an expense, which is what this
			// screen did before it could do anything else.
			opType = "EXPENSE"
			shape = shapes[opType]
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
			Shape:        shape,
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
