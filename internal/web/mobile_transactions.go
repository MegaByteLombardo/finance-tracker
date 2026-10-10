package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
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

// TransactionsSummaryView is the month's income and expenses, as
// internal/analytics computed them: from the first of the month to today,
// INCOME and EXPENSE only, each at the rate of its own day, in the valuation
// base. The handler copies it; it adds nothing up.
type TransactionsSummaryView struct {
	Available bool

	IncomeFormatted  string
	ExpenseFormatted string
	CurrencyCode     string

	// Complete is false when some flow had no rate on its day. MissingMessage
	// then names the currencies left out of the totals.
	Complete       bool
	MissingMessage string

	// ErrorMessage replaces the figures when they could not be computed.
	ErrorMessage string
}

// TransactionDateGroup groups transactions by chronological date headings.
type TransactionDateGroup struct {
	DateHeading   string
	GroupSubtotal string
	Items         []MobileTransactionItem
}

// mapMobileTransactionItem turns a domain TransactionView into a presentation MobileTransactionItem.
func mapMobileTransactionItem(tx TransactionView) MobileTransactionItem {
	idStr := strconv.FormatInt(tx.ID, 10)
	dateLabel := tx.EffectiveDate.Format("02 Jan")

	if tx.Type == TransactionTypeTransfer && tx.Transfer != nil {
		merchant := "Transferencia interna"
		if tx.Description != nil && *tx.Description != "" {
			merchant = *tx.Description
		}
		accountName := tx.Transfer.SourceAccountName + " → " + tx.Transfer.DestinationAccountName
		return MobileTransactionItem{
			ID:              idStr,
			Merchant:        merchant,
			AccountName:     accountName,
			CategoryName:    "Transferencia",
			DateLabel:       dateLabel,
			AmountFormatted: tx.Transfer.AmountFormatted + " " + tx.CurrencyCode,
			Type:            "TRANSFER",
			CategoryIcon:    "transfer",
			IsPositive:      false,
			CurrencyCode:    tx.CurrencyCode,
		}
	}

	// A currency exchange carries TWO amounts in two currencies, so it fills both
	// amount fields. Neither one alone is "the amount": one is what was
	// delivered and the other what was received, and there is no conversion
	// between them that this application would be entitled to perform.
	if tx.Type == TransactionTypeFXExchange && tx.Exchange != nil {
		detail := tx.Exchange

		merchant := exchangeHeadline(detail)
		if tx.Description != nil && *tx.Description != "" {
			merchant = *tx.Description
		}

		return MobileTransactionItem{
			ID:              idStr,
			Merchant:        merchant,
			AccountName:     detail.SourceAccountName + " → " + detail.DestinationAccountName,
			CategoryName:    "Cambio de divisas",
			DateLabel:       dateLabel,
			AmountFormatted: "- " + formatAmountString(detail.SourceAmountFormatted) + " " + detail.SourceCurrencyCode,
			SecondaryAmountFormatted: "+ " + formatAmountString(detail.DestinationAmountFormatted) +
				" " + detail.DestinationCurrencyCode,
			RateLabel:    exchangeRateLabel(detail.ExecutionRate),
			Type:         "FX_EXCHANGE",
			CategoryIcon: "exchange",
			IsPositive:   false,
			CurrencyCode: detail.SourceCurrencyCode,
		}
	}

	if tx.Type == TransactionTypeIncome {
		merchant := "Ingreso"
		if tx.Description != nil && *tx.Description != "" {
			merchant = *tx.Description
		} else if tx.CategoryName != nil {
			merchant = *tx.CategoryName
		}
		catName := "Ingreso"
		if tx.CategoryName != nil {
			catName = *tx.CategoryName
		}
		catIcon, _ := inferCategoryIconAndEmoji(catName)
		amtStr := "+" + strings.TrimPrefix(formatAmountString(tx.AmountFormatted), "+") + " " + tx.CurrencyCode
		return MobileTransactionItem{
			ID:              idStr,
			Merchant:        merchant,
			AccountName:     tx.AccountName,
			CategoryName:    catName,
			DateLabel:       dateLabel,
			AmountFormatted: amtStr,
			Type:            "INCOME",
			CategoryIcon:    catIcon,
			IsPositive:      true,
			CurrencyCode:    tx.CurrencyCode,
		}
	}

	if tx.Type == TransactionTypeExpense {
		merchant := "Gasto"
		if tx.Description != nil && *tx.Description != "" {
			merchant = *tx.Description
		} else if tx.CategoryName != nil {
			merchant = *tx.CategoryName
		}
		catName := "Gasto"
		if tx.CategoryName != nil {
			catName = *tx.CategoryName
		}
		catIcon, _ := inferCategoryIconAndEmoji(catName)
		amtStr := "-" + strings.TrimPrefix(formatAmountString(tx.AmountFormatted), "-") + " " + tx.CurrencyCode
		return MobileTransactionItem{
			ID:              idStr,
			Merchant:        merchant,
			AccountName:     tx.AccountName,
			CategoryName:    catName,
			DateLabel:       dateLabel,
			AmountFormatted: amtStr,
			Type:            "EXPENSE",
			CategoryIcon:    catIcon,
			IsPositive:      false,
			CurrencyCode:    tx.CurrencyCode,
		}
	}

	// OPENING_BALANCE
	return MobileTransactionItem{
		ID:              idStr,
		Merchant:        "Saldo inicial",
		AccountName:     tx.AccountName,
		CategoryName:    "Apertura",
		DateLabel:       dateLabel,
		AmountFormatted: tx.AmountFormatted + " " + tx.CurrencyCode,
		Type:            "OPENING_BALANCE",
		CategoryIcon:    "briefcase",
		IsPositive:      true,
		CurrencyCode:    tx.CurrencyCode,
	}
}

