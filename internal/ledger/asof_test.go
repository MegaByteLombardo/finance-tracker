package ledger_test

import (
	"errors"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// clockAt pins the ledger's "today" to a Venezuelan calendar date. Noon in
// Caracas is the same date in UTC, so the pin cannot slip across midnight.
func clockAt(t *testing.T, day string) func() time.Time {
	t.Helper()
	parsed, err := database.ParseDate(day)
	if err != nil {
		t.Fatalf("parsing %q: %v", day, err)
	}
	return func() time.Time { return parsed.Add(16 * time.Hour) }
}

func (w *world) income(t *testing.T, accountID int64, amount, on string) {
	t.Helper()
	if _, err := w.ledger.RecordIncome(t.Context(), ledger.RecordIncomeInput{
		AccountID: accountID, CategoryID: w.salary, Amount: amount,
		PaymentMethodID: methodBankTransfer, EffectiveDate: on,
	}); err != nil {
		t.Fatalf("recording income of %s on %s: %v", amount, on, err)
	}
}

func (w *world) expense(t *testing.T, accountID int64, amount, on string) {
	t.Helper()
	if _, err := w.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: accountID, CategoryID: w.food, Amount: amount,
		PaymentMethodID: methodCash, EffectiveDate: on,
	}); err != nil {
		t.Fatalf("recording expense of %s on %s: %v", amount, on, err)
	}
}

// TestACurrentBalanceExcludesFutureMovements is TD-063: the current balance is
// the movements dated on or before today, and a future-dated movement is
// counted, not summed.
func TestACurrentBalanceExcludesFutureMovements(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ledger.SetClock(w.ledger, clockAt(t, "2026-10-05"))

	w.income(t, w.usdAccount, "100.00", "2026-10-04")
	w.income(t, w.usdAccount, "10.00", "2026-10-05") // exactly today: counts
	w.income(t, w.usdAccount, "1000.00", "2026-10-06")
	w.expense(t, w.usdAccount, "5.00", "2026-11-01")

	balance, err := w.ledger.GetAccountBalance(t.Context(), w.usdAccount)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != 11000 {
		t.Errorf("balance = %d, want 11000: the movements dated after today must not count", balance.BalanceMinor)
	}
	if balance.FutureMovementCount != 2 {
		t.Errorf("future movements = %d, want 2", balance.FutureMovementCount)
	}

	balances, err := w.ledger.ListAccountBalances(t.Context(), false)
	if err != nil {
		t.Fatalf("ListAccountBalances: %v", err)
	}
	if len(balances) != 1 || balances[0].BalanceMinor != 11000 || balances[0].FutureMovementCount != 2 {
		t.Errorf("ListAccountBalances = %+v, want one account at 11000 with 2 future movements", balances)
	}

	totals, err := w.ledger.ListCurrencyTotals(t.Context(), false)
	if err != nil {
		t.Fatalf("ListCurrencyTotals: %v", err)
	}
	if len(totals) != 1 || totals[0].TotalMinor != 11000 || totals[0].AccountCount != 1 {
		t.Errorf("ListCurrencyTotals = %+v, want USD at 11000 over 1 account", totals)
	}

	// The same ledger a day later: the movement dated the 6th has taken effect.
	ledger.SetClock(w.ledger, clockAt(t, "2026-10-06"))
	balance, err = w.ledger.GetAccountBalance(t.Context(), w.usdAccount)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != 111000 || balance.FutureMovementCount != 1 {
		t.Errorf("on the 6th: balance %d with %d future, want 111000 with 1",
			balance.BalanceMinor, balance.FutureMovementCount)
	}
}

// TestAnAccountWithOnlyFutureMovementsIsListedAtZero: the derived table must not
// drop an account whose every movement is still in the future.
func TestAnAccountWithOnlyFutureMovementsIsListedAtZero(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ledger.SetClock(w.ledger, clockAt(t, "2026-10-05"))
	w.income(t, w.usdAccount, "100.00", "2026-12-24")

	balances, err := w.ledger.ListAccountBalances(t.Context(), false)
	if err != nil {
		t.Fatalf("ListAccountBalances: %v", err)
	}
	if len(balances) != 1 {
		t.Fatalf("%d accounts listed, want 1", len(balances))
	}
	if balances[0].BalanceMinor != 0 || balances[0].BalanceFormatted != "0.00" || balances[0].FutureMovementCount != 1 {
		t.Errorf("balance = %+v, want 0.00 with 1 future movement", balances[0])
	}
}

