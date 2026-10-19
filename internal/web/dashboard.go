package web

import (
	"log/slog"
	"net/http"
)

// handleDashboard serves the main dashboard (GET /?period=30D).
//
// Every financial figure comes from one call to the analytics service. The
// handler parses the period, lays the series out as a chart -- geometry, not
// arithmetic -- and renders. It reconstructs no balance and converts nothing.
//
// The period selector links back here with htmx selecting the analytics block
// out of the full page, so a deep link and a click render the same thing on the
// server.
func handleDashboard(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		preset := ParseDashboardPreset(r.URL.Query().Get("period"))
		chart, keepChart := parseChartPreset(r.URL.Query().Get("chart"), preset)

		balances, err := svcs.Ledger.ListAccountBalances(ctx, false)
		if err != nil {
			logger.Error("listing account balances", slog.Any("error", err))
			http.Error(w, "Failed to load account balances", http.StatusInternalServerError)
			return
		}

		txs, err := svcs.Ledger.ListTransactions(ctx, ListTransactionsInput{Limit: 20})
		if err != nil {
			logger.Error("listing recent transactions", slog.Any("error", err))
			http.Error(w, "Failed to load transactions", http.StatusInternalServerError)
			return
		}

		cats, err := svcs.Categories.List(ctx, ListCategoriesInput{IncludeInactive: false})
		if err != nil {
			logger.Error("listing categories", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		data := DashboardView{
			Periods:            periodOptions("/", preset, keepChart),
			RecentTransactions: txs,
			HasAccounts:        len(balances) > 0,
			HasCategories:      len(cats) > 0,
			HasTransactions:    len(txs) > 0,
		}

		// A failure to value must not take the dashboard down: the recent
		// activity below it needs no exchange rate at all.
		if dash, aerr := svcs.Analytics.Dashboard(ctx, preset); aerr != nil {
			logger.Error("computing the dashboard", slog.Any("error", aerr))
			data.NetWorthError = "No se pudo calcular el patrimonio consolidado."
		} else {
			data.Analytics = dash
			data.NetWorth = dash.NetWorth
			data.History = loadHistory(r, logger, svcs, "/", dash, preset, chart)
		}

		data.Savings = loadSavingsSummary(r, logger, svcs)

		pageData := PageData{
			Title:     "Dashboard",
			ActiveNav: NavDashboard,
			Data:      data,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "dashboard/index.html", pageData); err != nil {
			logger.Error("rendering dashboard", slog.Any("error", err))
		}
	}
}

// loadSavingsSummary reads the savings block of the dashboards. It is nil when
// savings are not in use yet, or when they cannot be read: a dashboard never
// fails because of the savings layer.
func loadSavingsSummary(r *http.Request, logger *slog.Logger, svcs *Services) *SavingsOverview {
	if svcs.Savings == nil {
		return nil
	}
	o, err := svcs.Savings.Overview(r.Context())
	if err != nil {
		logger.Warn("loading the savings summary", slog.Any("error", err))
		return nil
	}
	if !o.Policy.IsSet() && !o.Fund.CanRelease {
		return nil
	}
	return &o
}
