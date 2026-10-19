package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// handleNewTransferForm serves GET /transfers/new
func handleNewTransferForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		accounts, err := svcs.Accounts.List(ctx, false)
		if err != nil {
			logger.Error("listing accounts for new transfer", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		var fromID int64
		if rawFrom := r.URL.Query().Get("from_account_id"); rawFrom != "" {
			fromID, _ = strconv.ParseInt(rawFrom, 10, 64)
		}

		var selectedCurrency string
		var selectedSymbol *string
		var selectedMinorUnit int
		if fromID > 0 {
			for _, a := range accounts {
				if a.ID == fromID {
					selectedCurrency = a.CurrencyCode
					if curr, err := svcs.Currencies.Get(ctx, a.CurrencyCode); err == nil {
						selectedSymbol = curr.Symbol
						selectedMinorUnit = curr.MinorUnit
					}
					break
				}
			}
		}

		form := TransferForm{
			SourceAccountID:           fromID,
			EffectiveDate:             TodayDate(),
			SelectedCurrencyCode:      selectedCurrency,
			SelectedCurrencySymbol:    selectedSymbol,
			SelectedCurrencyMinorUnit: selectedMinorUnit,
			Accounts:                  accounts,
			Errors:                    make(map[string]string),
		}

		pageData := PageData{
			Title:     "New Internal Transfer",
			ActiveNav: NavTransactions,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "transfers/new.html", pageData); err != nil {
			logger.Error("rendering new transfer form", slog.Any("error", err))
		}
	}
}

// handleCreateTransfer serves POST /transfers
func handleCreateTransfer(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		sourceIDStr := strings.TrimSpace(r.FormValue("source_account_id"))
		destIDStr := strings.TrimSpace(r.FormValue("destination_account_id"))
		amount := strings.TrimSpace(r.FormValue("amount"))
		effectiveDate := strings.TrimSpace(r.FormValue("effective_date"))
		description := strings.TrimSpace(r.FormValue("description"))

		sourceID, sourceIDErr := parseID(sourceIDStr)
		destID, destIDErr := parseID(destIDStr)

		accounts, err := svcs.Accounts.List(ctx, false)
		if err != nil {
			logger.Error("listing accounts while creating a transfer", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		var selectedCurrency string
		var selectedSymbol *string
		var selectedMinorUnit int
		for _, a := range accounts {
			if a.ID == sourceID {
				selectedCurrency = a.CurrencyCode
				if curr, err := svcs.Currencies.Get(ctx, a.CurrencyCode); err == nil {
					selectedSymbol = curr.Symbol
					selectedMinorUnit = curr.MinorUnit
				}
				break
			}
		}

		form := TransferForm{
			SourceAccountID:           sourceID,
			DestinationAccountID:      destID,
			Amount:                    amount,
			Description:               description,
			EffectiveDate:             effectiveDate,
			SelectedCurrencyCode:      selectedCurrency,
			SelectedCurrencySymbol:    selectedSymbol,
			SelectedCurrencyMinorUnit: selectedMinorUnit,
			Accounts:                  accounts,
			Errors:                    make(map[string]string),
		}

		// Validation
		if sourceIDErr != nil || sourceID <= 0 {
			form.Errors["source_account_id"] = "Please select a source account."
		}
		if destIDErr != nil || destID <= 0 {
			form.Errors["destination_account_id"] = "Please select a destination account."
		}
		if sourceID > 0 && destID > 0 && sourceID == destID {
			form.Errors["destination_account_id"] = "Source and destination accounts must be different."
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
				Title:     "New Internal Transfer",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "transfers/new.html", pageData)
			return
		}

		var descPtr *string
		if description != "" {
			descPtr = &description
		}

		_, txErr := svcs.Ledger.RecordTransfer(ctx, RecordTransferInput{
			SourceAccountID:      sourceID,
			DestinationAccountID: destID,
			Amount:               amount,
			Description:          descPtr,
			EffectiveDate:        effectiveDate,
			// Reserved money whose backing moves with the transfer (M7.4).
			Savings: transferPlanFromForm(r),
		})

		if txErr != nil {
			logger.Warn("recording transfer failed", slog.Any("error", txErr))
			status := http.StatusUnprocessableEntity
			var accountErr *TransferAccountError

			switch {
			case errors.As(txErr, &accountErr):
				field := "source_account_id"
				if accountErr.Side == TransferSideDestination {
					field = "destination_account_id"
				}
				if errors.Is(accountErr.Err, ErrAccountInactive) {
					form.Errors[field] = "Selected account is inactive/archived."
				} else {
					form.Errors[field] = "Selected account does not exist."
				}
			case savingsMovementError(txErr) != "":
				form.Errors["general"] = savingsMovementError(txErr)
			case errors.Is(txErr, ErrSameAccount):
				form.Errors["destination_account_id"] = "Source and destination accounts must be different."
			case errors.Is(txErr, ErrCurrencyMismatch):
				form.Errors["destination_account_id"] = "Both accounts must be in the same currency."
			case errors.Is(txErr, ErrInvalidAmount):
				form.Errors["amount"] = "Invalid amount format. Enter a positive number with valid decimals."
			case errors.Is(txErr, ErrInvalidInput):
				form.Errors["general"] = "Validation failed: please check date and inputs."
			case errors.Is(txErr, ErrInconsistentTransfer):
				logger.Error("inconsistent transfer detected", slog.Any("error", txErr))
				status = http.StatusInternalServerError
				form.Errors["general"] = "An internal integrity failure occurred with this transfer."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "An unexpected error occurred. Please try again."
			}

			pageData := PageData{
				Title:     "New Internal Transfer",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "transfers/new.html", pageData)
			return
		}

		redirectURL := "/transactions"
		if ret := r.FormValue("return_to"); ret != "" && strings.HasPrefix(ret, "/") {
			redirectURL = ret
		}
		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", redirectURL)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
	}
}

