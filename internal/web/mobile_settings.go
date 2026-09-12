package web

import (
	"log/slog"
	"net/http"
	"strings"
)

// toNetWorthView adapts the consolidated net worth for a header that has one
// line to say it in.
//
// It is the ONLY place that turns web.NetWorth into display strings, so the
// desktop and the mobile interface cannot end up describing the same figure
// differently. In particular, an incomplete valuation produces a message here
// rather than being left to each template to remember.
func toNetWorthView(worth NetWorth) NetWorthView {
	view := NetWorthView{
		AmountFormatted: worth.TotalFormatted,
		CurrencyCode:    worth.BaseCurrencyCode,
		CurrencyLabel:   worth.BaseCurrencyCode + " · moneda base",
		Complete:        worth.Complete,
	}

	if !worth.RatesAsOf.IsZero() {
		view.RatesAsOf = worth.RatesAsOf.Format("2006-01-02")
	}

	if !worth.Complete {
		// Naming the currencies matters: "incomplete" alone gives the user
		// nothing to act on, and the fix is one form away.
		missing := make([]string, 0, len(worth.Missing))
		for _, m := range worth.Missing {
			missing = append(missing, m.FromCurrencyCode+"/"+m.ToCurrencyCode)
		}
		view.IncompleteMessage = "Falta la tasa " + strings.Join(missing, ", ") +
			"; el total no incluye ese saldo."
	}

	return view
}

// MobileSettingsView is the mobile settings screen.
type MobileSettingsView struct {
	Settings   SettingsView
	NetWorth   NetWorthView
	Navigation MobileNavView
}

// MobileExchangeRatesView is the mobile rates screen.
type MobileExchangeRatesView struct {
	Rates      ExchangeRatesView
	Navigation MobileNavView
}

func handleMobileSettings(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view, err := buildSettingsView(r, svcs, nil)
		if err != nil {
			logger.Error("failed to load mobile settings", slog.Any("error", err))
			http.Error(w, "Failed to load settings", http.StatusInternalServerError)
			return
		}

		netWorth := NetWorthView{Complete: true}
		if worth, verr := svcs.Valuation.GetCurrentNetWorth(r.Context()); verr != nil {
			logger.Error("computing the net worth for mobile settings", slog.Any("error", verr))
			netWorth.AmountFormatted = "-"
			netWorth.IncompleteMessage = "No se pudo calcular el patrimonio consolidado."
		} else {
			netWorth = toNetWorthView(worth)
		}

		data := MobileSettingsView{
			Settings:   view,
			NetWorth:   netWorth,
			Navigation: MobileNavView{ActiveTab: "configuracion"},
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/settings.html", PageData{
			Title: "Configuración",
			Data:  data,
		}); err != nil {
			logger.Error("failed to render mobile settings", slog.Any("error", err))
		}
	}
}

func handleMobileExchangeRates(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view, _, err := buildExchangeRatesView(r, svcs, newManualRateForm())
		if err != nil {
			logger.Error("failed to load mobile exchange rates", slog.Any("error", err))
			http.Error(w, "Failed to load exchange rates", http.StatusInternalServerError)
			return
		}

		data := MobileExchangeRatesView{
			Rates:      view,
			Navigation: MobileNavView{ActiveTab: "tasas"},
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/exchange_rates.html", PageData{
			Title: "Tasas de cambio",
			Data:  data,
		}); err != nil {
			logger.Error("failed to render mobile exchange rates", slog.Any("error", err))
		}
	}
}
