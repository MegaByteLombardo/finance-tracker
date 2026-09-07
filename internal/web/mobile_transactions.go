package web

import (
	"log/slog"
	"net/http"
)

// MobileTransactionsPageView holds all data needed to render the mobile transactions feed.
type MobileTransactionsPageView struct {
	Header     MobileTransactionsHeaderView
	Summary    TransactionsSummaryView
	DateGroups []TransactionDateGroup
	Navigation MobileNavView
}

// MobileTransactionsHeaderView holds metadata for the transactions top bar.
type MobileTransactionsHeaderView struct {
	TotalCount         int
	CurrentPeriodLabel string
}

// TransactionsSummaryView holds the income/expense totals for the active period.
type TransactionsSummaryView struct {
	TotalIncomeFormatted  string
	TotalExpenseFormatted string
}

// TransactionDateGroup groups transactions by chronological date headings.
type TransactionDateGroup struct {
	DateHeading   string
	GroupSubtotal string
	Items         []MobileTransactionItem
}

// DefaultMobileTransactionsMockData returns rich UI mock data for the Movimientos view.
func DefaultMobileTransactionsMockData() MobileTransactionsPageView {
	return MobileTransactionsPageView{
		Header: MobileTransactionsHeaderView{
			TotalCount:         9,
			CurrentPeriodLabel: "Septiembre 2026",
		},
		Summary: TransactionsSummaryView{
			TotalIncomeFormatted:  "+ $1,170.00",
			TotalExpenseFormatted: "- $41.99 · - Bs 1,940.00",
		},
		DateGroups: []TransactionDateGroup{
			{
				DateHeading:   "Hoy, 6 de septiembre",
				GroupSubtotal: "- Bs 1,240.00 · - $18.50",
				Items: []MobileTransactionItem{
					{
						ID:              "tx-m1",
						Merchant:        "Supermercado Automercados Plaza's",
						AccountName:     "Banesco",
						CategoryName:    "Alimentación",
						DateLabel:       "14:32",
						AmountFormatted: "- Bs 1,240.00",
						Type:            "EXPENSE",
						CategoryIcon:    "cart",
						IsPositive:      false,
						CurrencyCode:    "VES",
					},
					{
						ID:              "tx-m2",
						Merchant:        "Almuerzo Ejecutivo",
						AccountName:     "Mercantil",
						CategoryName:    "Comida",
						DateLabel:       "13:10",
						AmountFormatted: "- $18.50",
						Type:            "EXPENSE",
						CategoryIcon:    "coffee",
						IsPositive:      false,
						CurrencyCode:    "USD",
					},
				},
			},
			{
				DateHeading:   "Ayer, 5 de septiembre",
				GroupSubtotal: "+ $850.00 · - Bs 420.00 · - $4.50",
				Items: []MobileTransactionItem{
					{
						ID:              "tx-m3",
						Merchant:        "Nómina Quincenal",
						AccountName:     "Mercantil",
						CategoryName:    "Ingresos",
						DateLabel:       "09:15",
						AmountFormatted: "+ $850.00",
						Type:            "INCOME",
						CategoryIcon:    "briefcase",
						IsPositive:      true,
						CurrencyCode:    "USD",
					},
					{
						ID:              "tx-m4",
						Merchant:        "Cafetería Artesanal",
						AccountName:     "Efectivo USD",
						CategoryName:    "Comida",
						DateLabel:       "16:45",
						AmountFormatted: "- $4.50",
						Type:            "EXPENSE",
						CategoryIcon:    "coffee",
						IsPositive:      false,
						CurrencyCode:    "USD",
					},
					{
						ID:              "tx-m5",
						Merchant:        "Farmacia San Rafael",
						AccountName:     "Banesco",
						CategoryName:    "Salud",
						DateLabel:       "18:20",
						AmountFormatted: "- Bs 420.00",
						Type:            "EXPENSE",
						CategoryIcon:    "pill",
						IsPositive:      false,
						CurrencyCode:    "VES",
					},
				},
			},
			{
				DateHeading:   "Esta semana",
				GroupSubtotal: "3 operaciones",
				Items: []MobileTransactionItem{
					{
						ID:              "tx-m6",
						Merchant:        "Gasolina Estación Central",
						AccountName:     "Efectivo VES",
						CategoryName:    "Transporte",
						DateLabel:       "3 Sep",
						AmountFormatted: "- Bs 280.00",
						Type:            "EXPENSE",
						CategoryIcon:    "car",
						IsPositive:      false,
						CurrencyCode:    "VES",
					},
					{
						ID:              "tx-m7",
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
					{
						ID:              "tx-m8",
						Merchant:        "Transferencia a Banesco",
						AccountName:     "Mercantil ➔ Banesco",
						CategoryName:    "Transferencia propia",
						DateLabel:       "1 Sep",
						AmountFormatted: "Bs 5,000.00",
						Type:            "TRANSFER",
						CategoryIcon:    "briefcase",
						IsPositive:      false,
						CurrencyCode:    "VES",
					},
				},
			},
			{
				DateHeading:   "Semana anterior",
				GroupSubtotal: "+ $320.00 · - $4.00",
				Items: []MobileTransactionItem{
					{
						ID:              "tx-m9",
						Merchant:        "Honorarios Freelance",
						AccountName:     "Mercantil",
						CategoryName:    "Ingresos",
						DateLabel:       "28 Ago",
						AmountFormatted: "+ $320.00",
						Type:            "INCOME",
						CategoryIcon:    "briefcase",
						IsPositive:      true,
						CurrencyCode:    "USD",
					},
				},
			},
		},
		Navigation: MobileNavView{
			ActiveTab: "movimientos",
		},
	}
}

// handleMobileTransactions serves the mobile transactions view.
func handleMobileTransactions(logger *slog.Logger, renderer *Renderer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := DefaultMobileTransactionsMockData()
		pageData := PageData{
			Title: "Movimientos",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/transactions.html", pageData); err != nil {
			logger.Error("rendering mobile transactions", slog.Any("error", err))
		}
	}
}
