package web

import (
	"log/slog"
	"net/http"
	"time"
)

// MobileDashboardView encapsulates the presentation data for the mobile Inicio dashboard.
//
// Its financial figures come from the same analytics service as the desktop
// dashboard: the two screens differ in layout, never in the rules behind a
// number.
type MobileDashboardView struct {
	Header       MobileHeaderView
	NetWorth     NetWorthView
	History      MobileHistoryView
	CashFlow     MobileCashFlowView
	Balances     CurrencyBalancesView
	Transactions RecentTransactionsView
	Navigation   MobileNavView
}

// MobileHistoryView is the net-worth chart and its period selector.
type MobileHistoryView struct {
	Available      bool
	Chart          ChartView
	Periods        []PeriodOption
	IncompleteDays int
	CurrencyCode   string
}

// MobileCashFlowView is the period's cash flow and spending, as the backend
// computed them. Budgets are not a feature of this application; this card
// replaced a placeholder that suggested otherwise.
type MobileCashFlowView struct {
	Available bool

	CurrencyCode      string
	PeriodDescription string

	CashFlow    CashFlow
	Spending    Spending
	HasPrevious bool
}

// MobileHeaderView holds information for the mobile top bar.
type MobileHeaderView struct {
	UserName      string
	Greeting      string
	FormattedDate string
	UserInitials  string
	AvatarURL     string
}

// NetWorthView is the consolidated net worth as the mobile header shows it.
//
// It is built from web.NetWorth, which the valuation service DERIVES on every
// read from the ledger, the stored rates and the base currency. Nothing here is
// stored or cached.
//
// Its change over the period is computed by internal/analytics from the net
// worth AT A PAST DATE (TD-069). M4 once shipped a hard-coded "+2.4%" here with
// nothing behind it; the figure now exists because the history does, and when
// it cannot be computed the view says why instead of showing a number.
type NetWorthView struct {
	// AmountFormatted is the total, already computed in integer minor units by
	// the only code allowed to do arithmetic on money. A template must never add
	// to it or scale it.
	AmountFormatted string

	// CurrencyCode is the valuation base.
	CurrencyCode string

	// CurrencyLabel reads "USD - moneda base".
	CurrencyLabel string

	// RatesAsOf says which day's rates produced the figure, so the number on the
	// screen can be traced to a publication.
	RatesAsOf string

	// Complete is false when some currency could not be valued. The template
	// MUST then show IncompleteMessage rather than presenting the total as the
	// user's net worth.
	Complete bool

	// IncompleteMessage names what is missing and is empty when Complete.
	IncompleteMessage string

	// Change is the change over the selected period, and PeriodDescription
	// names that period ("últimos 30 días").
	Change            Change
	HasChange         bool
	PeriodDescription string

	// FutureMovementCount and ArchivedWithBalance are the two notes a net
	// worth can carry (TD-063, TD-064).
	FutureMovementCount int
	ArchivedWithBalance int
}

// CurrencyBalanceItem represents a single currency's consolidated balance.
type CurrencyBalanceItem struct {
	CurrencyCode   string
	CurrencyName   string
	FormattedTotal string
}

// CurrencyBalancesView holds the list of currency balances.
type CurrencyBalancesView struct {
	Currencies      []CurrencyBalanceItem
	ViewAccountsURL string
}

// MobileTransactionItem represents a single movement item in the mobile feed.
type MobileTransactionItem struct {
	ID              string
	Merchant        string
	AccountName     string
	CategoryName    string
	DateLabel       string
	AmountFormatted string
	Type            string // "EXPENSE", "INCOME", "TRANSFER", "FX_EXCHANGE"
	CategoryIcon    string // "cart", "briefcase", "coffee", "pill", "entertainment", "car", "transfer", "exchange"
	IsPositive      bool
	CurrencyCode    string

	// SecondaryAmountFormatted is the OTHER side of a currency exchange, in the
	// other currency: AmountFormatted holds what was delivered and this holds
	// what was received.
	//
	// It is empty for every other type, and that emptiness is what the template
	// checks. An exchange is the only transaction with two amounts, and folding
	// them into one string would either hide a currency or invent a conversion.
	SecondaryAmountFormatted string

	// RateLabel is the derived execution rate, already rendered — "1 USD =
	// 842.2067 VES". Empty for everything that is not an exchange.
	RateLabel string
}

// RecentTransactionsView holds the recent transactions feed.
type RecentTransactionsView struct {
	Transactions []MobileTransactionItem
	ViewAllURL   string
}

