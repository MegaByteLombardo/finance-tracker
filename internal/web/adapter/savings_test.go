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

// The fund through the web layer: a partial confirmation, a replayed form, a
// release and the overview's separate figures.
func TestTheFundThroughTheWebLayer(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svcs := newTestServices(t, dbtest.New(t))

	salary, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{Name: "Salario", Type: web.CategoryTypeIncome})
	if err != nil {
		t.Fatalf("category: %v", err)
	}
	opened, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account: web.CreateAccountInput{Name: "Efectivo", Type: web.AccountTypeCash, CurrencyCode: "USD"},
	})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	today := database.FormatDate(database.Today())
	if err := svcs.Savings.SetPolicy(ctx, web.SetSavingsPolicyInput{Rate: "20", EffectiveFrom: today}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	income, err := svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID: opened.Account.ID, CategoryID: salary.ID, Amount: "800.00", PaymentMethodID: 1, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("income: %v", err)
	}

	in := web.SavingsReserveInput{RequestKey: "adapter-key-000000000001", IncomeTransactionID: &income.ID,
		AccountID: opened.Account.ID, Amount: "100.00"}
	first, err := svcs.Savings.Reserve(ctx, in)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	again, err := svcs.Savings.Reserve(ctx, in)
	if err != nil || again.ID != first.ID {
		t.Fatalf("a replayed form reserved again: %v", err)
	}
	if first.KindLabel != "Reserva" || first.TotalFormatted != "100.00" {
		t.Errorf("operation = %+v", first)
	}

	list, err := svcs.Savings.ListSuggestions(ctx)
	if err != nil {
		t.Fatalf("ListSuggestions: %v", err)
	}
	s := list[0]
	if s.StatusLabel != "Confirmada en parte" || s.RemainingAmount != "60.00" || !s.CanConfirm {
		t.Errorf("suggestion after a partial confirmation: %+v", s)
	}

	_, err = svcs.Savings.Reserve(ctx, web.SavingsReserveInput{RequestKey: "adapter-key-000000000002",
		IncomeTransactionID: &income.ID, AccountID: opened.Account.ID, Amount: "61"})
	if !errors.Is(err, web.ErrSavingsExceedsSuggestion) {
		t.Errorf("confirming past the suggestion returned %v", err)
	}
	if _, err := svcs.Savings.Release(ctx, web.SavingsReleaseInput{RequestKey: "adapter-key-000000000003",
		AccountID: opened.Account.ID, Amount: "30", Reason: "Imprevisto"}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	fund, err := svcs.Savings.GetFund(ctx)
	if err != nil {
		t.Fatalf("GetFund: %v", err)
	}
	if fund.ReservedFormatted != "70.00" || fund.FreeFormatted != "730.00" || !fund.CanReserve {
		t.Errorf("fund = %+v", fund)
	}
	o, err := svcs.Savings.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if o.SuggestedFormatted != "160.00" || o.ConfirmedFormatted != "100.00" || o.ProgressFormatted != "62.50" ||
		o.GrossContributionsFormatted != "100.00" || o.WithdrawalsFormatted != "30.00" || o.NetChangeFormatted != "70.00" {
		t.Errorf("overview = %+v", o)
	}
	ops, err := svcs.Savings.ListOperations(ctx, 10)
	if err != nil || len(ops) != 2 {
		t.Errorf("operations = %+v (%v)", ops, err)
	}
}

// Goals through the web layer: a distributed confirmation, a withdrawal from a
// permanent goal, a completion keeping the money reserved.
func TestGoalsThroughTheWebLayer(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svcs := newTestServices(t, dbtest.New(t))

	salary, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{Name: "Salario", Type: web.CategoryTypeIncome})
	if err != nil {
		t.Fatalf("category: %v", err)
	}
	opened, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account: web.CreateAccountInput{Name: "Efectivo", Type: web.AccountTypeCash, CurrencyCode: "USD"},
	})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	today := database.FormatDate(database.Today())
	if err := svcs.Savings.SetPolicy(ctx, web.SetSavingsPolicyInput{Rate: "20", EffectiveFrom: today}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	emergency, err := svcs.Savings.CreateGoal(ctx, web.SavingsGoalInput{Name: "Emergencias", Type: "PERMANENT", Target: "1000", Distribution: "50"})
	if err != nil {
		t.Fatalf("goal: %v", err)
	}
	travel, err := svcs.Savings.CreateGoal(ctx, web.SavingsGoalInput{Name: "Viaje", Type: "ONE_TIME", Target: "80", Distribution: "50"})
	if err != nil {
		t.Fatalf("goal: %v", err)
	}
	income, err := svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID: opened.Account.ID, CategoryID: salary.ID, Amount: "800.00", PaymentMethodID: 1, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("income: %v", err)
	}

	lines, err := svcs.Savings.SuggestDistribution(ctx, "160.00")
	if err != nil {
		t.Fatalf("SuggestDistribution: %v", err)
	}
	if len(lines) != 3 || lines[0].Amount != "80.00" || lines[1].Amount != "80.00" || lines[2].Amount != "0.00" {
		t.Fatalf("distribution = %+v", lines)
	}
	in := web.SavingsReserveInput{RequestKey: "adapter-goals-0000000001", IncomeTransactionID: &income.ID, AccountID: opened.Account.ID}
	for _, l := range lines {
		in.Lines = append(in.Lines, web.SavingsReserveLine{GoalID: l.GoalID, Amount: l.Amount})
	}
	in.Lines = in.Lines[:2] // the empty unallocated line is dropped, as the form would
	if _, err := svcs.Savings.Reserve(ctx, in); err != nil {
		t.Fatalf("a distributed confirmation: %v", err)
	}

	g, err := svcs.Savings.GetGoal(ctx, travel)
	if err != nil {
		t.Fatalf("GetGoal: %v", err)
	}
	if g.ReservedFormatted != "80.00" || g.FundingLabel != "Financiada" || g.StatusLabel != "Activa" || g.ProgressWidth != 100 {
		t.Errorf("travel = %+v", g)
	}
	if _, err := svcs.Savings.Release(ctx, web.SavingsReleaseInput{RequestKey: "adapter-goals-0000000002",
		AccountID: opened.Account.ID, GoalID: &emergency, Amount: "30", Reason: "Imprevisto"}); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}
	if g, _ := svcs.Savings.GetGoal(ctx, emergency); g.ReservedFormatted != "50.00" || g.FundingLabel != "En progreso" {
		t.Errorf("emergency after a withdrawal = %+v", g)
	}
	if err := svcs.Savings.CompleteGoal(ctx, travel, web.SavingsCloseGoalInput{RequestKey: "adapter-goals-0000000003", Note: "Hecho"}); err != nil {
		t.Fatalf("CompleteGoal: %v", err)
	}
	fund, err := svcs.Savings.GetFund(ctx)
	if err != nil {
		t.Fatalf("GetFund: %v", err)
	}
	if fund.ReservedFormatted != "130.00" || fund.AllocatedFormatted != "50.00" || fund.UnallocatedFormatted != "80.00" {
		t.Errorf("fund = %+v", fund)
	}
	if err := svcs.Savings.CompleteGoal(ctx, emergency, web.SavingsCloseGoalInput{RequestKey: "adapter-goals-0000000004", Note: "x"}); !errors.Is(err, web.ErrSavingsInvalidState) {
		t.Errorf("completing a permanent goal returned %v", err)
	}
}
