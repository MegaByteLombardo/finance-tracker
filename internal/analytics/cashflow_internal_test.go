package analytics

import (
	"math/big"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// A refund is negative spending in its category (TD-090): the ledger reports it
// netted into that day's EXPENSE flow. A day that refunded more than it spent
// has a positive expense flow, which lowers the period's expenses rather than
// being dropped or counted as income.
func TestARefundLowersTheExpensesOfItsCategory(t *testing.T) {
	t.Parallel()

	day := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	period := Period{From: day.AddDate(0, 0, -1), To: day}
	identity := func(amountMinor int64, _ string, _ time.Time) (*big.Rat, bool, error) {
		return new(big.Rat).SetInt64(amountMinor), true, nil
	}
	flows := []ledger.Flow{
		{Date: day.AddDate(0, 0, -1), Type: ledger.Expense, CategoryID: 1, CategoryName: "Comida",
			CurrencyCode: "USD", MinorUnit: 2, AmountMinor: -5000, Count: 1},
		{Date: day.AddDate(0, 0, -1), Type: ledger.Expense, CategoryID: 2, CategoryName: "Ropa",
			CurrencyCode: "USD", MinorUnit: 2, AmountMinor: -10000, Count: 1},
		// A 30.00 refund of clothes on a day nothing else was spent on clothes.
		{Date: day, Type: ledger.Expense, CategoryID: 2, CategoryName: "Ropa",
			CurrencyCode: "USD", MinorUnit: 2, AmountMinor: 3000, Count: 1},
	}

	flow, err := buildCashFlow(period, flows, identity, "USD", 2)
	if err != nil {
		t.Fatalf("buildCashFlow: %v", err)
	}
	if flow.ExpenseMinor != 12000 || flow.IncomeMinor != 0 {
		t.Errorf("expenses %d, income %d; want 12000 and 0", flow.ExpenseMinor, flow.IncomeMinor)
	}
	for _, line := range flow.ExpenseCategories {
		if line.CategoryName == "Ropa" && line.AmountMinor != 7000 {
			t.Errorf("clothes = %d, want 7000 net of the refund", line.AmountMinor)
		}
	}
}
