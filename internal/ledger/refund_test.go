package ledger_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// inTx runs fn in a transaction and commits it, as internal/application does
// for every refund.
func (w *world) inTx(t *testing.T, fn func(exec *sql.Tx) error) error {
	t.Helper()
	tx, err := w.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	if err := fn(tx); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			t.Fatalf("rolling back: %v", rerr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing: %v", err)
	}
	return nil
}

func (w *world) refund(t *testing.T, in ledger.RecordRefundInput) (ledger.TransactionView, error) {
	t.Helper()
	var view ledger.TransactionView
	err := w.inTx(t, func(exec *sql.Tx) error {
		var err error
		view, err = ledger.RecordRefundTx(t.Context(), exec, in)
		return err
	})
	return view, err
}

func (w *world) purchase(t *testing.T, accountID int64, amount, date string) int64 {
	t.Helper()
	view, err := w.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: accountID, CategoryID: w.food, Amount: amount, PaymentMethodID: methodPOS, EffectiveDate: date,
	})
	if err != nil {
		t.Fatalf("recording an expense: %v", err)
	}
	return view.ID
}

func refundOf(w *world, accountID int64, amount string, original *int64) ledger.RecordRefundInput {
	return ledger.RecordRefundInput{
		AccountID: accountID, CategoryID: w.food, Amount: amount, PaymentMethodID: methodPOS,
		EffectiveDate: "2026-09-04", OriginalTransactionID: original,
	}
}

// A refund is one POSITIVE movement under an EXPENSE category: it raises the
// balance, and the amount it is given as is unsigned.
func TestARefundIsAPositiveMovementUnderAnExpenseCategory(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	expense := w.purchase(t, w.usdAccount, "100.00", "2026-09-01")

	view, err := w.refund(t, refundOf(w, w.usdAccount, "40.00", &expense))
	if err != nil {
		t.Fatalf("RecordRefundTx: %v", err)
	}
	if view.Type != ledger.Refund || view.AmountMinor != 4000 {
		t.Errorf("refund = %s %d, want REFUND 4000", view.Type, view.AmountMinor)
	}
	if view.RefundOf == nil || *view.RefundOf != expense {
		t.Errorf("RefundOf = %v, want %d", view.RefundOf, expense)
	}
	balance, err := w.ledger.GetAccountBalance(t.Context(), w.usdAccount)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != -6000 {
		t.Errorf("balance = %d, want -6000", balance.BalanceMinor)
	}

	for name, in := range map[string]ledger.RecordRefundInput{
		"a negative amount":   refundOf(w, w.usdAccount, "-5.00", nil),
		"zero":                refundOf(w, w.usdAccount, "0", nil),
		"an income category":  {AccountID: w.usdAccount, CategoryID: w.salary, Amount: "5", PaymentMethodID: methodPOS, EffectiveDate: today},
		"no payment method":   {AccountID: w.usdAccount, CategoryID: w.food, Amount: "5", EffectiveDate: today},
		"an unknown original": refundOf(w, w.usdAccount, "5.00", ptrInt(9999)),
	} {
		if _, err := w.refund(t, in); err == nil {
			t.Errorf("a refund with %s was accepted", name)
		}
	}
}

func ptrInt(v int64) *int64 { return &v }

// The refunds of one expense never add up to more than it, it must be an
// expense, and it must be in the same account.
func TestRefundLinksAreChecked(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	other := w.newAccount(t, "Banesco USD", "USD")
	expense := w.purchase(t, w.usdAccount, "100.00", "2026-09-01")

	if _, err := w.refund(t, refundOf(w, w.usdAccount, "60.00", &expense)); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	if _, err := w.refund(t, refundOf(w, w.usdAccount, "40.01", &expense)); !errors.Is(err, ledger.ErrRefundExceedsOriginal) {
		t.Errorf("refunding past the expense = %v, want ErrRefundExceedsOriginal", err)
	}
	if _, err := w.refund(t, refundOf(w, w.usdAccount, "40.00", &expense)); err != nil {
		t.Errorf("refunding exactly the rest: %v", err)
	}
	if _, err := w.refund(t, refundOf(w, other, "1.00", &expense)); !errors.Is(err, ledger.ErrRefundOriginal) {
		t.Errorf("a refund in another account = %v, want ErrRefundOriginal", err)
	}

	income, err := w.ledger.RecordIncome(t.Context(), ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "10", PaymentMethodID: methodPOS, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordIncome: %v", err)
	}
	if _, err := w.refund(t, refundOf(w, w.usdAccount, "1.00", &income.ID)); !errors.Is(err, ledger.ErrRefundOriginal) {
		t.Errorf("refunding an income = %v, want ErrRefundOriginal", err)
	}

	// Editing the expense below what was refunded, or into an income, is
	// refused too: the check runs after every write that could break it.
	if _, err := w.ledger.UpdateTransaction(t.Context(), expense, ledger.UpdateTransactionInput{
		AccountID: w.usdAccount, Type: ledger.Expense, CategoryID: w.food, PaymentMethodID: methodPOS,
		Amount: "99.99", EffectiveDate: "2026-09-01",
	}); !errors.Is(err, ledger.ErrRefundExceedsOriginal) {
		t.Errorf("shrinking a refunded expense = %v, want ErrRefundExceedsOriginal", err)
	}
	if _, err := w.ledger.UpdateTransaction(t.Context(), expense, ledger.UpdateTransactionInput{
		AccountID: w.usdAccount, Type: ledger.Income, CategoryID: w.salary, PaymentMethodID: methodPOS,
		Amount: "100", EffectiveDate: "2026-09-01",
	}); !errors.Is(err, ledger.ErrRefundOriginal) {
		t.Errorf("turning a refunded expense into income = %v, want ErrRefundOriginal", err)
	}
}

