package savings_test

import (
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// --- helpers -----------------------------------------------------------------

func (e *env) expenseInput(accountID int64, amount string) ledger.RecordExpenseInput {
	return ledger.RecordExpenseInput{AccountID: accountID, CategoryID: e.food, Amount: amount,
		PaymentMethodID: bankTransfer, EffectiveDate: "2026-10-10"}
}

func (e *env) spendFrom(goalID *int64, amount string) savings.SavingsUse {
	return savings.SavingsUse{GoalID: goalID, Amount: amount}
}

func (e *env) issues() []savings.Issue {
	e.t.Helper()
	list, err := e.svc.ListIssues(e.t.Context())
	if err != nil {
		e.t.Fatalf("ListIssues: %v", err)
	}
	return list
}

func issueOf(list []savings.Issue, kind savings.IssueKind) *savings.Issue {
	for i := range list {
		if list[i].Kind == kind {
			return &list[i]
		}
	}
	return nil
}

// --- Expense within the free balance --------------------------------------------

// Balance $1,000, $800 reserved, expense $150: no warning, no goal touched.
func TestAnExpenseWithinTheFreeBalanceTouchesNoSavings(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	e.reserve(e.usd, "800")

	impact, err := e.svc.AccountImpact(t.Context(), e.usd, "150")
	if err != nil {
		t.Fatalf("AccountImpact: %v", err)
	}
	if !impact.Applies || impact.ShortfallMinor != 0 || impact.FreeMinor != 20000 {
		t.Errorf("impact = %+v", impact)
	}
	if _, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "150"), savings.ExpensePlan{}); err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	if a := e.availability(e.usd); a.ReservedMinor != 80000 || a.FreeMinor != 5000 {
		t.Errorf("availability = %+v", a)
	}
	if n := e.count("savings_operations"); n != 1 || len(e.issues()) != 0 {
		t.Errorf("%d operations, issues %+v", n, e.issues())
	}
	if impact, _ := e.svc.AccountImpact(t.Context(), e.ves, "150"); impact.Applies {
		t.Error("a VES account reported a savings impact")
	}
}

// --- Expense using savings -------------------------------------------------------

// Expense $300 with $200 free: $100 come from reserves, released with a reason
// and linked to the expense, in the same transaction.
func TestAnExpenseBeyondTheFreeBalanceReleasesReservesWithAReason(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	e.reserve(e.usd, "800")

	impact, err := e.svc.AccountImpact(t.Context(), e.usd, "300")
	if err != nil {
		t.Fatalf("AccountImpact: %v", err)
	}
	if impact.ShortfallMinor != 10000 || len(impact.Slices) != 1 || impact.Slices[0].GoalID != nil {
		t.Fatalf("impact = %+v", impact)
	}

	_, err = e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "300"),
		savings.ExpensePlan{RequestKey: key(), Release: []savings.SavingsUse{e.spendFrom(nil, "100")}})
	if !errors.Is(err, savings.ErrInvalidInput) {
		t.Fatalf("a release without a reason returned %v", err)
	}
	if e.count("transactions") != 1 {
		t.Fatal("the expense was recorded although its savings plan failed")
	}

	view, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "300"),
		savings.ExpensePlan{RequestKey: key(), Release: []savings.SavingsUse{e.spendFrom(nil, "100")}, Reason: "Reparación urgente"})
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	ops, err := e.svc.ListOperations(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	op := ops[0]
	if op.Kind != savings.KindRelease || op.TransactionID == nil || *op.TransactionID != view.ID ||
		*op.Reason != "Reparación urgente" || op.TotalMinor != -10000 {
		t.Errorf("release = %+v", op)
	}
	if a := e.availability(e.usd); a.BalanceMinor != 70000 || a.ReservedMinor != 70000 || a.OverallocatedMinor != 0 {
		t.Errorf("availability = %+v", a)
	}
	if len(e.issues()) != 0 {
		t.Errorf("issues after a resolved expense: %+v", e.issues())
	}
}