func TestDailyDeltasRebuildTheCurrentBalance(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ledger.SetClock(w.ledger, clockAt(t, "2026-10-05"))

	w.fund(t, w.usdAccount, "500.00") // 2026-09-01
	w.income(t, w.usdAccount, "100.00", "2026-09-10")
	w.expense(t, w.usdAccount, "30.00", "2026-09-10")
	w.income(t, w.usdAccount, "999.00", "2026-10-20") // future
	if _, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: w.usdAccount, DestinationAccountID: w.efectivoUSD,
		Amount: "70.00", EffectiveDate: "2026-09-15",
	}); err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}

	deltas, err := w.ledger.ListDailyAccountDeltas(t.Context())
	if err != nil {
		t.Fatalf("ListDailyAccountDeltas: %v", err)
	}

	type key struct {
		account int64
		date    string
	}
	got := map[key]ledger.DailyDelta{}
	for i, delta := range deltas {
		if i > 0 && delta.Date.Before(deltas[i-1].Date) {
			t.Errorf("deltas are not ordered by date: %s after %s",
				database.FormatDate(delta.Date), database.FormatDate(deltas[i-1].Date))
		}
		got[key{delta.AccountID, database.FormatDate(delta.Date)}] = delta
	}

	// Two movements on the 10th net to +70.00, and the count says there were two.
	if d := got[key{w.usdAccount, "2026-09-10"}]; d.AmountMinor != 7000 || d.MovementCount != 2 {
		t.Errorf("USD on 2026-09-10 = %+v, want +7000 over 2 movements", d)
	}
	// Both halves of the transfer, each on its own account.
	if d := got[key{w.usdAccount, "2026-09-15"}]; d.AmountMinor != -7000 {
		t.Errorf("USD on 2026-09-15 = %+v, want -7000", d)
	}
	if d := got[key{w.efectivoUSD, "2026-09-15"}]; d.AmountMinor != 7000 {
		t.Errorf("cash on 2026-09-15 = %+v, want +7000", d)
	}
	// The future row is there: it is what the future-movement count is made of.
	if d := got[key{w.usdAccount, "2026-10-20"}]; d.AmountMinor != 99900 {
		t.Errorf("USD on 2026-10-20 = %+v, want +99900", d)
	}

	// The invariant: the deltas dated on or before today add up to what the
	// ledger reports as the current balance, for every account. If the two
	// ever disagree, history and the present are no longer the same ledger.
	today := database.TodayAt(clockAt(t, "2026-10-05")())
	balances, err := w.ledger.ListAccountBalances(t.Context(), true)
	if err != nil {
		t.Fatalf("ListAccountBalances: %v", err)
	}
	for _, balance := range balances {
		var sum int64
		for _, delta := range deltas {
			if delta.AccountID == balance.AccountID && !delta.Date.After(today) {
				sum += delta.AmountMinor
			}
		}
		if sum != balance.BalanceMinor {
			t.Errorf("account %q: deltas sum to %d, the ledger says %d",
				balance.AccountName, sum, balance.BalanceMinor)
		}
	}
}

