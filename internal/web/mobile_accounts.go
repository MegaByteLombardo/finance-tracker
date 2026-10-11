package web

import (
	"log/slog"
	"net/http"
)

// MobileAccountsPageView holds all data needed to render the mobile accounts dashboard.
type MobileAccountsPageView struct {
	Header         MobileAccountsHeaderView
	CurrencyGroups []MobileCurrencyGroupView

	// Archived lists the archived accounts apart from the active ones, each
	// with a restore action. They are shown rather than hidden because an
	// archived account can still hold money that counts in the net worth
	// (TD-064), and archiving is only undoable from where it can be seen.
	Archived []MobileAccountItemView

	Navigation MobileNavView
}

// MobileAccountsHeaderView holds metadata for the accounts top bar.
type MobileAccountsHeaderView struct {
	TotalAccounts int
}

// MobileCurrencyGroupView groups accounts belonging to the same currency.
type MobileCurrencyGroupView struct {
	CurrencyCode   string
	CurrencyName   string
	TotalFormatted string
	Accounts       []MobileAccountItemView
}

// MobileAccountItemView represents a single account card in the accounts list.
type MobileAccountItemView struct {
	AccountID        int64
	AccountName      string
	AccountType      string // "BANK", "CASH", "WALLET"
	AccountTypeLabel string // "Cuenta Corriente", "Billetera Digital", "Efectivo"
	Institution      string
	CurrencyCode     string
	BalanceFormatted string
	IsNegative       bool
	IsActive         bool

	// FutureMovementCount is how many of the account's movements are dated
	// after today and are therefore not in BalanceFormatted yet (TD-063).
	FutureMovementCount int

	// HasBalance is true when the balance is not zero, so an archived card can
	// say that its money still counts.
	HasBalance bool
}

// handleMobileAccounts serves the mobile accounts view populated with real data.
func handleMobileAccounts(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Archived accounts included: they are listed in their own section.
		accounts, err := svcs.Accounts.List(ctx, true)
		if err != nil {
			logger.Error("listing accounts for mobile view", slog.Any("error", err))
			http.Error(w, "Failed to load accounts", http.StatusInternalServerError)
			return
		}

		balances, err := svcs.Ledger.ListAccountBalances(ctx, true)
		if err != nil {
			logger.Error("listing balances for mobile accounts view", slog.Any("error", err))
			http.Error(w, "Failed to load balances", http.StatusInternalServerError)
			return
		}

		totals, err := svcs.Ledger.ListCurrencyTotals(ctx, false)
		if err != nil {
			logger.Error("listing currency totals for mobile accounts view", slog.Any("error", err))
			http.Error(w, "Failed to load totals", http.StatusInternalServerError)
			return
		}

		balMap := make(map[int64]AccountBalance, len(balances))
		for _, b := range balances {
			balMap[b.AccountID] = b
		}

		totalsMap := make(map[string]CurrencyTotal, len(totals))
		for _, t := range totals {
			totalsMap[t.CurrencyCode] = t
		}

		// Group accounts by currency
		var currencyOrder []string
		acctsByCurr := make(map[string][]MobileAccountItemView)

		var archived []MobileAccountItemView
		activeCount := 0

		for _, acct := range accounts {

			b, hasBal := balMap[acct.ID]
			balFormatted := "0.00"
			isNeg := false
			future := 0
			if hasBal {
				balFormatted = b.BalanceFormatted
				isNeg = b.BalanceMinor < 0
				future = b.FutureMovementCount
			}

			var inst string
			if acct.Institution != nil {
				inst = *acct.Institution
			}

			item := MobileAccountItemView{
				AccountID:        acct.ID,
				AccountName:      acct.Name,
				AccountType:      string(acct.Type),
				AccountTypeLabel: accountTypeFriendlyLabel(acct),
				Institution:      inst,
				CurrencyCode:     acct.CurrencyCode,
				BalanceFormatted: balFormatted,
				IsNegative:       isNeg,
				IsActive:         acct.IsActive,

				FutureMovementCount: future,
				HasBalance:          hasBal && b.BalanceMinor != 0,
			}

			if !acct.IsActive {
				archived = append(archived, item)
				continue
			}
			activeCount++
			if _, exists := acctsByCurr[acct.CurrencyCode]; !exists {
				currencyOrder = append(currencyOrder, acct.CurrencyCode)
			}
			acctsByCurr[acct.CurrencyCode] = append(acctsByCurr[acct.CurrencyCode], item)
		}

		var groups []MobileCurrencyGroupView
		for _, currCode := range currencyOrder {
			currAccounts := acctsByCurr[currCode]
			currName := currCode
			totFormatted := "0.00"
			if t, ok := totalsMap[currCode]; ok {
				currName = t.CurrencyName
				totFormatted = t.TotalFormatted
			}

			groups = append(groups, MobileCurrencyGroupView{
				CurrencyCode:   currCode,
				CurrencyName:   currName,
				TotalFormatted: totFormatted,
				Accounts:       currAccounts,
			})
		}

		data := MobileAccountsPageView{
			Header: MobileAccountsHeaderView{
				TotalAccounts: activeCount,
			},
			CurrencyGroups: groups,
			Archived:       archived,
			Navigation: MobileNavView{
				ActiveTab: "cuentas",
			},
		}

		pageData := PageData{
			Title: "Cuentas",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/accounts.html", pageData); err != nil {
			logger.Error("rendering mobile accounts", slog.Any("error", err))
		}
	}
}