// Recorded without resolving the reserves, the expense is kept and the
// overallocation is an explicit issue. Acknowledging it changes no balance and
// keeps the deficit visible; a larger deficit is a new issue.
func TestAnUnresolvedExpenseIsKeptAndReportedUntilResolved(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	e.reserve(e.usd, "800")
	if _, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "300"), savings.ExpensePlan{}); err != nil {
		t.Fatalf("the expense was refused: %v", err)
	}

	issue := issueOf(e.issues(), savings.IssueOverallocated)
	if issue == nil || issue.AmountMinor != 10000 || issue.Acknowledged != nil {
		t.Fatalf("issue = %+v", issue)
	}
	if err := e.svc.AcknowledgeIssue(t.Context(), issue.Key, " "); !errors.Is(err, savings.ErrInvalidInput) {
		t.Errorf("an acknowledgement without a note returned %v", err)
	}
	if err := e.svc.AcknowledgeIssue(t.Context(), issue.Key, "Lo repongo la próxima quincena"); err != nil {
		t.Fatalf("AcknowledgeIssue: %v", err)
	}
	issue = issueOf(e.issues(), savings.IssueOverallocated)
	if issue == nil || issue.Acknowledged == nil {
		t.Fatal("an acknowledged issue disappeared")
	}
	if f := e.fund(); f.OverallocatedMinor != 10000 || f.ReservedMinor != 80000 {
		t.Errorf("acknowledging changed the fund: %+v", f)
	}
	o, err := e.svc.Overview(t.Context())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if o.IssueCount != 0 || o.IssueTotal != 1 || o.Fund.OverallocatedMinor != 10000 {
		t.Errorf("overview: %d open of %d, overallocated %d", o.IssueCount, o.IssueTotal, o.Fund.OverallocatedMinor)
	}

	if _, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "50"), savings.ExpensePlan{}); err != nil {
		t.Fatalf("second expense: %v", err)
	}
	if issue := issueOf(e.issues(), savings.IssueOverallocated); issue == nil || issue.Acknowledged != nil || issue.AmountMinor != 15000 {
		t.Errorf("a larger deficit kept the old acknowledgement: %+v", issue)
	}
	if err := e.svc.AcknowledgeIssue(t.Context(), "overallocated:account:1:1", "x"); !errors.Is(err, savings.ErrNotFound) {
		t.Errorf("acknowledging an issue that is not current returned %v", err)
	}
}

// --- Goal completion with a real expense -------------------------------------------

// Travel $800 reserved, expense $800 linked: exactly one expense, the goal
// drops to zero, the free balance is unchanged, and it can be completed.
func TestAGoalPaysItsExpenseAndIsCompleted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "")
	e.reserveFor(e.usd, travel, "800")
	freeBefore := e.availability(e.usd).FreeMinor

	view, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "800"),
		savings.ExpensePlan{RequestKey: key(), Spend: []savings.SavingsUse{e.spendFrom(&travel, "800")}})
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	if e.count("transactions") != 2 || e.count("account_movements") != 2 {
		t.Error("spending a goal created a second expense or movement")
	}
	a := e.availability(e.usd)
	if a.BalanceMinor != 20000 || a.FreeMinor != freeBefore || a.ReservedMinor != 0 {
		t.Errorf("availability = %+v (free was %d)", a, freeBefore)
	}
	if err := e.svc.CompleteGoal(t.Context(), travel, savings.CloseGoalInput{RequestKey: key(), Note: "Viaje hecho"}); err != nil {
		t.Fatalf("CompleteGoal: %v", err)
	}
	g := e.getGoal(travel)
	if g.Status != savings.GoalCompleted || len(g.LinkedExpenses) != 1 || g.LinkedExpenses[0].TransactionID != view.ID ||
		g.ActualCostMinor != 80000 || g.DeviationMinor == nil || *g.DeviationMinor != 0 {
		t.Errorf("completed goal = %+v", g)
	}
	if len(g.Operations) != 2 {
		t.Errorf("the goal kept %d operations, want its contribution and its spend", len(g.Operations))
	}
}