// handleEditTransferForm serves GET /transfers/{id}/edit
func handleEditTransferForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
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
			logger.Error("failed to get transaction for transfer edit", slog.Any("error", err))
			http.Error(w, "Failed to load transfer", http.StatusInternalServerError)
			return
		}

		if tx.Type != TransactionTypeTransfer || tx.Transfer == nil {
			http.Error(w, "Transaction is not a transfer", http.StatusBadRequest)
			return
		}

		accounts, err := svcs.Accounts.List(ctx, true)
		if err != nil {
			logger.Error("listing accounts for transfer edit", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		var desc string
		if tx.Description != nil {
			desc = *tx.Description
		}

		form := EditTransferForm{
			ID:                        tx.ID,
			SourceAccountID:           tx.Transfer.SourceAccountID,
			DestinationAccountID:      tx.Transfer.DestinationAccountID,
			Amount:                    tx.Transfer.AmountFormatted,
			Description:               desc,
			EffectiveDate:             tx.EffectiveDate.Format("2006-01-02"),
			SelectedCurrencyCode:      tx.CurrencyCode,
			SelectedCurrencySymbol:    tx.CurrencySymbol,
			SelectedCurrencyMinorUnit: tx.MinorUnit,
			Accounts:                  accounts,
			Errors:                    make(map[string]string),
		}

		pageData := PageData{
			Title:     "Edit Transfer",
			ActiveNav: NavTransactions,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "transfers/edit.html", pageData); err != nil {
			logger.Error("rendering edit transfer form", slog.Any("error", err))
		}
	}
}