// A refund is corrected only as a refund, and its link is part of the
// correction: replaced, added or removed.
func TestUpdateRefund(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	first := w.purchase(t, w.usdAccount, "50.00", "2026-09-01")
	second := w.purchase(t, w.usdAccount, "30.00", "2026-09-02")
	refund, err := w.refund(t, refundOf(w, w.usdAccount, "20.00", &first))
	if err != nil {
		t.Fatalf("RecordRefundTx: %v", err)
	}

	update := func(in ledger.UpdateRefundInput) (ledger.TransactionView, error) {
		var view ledger.TransactionView
		err := w.inTx(t, func(exec *sql.Tx) error {
			var err error
			view, err = ledger.UpdateRefundTx(t.Context(), exec, refund.ID, in)
			return err
		})
		return view, err
	}
	in := ledger.UpdateRefundInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "25.00", PaymentMethodID: methodPOS,
		EffectiveDate: "2026-09-03", OriginalTransactionID: &second,
	}
	view, err := update(in)
	if err != nil {
		t.Fatalf("relinking: %v", err)
	}
	if view.AmountMinor != 2500 || view.RefundOf == nil || *view.RefundOf != second {
		t.Errorf("after relinking: %d of %v", view.AmountMinor, view.RefundOf)
	}

	in.Amount = "30.01"
	if _, err := update(in); !errors.Is(err, ledger.ErrRefundExceedsOriginal) {
		t.Errorf("growing past the new original = %v, want ErrRefundExceedsOriginal", err)
	}

	in.Amount, in.OriginalTransactionID = "30.01", nil
	view, err = update(in)
	if err != nil {
		t.Fatalf("unlinking: %v", err)
	}
	if view.RefundOf != nil {
		t.Errorf("RefundOf = %v after unlinking", view.RefundOf)
	}

	// Nothing else is edited as a refund, and a refund is edited as nothing else.
	err = w.inTx(t, func(exec *sql.Tx) error {
		_, err := ledger.UpdateRefundTx(t.Context(), exec, first, in)
		return err
	})
	if !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("correcting an expense as a refund = %v, want ErrTypeChangeNotAllowed", err)
	}
	if _, err := w.ledger.UpdateTransaction(t.Context(), refund.ID, ledger.UpdateTransactionInput{
		AccountID: w.usdAccount, Type: ledger.Expense, CategoryID: w.food, PaymentMethodID: methodPOS,
		Amount: "1", EffectiveDate: today,
	}); !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("correcting a refund as an expense = %v, want ErrTypeChangeNotAllowed", err)
	}
}

// In the cash flow a refund is negative spending in its category on its day
// (TD-068, TD-090): it is reported as EXPENSE and nets against that day's
// expenses. It is never among the expenses ranked by size.
func TestARefundIsNegativeSpendingInTheCashFlow(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	expense := w.purchase(t, w.usdAccount, "100.00", "2026-09-01")
	if _, err := w.refund(t, ledger.RecordRefundInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "30.00", PaymentMethodID: methodPOS,
		EffectiveDate: "2026-09-01", OriginalTransactionID: &expense,
	}); err != nil {
		t.Fatalf("RecordRefundTx: %v", err)
	}
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	flows, err := w.ledger.ListFlows(t.Context(), day, day)
	if err != nil {
		t.Fatalf("ListFlows: %v", err)
	}
	if len(flows) != 1 || flows[0].Type != ledger.Expense || flows[0].AmountMinor != -7000 || flows[0].Count != 2 {
		t.Errorf("flows = %+v, want one EXPENSE of -7000 counting 2", flows)
	}
	expenses, err := w.ledger.ListExpenses(t.Context(), day, day)
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if len(expenses) != 1 || expenses[0].AmountMinor != 10000 {
		t.Errorf("expenses = %+v, want the purchase alone", expenses)
	}
}

// An account's listing holds every transaction that moved it, both ends of a
// transfer included, one row each.
func TestListAccountTransactions(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	other := w.newAccount(t, "Banesco USD", "USD")
	w.purchase(t, w.usdAccount, "10.00", "2026-09-01")
	w.purchase(t, other, "20.00", "2026-09-02")
	if _, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: other, DestinationAccountID: w.usdAccount, Amount: "5.00", EffectiveDate: "2026-09-03",
	}); err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}

	views, err := w.ledger.ListAccountTransactions(t.Context(), w.usdAccount, ledger.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("ListAccountTransactions: %v", err)
	}
	if len(views) != 2 || views[0].Type != ledger.Transfer || views[1].Type != ledger.Expense {
		t.Errorf("listing = %+v, want the transfer then the expense", views)
	}
}
