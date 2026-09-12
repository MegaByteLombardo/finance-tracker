package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// SettingsView is the settings index: the valuation base, and where to go next.
type SettingsView struct {
	// BaseCurrencyCode is the currency the consolidated net worth is expressed
	// in.
	BaseCurrencyCode string

	// Currencies are the choices. Only ACTIVE ones are offered: archiving a
	// currency retires it from new use, and choosing a base is new use.
	//
	// The currently configured base is included even if it has since been
	// archived, so the form can show what is actually set rather than silently
	// displaying somebody else's currency as selected.
	Currencies []Currency

	// ProviderEnabled decides whether the rates page advertises a refresh.
	ProviderEnabled bool

	Errors map[string]string
}

func handleSettingsIndex(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view, err := buildSettingsView(r, svcs, nil)
		if err != nil {
			logger.Error("failed to load settings", slog.Any("error", err))
			http.Error(w, "Failed to load settings", http.StatusInternalServerError)
			return
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "settings/index.html", PageData{
			Title:     "Configuración",
			ActiveNav: NavSettings,
			Data:      view,
		}); err != nil {
			logger.Error("failed to render settings", slog.Any("error", err))
		}
	}
}

func buildSettingsView(r *http.Request, svcs *Services, formErrors map[string]string) (SettingsView, error) {
	ctx := r.Context()

	appSettings, err := svcs.Settings.Get(ctx)
	if err != nil {
		return SettingsView{}, err
	}

	all, err := svcs.Currencies.List(ctx, true)
	if err != nil {
		return SettingsView{}, err
	}

	offered := make([]Currency, 0, len(all))
	for _, c := range all {
		if c.IsActive || c.Code == appSettings.BaseCurrencyCode {
			offered = append(offered, c)
		}
	}

	if formErrors == nil {
		formErrors = make(map[string]string)
	}

	return SettingsView{
		BaseCurrencyCode: appSettings.BaseCurrencyCode,
		Currencies:       offered,
		ProviderEnabled:  svcs.Rates.ProviderEnabled(),
		Errors:           formErrors,
	}, nil
}

// handleSetBaseCurrency changes the currency the net worth is expressed in.
//
// It rewrites NOTHING. No transaction, no movement and no balance changes; the
// same money is simply shown converted into a different currency. That is worth
// saying here because it is the question a user asks before pressing the button.
func handleSetBaseCurrency(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		code := strings.ToUpper(strings.TrimSpace(r.FormValue("base_currency_code")))

		if _, err := svcs.Settings.SetBaseCurrency(ctx, code); err != nil {
			logger.Warn("failed to change the valuation base", slog.Any("error", err))

			status := http.StatusUnprocessableEntity
			formErrors := map[string]string{}

			switch {
			case errors.Is(err, ErrCurrencyNotFound):
				formErrors["base_currency_code"] = "Esa moneda no existe."
			case errors.Is(err, ErrCurrencyInactive):
				formErrors["base_currency_code"] = "Esa moneda está archivada y no puede elegirse como base."
			case errors.Is(err, ErrInvalidInput):
				formErrors["base_currency_code"] = "Seleccione una moneda."
			case errors.Is(err, ErrBaseCurrencyUnavailable):
				// Integrity failure: the settings row is gone, which no input
				// can cause.
				logger.Error("the settings row is missing", slog.Any("error", err))
				http.Error(w, "Failed to load settings", http.StatusInternalServerError)
				return
			default:
				status = http.StatusInternalServerError
				formErrors["general"] = "No se pudo cambiar la moneda base."
			}

			view, berr := buildSettingsView(r, svcs, formErrors)
			if berr != nil {
				logger.Error("failed to rebuild the settings page", slog.Any("error", berr))
				http.Error(w, "Failed to load settings", http.StatusInternalServerError)
				return
			}
			_ = renderer.RenderPage(w, r, status, "settings/index.html", PageData{
				Title:     "Configuración",
				ActiveNav: NavSettings,
				Data:      view,
			})
			return
		}

		redirectTo(w, r, "/settings")
	}
}