// Travel planned at $800 costs $1,000: the goal pays $800, the rest comes from
// free money, and the deviation is shown against the original budget, which
// stays untouched.
func TestAGoalThatCostMoreKeepsItsBudgetAndShowsTheDeviation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "2000")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "")
	e.reserveFor(e.usd, travel, "800")
	if _, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "1000"),
		savings.ExpensePlan{RequestKey: key(), Spend: []savings.SavingsUse{e.spendFrom(&travel, "800")}}); err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	g := e.getGoal(travel)
	if g.TargetUSDMinor != 80000 || g.OriginalTargetUSDMinor != 80000 || g.SpentMinor != 80000 ||
		g.ActualCostMinor != 100000 || *g.DeviationMinor != 20000 {
		t.Errorf("goal = target %d, original %d, spent %d, cost %d, deviation %v",
			g.TargetUSDMinor, g.OriginalTargetUSDMinor, g.SpentMinor, g.ActualCostMinor, g.DeviationMinor)
	}
}

// Expense $1,400 from Emergency $1,000 and Travel $400, atomically. Asking a
// goal for more than it holds records nothing at all.
func TestAnExpenseCanUseSeveralGoalsAtomically(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "2000")
	emergency := e.goal("Emergencias", savings.GoalPermanent, "1000", "", "")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "")
	e.reserveFor(e.usd, emergency, "1000")
	e.reserveFor(e.usd, travel, "400")

	_, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "1400"), savings.ExpensePlan{RequestKey: key(),
		Spend: []savings.SavingsUse{e.spendFrom(&emergency, "1000"), e.spendFrom(&travel, "400.01")}})
	if !errors.Is(err, savings.ErrInsufficientReserved) {
		t.Fatalf("over-using a goal returned %v", err)
	}
	if e.count("transactions") != 1 {
		t.Fatal("a failed plan left its expense behind")
	}

	k := key()
	plan := savings.ExpensePlan{RequestKey: k,
		Spend: []savings.SavingsUse{e.spendFrom(&emergency, "1000"), e.spendFrom(&travel, "400")}}
	first, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "1400"), plan)
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	again, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "1400"), plan)
	if err != nil || again.ID != first.ID || e.count("transactions") != 2 {
		t.Errorf("a replayed expense was recorded twice (%v)", err)
	}
	if e.getGoal(emergency).ReservedMinor != 0 || e.getGoal(travel).ReservedMinor != 0 {
		t.Error("the goals still hold what the expense used")
	}
	if _, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "10"), savings.ExpensePlan{RequestKey: key(),
		Spend: []savings.SavingsUse{e.spendFrom(nil, "11")}}); err == nil {
		t.Error("savings above the expense were accepted")
	}
}

// A VES expense never consumes USD savings directly.
func TestAVESExpenseCannotSpendUSDSavings(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "100")
	e.open(e.ves, "10000")
	e.reserve(e.usd, "100")
	_, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.ves, "4000"), savings.ExpensePlan{RequestKey: key(),
		Spend: []savings.SavingsUse{e.spendFrom(nil, "10")}})
	if !errors.Is(err, savings.ErrNotLinkable) {
		t.Errorf("a VES expense spending USD savings returned %v", err)
	}
	if e.count("transactions") != 2 {
		t.Error("the VES expense was recorded although its plan was refused")
	}
}

// A linked expense corrected afterwards is reported, never adjusted.
func TestACorrectedLinkedExpenseIsReported(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "")
	e.reserveFor(e.usd, travel, "800")
	view, err := e.app.RecordExpense(t.Context(), e.expenseInput(e.usd, "800"),
		savings.ExpensePlan{RequestKey: key(), Spend: []savings.SavingsUse{e.spendFrom(&travel, "800")}})
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	e.edit(view.ID, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Expense, CategoryID: e.food,
		Amount: "600", EffectiveDate: "2026-10-10"})
	issue := issueOf(e.issues(), savings.IssueSpendMismatch)
	if issue == nil || issue.AmountMinor != 80000 || *issue.TransactionID != view.ID {
		t.Errorf("issue = %+v", issue)
	}
	if e.getGoal(travel).ReservedMinor != 0 {
		t.Error("correcting the expense changed the goal")
	}
}

