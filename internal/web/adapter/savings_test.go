package adapter_test

import (
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// TestIncomesWrittenThroughTheWebLayerCarryTheirSuggestion drives M7.1 the way
// the composition root wires it, over real SQLite: an income recorded or
// corrected through web.LedgerService -- the only way any handler, desktop or
// mobile, writes one -- commits together with its savings suggestion.
func TestIncomesWrittenThroughTheWebLayerCarryTheirSuggestion(t *testing.T) {
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
	opened, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account: web.CreateAccountInput{Name: "Efectivo", Type: web.AccountTypeCash, CurrencyCode: "USD"},
	})
	if err != nil {
		t.Fatalf("opening an account: %v", err)
	}

	today := database.FormatDate(database.Today())
	if err := svcs.Savings.SetPolicy(ctx, web.SetSavingsPolicyInput{Rate: "20", EffectiveFrom: today}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	income, err := svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID: opened.Account.ID, CategoryID: salary.ID, Amount: "800.00", PaymentMethodID: 1, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("recording an income: %v", err)
	}

	list, err := svcs.Savings.ListSuggestions(ctx)
	if err != nil {
		t.Fatalf("ListSuggestions: %v", err)
	}
	if len(list) != 1 || list[0].IncomeTransactionID != income.ID || list[0].SuggestedUSDFormatted != "160.00" ||
		!list[0].CanDismiss || list[0].CanValue || list[0].StatusLabel != "Pendiente de confirmar" {
		t.Fatalf("suggestions after recording an income through the web layer: %+v", list)
	}

	// Corrected into an expense through the same interface: the suggestion is
	// annulled in the same transaction, and kept in the income's history.
	if _, err := svcs.Ledger.UpdateTransaction(ctx, income.ID, web.UpdateTransactionInput{
		AccountID: opened.Account.ID, Type: web.TransactionTypeExpense, CategoryID: food.ID,
		Amount: "800.00", PaymentMethodID: 1, EffectiveDate: today,
	}); err != nil {
		t.Fatalf("correcting the income: %v", err)
	}
	if list, err := svcs.Savings.ListSuggestions(ctx); err != nil || len(list) != 0 {
		t.Errorf("live suggestions after the income became an expense: %+v (%v)", list, err)
	}
	detail, err := svcs.Savings.IncomeDetail(ctx, income.ID)
	if err != nil {
		t.Fatalf("IncomeDetail: %v", err)
	}
	if len(detail.Revisions) != 1 || detail.Revisions[0].StatusLabel != "Anulada" {
		t.Errorf("history = %+v", detail.Revisions)
	}

	overview, err := svcs.Savings.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if overview.SuggestedFormatted != "0.00" || overview.Policy.Current == nil || overview.Policy.Current.RateFormatted != "20.00" {
		t.Errorf("overview = %+v", overview)
	}

	// Errors cross the boundary as the web layer's sentinels.
	err = svcs.Savings.SetPolicy(ctx, web.SetSavingsPolicyInput{Rate: "25", EffectiveFrom: "2020-01-01"})
	if !errors.Is(err, web.ErrSavingsRetroactivePolicy) {
		t.Errorf("a retroactive second version returned %v, want ErrSavingsRetroactivePolicy", err)
	}
	if _, err := svcs.Savings.IncomeDetail(ctx, 9999); !errors.Is(err, web.ErrSavingsIncomeNotFound) {
		t.Errorf("an unknown income returned %v, want ErrSavingsIncomeNotFound", err)
	}
}
