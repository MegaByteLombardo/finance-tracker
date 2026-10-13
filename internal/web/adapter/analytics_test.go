package adapter_test

import (
	"errors"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// TestTheDashboardEndToEndOnARealDatabase drives the whole M6 read path the way
// the composition root wires it: the analytics adapter, the analytics service,
// the valuation snapshot, the ledger and fx, over real SQLite.
func TestTheDashboardEndToEndOnARealDatabase(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svcs := newTestServices(t, dbtest.New(t))

	salary, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{Name: "Salario", Type: web.CategoryTypeIncome})
	if err != nil {
		t.Fatalf("creating a category: %v", err)
	}
	food, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{Name: "Comida", Type: web.CategoryTypeExpense})
	if err != nil {
		t.Fatalf("creating a category: %v", err)
	}

	today := database.Today()
	opening := "1000.00"
	opened, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account:            web.CreateAccountInput{Name: "Efectivo", Type: web.AccountTypeCash, CurrencyCode: "USD"},
		OpeningBalance:     &opening,
		OpeningBalanceDate: database.FormatDate(today.AddDate(0, 0, -40)),
	})
	if err != nil {
		t.Fatalf("opening an account: %v", err)
	}
	accountID := opened.Account.ID

	if _, err := svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID: accountID, CategoryID: salary.ID, Amount: "500.00",
		PaymentMethodID: 3, EffectiveDate: database.FormatDate(today.AddDate(0, 0, -2)),
	}); err != nil {
		t.Fatalf("recording income: %v", err)
	}
	if _, err := svcs.Ledger.RecordExpense(ctx, web.RecordExpenseInput{
		AccountID: accountID, CategoryID: food.ID, Amount: "125.00",
		PaymentMethodID: 3, EffectiveDate: database.FormatDate(today.AddDate(0, 0, -1)),
	}); err != nil {
		t.Fatalf("recording an expense: %v", err)
	}
	// Dated tomorrow: in the ledger, and in no figure yet.
	if _, err := svcs.Ledger.RecordExpense(ctx, web.RecordExpenseInput{
		AccountID: accountID, CategoryID: food.ID, Amount: "999.00",
		PaymentMethodID: 3, EffectiveDate: database.FormatDate(today.AddDate(0, 0, 1)),
	}); err != nil {
		t.Fatalf("recording a future expense: %v", err)
	}

	d, err := svcs.Analytics.Dashboard(ctx, web.DashboardPreset30D)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}

	if d.NetWorth.TotalMinor != 137500 || d.NetWorth.FutureMovementCount != 1 {
		t.Errorf("net worth = %d with %d future movements, want 137500 and 1",
			d.NetWorth.TotalMinor, d.NetWorth.FutureMovementCount)
	}
	if len(d.Points) != 30 || d.Points[29].TotalMinor != d.NetWorth.TotalMinor {
		t.Errorf("%d points, the last at %d; want 30 ending at the net worth", len(d.Points), d.Points[len(d.Points)-1].TotalMinor)
	}
	if !d.Change.Defined || d.Change.AmountFormatted != "375.00" || d.Change.Percent.Formatted != "37.50" {
		t.Errorf("change = %+v, want +375.00 and +37.50%%", d.Change)
	}
	if d.CashFlow.IncomeFormatted != "500.00" || d.CashFlow.ExpenseFormatted != "125.00" ||
		d.CashFlow.SavingsRate.Formatted != "75.00" {
		t.Errorf("cash flow = %+v", d.CashFlow)
	}

	// The account screen agrees with the dashboard about today.
	balance, err := svcs.Ledger.GetAccountBalance(ctx, accountID)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != d.NetWorth.TotalMinor || balance.FutureMovementCount != 1 {
		t.Errorf("the account says %d (%d future), the dashboard %d",
			balance.BalanceMinor, balance.FutureMovementCount, d.NetWorth.TotalMinor)
	}

	if _, err := svcs.Analytics.Dashboard(ctx, web.DashboardPreset("FOREVER")); !errors.Is(err, web.ErrInvalidInput) {
		t.Errorf("an unknown preset returned %v, want ErrInvalidInput", err)
	}
}

// TestTheRecapEndToEndOnARealDatabase drives the M6.5 read path as the
// composition root wires it, on the real clock: the default recap is last
// month, against the month before.
func TestTheRecapEndToEndOnARealDatabase(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svcs := newTestServices(t, dbtest.New(t))

	food, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{Name: "Comida", Type: web.CategoryTypeExpense})
	if err != nil {
		t.Fatalf("creating a category: %v", err)
	}

	today := database.Today()
	thisMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonth := thisMonth.AddDate(0, -1, 0)

	opening := "1000.00"
	opened, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account:            web.CreateAccountInput{Name: "Efectivo", Type: web.AccountTypeCash, CurrencyCode: "USD"},
		OpeningBalance:     &opening,
		OpeningBalanceDate: database.FormatDate(lastMonth.AddDate(0, -1, 0)),
	})
	if err != nil {
		t.Fatalf("opening an account: %v", err)
	}
	if _, err := svcs.Ledger.RecordExpense(ctx, web.RecordExpenseInput{
		AccountID: opened.Account.ID, CategoryID: food.ID, Amount: "125.00",
		PaymentMethodID: 3, EffectiveDate: database.FormatDate(lastMonth.AddDate(0, 0, 2)),
	}); err != nil {
		t.Fatalf("recording an expense: %v", err)
	}

	r, err := svcs.Analytics.Recap(ctx, web.RecapRequest{Granularity: web.RecapMonth})
	if err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if !r.PeriodFrom.Equal(lastMonth) || r.InProgress || !r.HasPrevious {
		t.Errorf("recap of %s (in progress %v, previous %v), want last month, closed, with a previous one",
			database.FormatDate(r.PeriodFrom), r.InProgress, r.HasPrevious)
	}
	if r.Closing.TotalFormatted != "875.00" || r.Change.AmountFormatted != "-125.00" {
		t.Errorf("net worth %s, change %s; want 875.00 and -125.00", r.Closing.TotalFormatted, r.Change.AmountFormatted)
	}
	if len(r.ExpenseCategories) != 1 || r.ExpenseCategories[0].Direction != web.DirectionNew {
		t.Errorf("expense categories = %+v, want Comida as new", r.ExpenseCategories)
	}
	if len(r.TopExpenses) != 1 || r.TopExpenses[0].BaseFormatted != "125.00" || r.TopExpenses[0].TransactionID == 0 {
		t.Errorf("top expenses = %+v", r.TopExpenses)
	}
	if len(r.Indicators) != 4 || r.Summary.Total != 4 {
		t.Errorf("indicators = %+v", r.Indicators)
	}

	if _, err := svcs.Analytics.Recap(ctx, web.RecapRequest{Granularity: "YEAR"}); !errors.Is(err, web.ErrInvalidInput) {
		t.Errorf("an unknown granularity returned %v, want ErrInvalidInput", err)
	}
}
