package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// The currency exchange screens: "Cambiar divisas".
//
// They are deliberately separate from the transfer screens rather than a mode of
// them. A transfer has one amount and one currency; an exchange has two of each,
// and a form that tried to be both would have to hide half of itself depending
// on the pair of accounts chosen — which is exactly the kind of form that ends
// up posting a field the backend reads as something else.
//
// The backend remains the authority on every number here. The rate this page
// shows before saving is a preview computed in the browser for the user's
// benefit; what is recorded is the two amounts, and the rate that comes back is
// the one the ledger derived.

// exchangeCurrencies resolves the currency of each chosen account, so that the
// form can label both sides and show the right number of decimals.
//
// An account that was not chosen, or was not found, simply leaves its side
// blank: this runs before validation and must not itself decide anything.
func exchangeCurrencies(
	r *http.Request, svcs *Services, accounts []Account, sourceID, destinationID int64,
) (source, destination Currency) {
	ctx := r.Context()
	for _, a := range accounts {
		switch a.ID {
		case sourceID:
			if c, err := svcs.Currencies.Get(ctx, a.CurrencyCode); err == nil {
				source = c
			}
		case destinationID:
			if c, err := svcs.Currencies.Get(ctx, a.CurrencyCode); err == nil {
				destination = c
			}
		}
	}
	return source, destination
}

// applyExchangeCurrencies fills the two currency triples on a form.
func applyExchangeCurrencies(form *ExchangeForm, source, destination Currency) {
	form.SourceCurrencyCode = source.Code
	form.SourceCurrencySymbol = source.Symbol
	form.SourceCurrencyMinorUnit = source.MinorUnit
	form.DestinationCurrencyCode = destination.Code
	form.DestinationCurrencySymbol = destination.Symbol
	form.DestinationCurrencyMinorUnit = destination.MinorUnit
}

func applyEditExchangeCurrencies(form *EditExchangeForm, source, destination Currency) {
	form.SourceCurrencyCode = source.Code
	form.SourceCurrencySymbol = source.Symbol
	form.SourceCurrencyMinorUnit = source.MinorUnit
	form.DestinationCurrencyCode = destination.Code
	form.DestinationCurrencySymbol = destination.Symbol
	form.DestinationCurrencyMinorUnit = destination.MinorUnit
}

// validateExchangeFields applies the checks a form can make on its own, before
// anything is sent to the backend.
//
// It deliberately does NOT check that the two currencies differ. That needs the
// accounts, which the backend resolves inside the same database transaction that
// writes; duplicating it here would be a second rule able to disagree with the
// first. What it does check is what is cheap and unambiguous: something was
// chosen, the amounts look positive, a date is present.
func validateExchangeFields(
	errs map[string]string,
	sourceID int64, sourceIDErr error, sourceAmount string,
	destinationID int64, destinationIDErr error, destinationAmount string,
	effectiveDate, description string,
) {
	if sourceIDErr != nil || sourceID <= 0 {
		errs["source_account_id"] = "Please select the account the money leaves."
	}
	if destinationIDErr != nil || destinationID <= 0 {
		errs["destination_account_id"] = "Please select the account the money arrives in."
	}
	if sourceID > 0 && destinationID > 0 && sourceID == destinationID {
		errs["destination_account_id"] = "The two accounts must be different."
	}

	switch {
	case sourceAmount == "":
		errs["source_amount"] = "The amount delivered is required."
	case strings.HasPrefix(sourceAmount, "-"):
		errs["source_amount"] = "The amount delivered must be positive."
	}
	switch {
	case destinationAmount == "":
		errs["destination_amount"] = "The amount received is required."
	case strings.HasPrefix(destinationAmount, "-"):
		errs["destination_amount"] = "The amount received must be positive."
	}

	if effectiveDate == "" {
		errs["effective_date"] = "Effective date is required."
	}
	if len(description) > 255 {
		errs["description"] = "Description must not exceed 255 characters."
	}
}

