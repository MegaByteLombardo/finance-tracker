package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// handleTransactionsList serves GET /transactions
func handleTransactionsList(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		txs, err := svcs.Ledger.ListTransactions(ctx, ListTransactionsInput{Limit: 100})
		if err != nil {
			logger.Error("listing transactions", slog.Any("error", err))
			http.Error(w, "Failed to load transactions", http.StatusInternalServerError)
			return
		}

		pageData := PageData{
			Title:     "Transactions",
			ActiveNav: NavTransactions,
			Data: struct {
				Transactions []TransactionView
			}{
				Transactions: txs,
			},
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "transactions/index.html", pageData); err != nil {
			logger.Error("rendering transactions list", slog.Any("error", err))
		}
	}
}

// handleNewTransactionForm serves GET /transactions/new
func handleNewTransactionForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		txTypeStr := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("type")))
		txType := TransactionTypeExpense
		if txTypeStr == string(TransactionTypeIncome) {
			txType = TransactionTypeIncome
		}

		accounts, err := svcs.Accounts.List(ctx, false)
		if err != nil {
			logger.Error("listing accounts for new transaction", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		catType := CategoryTypeExpense
		if txType == TransactionTypeIncome {
			catType = CategoryTypeIncome
		}

		categories, err := svcs.Categories.List(ctx, ListCategoriesInput{
			Type:            &catType,
			IncludeInactive: false,
		})
		if err != nil {
			logger.Error("listing categories for new transaction", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		var selectedCurrency string
		var selectedSymbol *string
		var selectedAccountID int64
		if len(accounts) > 0 {
			selectedAccountID = accounts[0].ID
			selectedCurrency = accounts[0].CurrencyCode
			if curr, err := svcs.Currencies.Get(ctx, selectedCurrency); err == nil {
				selectedSymbol = curr.Symbol
			}
		}

		form := TransactionForm{
			Type:                   txType,
			AccountID:              selectedAccountID,
			PaymentMethod:          PaymentMethodPOS,
			EffectiveDate:          TodayDate(),
			SelectedCurrencyCode:   selectedCurrency,
			SelectedCurrencySymbol: selectedSymbol,
			Accounts:               accounts,
			Categories:             categories,
			Errors:                 make(map[string]string),
		}

		pageData := PageData{
			Title:     "Record Transaction",
			ActiveNav: NavTransactions,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "transactions/new.html", pageData); err != nil {
			logger.Error("rendering new transaction form", slog.Any("error", err))
		}
	}
}

// handleCreateTransaction serves POST /transactions
func handleCreateTransaction(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		txTypeStr := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
		txType := TransactionTypeExpense
		if txTypeStr == string(TransactionTypeIncome) {
			txType = TransactionTypeIncome
		}

		accountIDStr := strings.TrimSpace(r.FormValue("account_id"))
		categoryIDStr := strings.TrimSpace(r.FormValue("category_id"))
		amount := strings.TrimSpace(r.FormValue("amount"))
		amount = strings.ReplaceAll(amount, ",", "")
		paymentMethodStr := strings.TrimSpace(r.FormValue("payment_method"))
		effectiveDate := strings.TrimSpace(r.FormValue("effective_date"))
		description := strings.TrimSpace(r.FormValue("description"))

		accountID, _ := strconv.ParseInt(accountIDStr, 10, 64)
		categoryID, _ := strconv.ParseInt(categoryIDStr, 10, 64)

		accounts, _ := svcs.Accounts.List(ctx, false)

		catType := CategoryTypeExpense
		if txType == TransactionTypeIncome {
			catType = CategoryTypeIncome
		}
		categories, _ := svcs.Categories.List(ctx, ListCategoriesInput{
			Type:            &catType,
			IncludeInactive: false,
		})

		var selectedCurrency string
		var selectedSymbol *string
		if accountID > 0 {
			if acct, err := svcs.Accounts.Get(ctx, accountID); err == nil {
				selectedCurrency = acct.CurrencyCode
				if curr, err := svcs.Currencies.Get(ctx, selectedCurrency); err == nil {
					selectedSymbol = curr.Symbol
				}
			}
		}

		form := TransactionForm{
			Type:                   txType,
			AccountID:              accountID,
			CategoryID:             categoryID,
			Amount:                 amount,
			PaymentMethod:          PaymentMethod(paymentMethodStr),
			EffectiveDate:          effectiveDate,
			Description:            description,
			SelectedCurrencyCode:   selectedCurrency,
			SelectedCurrencySymbol: selectedSymbol,
			Accounts:               accounts,
			Categories:             categories,
			Errors:                 make(map[string]string),
		}

		// Validation
		if accountID <= 0 {
			form.Errors["account_id"] = "Please select an account."
		}
		if categoryID <= 0 {
			form.Errors["category_id"] = "Please select a category."
		}
		if amount == "" {
			form.Errors["amount"] = "Amount is required."
		} else if strings.HasPrefix(amount, "-") {
			form.Errors["amount"] = "Amount must be positive."
		}
		if paymentMethodStr == "" {
			form.Errors["payment_method"] = "Payment method is required."
		}
		if effectiveDate == "" {
			form.Errors["effective_date"] = "Effective date is required."
		}
		if len(description) > 255 {
			form.Errors["description"] = "Description must not exceed 255 characters."
		}

		if len(form.Errors) > 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			pageData := PageData{
				Title:     "Record Transaction",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "transactions/new.html", pageData)
			return
		}

		var descPtr *string
		if description != "" {
			descPtr = &description
		}

		var txErr error
		if txType == TransactionTypeIncome {
			_, txErr = svcs.Ledger.RecordIncome(ctx, RecordIncomeInput{
				AccountID:     accountID,
				CategoryID:    categoryID,
				Amount:        amount,
				PaymentMethod: PaymentMethod(paymentMethodStr),
				Description:   descPtr,
				EffectiveDate: effectiveDate,
			})
		} else {
			_, txErr = svcs.Ledger.RecordExpense(ctx, RecordExpenseInput{
				AccountID:     accountID,
				CategoryID:    categoryID,
				Amount:        amount,
				PaymentMethod: PaymentMethod(paymentMethodStr),
				Description:   descPtr,
				EffectiveDate: effectiveDate,
			})
		}

		if txErr != nil {
			logger.Warn("recording transaction failed", slog.Any("error", txErr))
			status := http.StatusUnprocessableEntity
			switch {
			case errors.Is(txErr, ErrInvalidAmount):
				form.Errors["amount"] = "Invalid amount format. Enter a positive number with valid decimals."
			case errors.Is(txErr, ErrCategoryTypeMismatch):
				form.Errors["category_id"] = "Category type does not match transaction type."
			case errors.Is(txErr, ErrAccountInactive):
				form.Errors["account_id"] = "Selected account is inactive/archived."
			case errors.Is(txErr, ErrCategoryInactive):
				form.Errors["category_id"] = "Selected category is inactive/archived."
			case errors.Is(txErr, ErrAccountNotFound):
				form.Errors["account_id"] = "Selected account does not exist."
			case errors.Is(txErr, ErrCategoryNotFound):
				form.Errors["category_id"] = "Selected category does not exist."
			case errors.Is(txErr, ErrInvalidInput):
				form.Errors["general"] = "Validation failed: please check date and inputs."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "An unexpected error occurred. Please try again."
			}

			w.WriteHeader(status)
			pageData := PageData{
				Title:     "Record Transaction",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "transactions/new.html", pageData)
			return
		}

		// Success: PRG redirect
		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/transactions")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/transactions", http.StatusSeeOther)
	}
}