// handleUpdateTransfer serves POST /transfers/{id}
func handleUpdateTransfer(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
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
			logger.Error("failed to get transaction for transfer update", slog.Any("error", err))
			http.Error(w, "Failed to load transfer", http.StatusInternalServerError)
			return
		}

		if existing.Type != TransactionTypeTransfer || existing.Transfer == nil {
			http.Error(w, "Transaction is not a transfer", http.StatusBadRequest)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		sourceIDStr := strings.TrimSpace(r.FormValue("source_account_id"))
		destIDStr := strings.TrimSpace(r.FormValue("destination_account_id"))
		amount := strings.TrimSpace(r.FormValue("amount"))
		effectiveDate := strings.TrimSpace(r.FormValue("effective_date"))
		description := strings.TrimSpace(r.FormValue("description"))
		allowCurrencyChangeStr := strings.TrimSpace(r.FormValue("allow_currency_change"))
		allowCurrencyChange := allowCurrencyChangeStr == "true" || allowCurrencyChangeStr == "1"

		accounts, _ := svcs.Accounts.List(ctx, true)

		sourceID, errSrc := parseID(sourceIDStr)
		destID, errDest := parseID(destIDStr)

		selectedCurrency := existing.CurrencyCode
		selectedSymbol := existing.CurrencySymbol
		selectedMinorUnit := existing.MinorUnit
		for _, a := range accounts {
			if a.ID == sourceID {
				selectedCurrency = a.CurrencyCode
				if curr, err := svcs.Currencies.Get(ctx, a.CurrencyCode); err == nil {
					selectedSymbol = curr.Symbol
					selectedMinorUnit = curr.MinorUnit
				}
				break
			}
		}

		form := EditTransferForm{
			ID:                        id,
			SourceAccountID:           sourceID,
			DestinationAccountID:      destID,
			Amount:                    amount,
			Description:               description,
			EffectiveDate:             effectiveDate,
			AllowCurrencyChange:       allowCurrencyChange,
			SelectedCurrencyCode:      selectedCurrency,
			SelectedCurrencySymbol:    selectedSymbol,
			SelectedCurrencyMinorUnit: selectedMinorUnit,
			Accounts:                  accounts,
			Errors:                    make(map[string]string),
		}

		if errSrc != nil || sourceID <= 0 {
			form.Errors["source_account_id"] = "Source account selection is required."
		}
		if errDest != nil || destID <= 0 {
			form.Errors["destination_account_id"] = "Destination account selection is required."
		}
		if sourceID > 0 && destID > 0 && sourceID == destID {
			form.Errors["destination_account_id"] = "Source and destination accounts must be different."
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
				Title:     "Edit Transfer",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "transfers/edit.html", pageData)
			return
		}

		var descPtr *string
		if description != "" {
			descPtr = &description
		}

		_, err = svcs.Ledger.UpdateTransfer(ctx, id, UpdateTransferInput{
			SourceAccountID:      sourceID,
			DestinationAccountID: destID,
			Amount:               amount,
			Description:          descPtr,
			EffectiveDate:        effectiveDate,
			AllowCurrencyChange:  allowCurrencyChange,
		})

		if err != nil {
			logger.Warn("failed to update transfer", slog.Any("error", err))
			status := http.StatusUnprocessableEntity
			var accountErr *TransferAccountError

			switch {
			case errors.Is(err, ErrNotFound):
				http.NotFound(w, r)
				return
			case errors.Is(err, ErrCurrencyChangeNotConfirmed):
				status = http.StatusConflict
				form.NeedsCurrencyConfirmation = true
				form.OriginalCurrencyCode = existing.CurrencyCode
				form.TargetCurrencyCode = selectedCurrency
			case errors.As(err, &accountErr):
				field := "source_account_id"
				if accountErr.Side == TransferSideDestination {
					field = "destination_account_id"
				}
				if errors.Is(accountErr.Err, ErrAccountInactive) {
					form.Errors[field] = "Selected account is inactive."
				} else {
					form.Errors[field] = "Selected account was not found."
				}
			case errors.Is(err, ErrSameAccount):
				form.Errors["destination_account_id"] = "Source and destination accounts must be different."
			case errors.Is(err, ErrCurrencyMismatch):
				form.Errors["destination_account_id"] = "Both accounts must be in the same currency."
			case errors.Is(err, ErrInvalidAmount):
				form.Errors["amount"] = "Invalid amount format. Must be a positive number."
			case errors.Is(err, ErrTypeChangeNotAllowed):
				form.Errors["general"] = "Cannot change the type of a transfer."
			case errors.Is(err, ErrInvalidInput):
				form.Errors["general"] = "Validation failed: please check all fields."
			case errors.Is(err, ErrInconsistentTransfer):
				logger.Error("inconsistent transfer detected on update", slog.Any("error", err))
				status = http.StatusInternalServerError
				form.Errors["general"] = "An internal integrity failure occurred with this transfer."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "An unexpected error occurred. Please try again."
			}

			pageData := PageData{
				Title:     "Edit Transfer",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "transfers/edit.html", pageData)
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