// MobileNavView represents the state of the 3-tab bottom navigation bar.
type MobileNavView struct {
	ActiveTab string // "inicio", "movimientos", "cuentas"
}

// handleMobileDashboard serves the mobile dashboard view with real ledger balances and movements.
func handleMobileDashboard(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Archived accounts included: the balances by currency are the same
		// money the net worth values, and archiving an account does not make
		// its money stop existing (TD-064).
		totals, err := svcs.Ledger.ListCurrencyTotals(ctx, true)
		if err != nil {
			logger.Error("listing currency totals for mobile dashboard", slog.Any("error", err))
			http.Error(w, "Failed to load totals", http.StatusInternalServerError)
			return
		}

		recentTxs, err := svcs.Ledger.ListTransactions(ctx, ListTransactionsInput{Limit: 5})
		if err != nil {
			logger.Error("listing recent transactions for mobile dashboard", slog.Any("error", err))
			http.Error(w, "Failed to load transactions", http.StatusInternalServerError)
			return
		}

		now := time.Now().In(economicLocation)
		dateFormatted := formatDateSpanish(now)

		// The per-currency balances, exactly as the ledger derived them. They are
		// never converted here: each one is money held in that currency, and the
		// consolidated figure is computed once, by the valuation service.
		var balanceItems []CurrencyBalanceItem
		var primaryCurrency string

		for _, t := range totals {
			balanceItems = append(balanceItems, CurrencyBalanceItem{
				CurrencyCode:   t.CurrencyCode,
				CurrencyName:   t.CurrencyName,
				FormattedTotal: t.TotalFormatted,
			})
			if t.CurrencyCode == "USD" || primaryCurrency == "" {
				primaryCurrency = t.CurrencyCode
			}
		}

		if primaryCurrency == "" {
			primaryCurrency = "USD"
		}

		var mRecentTxs []MobileTransactionItem
		for _, tx := range recentTxs {
			mRecentTxs = append(mRecentTxs, mapMobileTransactionItem(tx))
		}

		// The consolidated net worth, its history and the period's cash flow,
		// all from one analytics call -- the same one the desktop dashboard
		// makes. A failure to value must not take the screen down: the
		// per-currency balances below are the ledger's own figures and are still
		// worth showing, so the figures degrade to a message instead.
		preset := ParseDashboardPreset(r.URL.Query().Get("period"))
		netWorth := NetWorthView{CurrencyCode: primaryCurrency, CurrencyLabel: primaryCurrency + " · moneda base"}
		history := MobileHistoryView{Periods: periodOptions("/mobile", preset)}
		var cashFlow MobileCashFlowView

		if dash, aerr := svcs.Analytics.Dashboard(ctx, preset); aerr != nil {
			logger.Error("computing the mobile dashboard", slog.Any("error", aerr))
			netWorth.AmountFormatted = "-"
			netWorth.IncompleteMessage = "No se pudo calcular el patrimonio consolidado."
		} else {
			netWorth = toNetWorthView(dash.NetWorth)
			netWorth.Change = dash.Change
			netWorth.HasChange = true
			netWorth.PeriodDescription = dash.Preset.Description()

			base := dash.NetWorth.BaseCurrencyCode
			history.Available = true
			history.Chart = BuildNetWorthChart(dash.Points, base)
			history.IncompleteDays = dash.IncompleteDays
			history.CurrencyCode = base

			cashFlow = MobileCashFlowView{
				Available:         true,
				CurrencyCode:      base,
				PeriodDescription: dash.Preset.Description(),
				CashFlow:          dash.CashFlow,
				Spending:          dash.Spending,
				HasPrevious:       dash.HasPrevious,
			}
		}

		data := MobileDashboardView{
			Header: MobileHeaderView{
				UserName:      "Andres",
				Greeting:      "Hola, Andres",
				FormattedDate: dateFormatted,
				UserInitials:  "A",
			},
			NetWorth: netWorth,
			Balances: CurrencyBalancesView{
				Currencies:      balanceItems,
				ViewAccountsURL: "/mobile/cuentas",
			},
			History:  history,
			CashFlow: cashFlow,
			Transactions: RecentTransactionsView{
				Transactions: mRecentTxs,
				ViewAllURL:   "/mobile/movimientos",
			},
			Navigation: MobileNavView{
				ActiveTab: "inicio",
			},
		}

		pageData := PageData{
			Title: "Inicio",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/index.html", pageData); err != nil {
			logger.Error("rendering mobile dashboard", slog.Any("error", err))
		}
	}
}
