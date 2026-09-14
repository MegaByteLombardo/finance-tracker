package web

import (
	"log/slog"
	"net/http"
	"time"
)

// MobileDashboardView encapsulates the presentation data for the mobile Inicio dashboard.
type MobileDashboardView struct {
	Header       MobileHeaderView
	NetWorth     NetWorthView
	Balances     CurrencyBalancesView
	Budget       WeeklyBudgetView
	Transactions RecentTransactionsView
	Navigation   MobileNavView
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
// It carries no monthly change. M4 shipped one that was a hard-coded "+2.4%"
// with no computation behind it, which in a finance application is worse than
// showing nothing: a fabricated trend is indistinguishable from a real one.
// A genuine change needs the net worth AT A PAST DATE, which is the historical
// milestone's job, not this one's.
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

// WeeklyBudgetView represents the weekly budget progress and metrics.
type WeeklyBudgetView struct {
	SpentFormatted     string
	TotalFormatted     string
	RemainingFormatted string
	CurrencyCode       string
	Percentage         float64
	DaysRemaining      int
	DaysRemainingText  string
	Status             string // "normal", "warning", "danger"
}

// MobileTransactionItem represents a single movement item in the mobile feed.
type MobileTransactionItem struct {
	ID              string
	Merchant        string
	AccountName     string
	CategoryName    string
	DateLabel       string
	AmountFormatted string
	Type            string // "EXPENSE", "INCOME", "TRANSFER"
	CategoryIcon    string // "cart", "briefcase", "coffee", "pill", "entertainment", "car", "transfer"
	IsPositive      bool
	CurrencyCode    string
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

		totals, err := svcs.Ledger.ListCurrencyTotals(ctx, false)
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

		// The consolidated net worth, derived on every read. A failure to value
		// must not take the dashboard down: the per-currency balances below are
		// the ledger's own figures and are still worth showing, so the header
		// degrades to a message instead.
		netWorth := NetWorthView{CurrencyCode: primaryCurrency, CurrencyLabel: primaryCurrency + " · moneda base"}
		if worth, verr := svcs.Valuation.GetCurrentNetWorth(ctx); verr != nil {
			logger.Error("computing the net worth for the mobile dashboard", slog.Any("error", verr))
			netWorth.AmountFormatted = "-"
			netWorth.IncompleteMessage = "No se pudo calcular el patrimonio consolidado."
		} else {
			netWorth = toNetWorthView(worth)
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
			Budget: WeeklyBudgetView{
				SpentFormatted:     "0.00",
				TotalFormatted:     "Activo",
				RemainingFormatted: "Sin límite",
				CurrencyCode:       primaryCurrency,
				Percentage:         0,
				DaysRemaining:      7 - int(now.Weekday()),
				DaysRemainingText:  "Periodo activo",
				Status:             "normal",
			},
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

// DefaultMobileMockData returns the high-fidelity mock data for test fallbacks.
func DefaultMobileMockData() MobileDashboardView {
	return MobileDashboardView{
		Header: MobileHeaderView{
			UserName:      "Andres",
			Greeting:      "Hola, Andres",
			FormattedDate: "Domingo, 6 de septiembre",
			UserInitials:  "A",
			AvatarURL:     "",
		},
		// No invented figure and no invented trend: the mock shows the shape of
		// the view, not a plausible-looking net worth.
		NetWorth: NetWorthView{
			AmountFormatted: "0.00",
			CurrencyCode:    "USD",
			CurrencyLabel:   "USD · moneda base",
			Complete:        true,
		},
		Balances: CurrencyBalancesView{
			Currencies: []CurrencyBalanceItem{
				{
					CurrencyCode:   "USD",
					CurrencyName:   "Dólares",
					FormattedTotal: "$2,057.56",
				},
				{
					CurrencyCode:   "VES",
					CurrencyName:   "Bolívares",
					FormattedTotal: "Bs 150,548.84",
				},
				{
					CurrencyCode:   "EUR",
					CurrencyName:   "Euros",
					FormattedTotal: "€850.00",
				},
			},
			ViewAccountsURL: "/mobile/cuentas",
		},
		Budget: WeeklyBudgetView{
			SpentFormatted:     "Bs 8,450",
			TotalFormatted:     "Bs 12,000",
			RemainingFormatted: "Bs 3,550",
			CurrencyCode:       "VES",
			Percentage:         70.4,
			DaysRemaining:      3,
			DaysRemainingText:  "Quedan 3 días",
			Status:             "normal",
		},
		Transactions: RecentTransactionsView{
			Transactions: []MobileTransactionItem{
				{
					ID:              "tx-1",
					Merchant:        "Supermercado",
					AccountName:     "Banesco",
					CategoryName:    "Alimentación",
					DateLabel:       "Hoy",
					AmountFormatted: "- Bs 1,240.00",
					Type:            "EXPENSE",
					CategoryIcon:    "cart",
					IsPositive:      false,
					CurrencyCode:    "VES",
				},
			},
			ViewAllURL: "/mobile/movimientos",
		},
		Navigation: MobileNavView{
			ActiveTab: "inicio",
		},
	}
}