func TestFlowsAreIncomeAndExpenseOnly(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	w.fund(t, w.usdAccount, "500.00")  // opening balance: not a flow
	w.fund(t, w.banescoVES, "8500.00") // opening balance: not a flow

	w.income(t, w.usdAccount, "100.00", "2026-09-01") // on `from`: included
	w.expense(t, w.usdAccount, "30.00", "2026-09-30") // on `to`: included
	w.expense(t, w.usdAccount, "1.00", "2026-08-31")  // the day before: excluded
	w.income(t, w.usdAccount, "2.00", "2026-10-01")   // the day after: excluded

	if _, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: w.usdAccount, DestinationAccountID: w.efectivoUSD,
		Amount: "70.00", EffectiveDate: "2026-09-15",
	}); err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}
	if _, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID: w.banescoVES, SourceAmount: "8500.00",
		DestinationAccountID: w.usdAccount, DestinationAmount: "10.00",
		EffectiveDate: "2026-09-16",
	}); err != nil {
		t.Fatalf("RecordExchange: %v", err)
	}

	flows, err := w.ledger.ListFlows(t.Context(), mustParse(t, "2026-09-01"), mustParse(t, "2026-09-30"))
	if err != nil {
		t.Fatalf("ListFlows: %v", err)
	}
	if len(flows) != 2 {
		t.Fatalf("%d flows, want 2 (one income, one expense): %+v", len(flows), flows)
	}

	income, expense := flows[0], flows[1]
	if income.Type != ledger.Income || income.AmountMinor != 10000 || income.CategoryID != w.salary ||
		income.CategoryName != "Salario" || income.CurrencyCode != "USD" || income.MinorUnit != 2 || income.Count != 1 {
		t.Errorf("income flow = %+v", income)
	}
	if expense.Type != ledger.Expense || expense.AmountMinor != -3000 || expense.CategoryID != w.food {
		t.Errorf("expense flow = %+v, want -3000 in the food category", expense)
	}

	if _, err := w.ledger.ListFlows(t.Context(), mustParse(t, "2026-09-30"), mustParse(t, "2026-09-01")); !errors.Is(err, ledger.ErrInvalidInput) {
		t.Errorf("a reversed range returned %v, want ErrInvalidInput", err)
	}
}

// TestExpensesAreExpensesOnly: the largest expenses of a recap are ranked from
// this list, so a transfer, an exchange or an opening balance must never be in
// it, and the range is inclusive at both ends.
func TestExpensesAreExpensesOnly(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	w.fund(t, w.usdAccount, "500.00")  // opening balance: not an expense
	w.fund(t, w.banescoVES, "8500.00") // opening balance: not an expense

	w.expense(t, w.usdAccount, "30.00", "2026-09-01") // on `from`: included
	w.expense(t, w.usdAccount, "12.50", "2026-09-30") // on `to`: included
	w.expense(t, w.usdAccount, "1.00", "2026-08-31")  // the day before: excluded
	w.income(t, w.usdAccount, "100.00", "2026-09-10") // income: excluded

	if _, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: w.usdAccount, DestinationAccountID: w.efectivoUSD,
		Amount: "70.00", EffectiveDate: "2026-09-15",
	}); err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}
	if _, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID: w.banescoVES, SourceAmount: "8500.00",
		DestinationAccountID: w.usdAccount, DestinationAmount: "10.00",
		EffectiveDate: "2026-09-16",
	}); err != nil {
		t.Fatalf("RecordExchange: %v", err)
	}

	items, err := w.ledger.ListExpenses(t.Context(), mustParse(t, "2026-09-01"), mustParse(t, "2026-09-30"))
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("%d expenses, want 2: %+v", len(items), items)
	}
	first, second := items[0], items[1]
	if first.AmountMinor != 3000 || first.CategoryID != w.food || first.CurrencyCode != "USD" ||
		first.MinorUnit != 2 || first.AccountID != w.usdAccount || database.FormatDate(first.Date) != "2026-09-01" {
		t.Errorf("first expense = %+v, want 3000 USD in food on 2026-09-01", first)
	}
	if second.AmountMinor != 1250 || database.FormatDate(second.Date) != "2026-09-30" {
		t.Errorf("second expense = %+v, want 1250 on 2026-09-30", second)
	}
	if first.TransactionID == 0 || first.TransactionID == second.TransactionID {
		t.Errorf("transaction ids %d and %d, want two distinct ids", first.TransactionID, second.TransactionID)
	}

	if _, err := w.ledger.ListExpenses(t.Context(), mustParse(t, "2026-09-30"), mustParse(t, "2026-09-01")); !errors.Is(err, ledger.ErrInvalidInput) {
		t.Errorf("a reversed range returned %v, want ErrInvalidInput", err)
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := database.ParseDate(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return parsed
}
