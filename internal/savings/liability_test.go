package savings_test

import (
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// M8.1 brings the first liability, and with it the two rules of TD-094: an
// income is saved from only when it is credited to an ASSET, and only assets
// back reserved money. Before M8 no liability existed, so neither rule changes
// anything M7 already holds -- the rest of this package's suite is that proof.

// card opens a credit card in a currency, through the application as the form
// does.
func (e *env) card(name, currencyCode string) int64 {
	e.t.Helper()
	result, err := e.app.OpenCreditCard(e.t.Context(), application.OpenCreditCardInput{
		RequestKey: key(), Name: name, CurrencyCode: currencyCode,
		Terms: creditcard.TermsInput{StatementDay: 25},
	})
	if err != nil {
		e.t.Fatalf("opening card %s: %v", name, err)
	}
	return result.Account.ID
}

// Cashback credited to a card is an income in the books and is not eligible:
// it lowers a debt and adds nothing anyone could set aside. No suggestion.
func TestCashbackOnACardIsNotSavedFrom(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	card := e.card("Visa USD", "USD")

	id := e.incomeIn(card, e.refunds, "12.00", "2026-10-10")
	if n := e.count("savings_suggestions"); n != 0 {
		t.Errorf("%d suggestions for cashback on a card, want 0", n)
	}
	detail, err := e.svc.IncomeDetail(t.Context(), id)
	if err != nil {
		t.Fatalf("IncomeDetail: %v", err)
	}
	if detail.Eligible {
		t.Error("cashback on a card is reported eligible")
	}

	// A manual inclusion cannot make it eligible: the asset rule comes first.
	if err := e.svc.SetIncomeOverride(t.Context(), id, true, "Quiero ahorrarlo"); err != nil {
		t.Fatalf("SetIncomeOverride: %v", err)
	}
	if n := e.count("savings_suggestions"); n != 0 {
		t.Errorf("an inclusion override produced %d suggestions on a card income", n)
	}
}

// An income moved to a card stops being eligible: its live revision is
// invalidated, the reservation confirmed against it is left exactly where it
// was, and M7 reports the confirmation that now exceeds the (absent)
// suggestion.
func TestAnIncomeMovedToACardIsInvalidatedAndItsReservesKept(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	e.open(e.usd, "1000")
	card := e.card("Visa USD", "USD")

	id := e.income(e.usd, "500.00", "2026-10-10")
	if _, err := e.confirm(id, e.usd, "100.00"); err != nil {
		t.Fatalf("confirming: %v", err)
	}
	e.edit(id, ledger.UpdateTransactionInput{
		AccountID: card, Type: ledger.Income, CategoryID: e.salary, Amount: "500.00", EffectiveDate: "2026-10-10",
	})

	revisions := e.revisions(id)
	if last := revisions[len(revisions)-1]; last.Status != savings.StatusInvalidated {
		t.Errorf("the latest revision is %s, want INVALIDATED", last.Status)
	}
	if a := e.availability(e.usd); a.ReservedMinor != 10000 {
		t.Errorf("reserved on the bank account = %d, want 10000 untouched", a.ReservedMinor)
	}
	if issueOf(e.issues(), savings.IssueOverConfirmed) == nil {
		t.Error("no OVER_CONFIRMED issue for a confirmation without a live suggestion")
	}
}

// A USD card holding a credit is not money in hand. It is left out of the
// fund's balance, of what is free and of the table, and nothing can be
// reserved on it, impacted on it, or moved onto it by a plan.
func TestAUSDCardNeverBacksSavings(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	card := e.card("Visa USD", "USD")
	// An overpayment: the card owes the user 50.
	if _, err := e.app.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: e.usd, DestinationAccountID: card, Amount: "50.00", EffectiveDate: "2026-10-05",
	}, savings.TransferPlan{}); err != nil {
		t.Fatalf("overpaying the card: %v", err)
	}

	fund := e.fund()
	if _, listed := fund.Account(card); listed {
		t.Error("the card is listed in the fund")
	}
	if fund.BalanceMinor != 95000 {
		t.Errorf("fund balance = %d, want 95000 (the bank account only)", fund.BalanceMinor)
	}

	_, err := e.svc.Reserve(t.Context(), savings.ReserveInput{
		RequestKey: key(), Lines: []savings.ReserveLine{{AccountID: card, Amount: "10.00"}},
	})
	if !errors.Is(err, savings.ErrLiabilityAccount) {
		t.Errorf("reserving on a card = %v, want ErrLiabilityAccount", err)
	}

	impact, err := e.svc.AccountImpact(t.Context(), card, "10")
	if err != nil {
		t.Fatalf("AccountImpact: %v", err)
	}
	if impact.Applies {
		t.Error("a card reported a savings impact")
	}

	// A fabricated plan moving reserved money onto the card is the plan's
	// error, never a reason the ledger is wrong: the same transfer without it
	// records (TD-086).
	e.reserve(e.usd, "100")
	in := ledger.RecordTransferInput{SourceAccountID: e.usd, DestinationAccountID: card, Amount: "20.00", EffectiveDate: "2026-10-06"}
	_, err = e.app.RecordTransfer(t.Context(), in, savings.TransferPlan{
		RequestKey: key(), Move: []savings.SavingsUse{{Amount: "20.00"}},
	})
	if !errors.Is(err, savings.ErrNotLinkable) || !errors.Is(err, savings.ErrLiabilityAccount) {
		t.Errorf("a plan onto a card = %v, want ErrNotLinkable and ErrLiabilityAccount", err)
	}
	if _, err := e.app.RecordTransfer(t.Context(), in, savings.TransferPlan{}); err != nil {
		t.Errorf("the same transfer without a plan was refused: %v", err)
	}
}

// A payment from a USD account into a card lowers the USD balance like any
// outflow; if that leaves less than is reserved, M7 reports the account
// overallocated, as it always has. The payment itself is never refused.
func TestAPaymentFromUSDIsAnOutflowM7AlreadyReports(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "100")
	e.reserve(e.usd, "80")
	card := e.card("Visa USD", "USD")

	if _, err := e.app.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: e.usd, DestinationAccountID: card, Amount: "60.00", EffectiveDate: "2026-10-10",
	}, savings.TransferPlan{}); err != nil {
		t.Fatalf("paying the card: %v", err)
	}
	if issueOf(e.issues(), savings.IssueOverallocated) == nil {
		t.Error("no OVERALLOCATED issue after paying the card with reserved dollars")
	}
}

// A refund is not an income and never produces a suggestion, wherever it lands.
func TestARefundIsNeverSavedFrom(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	if _, err := e.app.RecordRefund(t.Context(), key(), ledger.RecordRefundInput{
		AccountID: e.usd, CategoryID: e.food, Amount: "30.00", PaymentMethodID: bankTransfer, EffectiveDate: "2026-10-10",
	}); err != nil {
		t.Fatalf("RecordRefund: %v", err)
	}
	if n := e.count("savings_suggestions"); n != 0 {
		t.Errorf("%d suggestions after a refund, want 0", n)
	}
}