// DefaultMobileAccountsMockData returns rich UI mock data for the Cuentas view.
func DefaultMobileAccountsMockData() MobileAccountsPageView {
	return MobileAccountsPageView{
		Header: MobileAccountsHeaderView{
			TotalAccounts: 6,
		},
		CurrencyGroups: []MobileCurrencyGroupView{
			{
				CurrencyCode:   "USD",
				CurrencyName:   "Dólares",
				TotalFormatted: "$2,057.56",
				Accounts: []MobileAccountItemView{
					{
						AccountID:        1,
						AccountName:      "Mercantil USD",
						AccountType:      "BANK",
						AccountTypeLabel: "Cuenta Corriente",
						Institution:      "Banco Mercantil",
						CurrencyCode:     "USD",
						BalanceFormatted: "$1,650.00",
						IsNegative:       false,
						IsActive:         true,
					},
					{
						AccountID:        2,
						AccountName:      "Tarjeta prepagada",
						AccountType:      "WALLET",
						AccountTypeLabel: "Billetera digital",
						Institution:      "Wally Tech",
						CurrencyCode:     "USD",
						BalanceFormatted: "$350.00",
						IsNegative:       false,
						IsActive:         true,
					},
					{
						AccountID:        3,
						AccountName:      "Efectivo en mano",
						AccountType:      "CASH",
						AccountTypeLabel: "Efectivo",
						Institution:      "",
						CurrencyCode:     "USD",
						BalanceFormatted: "$57.56",
						IsNegative:       false,
						IsActive:         true,
					},
				},
			},
			{
				CurrencyCode:   "VES",
				CurrencyName:   "Bolívares",
				TotalFormatted: "Bs 150,548.84",
				Accounts: []MobileAccountItemView{
					{
						AccountID:        4,
						AccountName:      "Banesco Corriente",
						AccountType:      "BANK",
						AccountTypeLabel: "Cuenta Corriente",
						Institution:      "Banesco Banco Universal",
						CurrencyCode:     "VES",
						BalanceFormatted: "Bs 142,548.84",
						IsNegative:       false,
						IsActive:         true,
					},
					{
						AccountID:        5,
						AccountName:      "Efectivo Bolívares",
						AccountType:      "CASH",
						AccountTypeLabel: "Efectivo",
						Institution:      "",
						CurrencyCode:     "VES",
						BalanceFormatted: "Bs 8,000.00",
						IsNegative:       false,
						IsActive:         true,
					},
				},
			},
			{
				CurrencyCode:   "EUR",
				CurrencyName:   "Euros",
				TotalFormatted: "€850.00",
				Accounts: []MobileAccountItemView{
					{
						AccountID:        6,
						AccountName:      "Cuenta Ahorro Europa",
						AccountType:      "BANK",
						AccountTypeLabel: "Cuenta de Ahorro",
						Institution:      "N26 Bank",
						CurrencyCode:     "EUR",
						BalanceFormatted: "€850.00",
						IsNegative:       false,
						IsActive:         true,
					},
				},
			},
		},
		Navigation: MobileNavView{
			ActiveTab: "cuentas",
		},
	}
}