// applyExchangeError turns what the backend returned into messages on the right
// fields, and reports the status to answer with.
//
// The switch names each condition; an integrity failure is a 500 because no
// input can cause one, and everything a person can fix is a 422.
func applyExchangeError(logger *slog.Logger, errs map[string]string, err error) int {
	var accountErr *AccountSideError

	switch {
	case errors.As(err, &accountErr):
		field := "source_account_id"
		if accountErr.Side == MovementSideDestination {
			field = "destination_account_id"
		}
		if errors.Is(accountErr.Err, ErrAccountInactive) {
			errs[field] = "That account is archived, so no new movement can be recorded against it."
		} else {
			errs[field] = "That account does not exist."
		}
	case errors.Is(err, ErrSameAccount):
		errs["destination_account_id"] = "The two accounts must be different."
	case errors.Is(err, ErrSameCurrency):
		errs["destination_account_id"] =
			"Both accounts are in the same currency. Moving money between them is a transfer, not an exchange."
	case errors.Is(err, ErrInvalidAmount):
		// The backend reports which side in its message; the form marks both
		// rather than guessing, because only one of them is wrong and marking
		// the wrong one is worse than marking two.
		errs["source_amount"] = "Enter both amounts as positive numbers with this currency's decimals."
		errs["destination_amount"] = "Enter both amounts as positive numbers with this currency's decimals."
	case errors.Is(err, ErrTypeChangeNotAllowed):
		errs["general"] = "This transaction is not a currency exchange and cannot be edited here."
	case errors.Is(err, ErrInvalidInput):
		errs["general"] = "Validation failed: please check the date and the description."
	case errors.Is(err, ErrInconsistentExchange):
		logger.Error("inconsistent exchange detected", slog.Any("error", err))
		errs["general"] = "An internal integrity failure occurred with this exchange."
		return http.StatusInternalServerError
	default:
		errs["general"] = "An unexpected error occurred. Please try again."
		return http.StatusInternalServerError
	}
	return http.StatusUnprocessableEntity
}

// handleNewExchangeForm serves GET /exchanges/new
func handleNewExchangeForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accounts, err := svcs.Accounts.List(r.Context(), false)
		if err != nil {
			logger.Error("listing accounts for new exchange", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		var sourceID, destinationID int64
		if raw := r.URL.Query().Get("from_account_id"); raw != "" {
			sourceID, _ = strconv.ParseInt(raw, 10, 64)
		}
		// A card's "pay from another currency" button preselects the card as
		// the destination: such a payment is an exchange into it (TD-091).
		if raw := r.URL.Query().Get("to_account_id"); raw != "" {
			destinationID, _ = strconv.ParseInt(raw, 10, 64)
		}

		form := ExchangeForm{
			ReturnTo:             returnTo(r, "/transactions"),
			MinorUnits:           currencyMinorUnits(r, logger, svcs),
			SourceAccountID:      sourceID,
			DestinationAccountID: destinationID,
			// The business date in Venezuela, not UTC: this form records money
			// moving, and after 20:00 in Caracas a UTC "today" is tomorrow.
			EffectiveDate: todayForForms(),
			Accounts:      accounts,
			Errors:        make(map[string]string),
		}
		sourceCurrency, destinationCurrency := exchangeCurrencies(r, svcs, accounts, sourceID, destinationID)
		applyExchangeCurrencies(&form, sourceCurrency, destinationCurrency)

		pageData := PageData{
			Title:     "Exchange Currency",
			ActiveNav: NavTransactions,
			Data:      form,
		}
		if err := renderer.RenderPage(w, r, http.StatusOK, "exchanges/new.html", pageData); err != nil {
			logger.Error("rendering new exchange form", slog.Any("error", err))
		}
	}
}

