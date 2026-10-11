package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// handleAccountsList serves GET /accounts
func handleAccountsList(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		balances, err := svcs.Ledger.ListAccountBalances(ctx, true)
		if err != nil {
			logger.Error("listing accounts", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		pageData := PageData{
			Title:     "Accounts",
			ActiveNav: NavAccounts,
			Data: struct {
				Accounts []AccountBalance
			}{
				Accounts: balances,
			},
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "accounts/index.html", pageData); err != nil {
			logger.Error("rendering accounts list", slog.Any("error", err))
		}
	}
}

// handleNewAccountForm serves GET /accounts/new
func handleNewAccountForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		currencies, err := svcs.Currencies.List(ctx, false)
		if err != nil {
			logger.Error("listing currencies for new account", slog.Any("error", err))
			http.Error(w, "Failed to load currencies", http.StatusInternalServerError)
			return
		}

		form := AccountForm{
			Type:               AccountTypeBank,
			Subtype:            string(AccountSubtypeChecking),
			InitialBalanceDate: TodayDate(),
			MinorUnit:          minorUnitOf(currencies, ""),
			Currencies:         currencies,
			Errors:             make(map[string]string),
		}

		pageData := PageData{
			Title:     "Create Account",
			ActiveNav: NavAccounts,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "accounts/new.html", pageData); err != nil {
			logger.Error("rendering new account form", slog.Any("error", err))
		}
	}
}

// handleCreateAccount serves POST /accounts
func handleCreateAccount(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		acctTypeStr := strings.TrimSpace(r.FormValue("type"))
		subtypeStr := strings.TrimSpace(r.FormValue("subtype"))
		currencyCode := strings.TrimSpace(r.FormValue("currency_code"))
		institution := strings.TrimSpace(r.FormValue("institution"))
		// Taken as sent: the form submits the canonical format from
		// docs/m3-contract.md. Stripping separators blindly would turn "1,5"
		// into "15".
		initialBalance := strings.TrimSpace(r.FormValue("initial_balance"))
		initialBalanceDate := strings.TrimSpace(r.FormValue("initial_balance_date"))

		currencies, err := svcs.Currencies.List(ctx, false)
		if err != nil {
			logger.Error("listing currencies while creating an account", slog.Any("error", err))
			http.Error(w, "Failed to load currencies", http.StatusInternalServerError)
			return
		}

		form := AccountForm{
			Name:               name,
			Type:               AccountType(acctTypeStr),
			Subtype:            subtypeStr,
			Institution:        institution,
			CurrencyCode:       currencyCode,
			InitialBalance:     initialBalance,
			InitialBalanceDate: initialBalanceDate,
			MinorUnit:          minorUnitOf(currencies, currencyCode),
			Currencies:         currencies,
			Errors:             make(map[string]string),
		}

		// Presentation validation
		if name == "" {
			form.Errors["name"] = "Account name is required."
		} else if len(name) > 120 {
			form.Errors["name"] = "Account name must not exceed 120 characters."
		}

		if form.Type == "" {
			form.Errors["type"] = "Account type is required."
		}

		if currencyCode == "" {
			form.Errors["currency_code"] = "Currency is required."
		}

		var subPtr *AccountSubtype
		if form.Type == AccountTypeBank {
			if subtypeStr == "" {
				form.Errors["subtype"] = "Bank accounts require a subtype (Checking or Savings)."
			} else if subtypeStr != string(AccountSubtypeChecking) && subtypeStr != string(AccountSubtypeSavings) {
				form.Errors["subtype"] = "Subtype must be CHECKING or SAVINGS."
			} else {
				st := AccountSubtype(subtypeStr)
				subPtr = &st
			}
		} else {
			// Subtype must not be provided for non-bank accounts
			subPtr = nil
		}

		var instPtr *string
		if institution != "" {
			if len(institution) > 120 {
				form.Errors["institution"] = "Institution must not exceed 120 characters."
			} else {
				instPtr = &institution
			}
		}

		var initBalPtr *string
		if initialBalance != "" {
			initBalPtr = &initialBalance
			if initialBalanceDate == "" {
				form.Errors["initial_balance_date"] = "Effective date is required when starting balance is provided."
			}
		}

		if len(form.Errors) > 0 {
			pageData := PageData{
				Title:     "Create Account",
				ActiveNav: NavAccounts,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "accounts/new.html", pageData)
			return
		}

		// Call application workflow: OpenAccount (atomic)
		input := OpenAccountInput{
			Account: CreateAccountInput{
				Name:         name,
				Type:         form.Type,
				Subtype:      subPtr,
				CurrencyCode: currencyCode,
				Institution:  instPtr,
			},
			OpeningBalance:     initBalPtr,
			OpeningBalanceDate: initialBalanceDate,
		}

		_, err = svcs.Application.OpenAccount(ctx, input)
		if err != nil {
			logger.Warn("failed to open account", slog.Any("error", err))
			status := http.StatusUnprocessableEntity
			switch {
			case errors.Is(err, ErrInvalidInput):
				form.Errors["general"] = "Validation failed: please verify all fields."
			case errors.Is(err, ErrInvalidAmount):
				form.Errors["initial_balance"] = "Invalid balance amount format. Use positive/negative digits with up to 2 decimal places."
			case errors.Is(err, ErrCurrencyNotFound):
				form.Errors["currency_code"] = "Selected currency does not exist."
			case errors.Is(err, ErrCurrencyInactive):
				form.Errors["currency_code"] = "Selected currency is inactive."
			case errors.Is(err, ErrConflict):
				status = http.StatusConflict
				form.Errors["general"] = "An account with this name already exists."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "An unexpected error occurred. Please try again."
			}

			pageData := PageData{
				Title:     "Create Account",
				ActiveNav: NavAccounts,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "accounts/new.html", pageData)
			return
		}

		// Success: PRG redirect
		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/accounts")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
	}
}

