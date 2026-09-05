package web

import (
	"errors"
	"log/slog"
	"net/http"
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
		initialBalance := strings.TrimSpace(r.FormValue("initial_balance"))
		initialBalanceDate := strings.TrimSpace(r.FormValue("initial_balance_date"))

		currencies, _ := svcs.Currencies.List(ctx, false)

		form := AccountForm{
			Name:               name,
			Type:               AccountType(acctTypeStr),
			Subtype:            subtypeStr,
			Institution:        institution,
			CurrencyCode:       currencyCode,
			InitialBalance:     initialBalance,
			InitialBalanceDate: initialBalanceDate,
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
			w.WriteHeader(http.StatusUnprocessableEntity)
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

		_, err := svcs.Application.OpenAccount(ctx, input)
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

			w.WriteHeader(status)
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
