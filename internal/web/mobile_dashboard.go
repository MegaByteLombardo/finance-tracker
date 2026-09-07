package web

import (
	"log/slog"
	"net/http"
)

// MobileDashboardView encapsulates the presentation data for the mobile Inicio dashboard.
// It is designed to be populated dynamically by backend services when available,
// while currently carrying high-fidelity mock UI data.
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
	CategoryIcon    string // "cart", "briefcase", "coffee", "pill", "entertainment", "car"
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

// DefaultMobileMockData returns the high-fidelity mock data requested for the Finance Tracker mobile view.
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
			ViewAccountsURL: "/accounts",
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
				{
					ID:              "tx-2",
					Merchant:        "Nómina",
					AccountName:     "Mercantil",
					CategoryName:    "Ingresos",
					DateLabel:       "Ayer",
					AmountFormatted: "+ $850.00",
					Type:            "INCOME",
					CategoryIcon:    "briefcase",
					IsPositive:      true,
					CurrencyCode:    "USD",
				},
				{
					ID:              "tx-3",
					Merchant:        "Café",
					AccountName:     "Efectivo",
					CategoryName:    "Comida",
					DateLabel:       "5 Sep",
					AmountFormatted: "- $4.50",
					Type:            "EXPENSE",
					CategoryIcon:    "coffee",
					IsPositive:      false,
					CurrencyCode:    "USD",
				},
				{
					ID:              "tx-4",
					Merchant:        "Farmacia",
					AccountName:     "Banesco",
					CategoryName:    "Salud",
					DateLabel:       "4 Sep",
					AmountFormatted: "- Bs 420.00",
					Type:            "EXPENSE",
					CategoryIcon:    "pill",
					IsPositive:      false,
					CurrencyCode:    "VES",
				},
				{
					ID:              "tx-5",
					Merchant:        "Suscripción Streaming",
					AccountName:     "Tarjeta USD",
					CategoryName:    "Entretenimiento",
					DateLabel:       "2 Sep",
					AmountFormatted: "- $14.99",
					Type:            "EXPENSE",
					CategoryIcon:    "entertainment",
					IsPositive:      false,
					CurrencyCode:    "USD",
				},
			},
			ViewAllURL: "/mobile/movimientos",
		},
		Navigation: MobileNavView{
			ActiveTab: "inicio",
		},
	}
}

// handleMobileDashboard serves the mobile dashboard view.
func handleMobileDashboard(logger *slog.Logger, renderer *Renderer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := DefaultMobileMockData()
		pageData := PageData{
			Title: "Inicio",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/index.html", pageData); err != nil {
			logger.Error("rendering mobile dashboard", slog.Any("error", err))
		}
	}
}
