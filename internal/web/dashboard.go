package web

import (
	"log/slog"
	"net/http"
)

// handleDashboard serves the main dashboard (GET /).
func handleDashboard(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		balances, err := svcs.Ledger.ListAccountBalances(ctx, false)
		if err != nil {
			logger.Error("listing account balances", slog.Any("error", err))
			http.Error(w, "Failed to load account balances", http.StatusInternalServerError)
			return
		}

		totals, err := svcs.Ledger.ListCurrencyTotals(ctx, false)
		if err != nil {
			logger.Error("listing currency totals", slog.Any("error", err))
			http.Error(w, "Failed to load currency totals", http.StatusInternalServerError)
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

		// Group account balances by currency code
		totalsMap := make(map[string]CurrencyTotal)
		for _, tot := range totals {
			totalsMap[tot.CurrencyCode] = tot
		}

		sectionsMap := make(map[string][]AccountBalance)
		var currencyOrder []string

		for _, b := range balances {
			if _, exists := sectionsMap[b.CurrencyCode]; !exists {
				sectionsMap[b.CurrencyCode] = make([]AccountBalance, 0)
				currencyOrder = append(currencyOrder, b.CurrencyCode)
			}
			sectionsMap[b.CurrencyCode] = append(sectionsMap[b.CurrencyCode], b)
		}

		var currencySections []CurrencySection
		for _, code := range currencyOrder {
			tot := totalsMap[code]
			currencySections = append(currencySections, CurrencySection{
				CurrencyCode:   code,
				CurrencyName:   tot.CurrencyName,
				CurrencySymbol: tot.CurrencySymbol,
				TotalFormatted: tot.TotalFormatted,
				Accounts:       sectionsMap[code],
			})
		}

		data := DashboardView{
			CurrencySections:   currencySections,
			CurrencyTotals:     totals,
			RecentTransactions: txs,
			HasAccounts:        len(balances) > 0,
			HasCategories:      len(cats) > 0,
			HasTransactions:    len(txs) > 0,
		}

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
