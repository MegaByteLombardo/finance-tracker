package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ExchangeRatesView is everything the rates screen shows.
type ExchangeRatesView struct {
	// BaseCurrencyCode is the valuation base, shown here because it is what
	// decides which pairs matter.
	BaseCurrencyCode string

	// Rates are the observations, newest effective date first.
	Rates []ExchangeRate

	// Currencies populates the pair selectors. Archived currencies are included:
	// an active account may still be denominated in one, and it has to be
	// possible to record the rate that values it.
	Currencies []Currency

	// ProviderEnabled decides whether the refresh button is offered at all.
	ProviderEnabled bool

	// Form carries the manual-entry form back when it failed validation, so a
	// rejected submission is redisplayed with what the user typed.
	Form ManualRateForm

	// ShowArchived reflects the current filter.
	ShowArchived bool
}

// ManualRateForm is the manual-entry form's state.
type ManualRateForm struct {
	ID                int64
	BaseCurrencyCode  string
	QuoteCurrencyCode string
	Rate              string
	EffectiveDate     string
	Errors            map[string]string
}

func newManualRateForm() ManualRateForm {
	return ManualRateForm{Errors: make(map[string]string)}
}

func handleExchangeRatesList(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view, alert, err := buildExchangeRatesView(r, svcs, newManualRateForm())
		if err != nil {
			logger.Error("failed to load exchange rates", slog.Any("error", err))
			http.Error(w, "Failed to load exchange rates", http.StatusInternalServerError)
			return
		}

		pageData := PageData{
			Title:     "Tasas de cambio",
			ActiveNav: NavSettings,
			Alert:     alert,
			Data:      view,
		}
		if err := renderer.RenderPage(w, r, http.StatusOK, "settings/exchange_rates.html", pageData); err != nil {
			logger.Error("failed to render exchange rates", slog.Any("error", err))
		}
	}
}

// buildExchangeRatesView gathers the page's data.
//
// It takes the form as a parameter so that a failed submission is redisplayed
// with what the user typed, rather than being silently emptied.
func buildExchangeRatesView(
	r *http.Request, svcs *Services, form ManualRateForm,
) (ExchangeRatesView, *FlashAlert, error) {
	ctx := r.Context()

	showArchived := r.URL.Query().Get("archived") == "1"

	rates, err := svcs.Rates.List(ctx, ListExchangeRatesInput{IncludeArchived: showArchived})
	if err != nil {
		return ExchangeRatesView{}, nil, err
	}

	// Archived currencies included on purpose: see the field comment.
	currencies, err := svcs.Currencies.List(ctx, true)
	if err != nil {
		return ExchangeRatesView{}, nil, err
	}

	view := ExchangeRatesView{
		Rates:           rates,
		Currencies:      currencies,
		ProviderEnabled: svcs.Rates.ProviderEnabled(),
		Form:            form,
		ShowArchived:    showArchived,
	}

	// The settings row is the one thing here whose absence is an integrity
	// failure rather than an empty state, but it must not stop the page: the
	// rates themselves are still worth showing.
	var alert *FlashAlert
	if appSettings, serr := svcs.Settings.Get(ctx); serr != nil {
		alert = &FlashAlert{Type: "warning", Message: "No se pudo leer la moneda base."}
	} else {
		view.BaseCurrencyCode = appSettings.BaseCurrencyCode
	}

	if form.EffectiveDate == "" {
		// Today in Venezuela, which is the business day the source publishes
		// against -- not a UTC date, which after 20:00 names tomorrow.
		view.Form.EffectiveDate = todayForForms()
	}
	return view, alert, nil
}