// --- Transfers ---------------------------------------------------------------------

// A USD transfer never consumes savings. Moving the backing with it keeps every
// goal whole; without it, the source is left overallocated, and reported.
func TestATransferCanMoveTheBackingOfReservedMoney(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	emergency := e.goal("Emergencias", savings.GoalPermanent, "1000", "", "")
	e.reserveFor(e.usd, emergency, "500")
	e.reserve(e.usd, "300")

	transfer := ledger.RecordTransferInput{SourceAccountID: e.usd, DestinationAccountID: e.usdSavings,
		Amount: "900", EffectiveDate: "2026-10-10"}
	plan := savings.TransferPlan{RequestKey: key(), Move: []savings.SavingsUse{e.spendFrom(&emergency, "500"), e.spendFrom(nil, "300")}}
	first, err := e.app.RecordTransfer(t.Context(), transfer, plan)
	if err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}
	again, err := e.app.RecordTransfer(t.Context(), transfer, plan)
	if err != nil || again.ID != first.ID {
		t.Errorf("a replayed transfer was recorded twice (%v)", err)
	}
	if a := e.availability(e.usd); a.ReservedMinor != 0 || a.BalanceMinor != 10000 {
		t.Errorf("source = %+v", a)
	}
	if a := e.availability(e.usdSavings); a.ReservedMinor != 80000 || a.BalanceMinor != 90000 {
		t.Errorf("destination = %+v", a)
	}
	g := e.getGoal(emergency)
	if g.ReservedMinor != 50000 || len(g.Accounts) != 1 || g.Accounts[0].AccountID != e.usdSavings {
		t.Errorf("goal after the transfer = %+v", g)
	}
	if f := e.fund(); f.ReservedMinor != 80000 || f.OverallocatedMinor != 0 {
		t.Errorf("fund = %+v", f)
	}

	if _, err := e.app.RecordTransfer(t.Context(), ledger.RecordTransferInput{SourceAccountID: e.usdSavings,
		DestinationAccountID: e.usdTwo, Amount: "900", EffectiveDate: "2026-10-11"}, savings.TransferPlan{}); err != nil {
		t.Fatalf("a transfer without a plan was refused: %v", err)
	}
	if issue := issueOf(e.issues(), savings.IssueOverallocated); issue == nil || issue.AmountMinor != 80000 {
		t.Errorf("the source left overallocated is not reported: %+v", issue)
	}
}

// --- Over-confirmation and history -----------------------------------------------------

func TestOverConfirmationIsReported(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	income := e.income(e.usd, "800", "2026-10-05")
	if _, err := e.confirm(income, e.usd, "160"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	e.edit(income, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Income, CategoryID: e.salary,
		Amount: "500", EffectiveDate: "2026-10-05"})
	issue := issueOf(e.issues(), savings.IssueOverConfirmed)
	if issue == nil || issue.AmountMinor != 6000 {
		t.Errorf("issue = %+v", issue)
	}
}

func TestTheFundAndEachGoalHaveAMonthlyHistory(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "")
	e.reserveFor(e.usd, travel, "300")
	e.reserve(e.usd, "100")
	if _, err := e.svc.Release(t.Context(), savings.ReleaseInput{RequestKey: key(), AccountID: e.usd, GoalID: &travel,
		Amount: "50", Reason: "x"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	months, err := e.svc.FundHistory(t.Context())
	if err != nil {
		t.Fatalf("FundHistory: %v", err)
	}
	if len(months) != 1 || months[0].ContributionsMinor != 40000 || months[0].WithdrawalsMinor != 5000 ||
		months[0].NetMinor != 35000 || months[0].ClosingMinor != 35000 {
		t.Errorf("fund months = %+v", months)
	}
	g := e.getGoal(travel)
	if len(g.Months) != 1 || g.Months[0].InMinor != 30000 || g.Months[0].OutMinor != 5000 || g.Months[0].ClosingMinor != 25000 {
		t.Errorf("goal months = %+v", g.Months)
	}
}
