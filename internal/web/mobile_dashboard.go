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

// NetWorthView represents the consolidated net worth in the user's base currency.
type NetWorthView struct {
	AmountFormatted  string
	CurrencyCode     string
	CurrencyLabel    string
	MonthlyChange    string
	MonthlyChangeMsg string
	IsPositiveChange bool
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

		now := time.Now().UTC()
		dateFormatted := formatDateSpanish(now)

		var balanceItems []CurrencyBalanceItem
		var primaryTotal string
		var primaryCurrency string

		for _, t := range totals {
			balanceItems = append(balanceItems, CurrencyBalanceItem{
				CurrencyCode:   t.CurrencyCode,
				CurrencyName:   t.CurrencyName,
				FormattedTotal: t.TotalFormatted,
			})
			if t.CurrencyCode == "USD" || primaryCurrency == "" {
				primaryTotal = t.TotalFormatted
				primaryCurrency = t.CurrencyCode
			}
		}

		if primaryCurrency == "" {
			primaryCurrency = "USD"
			primaryTotal = "0.00"
		}

		var mRecentTxs []MobileTransactionItem
		for _, tx := range recentTxs {
			mRecentTxs = append(mRecentTxs, mapMobileTransactionItem(tx))
		}

		data := MobileDashboardView{
			Header: MobileHeaderView{
				UserName:      "Andres",
				Greeting:      "Hola, Andres",
				FormattedDate: dateFormatted,
				UserInitials:  "A",
			},
			NetWorth: NetWorthView{
				AmountFormatted:  primaryTotal + " " + primaryCurrency,
				CurrencyCode:     primaryCurrency,
				CurrencyLabel:    primaryCurrency + " · balance",
				MonthlyChange:    "Ledger",
				MonthlyChangeMsg: "Saldos derivados de movimientos",
				IsPositiveChange: true,
			},
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
		NetWorth: NetWorthView{
			AmountFormatted:  "$ 5,842.37",
			CurrencyCode:     "USD",
			CurrencyLabel:    "USD · moneda base",
			MonthlyChange:    "+2.4%",
			MonthlyChangeMsg: "+2.4% este mes",
			IsPositiveChange: true,
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