// handleAccountInfo serves GET /transactions/account-info?account_id=... for htmx dynamic currency display
func handleAccountInfo(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		accountIDStr := strings.TrimSpace(r.URL.Query().Get("account_id"))
		accountID, _ := strconv.ParseInt(accountIDStr, 10, 64)

		var currCode string
		var currSymbol *string

		if accountID > 0 {
			if acct, err := svcs.Accounts.Get(ctx, accountID); err == nil {
				currCode = acct.CurrencyCode
				if curr, err := svcs.Currencies.Get(ctx, currCode); err == nil {
					currSymbol = curr.Symbol
				}
			}
		}

		data := struct {
			SelectedCurrencyCode   string
			SelectedCurrencySymbol *string
		}{
			SelectedCurrencyCode:   currCode,
			SelectedCurrencySymbol: currSymbol,
		}

		if err := renderer.RenderPartial(w, r, http.StatusOK, "transactions/account_info.html", "account_info", data); err != nil {
			logger.Error("rendering account info partial", slog.Any("error", err))
		}
	}
}

// handleCategoryOptions serves GET /transactions/category-options?type=... for dynamic htmx category options swap
func handleCategoryOptions(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		typeStr := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("type")))
		catType := CategoryTypeExpense
		if typeStr == string(CategoryTypeIncome) {
			catType = CategoryTypeIncome
		}

		categories, err := svcs.Categories.List(ctx, ListCategoriesInput{
			Type:            &catType,
			IncludeInactive: false,
		})
		if err != nil {
			logger.Error("listing categories for options partial", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		form := TransactionForm{
			Type:       TransactionType(catType),
			Categories: categories,
			Errors:     make(map[string]string),
		}

		if err := renderer.RenderPartial(w, r, http.StatusOK, "transactions/category_options.html", "category_options", form); err != nil {
			logger.Error("rendering category options partial", slog.Any("error", err))
		}
	}
}