func handleCreateManualRate(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		form := newManualRateForm()
		form.BaseCurrencyCode = strings.ToUpper(strings.TrimSpace(r.FormValue("base_currency_code")))
		form.QuoteCurrencyCode = strings.ToUpper(strings.TrimSpace(r.FormValue("quote_currency_code")))
		form.Rate = strings.TrimSpace(r.FormValue("rate"))
		form.EffectiveDate = strings.TrimSpace(r.FormValue("effective_date"))

		// The source is NOT read from the form. There is no field for it in
		// CreateManualRateInput, and adding one here would let a POST claim to
		// be the Banco Central.
		_, err := svcs.Rates.CreateManual(ctx, CreateManualRateInput{
			BaseCurrencyCode:  form.BaseCurrencyCode,
			QuoteCurrencyCode: form.QuoteCurrencyCode,
			Rate:              form.Rate,
			EffectiveDate:     form.EffectiveDate,
		})
		if err != nil {
			status := rateErrorStatus(err)
			assignRateFormError(&form, err)
			logger.Warn("failed to record a manual rate", slog.Any("error", err))

			view, alert, berr := buildExchangeRatesView(r, svcs, form)
			if berr != nil {
				logger.Error("failed to rebuild the rates page", slog.Any("error", berr))
				http.Error(w, "Failed to load exchange rates", http.StatusInternalServerError)
				return
			}
			_ = renderer.RenderPage(w, r, status, "settings/exchange_rates.html", PageData{
				Title:     "Tasas de cambio",
				ActiveNav: NavSettings,
				Alert:     alert,
				Data:      view,
			})
			return
		}

		redirectTo(w, r, "/settings/exchange-rates")
	}
}

func handleUpdateManualRate(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		_, err := svcs.Rates.UpdateManual(ctx, id, UpdateManualRateInput{
			Rate: strings.TrimSpace(r.FormValue("rate")),
		})
		if err != nil {
			if errors.Is(err, ErrRateNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Warn("failed to update a manual rate", slog.Any("error", err))

			form := newManualRateForm()
			form.ID = id
			assignRateFormError(&form, err)

			view, alert, berr := buildExchangeRatesView(r, svcs, form)
			if berr != nil {
				logger.Error("failed to rebuild the rates page", slog.Any("error", berr))
				http.Error(w, "Failed to load exchange rates", http.StatusInternalServerError)
				return
			}
			_ = renderer.RenderPage(w, r, rateErrorStatus(err), "settings/exchange_rates.html", PageData{
				Title:     "Tasas de cambio",
				ActiveNav: NavSettings,
				Alert:     alert,
				Data:      view,
			})
			return
		}

		redirectTo(w, r, "/settings/exchange-rates")
	}
}

// handleToggleManualRate archives or restores a manual rate.
//
// Archiving is how an override is withdrawn: valuation ignores archived
// observations, so the published rate underneath applies again. Nothing is
// deleted, here or anywhere else.
func handleToggleManualRate(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		id, ok := pathID(w, r)
		if !ok {
			return
		}

		current, err := svcs.Rates.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrRateNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to read a rate before archiving", slog.Any("error", err))
			http.Error(w, "Failed to load the exchange rate", http.StatusInternalServerError)
			return
		}

		if _, err := svcs.Rates.SetManualActive(ctx, id, !current.IsActive); err != nil {
			logger.Warn("failed to archive a rate", slog.Any("error", err))
			http.Error(w, "No se pudo archivar la tasa", rateErrorStatus(err))
			return
		}

		redirectTo(w, r, "/settings/exchange-rates")
	}
}

// handleRefreshRates fetches the external source on demand.
//
// A provider failure is NOT a server error. The site being unreachable, or
// having changed shape, says nothing about this application: the page is
// re-rendered with a message, every stored rate is still shown, and manual entry
// still works. A 500 here would hide all of that behind an error page.
func handleRefreshRates(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		result, err := svcs.Rates.RefreshNow(ctx)

		var alert *FlashAlert
		status := http.StatusOK

		switch {
		case errors.Is(err, ErrProviderInvalidResponse):
			// The one failure worth different words: the site answered and this
			// application could not understand it, which usually means its page
			// changed and the parser needs updating.
			logger.Error("the rate provider returned something unexpected", slog.Any("error", err))
			alert = &FlashAlert{Type: "danger", Message: "El BCV respondió, pero su página no pudo interpretarse. " +
				"Las tasas guardadas siguen disponibles y puede registrar una manualmente."}
			status = http.StatusOK
		case errors.Is(err, ErrProviderUnavailable):
			logger.Warn("the rate provider could not be reached", slog.Any("error", err))
			alert = &FlashAlert{Type: "warning", Message: "No se pudo contactar al BCV. " +
				"Las tasas guardadas siguen disponibles y puede registrar una manualmente."}
		case err != nil:
			logger.Error("the rate refresh failed", slog.Any("error", err))
			alert = &FlashAlert{Type: "danger", Message: "No se pudieron actualizar las tasas."}
		default:
			message := "Tasas del BCV actualizadas: " + strconv.Itoa(result.Stored) +
				", con fecha valor " + result.EffectiveDate.Format("2006-01-02") + "."
			if len(result.Skipped) > 0 {
				message += " Se ignoraron monedas no configuradas: " + strings.Join(result.Skipped, ", ") + "."
			}
			alert = &FlashAlert{Type: "success", Message: message}
		}

		view, settingsAlert, berr := buildExchangeRatesView(r, svcs, newManualRateForm())
		if berr != nil {
			logger.Error("failed to load the rates page", slog.Any("error", berr))
			http.Error(w, "Failed to load exchange rates", http.StatusInternalServerError)
			return
		}
		if alert == nil {
			alert = settingsAlert
		}

		if rerr := renderer.RenderPage(w, r, status, "settings/exchange_rates.html", PageData{
			Title:     "Tasas de cambio",
			ActiveNav: NavSettings,
			Alert:     alert,
			Data:      view,
		}); rerr != nil {
			logger.Error("failed to render the rates page", slog.Any("error", rerr))
		}
	}
}