// handleCreateExchange serves POST /exchanges
func handleCreateExchange(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		sourceID, sourceIDErr := parseID(strings.TrimSpace(r.FormValue("source_account_id")))
		destinationID, destinationIDErr := parseID(strings.TrimSpace(r.FormValue("destination_account_id")))
		sourceAmount := strings.TrimSpace(r.FormValue("source_amount"))
		destinationAmount := strings.TrimSpace(r.FormValue("destination_amount"))
		effectiveDate := strings.TrimSpace(r.FormValue("effective_date"))
		description := strings.TrimSpace(r.FormValue("description"))

		accounts, err := svcs.Accounts.List(ctx, false)
		if err != nil {
			logger.Error("listing accounts while creating an exchange", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		form := ExchangeForm{
			ReturnTo:             returnTo(r, "/transactions"),
			MinorUnits:           currencyMinorUnits(r, logger, svcs),
			SourceAccountID:      sourceID,
			SourceAmount:         sourceAmount,
			DestinationAccountID: destinationID,
			DestinationAmount:    destinationAmount,
			Description:          description,
			EffectiveDate:        effectiveDate,
			Accounts:             accounts,
			Errors:               make(map[string]string),
		}
		sourceCurrency, destinationCurrency := exchangeCurrencies(r, svcs, accounts, sourceID, destinationID)
		applyExchangeCurrencies(&form, sourceCurrency, destinationCurrency)

		render := func(status int) {
			pageData := PageData{
				Title:     "Exchange Currency",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			if err := renderer.RenderPage(w, r, status, "exchanges/new.html", pageData); err != nil {
				logger.Error("rendering the new exchange form", slog.Any("error", err))
			}
		}

		validateExchangeFields(form.Errors,
			sourceID, sourceIDErr, sourceAmount,
			destinationID, destinationIDErr, destinationAmount,
			effectiveDate, description)
		if len(form.Errors) > 0 {
			render(http.StatusUnprocessableEntity)
			return
		}

		var descPtr *string
		if description != "" {
			descPtr = &description
		}

		if _, err := svcs.Ledger.RecordExchange(ctx, RecordExchangeInput{
			SourceAccountID:      sourceID,
			SourceAmount:         sourceAmount,
			DestinationAccountID: destinationID,
			DestinationAmount:    destinationAmount,
			Description:          descPtr,
			EffectiveDate:        effectiveDate,
		}); err != nil {
			logger.Warn("recording an exchange failed", slog.Any("error", err))
			render(applyExchangeError(logger, form.Errors, err))
			return
		}

		redirectTo(w, r, returnTo(r, "/transactions"))
	}
}

// handleEditExchangeForm serves GET /exchanges/{id}/edit
func handleEditExchangeForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		tx, err := svcs.Ledger.GetTransaction(ctx, id)
		if err != nil {
			switch {
			case errors.Is(err, ErrNotFound):
				http.NotFound(w, r)
			case errors.Is(err, ErrInconsistentExchange):
				logger.Error("inconsistent exchange detected", slog.Any("error", err))
				http.Error(w, "This exchange is stored inconsistently and cannot be shown.",
					http.StatusInternalServerError)
			default:
				logger.Error("failed to get transaction for exchange edit", slog.Any("error", err))
				http.Error(w, "Failed to load exchange", http.StatusInternalServerError)
			}
			return
		}

		// The guard names what this screen ACCEPTS.
		if tx.Type != TransactionTypeFXExchange || tx.Exchange == nil {
			http.Error(w, "This transaction is not a currency exchange.", http.StatusBadRequest)
			return
		}

		// Archived accounts are included so that an exchange involving one can
		// still be opened and read; the backend refuses to record a new movement
		// against it, which is the right place for that rule.
		accounts, err := svcs.Accounts.List(ctx, true)
		if err != nil {
			logger.Error("listing accounts for exchange edit", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		var description string
		if tx.Description != nil {
			description = *tx.Description
		}

		detail := tx.Exchange
		form := EditExchangeForm{
			ReturnTo:             returnTo(r, "/transactions"),
			MinorUnits:           currencyMinorUnits(r, logger, svcs),
			ID:                   tx.ID,
			SourceAccountID:      detail.SourceAccountID,
			SourceAmount:         detail.SourceAmountFormatted,
			DestinationAccountID: detail.DestinationAccountID,
			DestinationAmount:    detail.DestinationAmountFormatted,
			Description:          description,
			EffectiveDate:        tx.EffectiveDate.Format("2006-01-02"),

			SourceCurrencyCode:           detail.SourceCurrencyCode,
			SourceCurrencySymbol:         detail.SourceCurrencySymbol,
			SourceCurrencyMinorUnit:      detail.SourceMinorUnit,
			DestinationCurrencyCode:      detail.DestinationCurrencyCode,
			DestinationCurrencySymbol:    detail.DestinationCurrencySymbol,
			DestinationCurrencyMinorUnit: detail.DestinationMinorUnit,

			ExecutionRate: detail.ExecutionRate,

			Accounts: accounts,
			Errors:   make(map[string]string),
		}

		pageData := PageData{
			Title:     "Edit Currency Exchange",
			ActiveNav: NavTransactions,
			Data:      form,
		}
		if err := renderer.RenderPage(w, r, http.StatusOK, "exchanges/edit.html", pageData); err != nil {
			logger.Error("rendering the edit exchange form", slog.Any("error", err))
		}
	}
}

// handleUpdateExchange serves POST /exchanges/{id}
func handleUpdateExchange(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		existing, err := svcs.Ledger.GetTransaction(ctx, id)
		if err != nil {
			switch {
			case errors.Is(err, ErrNotFound):
				http.NotFound(w, r)
			case errors.Is(err, ErrInconsistentExchange):
				logger.Error("inconsistent exchange detected", slog.Any("error", err))
				http.Error(w, "This exchange is stored inconsistently and cannot be edited.",
					http.StatusInternalServerError)
			default:
				logger.Error("failed to get transaction for exchange update", slog.Any("error", err))
				http.Error(w, "Failed to load exchange", http.StatusInternalServerError)
			}
			return
		}
		if existing.Type != TransactionTypeFXExchange || existing.Exchange == nil {
			http.Error(w, "This transaction is not a currency exchange.", http.StatusBadRequest)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		sourceID, sourceIDErr := parseID(strings.TrimSpace(r.FormValue("source_account_id")))
		destinationID, destinationIDErr := parseID(strings.TrimSpace(r.FormValue("destination_account_id")))
		sourceAmount := strings.TrimSpace(r.FormValue("source_amount"))
		destinationAmount := strings.TrimSpace(r.FormValue("destination_amount"))
		effectiveDate := strings.TrimSpace(r.FormValue("effective_date"))
		description := strings.TrimSpace(r.FormValue("description"))

		accounts, err := svcs.Accounts.List(ctx, true)
		if err != nil {
			logger.Error("listing accounts while updating an exchange", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		form := EditExchangeForm{
			ReturnTo:             returnTo(r, "/transactions"),
			MinorUnits:           currencyMinorUnits(r, logger, svcs),
			ID:                   id,
			SourceAccountID:      sourceID,
			SourceAmount:         sourceAmount,
			DestinationAccountID: destinationID,
			DestinationAmount:    destinationAmount,
			Description:          description,
			EffectiveDate:        effectiveDate,
			// What it reads as today. It is replaced by whatever the backend
			// derives once the edit succeeds, and never posted back.
			ExecutionRate: existing.Exchange.ExecutionRate,
			Accounts:      accounts,
			Errors:        make(map[string]string),
		}
		sourceCurrency, destinationCurrency := exchangeCurrencies(r, svcs, accounts, sourceID, destinationID)
		applyEditExchangeCurrencies(&form, sourceCurrency, destinationCurrency)

		render := func(status int) {
			pageData := PageData{
				Title:     "Edit Currency Exchange",
				ActiveNav: NavTransactions,
				Data:      form,
			}
			if err := renderer.RenderPage(w, r, status, "exchanges/edit.html", pageData); err != nil {
				logger.Error("rendering the edit exchange form", slog.Any("error", err))
			}
		}

		validateExchangeFields(form.Errors,
			sourceID, sourceIDErr, sourceAmount,
			destinationID, destinationIDErr, destinationAmount,
			effectiveDate, description)
		if len(form.Errors) > 0 {
			render(http.StatusUnprocessableEntity)
			return
		}

		var descPtr *string
		if description != "" {
			descPtr = &description
		}

		if _, err := svcs.Ledger.UpdateExchange(ctx, id, UpdateExchangeInput{
			SourceAccountID:      sourceID,
			SourceAmount:         sourceAmount,
			DestinationAccountID: destinationID,
			DestinationAmount:    destinationAmount,
			Description:          descPtr,
			EffectiveDate:        effectiveDate,
		}); err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Warn("updating an exchange failed", slog.Any("error", err))
			render(applyExchangeError(logger, form.Errors, err))
			return
		}

		redirectTo(w, r, returnTo(r, "/transactions"))
	}
}

// currencyMinorUnits maps every currency, archived ones included, to its
// decimals. A form that offers accounts in several currencies renders it onto
// each option, so the amount field formats at the scale of the account chosen
// rather than assuming two decimals and truncating a third. Without it the
// field keeps its default, and the backend still validates the scale.
func currencyMinorUnits(r *http.Request, logger *slog.Logger, svcs *Services) map[string]int {
	currencies, err := svcs.Currencies.List(r.Context(), true)
	if err != nil {
		logger.Warn("listing currencies for amount formatting", slog.Any("error", err))
		return nil
	}
	units := make(map[string]int, len(currencies))
	for _, c := range currencies {
		units[c.Code] = c.MinorUnit
	}
	return units
}

// returnTo is the page a form came from: where Cancel leads and where the
// browser goes once the form has succeeded.
//
// It honours an explicit return_to -- in the query when the form is shown, in a
// hidden field when it is posted -- which is how a card's page or a mobile
// screen gets its user back, and falls back otherwise.
func returnTo(r *http.Request, fallback string) string {
	return safeReturn(r.FormValue("return_to"), fallback)
}

// safeReturn accepts one of this application's own pages and nothing else. A
// value that does not begin with a single "/" is ignored rather than followed,
// which is what stops it being an open redirect: "//host" and "/\host" both
// name another host to a browser.
func safeReturn(raw string, fallback string) string {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") && !strings.Contains(raw, "\\") {
		return raw
	}
	return fallback
}

// keepQuery carries the request's query string over to path, so a page that
// hands over to another keeps its return_to.
func keepQuery(path string, r *http.Request) string {
	if r.URL.RawQuery == "" {
		return path
	}
	return path + "?" + r.URL.RawQuery
}
