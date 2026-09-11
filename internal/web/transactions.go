package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// parseIncomeOrExpenseType reads the type field of the income-and-expense form.
//
// It accepts INCOME and EXPENSE, and treats an absent value as EXPENSE because
// that is what the form defaults to. Everything else is an error.
//
// TRANSFER is a real transaction type and is deliberately NOT one of them. A
// transfer has two accounts and two movements, which this form has no fields
// for, so it is created and edited only through the /transfers flow. What this
// replaced coerced any unrecognised value to EXPENSE, which meant a submission
// saying TRANSFER was quietly filed as an expense against one account -- a wrong
// record rather than a rejected one. OPENING_BALANCE is excluded for the same
// reason: it belongs to the account-opening workflow.
func parseIncomeOrExpenseType(raw string) (TransactionType, error) {
	switch TransactionType(strings.ToUpper(strings.TrimSpace(raw))) {
	case TransactionTypeIncome:
		return TransactionTypeIncome, nil
	case TransactionTypeExpense, "":
		return TransactionTypeExpense, nil
	default:
		return "", ErrInvalidInput
	}
}

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

		txType, err := parseIncomeOrExpenseType(r.URL.Query().Get("type"))
		if err != nil {
			// This form records income and expenses only. A transfer has its
			// own flow; asking this one for one is a bad request, not something
			// to reinterpret.
			http.Error(w, "This form records income and expenses only", http.StatusBadRequest)
			return
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

		methods, err := svcs.Methods.List(ctx, ListPaymentMethodsInput{})
		if err != nil {
			logger.Error("listing payment methods for new transaction", slog.Any("error", err))
			http.Error(w, "Failed to load payment methods", http.StatusInternalServerError)
			return
		}

		var selectedCurrency string
		var selectedSymbol *string
		var selectedMinorUnit int
		var selectedAccountID int64
		if len(accounts) > 0 {
			selectedAccountID = accounts[0].ID
			selectedCurrency = accounts[0].CurrencyCode
			if curr, err := svcs.Currencies.Get(ctx, selectedCurrency); err == nil {
				selectedSymbol = curr.Symbol
				selectedMinorUnit = curr.MinorUnit
			}
		}

		form := TransactionForm{
			Type:                      txType,
			AccountID:                 selectedAccountID,
			PaymentMethodID:           defaultMethodID(methods),
			EffectiveDate:             TodayDate(),
			SelectedCurrencyCode:      selectedCurrency,
			SelectedCurrencySymbol:    selectedSymbol,
			SelectedCurrencyMinorUnit: selectedMinorUnit,
			Accounts:                  accounts,
			Categories:                categories,
			Methods:                   methods,
			Errors:                    make(map[string]string),
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

		txType, txTypeErr := parseIncomeOrExpenseType(r.FormValue("type"))
		if txTypeErr != nil {
			// Fall back to the form's own default so the page can still be
			// rendered with the error on it, rather than recording the wrong
			// kind of transaction.
			txType = TransactionTypeExpense
		}

		accountIDStr := strings.TrimSpace(r.FormValue("account_id"))
		categoryIDStr := strings.TrimSpace(r.FormValue("category_id"))
		// Taken as sent. The presentation layer submits the canonical format
		// defined in docs/m3-contract.md -- digits, an optional single decimal
		// point, no separators -- so there is nothing to strip here.
		//
		// Stripping separators blindly is what this replaced: it turns "1,5"
		// into "15", multiplying by ten an amount someone typed with a decimal
		// comma. Rejecting an unexpected format is the correct outcome; guessing
		// at its meaning is not.
		amount := strings.TrimSpace(r.FormValue("amount"))
		paymentMethodIDStr := strings.TrimSpace(r.FormValue("payment_method_id"))
		effectiveDate := strings.TrimSpace(r.FormValue("effective_date"))
		description := strings.TrimSpace(r.FormValue("description"))

		// Parsed explicitly rather than discarded: a malformed id is a
		// validation failure the user can see, not a silent zero that turns into
		// "account not found" further down.
		accountID, accountIDErr := parseID(accountIDStr)
		categoryID, categoryIDErr := parseID(categoryIDStr)
		paymentMethodID, paymentMethodIDErr := parseID(paymentMethodIDStr)

		accounts, err := svcs.Accounts.List(ctx, false)
		if err != nil {
			logger.Error("listing accounts while recording a transaction", slog.Any("error", err))
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
			logger.Error("listing categories while recording a transaction", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		methods, err := svcs.Methods.List(ctx, ListPaymentMethodsInput{})
		if err != nil {
			logger.Error("listing payment methods while recording a transaction", slog.Any("error", err))
			http.Error(w, "Failed to load payment methods", http.StatusInternalServerError)
			return
		}

		var selectedCurrency string
		var selectedSymbol *string
		var selectedMinorUnit int
		if accountID > 0 {
			if acct, err := svcs.Accounts.Get(ctx, accountID); err == nil {
				selectedCurrency = acct.CurrencyCode
				if curr, err := svcs.Currencies.Get(ctx, selectedCurrency); err == nil {
					selectedSymbol = curr.Symbol
					selectedMinorUnit = curr.MinorUnit
				}
			}
		}

		form := TransactionForm{
			Type:                      txType,
			AccountID:                 accountID,
			CategoryID:                categoryID,
			Amount:                    amount,
			PaymentMethodID:           paymentMethodID,
			EffectiveDate:             effectiveDate,
			Description:               description,
			SelectedCurrencyCode:      selectedCurrency,
			SelectedCurrencySymbol:    selectedSymbol,
			SelectedCurrencyMinorUnit: selectedMinorUnit,
			Accounts:                  accounts,
			Categories:                categories,
			Methods:                   methods,
			Errors:                    make(map[string]string),
		}

		// Validation
		if txTypeErr != nil {
			// "general" rather than a per-field key: this form has no visible
			// type control for the user to correct, so the failure is about the
			// submission as a whole.
			form.Errors["general"] = "This form records Income or Expense only. " +
				"Use the transfer form to move money between your own accounts."
		}
		if accountIDErr != nil || accountID <= 0 {
			form.Errors["account_id"] = "Please select an account."
		}
		if categoryIDErr != nil || categoryID <= 0 {
			form.Errors["category_id"] = "Please select a category."
		}
		if amount == "" {
			form.Errors["amount"] = "Amount is required."
		} else if strings.HasPrefix(amount, "-") {
			form.Errors["amount"] = "Amount must be positive."
		}
		if paymentMethodIDErr != nil || paymentMethodID <= 0 {
			form.Errors["payment_method_id"] = "Please select a payment method."
		}
		if effectiveDate == "" {
			form.Errors["effective_date"] = "Effective date is required."
		}
		if len(description) > 255 {
			form.Errors["description"] = "Description must not exceed 255 characters."
		}

		if len(form.Errors) > 0 {
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
				AccountID:       accountID,
				CategoryID:      categoryID,
				Amount:          amount,
				PaymentMethodID: paymentMethodID,
				Description:     descPtr,
				EffectiveDate:   effectiveDate,
			})
		} else {
			_, txErr = svcs.Ledger.RecordExpense(ctx, RecordExpenseInput{
				AccountID:       accountID,
				CategoryID:      categoryID,
				Amount:          amount,
				PaymentMethodID: paymentMethodID,
				Description:     descPtr,
				EffectiveDate:   effectiveDate,
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
			case errors.Is(txErr, ErrPaymentMethodNotFound):
				form.Errors["payment_method_id"] = "Selected payment method does not exist."
			case errors.Is(txErr, ErrPaymentMethodInactive):
				form.Errors["payment_method_id"] = "Selected payment method is inactive/archived."
			case errors.Is(txErr, ErrInvalidInput):
				form.Errors["general"] = "Validation failed: please check date and inputs."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "An unexpected error occurred. Please try again."
			}

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
		accountID, err := parseID(strings.TrimSpace(r.URL.Query().Get("account_id")))
		if err != nil {
			// A partial fragment for an unreadable id: the form is still being
			// filled in, so this is not an error worth reporting to anyone.
			accountID = 0
		}

		var currCode string
		var currSymbol *string
		currMinorUnit := 2

		if accountID > 0 {
			if acct, err := svcs.Accounts.Get(ctx, accountID); err == nil {
				currCode = acct.CurrencyCode
				if curr, err := svcs.Currencies.Get(ctx, currCode); err == nil {
					currSymbol = curr.Symbol
					currMinorUnit = curr.MinorUnit
				}
			}
		}

		data := struct {
			SelectedCurrencyCode      string
			SelectedCurrencySymbol    *string
			SelectedCurrencyMinorUnit int
		}{
			SelectedCurrencyCode:      currCode,
			SelectedCurrencySymbol:    currSymbol,
			SelectedCurrencyMinorUnit: currMinorUnit,
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

// parseID reads a form field that must hold a positive row id.
//
// The error is returned rather than discarded: a malformed id is something the
// user can be told about, whereas a silently-zero id becomes "not found" three
// layers down and reads like a backend fault.
func parseID(raw string) (int64, error) {
	if raw == "" {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseInt(raw, 10, 64)
}

// defaultMethodID preselects a payment method on an empty form. Nothing is
// selected when the user has archived them all.
func defaultMethodID(methods []PaymentMethod) int64 {
	if len(methods) == 0 {
		return 0
	}
	return methods[0].ID
}

// handleEditTransactionForm serves GET /transactions/{id}/edit
func handleEditTransactionForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		tx, err := svcs.Ledger.GetTransaction(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to get transaction for edit", slog.Any("error", err))
			http.Error(w, "Failed to load transaction", http.StatusInternalServerError)
			return
		}

		if tx.Type == TransactionTypeOpeningBalance {
			amountStr := tx.AmountFormatted
			form := EditOpeningBalanceForm{
				ID:                tx.ID,
				AccountID:         tx.AccountID,
				AccountName:       tx.AccountName,
				CurrencyCode:      tx.CurrencyCode,
				CurrencySymbol:    tx.CurrencySymbol,
				CurrencyMinorUnit: tx.MinorUnit,
				Amount:            amountStr,
				EffectiveDate:     tx.EffectiveDate.Format("2006-01-02"),
				Errors:            make(map[string]string),
			}

			pageData := PageData{
				Title:     "Edit Opening Balance",
				ActiveNav: NavTransactions,
				Data:      form,
			}

			if err := renderer.RenderPage(w, r, http.StatusOK, "transactions/edit_opening.html", pageData); err != nil {
				logger.Error("rendering edit opening balance form", slog.Any("error", err))
			}
			return
		}

		// Income or Expense
		accounts, err := svcs.Accounts.List(ctx, true)
		if err != nil {
			logger.Error("listing accounts for edit transaction", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		catType := CategoryTypeExpense
		if tx.Type == TransactionTypeIncome {
			catType = CategoryTypeIncome
		}

		categories, err := svcs.Categories.List(ctx, ListCategoriesInput{
			Type:            &catType,
			IncludeInactive: true,
		})
		if err != nil {
			logger.Error("listing categories for edit transaction", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		methods, err := svcs.Methods.List(ctx, ListPaymentMethodsInput{IncludeInactive: true})
		if err != nil {
			logger.Error("listing payment methods for edit transaction", slog.Any("error", err))
			http.Error(w, "Failed to load payment methods", http.StatusInternalServerError)
			return
		}

		amountPos := strings.TrimPrefix(tx.AmountFormatted, "-")
		var catID int64
		if tx.CategoryID != nil {
			catID = *tx.CategoryID
		}
		var methodID int64
		if tx.PaymentMethodID != nil {
			methodID = *tx.PaymentMethodID
		}
		var desc string
		if tx.Description != nil {
			desc = *tx.Description
		}

		form := EditTransactionForm{
			ID:                        tx.ID,
			AccountID:                 tx.AccountID,
			Type:                      tx.Type,
			CategoryID:                catID,
			PaymentMethodID:           methodID,
			Amount:                    amountPos,
			Description:               desc,
			EffectiveDate:             tx.EffectiveDate.Format("2006-01-02"),
			SelectedCurrencyCode:      tx.CurrencyCode,
			SelectedCurrencySymbol:    tx.CurrencySymbol,
			SelectedCurrencyMinorUnit: tx.MinorUnit,
			Accounts:                  accounts,
			Categories:                categories,
			Methods:                   methods,
			Errors:                    make(map[string]string),
		}

		pageData := PageData{
			Title:     "Edit Transaction",
			ActiveNav: NavTransactions,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "transactions/edit.html", pageData); err != nil {
			logger.Error("rendering edit transaction form", slog.Any("error", err))
		}
	}
}

// handleUpdateTransaction serves POST /transactions/{id}
func handleUpdateTransaction(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		existing, err := svcs.Ledger.GetTransaction(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to get transaction for update", slog.Any("error", err))
			http.Error(w, "Failed to load transaction", http.StatusInternalServerError)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		if existing.Type == TransactionTypeOpeningBalance {
			amount := strings.TrimSpace(r.FormValue("amount"))
			effectiveDate := strings.TrimSpace(r.FormValue("effective_date"))

			form := EditOpeningBalanceForm{
				ID:                existing.ID,
				AccountID:         existing.AccountID,
				AccountName:       existing.AccountName,
				CurrencyCode:      existing.CurrencyCode,
				CurrencySymbol:    existing.CurrencySymbol,
				CurrencyMinorUnit: existing.MinorUnit,
				Amount:            amount,
				EffectiveDate:     effectiveDate,
				Errors:            make(map[string]string),
			}

			if amount == "" {
				form.Errors["amount"] = "Amount is required."
			}
			if effectiveDate == "" {
				form.Errors["effective_date"] = "Effective date is required."
			}

			if len(form.Errors) > 0 {
				pageData := PageData{
					Title:     "Edit Opening Balance",
					ActiveNav: NavTransactions,
					Data:      form,
				}
				_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "transactions/edit_opening.html", pageData)
				return
			}

			_, err = svcs.Ledger.UpdateOpeningBalance(ctx, id, UpdateOpeningBalanceInput{
				Amount:        amount,
				EffectiveDate: effectiveDate,
			})
			if err != nil {
				logger.Warn("failed to update opening balance", slog.Any("error", err))
				status := http.StatusUnprocessableEntity
				switch {
				case errors.Is(err, ErrNotFound):
					http.NotFound(w, r)
					return
				case errors.Is(err, ErrInvalidAmount):
					form.Errors["amount"] = "Invalid amount. Opening balance cannot be zero."
				case errors.Is(err, ErrInvalidInput):
					form.Errors["general"] = "Validation failed: please verify all fields."
				default:
					status = http.StatusInternalServerError
					form.Errors["general"] = "An unexpected error occurred. Please try again."
				}

				pageData := PageData{
					Title:     "Edit Opening Balance",
					ActiveNav: NavTransactions,
					Data:      form,
				}
				_ = renderer.RenderPage(w, r, status, "transactions/edit_opening.html", pageData)
				return
			}

			if IsHXRequest(r) {
				w.Header().Set("HX-Redirect", "/transactions")
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, "/transactions", http.StatusSeeOther)
			return
		}

		// Income or Expense editing
		txTypeStr := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
		txType := TransactionType(txTypeStr)
		accountIDStr := strings.TrimSpace(r.FormValue("account_id"))
		categoryIDStr := strings.TrimSpace(r.FormValue("category_id"))
		methodIDStr := strings.TrimSpace(r.FormValue("payment_method_id"))
		amount := strings.TrimSpace(r.FormValue("amount"))
		effectiveDate := strings.TrimSpace(r.FormValue("effective_date"))
		description := strings.TrimSpace(r.FormValue("description"))
		allowCurrencyChangeStr := strings.TrimSpace(r.FormValue("allow_currency_change"))
		allowCurrencyChange := allowCurrencyChangeStr == "true" || allowCurrencyChangeStr == "1"

		accounts, _ := svcs.Accounts.List(ctx, true)
		catType := CategoryTypeExpense
		if txType == TransactionTypeIncome {
			catType = CategoryTypeIncome
		}
		categories, _ := svcs.Categories.List(ctx, ListCategoriesInput{Type: &catType, IncludeInactive: true})
		methods, _ := svcs.Methods.List(ctx, ListPaymentMethodsInput{IncludeInactive: true})

		accountID, errAcct := parseID(accountIDStr)
		categoryID, errCat := parseID(categoryIDStr)
		methodID, errMethod := parseID(methodIDStr)

		selectedCurrency := existing.CurrencyCode
		selectedSymbol := existing.CurrencySymbol
		selectedMinorUnit := existing.MinorUnit
		for _, a := range accounts {
			if a.ID == accountID {
				selectedCurrency = a.CurrencyCode
				if curr, err := svcs.Currencies.Get(ctx, a.CurrencyCode); err == nil {
					selectedSymbol = curr.Symbol
					selectedMinorUnit = curr.MinorUnit
				}
				break
			}
		}

		form := EditTransactionForm{
			ID:                        id,
			AccountID:                 accountID,
			Type:                      txType,
			CategoryID:                categoryID,
			PaymentMethodID:           methodID,
			Amount:                    amount,
			Description:               description,
			EffectiveDate:             effectiveDate,
			AllowCurrencyChange:       allowCurrencyChange,
			SelectedCurrencyCode:      selectedCurrency,
			SelectedCurrencySymbol:    selectedSymbol,
			SelectedCurrencyMinorUnit: selectedMinorUnit,
			Accounts:                  accounts,
			Categories:                categories,
			Methods:                   methods,
			Errors:                    make(map[string]string),
		}

		if txType != TransactionTypeIncome && txType != TransactionTypeExpense {
			form.Errors["type"] = "Transaction type must be Income or Expense."
		}
		if errAcct != nil {
			form.Errors["account_id"] = "Account selection is required."
		}
		if errCat != nil {
			form.Errors["category_id"] = "Category selection is required."
		}
		if errMethod != nil {
			form.Errors["payment_method_id"] = "Payment method selection is required."
		}
		if amount == "" {
			form.Errors["amount"] = "Amount is required."
		} else if strings.HasPrefix(amount, "-") {
			form.Errors["amount"] = "Amount must be positive."
		}
		if effectiveDate == "" {
			form.Errors["effective_date"] = "Effective date is required."
		}
		if len(description) > 255 {
			form.Errors["description"] = "Description must not exceed 255 characters."
		}

		if len(form.Errors) > 0 {
			pageData := PageData{
				Title:     "Edit Transaction",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "transactions/edit.html", pageData)
			return
		}

		var descPtr *string
		if description != "" {
			descPtr = &description
		}

		_, err = svcs.Ledger.UpdateTransaction(ctx, id, UpdateTransactionInput{
			AccountID:           accountID,
			Type:                txType,
			CategoryID:          categoryID,
			PaymentMethodID:     methodID,
			Amount:              amount,
			Description:         descPtr,
			EffectiveDate:       effectiveDate,
			AllowCurrencyChange: allowCurrencyChange,
		})
		if err != nil {
			logger.Warn("failed to update transaction", slog.Any("error", err))
			status := http.StatusUnprocessableEntity
			switch {
			case errors.Is(err, ErrNotFound):
				http.NotFound(w, r)
				return
			case errors.Is(err, ErrCurrencyChangeNotConfirmed):
				status = http.StatusConflict
				form.NeedsCurrencyConfirmation = true
				form.OriginalCurrencyCode = existing.CurrencyCode
				form.TargetCurrencyCode = selectedCurrency
			case errors.Is(err, ErrTypeChangeNotAllowed):
				form.Errors["type"] = "Cannot change transaction to or from an opening balance."
			case errors.Is(err, ErrInvalidAmount):
				form.Errors["amount"] = "Invalid amount format. Must be a positive number."
			case errors.Is(err, ErrAccountNotFound):
				form.Errors["account_id"] = "Selected account was not found."
			case errors.Is(err, ErrAccountInactive):
				form.Errors["account_id"] = "Selected account is inactive."
			case errors.Is(err, ErrCategoryNotFound):
				form.Errors["category_id"] = "Selected category was not found."
			case errors.Is(err, ErrCategoryInactive):
				form.Errors["category_id"] = "Selected category is inactive."
			case errors.Is(err, ErrCategoryTypeMismatch):
				form.Errors["category_id"] = "Category type does not match transaction type."
			case errors.Is(err, ErrPaymentMethodNotFound):
				form.Errors["payment_method_id"] = "Selected payment method was not found."
			case errors.Is(err, ErrPaymentMethodInactive):
				form.Errors["payment_method_id"] = "Selected payment method is inactive."
			case errors.Is(err, ErrInvalidInput):
				form.Errors["general"] = "Validation failed: please check all fields."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "An unexpected error occurred. Please try again."
			}

			pageData := PageData{
				Title:     "Edit Transaction",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "transactions/edit.html", pageData)
			return
		}

		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/transactions")
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Redirect(w, r, "/transactions", http.StatusSeeOther)
	}
}