// rateErrorStatus maps a rate error onto an HTTP status.
//
// It names each case rather than falling through to 500, so that a validation
// failure never reads as a broken server and a provider failure never reads as
// either.
func rateErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrRateNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrRateConflict), errors.Is(err, ErrRateNotEditable):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidRate),
		errors.Is(err, ErrInvalidInput),
		errors.Is(err, ErrSameCurrencyPair),
		errors.Is(err, ErrCurrencyNotFound):
		return http.StatusUnprocessableEntity
	case errors.Is(err, ErrProviderUnavailable), errors.Is(err, ErrProviderInvalidResponse):
		// Reported on the page, not as a failure of this server.
		return http.StatusOK
	default:
		return http.StatusInternalServerError
	}
}

// assignRateFormError puts the message next to the field it is about.
func assignRateFormError(form *ManualRateForm, err error) {
	if form.Errors == nil {
		form.Errors = make(map[string]string)
	}

	switch {
	case errors.Is(err, ErrInvalidRate):
		form.Errors["rate"] = "La tasa debe ser un decimal mayor que cero, con punto decimal " +
			"y sin separadores de miles."
	case errors.Is(err, ErrSameCurrencyPair):
		form.Errors["quote_currency_code"] = "Una tasa necesita dos monedas distintas."
	case errors.Is(err, ErrCurrencyNotFound):
		form.Errors["base_currency_code"] = "Esa moneda no existe."
	case errors.Is(err, ErrRateConflict):
		form.Errors["effective_date"] = "Ya existe una tasa manual para ese par en esa fecha. " +
			"Edítela en lugar de crear otra."
	case errors.Is(err, ErrRateNotEditable):
		form.Errors["general"] = "Una tasa publicada por el BCV no se edita. " +
			"Registre una tasa manual para esa misma fecha."
	case errors.Is(err, ErrInvalidInput):
		form.Errors["effective_date"] = "Revise la fecha (AAAA-MM-DD) y las monedas."
	default:
		form.Errors["general"] = "No se pudo guardar la tasa."
	}
}

// pathID reads the {id} path value, answering 404 for anything that is not a
// number rather than letting a parse error reach the service.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

// redirectTo answers an htmx request and an ordinary one the same way.
func redirectTo(w http.ResponseWriter, r *http.Request, path string) {
	if IsHXRequest(r) {
		w.Header().Set("HX-Redirect", path)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// todayForForms is the date a form defaults to: today in Venezuela.
//
// It is not time.Now().UTC(): between 20:00 and midnight in Caracas, UTC is
// already tomorrow, so a UTC default would pre-fill a date the user has not
// reached.
func todayForForms() string {
	return time.Now().In(economicLocation).Format("2006-01-02")
}

// economicLocation mirrors database.EconomicLocation.
//
// It is duplicated rather than imported because internal/web deliberately does
// not depend on the persistence layer -- the same reason this package declares
// its own vocabulary instead of using the domain's. A test asserts the two
// agree.
var economicLocation = time.FixedZone("-04:00", -4*60*60)