// handleMobileTransactions serves the mobile transactions view populated with real database movements.
func handleMobileTransactions(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		txs, err := svcs.Ledger.ListTransactions(ctx, ListTransactionsInput{Limit: 100})
		if err != nil {
			logger.Error("listing transactions for mobile view", slog.Any("error", err))
			http.Error(w, "Failed to load transactions", http.StatusInternalServerError)
			return
		}

		now := time.Now().In(economicLocation)
		monthName := spanishMonths[now.Month()]
		if len(monthName) > 0 {
			monthName = strings.ToUpper(monthName[:1]) + monthName[1:]
		}
		currentPeriod := fmt.Sprintf("%s %d", monthName, now.Year())

		// Group by date heading
		var dateGroupHeadings []string
		itemsByHeading := make(map[string][]MobileTransactionItem)

		for _, tx := range txs {
			heading := formatDateHeadingSpanish(tx.EffectiveDate)
			if _, exists := itemsByHeading[heading]; !exists {
				dateGroupHeadings = append(dateGroupHeadings, heading)
			}
			mItem := mapMobileTransactionItem(tx)
			itemsByHeading[heading] = append(itemsByHeading[heading], mItem)
		}

		var dateGroups []TransactionDateGroup
		for _, h := range dateGroupHeadings {
			dateGroups = append(dateGroups, TransactionDateGroup{
				DateHeading: h,
				Items:       itemsByHeading[h],
			})
		}

		// The month's totals come from the analytics service, by the same rules
		// as the dashboard. They used to be summed here, over whichever hundred
		// rows the list happened to show -- future-dated ones included -- under
		// a heading that named the month. A failure is a message, not a 500:
		// the list below needs no exchange rate.
		summary := TransactionsSummaryView{}
		if cf, cerr := svcs.Analytics.MonthToDateCashFlow(ctx); cerr != nil {
			logger.Error("computing the month's cash flow", slog.Any("error", cerr))
			summary.ErrorMessage = "No se pudo calcular el flujo del mes."
		} else {
			summary = toTransactionsSummary(cf)
		}

		data := MobileTransactionsPageView{
			Header: MobileTransactionsHeaderView{
				TotalCount:         len(txs),
				CurrentPeriodLabel: currentPeriod,
			},
			Summary:    summary,
			DateGroups: dateGroups,
			Navigation: MobileNavView{
				ActiveTab: "movimientos",
			},
		}

		pageData := PageData{
			Title: "Movimientos",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/transactions.html", pageData); err != nil {
			logger.Error("rendering mobile transactions", slog.Any("error", err))
		}
	}
}

// DefaultMobileTransactionsMockData returns rich UI mock data for the Movimientos view.
func DefaultMobileTransactionsMockData() MobileTransactionsPageView {
	return MobileTransactionsPageView{
		Header: MobileTransactionsHeaderView{
			TotalCount:         9,
			CurrentPeriodLabel: "Septiembre 2026",
		},
		Summary: TransactionsSummaryView{
			Available: true, IncomeFormatted: "1170.00", ExpenseFormatted: "41.99",
			CurrencyCode: "USD", Complete: true,
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
						Merchant:        "Café & Panadería",
						AccountName:     "Efectivo USD",
						CategoryName:    "Comida & Café",
						DateLabel:       "09:15",
						AmountFormatted: "- $18.50",
						Type:            "EXPENSE",
						CategoryIcon:    "coffee",
						IsPositive:      false,
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

// toTransactionsSummary carries the month's cash flow into the summary card.
func toTransactionsSummary(cf CashFlow) TransactionsSummaryView {
	view := TransactionsSummaryView{
		Available:        true,
		IncomeFormatted:  cf.IncomeFormatted,
		ExpenseFormatted: cf.ExpenseFormatted,
		CurrencyCode:     cf.CurrencyCode,
		Complete:         cf.Complete,
	}
	if !cf.Complete {
		codes := make([]string, 0, len(cf.Missing))
		for _, m := range cf.Missing {
			codes = append(codes, m.CurrencyCode)
		}
		view.MissingMessage = "Sin tasa para valorar movimientos en " + strings.Join(codes, ", ") +
			"; no están incluidos en estos totales."
	}
	return view
}

// exchangeHeadline names an exchange in Spanish, the way the mobile interface
// speaks.
//
// It is PRESENTATION ONLY: the ledger stores one type, FX_EXCHANGE, and which
// currency was bought is simply which one was the destination. There is no
// BUY_USD and no SELL_USD anywhere below the interface.
func exchangeHeadline(d *ExchangeDetail) string {
	switch {
	case d.IsPurchaseOf("USD"):
		return "Compra de USD"
	case d.IsSaleOf("USD"):
		return "Venta de USD"
	default:
		return "Cambio " + d.SourceCurrencyCode + " → " + d.DestinationCurrencyCode
	}
}

// exchangeRateLabel renders the derived execution rate: "1 USD = 842.2067 VES",
// prefixed with "≈" when the rate has no finite decimal expansion.
//
// The orientation is the backend's and must not be reversed here: the inverse of
// a rate is a different number, not the same string with the codes swapped.
func exchangeRateLabel(rate ExecutionRate) string {
	if rate.RateFormatted == "" {
		return ""
	}
	approximately := ""
	if !rate.Exact {
		approximately = "≈ "
	}
	return "1 " + rate.BaseCurrencyCode + " = " + approximately +
		formatAmountString(rate.RateFormatted) + " " + rate.QuoteCurrencyCode
}