// handleSubtypeOptions serves GET /accounts/subtype-options?type=... for dynamic htmx swap
func handleSubtypeOptions(logger *slog.Logger, renderer *Renderer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctType := AccountType(strings.TrimSpace(r.URL.Query().Get("type")))
		form := AccountForm{
			Type:    acctType,
			Subtype: string(AccountSubtypeChecking),
			Errors:  make(map[string]string),
		}

		if err := renderer.RenderPartial(w, r, http.StatusOK, "accounts/subtype_options.html", "subtype_options", form); err != nil {
			logger.Error("rendering subtype options partial", slog.Any("error", err))
		}
	}
}

// minorUnitOf reports how many decimals a currency has, for the amount field.
//
// It falls back to the first currency offered, and then to 2, so a form always
// has a scale to work with. The point is that the number comes from the currency
// rather than from an assumption: JPY has 0 decimals and KWD has 3, and a form
// that hard-codes 2 would silently mangle either.
func minorUnitOf(currencies []Currency, code string) int {
	for _, c := range currencies {
		if c.Code == code {
			return c.MinorUnit
		}
	}
	if len(currencies) > 0 {
		return currencies[0].MinorUnit
	}
	return 2
}

// handleEditAccountForm serves GET /accounts/{id}/edit
func handleEditAccountForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		acct, err := svcs.Accounts.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to get account for edit", slog.Any("error", err))
			http.Error(w, "Failed to load account", http.StatusInternalServerError)
			return
		}

		subtype := ""
		if acct.Subtype != nil {
			subtype = string(*acct.Subtype)
		}
		institution := ""
		if acct.Institution != nil {
			institution = *acct.Institution
		}

		form := EditAccountForm{
			ID:           acct.ID,
			Name:         acct.Name,
			Type:         acct.Type,
			Subtype:      subtype,
			Institution:  institution,
			CurrencyCode: acct.CurrencyCode,
			Errors:       make(map[string]string),
		}
		if err := fillAccountStatus(ctx, svcs, &form, acct.IsActive); err != nil {
			logger.Error("reading the balance for the edit form", slog.Any("error", err))
			http.Error(w, "Failed to load account", http.StatusInternalServerError)
			return
		}

		pageData := PageData{
			Title:     "Edit Account",
			ActiveNav: NavAccounts,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "accounts/edit.html", pageData); err != nil {
			logger.Error("rendering edit account form", slog.Any("error", err))
		}
	}
}

