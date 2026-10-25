package application_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// usdAccount opens a USD bank account holding 100.00 with 50.00 reserved.
func usdAccount(t *testing.T, w cardWorld) (int64, *savings.Service) {
	t.Helper()
	opened, err := w.app.OpenAccount(t.Context(), application.OpenAccountInput{
		Account:        account.CreateInput{Name: "Zelle", Type: account.Bank, Subtype: checking(), CurrencyCode: "USD"},
		OpeningBalance: ptr("100.00"), OpeningBalanceDate: "2026-09-01",
	})
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	fund := savings.NewService(w.db)
	if _, err := fund.Reserve(t.Context(), savings.ReserveInput{
		RequestKey: key(), Lines: []savings.ReserveLine{{AccountID: opened.Account.ID, Amount: "50.00"}},
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return opened.Account.ID, fund
}

func balanceOf(t *testing.T, w cardWorld, accountID int64) int64 {
	t.Helper()
	b, err := ledger.NewService(w.db).GetAccountBalance(t.Context(), accountID)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	return b.BalanceMinor
}

func issuesOf(t *testing.T, fund *savings.Service) []savings.Issue {
	t.Helper()
	issues, err := fund.ListIssues(t.Context())
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	return issues
}

// From an account in the card's currency, a payment is a TRANSFER into the
// card (TD-091), recorded once however many times it is submitted.
func TestRecordCardPaymentFromVES(t *testing.T) {
	t.Parallel()
	w := newCardWorld(t)
	ctx := t.Context()
	in := application.RecordCardPaymentInput{
		RequestKey: key(), CardAccountID: w.card, SourceAccountID: w.bank,
		Amount: "1500.00", EffectiveDate: "2026-10-05",
	}
	view, err := w.app.RecordCardPayment(ctx, in)
	if err != nil {
		t.Fatalf("RecordCardPayment: %v", err)
	}
	if view.Type != ledger.Transfer || view.Transfer == nil || view.Transfer.DestinationAccountID != w.card ||
		view.Transfer.AmountMinor != 150000 {
		t.Errorf("payment = %+v", view)
	}
	again, err := w.app.RecordCardPayment(ctx, in)
	if err != nil || again.ID != view.ID {
		t.Errorf("the replay = %d, %v; want %d", again.ID, err, view.ID)
	}
	if n := rows(t, w.db, "transactions"); n != 1 {
		t.Errorf("%d transactions after a double submission", n)
	}
	if got := balanceOf(t, w, w.card); got != 150000 {
		t.Errorf("the card's balance is %d", got)
	}

	// A plan on a VES payment is refused, and nothing is recorded with it.
	in.RequestKey = key()
	in.Savings = savings.ExpensePlan{Spend: []savings.SavingsUse{{Amount: "1.00"}}}
	if _, err := w.app.RecordCardPayment(ctx, in); !errors.Is(err, savings.ErrNotLinkable) {
		t.Errorf("a savings plan on a VES payment = %v", err)
	}
	if n := rows(t, w.db, "transactions"); n != 1 {
		t.Errorf("the refused plan left %d transactions", n)
	}
}

// From USD into a VES card the payment is an FX_EXCHANGE with both amounts.
// Without a plan it leaves the account overallocated, as any outflow does; with
// one it consumes the reservations it names, linked to the payment (D-12).
func TestRecordCardPaymentFromUSD(t *testing.T) {
	t.Parallel()
	w := newCardWorld(t)
	ctx := t.Context()
	usd, fund := usdAccount(t, w)

	missing := application.RecordCardPaymentInput{
		RequestKey: key(), CardAccountID: w.card, SourceAccountID: usd, Amount: "10000.00", EffectiveDate: "2026-10-05",
	}
	if _, err := w.app.RecordCardPayment(ctx, missing); !errors.Is(err, ledger.ErrInvalidAmount) {
		t.Errorf("an exchange without the USD amount = %v", err)
	}

	// 80 of 100 USD, 50 reserved: 30 overallocated, reported and not refused.
	view, err := w.app.RecordCardPayment(ctx, application.RecordCardPaymentInput{
		RequestKey: key(), CardAccountID: w.card, SourceAccountID: usd,
		Amount: "20000.00", SourceAmount: "80.00", EffectiveDate: "2026-10-05",
	})
	if err != nil {
		t.Fatalf("RecordCardPayment without a plan: %v", err)
	}
	if view.Type != ledger.FXExchange || view.Exchange == nil || view.Exchange.DestinationAccountID != w.card ||
		view.Exchange.SourceAmountMinor != 8000 || view.Exchange.DestinationAmountMinor != 2000000 {
		t.Errorf("payment = %+v", view.Exchange)
	}
	issues := issuesOf(t, fund)
	if len(issues) != 1 || issues[0].Kind != savings.IssueOverallocated || issues[0].AmountMinor != 3000 {
		t.Errorf("issues = %+v, want one OVERALLOCATED of 30.00", issues)
	}
}

// With a plan, the reservations it names are spent by the payment in the same
// transaction, and the fund stays consistent.
func TestRecordCardPaymentWithASavingsPlan(t *testing.T) {
	t.Parallel()
	w := newCardWorld(t)
	ctx := t.Context()
	usd, fund := usdAccount(t, w)

	in := application.RecordCardPaymentInput{
		RequestKey: key(), CardAccountID: w.card, SourceAccountID: usd,
		Amount: "20000.00", SourceAmount: "80.00", EffectiveDate: "2026-10-05",
		Savings: savings.ExpensePlan{Spend: []savings.SavingsUse{{Amount: "30.00"}}},
	}
	view, err := w.app.RecordCardPayment(ctx, in)
	if err != nil {
		t.Fatalf("RecordCardPayment: %v", err)
	}
	if again, err := w.app.RecordCardPayment(ctx, in); err != nil || again.ID != view.ID {
		t.Errorf("the replay = %d, %v", again.ID, err)
	}
	ops, err := fund.ListOperations(ctx, 10)
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if ops[0].Kind != savings.KindSpend || ops[0].TransactionID == nil || *ops[0].TransactionID != view.ID || ops[0].TotalMinor != -3000 {
		t.Errorf("the newest operation = %+v, want a SPEND of 30.00 linked to the payment", ops[0])
	}
	if n := len(ops); n != 2 {
		t.Errorf("%d savings operations after a replay, want the reservation and one SPEND", n)
	}
	if issues := issuesOf(t, fund); len(issues) != 0 {
		t.Errorf("issues after a planned payment = %+v", issues)
	}

	// More savings than what left the account is refused, and so is more than
	// is reserved; the payment is not recorded with the refused plan.
	before := rows(t, w.db, "transactions")
	for name, use := range map[string]string{"more than the payment": "90.00", "more than reserved": "25.00"} {
		in.RequestKey = key()
		in.SourceAmount = "20.00"
		in.Savings = savings.ExpensePlan{Spend: []savings.SavingsUse{{Amount: use}}}
		if _, err := w.app.RecordCardPayment(ctx, in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if after := rows(t, w.db, "transactions"); after != before {
		t.Errorf("refused plans left %d transactions", after-before)
	}

	// Correcting the payment below what it spent is reported, never fixed.
	if _, err := ledger.NewService(w.db).UpdateExchange(ctx, view.ID, ledger.UpdateExchangeInput{
		SourceAccountID: usd, SourceAmount: "20.00", DestinationAccountID: w.card, DestinationAmount: "5000.00",
		EffectiveDate: "2026-10-05",
	}); err != nil {
		t.Fatalf("UpdateExchange: %v", err)
	}
	issues := issuesOf(t, fund)
	if len(issues) != 1 || issues[0].Kind != savings.IssueSpendMismatch || !strings.Contains(issues[0].Detail, "El pago es ahora de 20.00 USD") {
		t.Errorf("issues after the correction = %+v", issues)
	}
}

// A card is paid from an asset; a card is not a source, and an account that
// is not a card is not a destination.
func TestRecordCardPaymentNamesACardAndAnAsset(t *testing.T) {
	t.Parallel()
	w := newCardWorld(t)
	ctx := t.Context()
	other, err := w.app.OpenCreditCard(ctx, plazaBlack(key()))
	if err != nil {
		t.Fatalf("OpenCreditCard: %v", err)
	}
	for name, tc := range map[string]struct {
		card, source int64
		want         error
	}{
		"from a card":       {w.card, other.Account.ID, creditcard.ErrInvalidInput},
		"into a bank":       {w.bank, w.bank, creditcard.ErrNotACreditCard},
		"from no account":   {w.card, 9999, ledger.ErrAccountNotFound},
		"into the same one": {w.card, w.card, creditcard.ErrInvalidInput},
	} {
		_, err := w.app.RecordCardPayment(ctx, application.RecordCardPaymentInput{
			RequestKey: key(), CardAccountID: tc.card, SourceAccountID: tc.source, Amount: "10.00", EffectiveDate: "2026-10-05",
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s = %v, want %v", name, err, tc.want)
		}
	}
	if n := rows(t, w.db, "transactions"); n != 0 {
		t.Errorf("%d transactions recorded by refused payments", n)
	}
}