// handleUpdateAccount serves POST /accounts/{id}
func handleUpdateAccount(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		existing, err := svcs.Accounts.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to get account for update", slog.Any("error", err))
			http.Error(w, "Failed to load account", http.StatusInternalServerError)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		acctTypeStr := strings.TrimSpace(r.FormValue("type"))
		subtypeStr := strings.TrimSpace(r.FormValue("subtype"))
		institution := strings.TrimSpace(r.FormValue("institution"))

		form := EditAccountForm{
			ID:           id,
			Name:         name,
			Type:         AccountType(acctTypeStr),
			Subtype:      subtypeStr,
			Institution:  institution,
			CurrencyCode: existing.CurrencyCode,
			Errors:       make(map[string]string),
		}
		if err := fillAccountStatus(ctx, svcs, &form, existing.IsActive); err != nil {
			logger.Error("reading the balance for the edit form", slog.Any("error", err))
			http.Error(w, "Failed to load account", http.StatusInternalServerError)
			return
		}

		if name == "" {
			form.Errors["name"] = "Account name is required."
		} else if len(name) > 120 {
			form.Errors["name"] = "Account name must not exceed 120 characters."
		}

		if form.Type == "" {
			form.Errors["type"] = "Account type is required."
		}

		var subPtr *AccountSubtype
		if form.Type == AccountTypeBank {
			if subtypeStr == "" {
				form.Errors["subtype"] = "Bank accounts require a subtype (Checking or Savings)."
			} else if subtypeStr != string(AccountSubtypeChecking) && subtypeStr != string(AccountSubtypeSavings) {
				form.Errors["subtype"] = "Subtype must be CHECKING or SAVINGS."
			} else {
				st := AccountSubtype(subtypeStr)
				subPtr = &st
			}
		} else {
			if subtypeStr != "" {
				form.Errors["subtype"] = "Non-bank accounts cannot have a subtype."
			}
			subPtr = nil
		}

		var instPtr *string
		if institution != "" {
			if len(institution) > 120 {
				form.Errors["institution"] = "Institution must not exceed 120 characters."
			} else {
				instPtr = &institution
			}
		}

		if len(form.Errors) > 0 {
			pageData := PageData{
				Title:     "Edit Account",
				ActiveNav: NavAccounts,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "accounts/edit.html", pageData)
			return
		}

		_, err = svcs.Accounts.UpdateMetadata(ctx, id, UpdateAccountMetadataInput{
			Name:        name,
			Type:        form.Type,
			Subtype:     subPtr,
			Institution: instPtr,
		})
		if err != nil {
			logger.Warn("failed to update account metadata", slog.Any("error", err))
			status := http.StatusUnprocessableEntity
			switch {
			case errors.Is(err, ErrNotFound):
				http.NotFound(w, r)
				return
			case errors.Is(err, ErrInvalidInput):
				form.Errors["general"] = "Validation failed: please verify all fields."
			case errors.Is(err, ErrConflict):
				status = http.StatusConflict
				form.Errors["name"] = "An account with this name already exists."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "An unexpected error occurred. Please try again."
			}

			pageData := PageData{
				Title:     "Edit Account",
				ActiveNav: NavAccounts,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "accounts/edit.html", pageData)
			return
		}

		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/accounts")
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
	}
}
