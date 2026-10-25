package creditcard_test

import (
	"errors"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// November 1st, after the October closing, for the statement tests.
func november() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, database.EconomicLocation) }

type statementFixture struct {
	cards    *creditcard.Service
	card     int64
	purchase func(amount, date string) int64
}

func newStatementFixture(t *testing.T) statementFixture {
	t.Helper()
	db := dbtest.New(t)
	ctx := t.Context()
	app := application.NewService(db, savings.NewService(db))
	cards := creditcard.NewService(db)
	creditcard.SetClock(cards, november)

	opened, err := app.OpenCreditCard(ctx, application.OpenCreditCardInput{
		RequestKey: key(), Name: "Plaza Black", CurrencyCode: "VES",
		Terms: creditcard.TermsInput{StatementDay: 25},
	})
	if err != nil {
		t.Fatalf("OpenCreditCard: %v", err)
	}
	food, err := category.NewService(db).Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense})
	if err != nil {
		t.Fatalf("creating a category: %v", err)
	}
	f := statementFixture{cards: cards, card: opened.Account.ID}
	f.purchase = func(amount, date string) int64 {
		t.Helper()
		view, err := app.RecordCardPurchase(ctx, key(), ledger.RecordExpenseInput{
			AccountID: f.card, CategoryID: food.ID, Amount: amount, PaymentMethodID: 2, EffectiveDate: date,
		})
		if err != nil {
			t.Fatalf("RecordCardPurchase: %v", err)
		}
		return view.ID
	}
	return f
}

func septemberInput() creditcard.StatementInput {
	return creditcard.StatementInput{
		PeriodStart: "2026-08-26", ClosingDate: "2026-09-25", DueDate: "2026-10-15",
		ClosingOwed: "10000.00", MinimumPayment: "1000.00",
	}
}

// A statement is recorded as the bank printed it; the same one twice is a
// replay; a different one on the same closing, an overlapping period, a due
// date not after the closing or a closing after today are refused.
func TestRecordStatement(t *testing.T) {
	t.Parallel()
	f := newStatementFixture(t)
	ctx := t.Context()

	sep, err := f.cards.RecordStatement(ctx, f.card, septemberInput())
	if err != nil {
		t.Fatalf("RecordStatement: %v", err)
	}
	if sep.ClosingBalanceMinor != -1000000 || sep.OwedMinor() != 1000000 || *sep.MinimumPaymentMinor != 100000 ||
		!sep.DueDate.Equal(day(t, "2026-10-15")) || sep.PaymentsMinor != nil {
		t.Errorf("recorded %+v", sep)
	}
	again, err := f.cards.RecordStatement(ctx, f.card, septemberInput())
	if err != nil || again.ID != sep.ID {
		t.Errorf("the same statement twice = %d, %v; want %d", again.ID, err, sep.ID)
	}

	different := septemberInput()
	different.ClosingOwed = "10500.00"
	if _, err := f.cards.RecordStatement(ctx, f.card, different); !errors.Is(err, creditcard.ErrStatementExists) {
		t.Errorf("a different statement on the same closing = %v", err)
	}
	overlapping := creditcard.StatementInput{PeriodStart: "2026-09-20", ClosingDate: "2026-10-25", ClosingOwed: "0"}
	if _, err := f.cards.RecordStatement(ctx, f.card, overlapping); !errors.Is(err, creditcard.ErrStatementOverlap) {
		t.Errorf("an overlapping period = %v", err)
	}
	for name, in := range map[string]creditcard.StatementInput{
		"due on the closing":   {PeriodStart: "2026-09-26", ClosingDate: "2026-10-25", DueDate: "2026-10-25", ClosingOwed: "0"},
		"a negative minimum":   {PeriodStart: "2026-09-26", ClosingDate: "2026-10-25", ClosingOwed: "0", MinimumPayment: "-1"},
		"no balance":           {PeriodStart: "2026-09-26", ClosingDate: "2026-10-25"},
		"a closing in future":  {PeriodStart: "2026-10-26", ClosingDate: "2026-11-25", ClosingOwed: "0"},
		"too many decimals":    {PeriodStart: "2026-09-26", ClosingDate: "2026-10-25", ClosingOwed: "1.005"},
		"start after closing":  {PeriodStart: "2026-10-26", ClosingDate: "2026-10-25", ClosingOwed: "0"},
		"an impossible date":   {PeriodStart: "2026-09-26", ClosingDate: "2026-09-31", ClosingOwed: "0"},
		"negative past due":    {PeriodStart: "2026-09-26", ClosingDate: "2026-10-25", ClosingOwed: "0", PastDue: "-5"},
		"payments in letters":  {PeriodStart: "2026-09-26", ClosingDate: "2026-10-25", ClosingOwed: "0", Payments: "mucho"},
		"missing period start": {ClosingDate: "2026-10-25", ClosingOwed: "0"},
	} {
		if _, err := f.cards.RecordStatement(ctx, f.card, in); !errors.Is(err, creditcard.ErrInvalidStatement) {
			t.Errorf("%s = %v, want ErrInvalidStatement", name, err)
		}
	}

	// A credit balance is typed negative.
	oct, err := f.cards.RecordStatement(ctx, f.card, creditcard.StatementInput{
		PeriodStart: "2026-09-26", ClosingDate: "2026-10-25", ClosingOwed: "-500.00", PreviousOwed: "10000.00",
	})
	if err != nil {
		t.Fatalf("RecordStatement October: %v", err)
	}
	if oct.ClosingBalanceMinor != 50000 || *oct.PreviousBalanceMinor != -1000000 {
		t.Errorf("October recorded %+v", oct)
	}

	view, err := f.cards.Standing(ctx, f.card)
	if err != nil {
		t.Fatalf("Standing: %v", err)
	}
	current, ok := view.Standing.Current()
	if !ok || current.Statement == nil || current.Statement.ID != oct.ID {
		t.Errorf("the statement in force is %+v", current)
	}
	start, closing, err := f.cards.NextStatementDates(ctx, f.card)
	if err != nil || !start.Equal(day(t, "2026-10-26")) || !closing.Equal(day(t, "2026-11-25")) {
		t.Errorf("NextStatementDates = %s, %s, %v", start, closing, err)
	}
}

// An assertion ties a movement of the card to a statement closing on or after
// it; a statement's closing cannot move before a movement asserted into it.
func TestAssertMembership(t *testing.T) {
	t.Parallel()
	f := newStatementFixture(t)
	ctx := t.Context()
	onClosing := f.purchase("100.00", "2026-09-25")
	afterClosing := f.purchase("200.00", "2026-09-26")
	lastDay := f.purchase("300.00", "2026-10-25")

	sep, err := f.cards.RecordStatement(ctx, f.card, septemberInput())
	if err != nil {
		t.Fatalf("RecordStatement: %v", err)
	}
	oct, err := f.cards.RecordStatement(ctx, f.card, creditcard.StatementInput{
		PeriodStart: "2026-09-26", ClosingDate: "2026-10-25", ClosingOwed: "10600.00",
	})
	if err != nil {
		t.Fatalf("RecordStatement October: %v", err)
	}

	if err := f.cards.AssertMembership(ctx, f.card, afterClosing, &sep.ID); !errors.Is(err, creditcard.ErrAssertionAfterClosing) {
		t.Errorf("asserting 09-26 into the statement of 09-25 = %v", err)
	}
	if err := f.cards.AssertMembership(ctx, f.card, onClosing, &oct.ID); err != nil {
		t.Fatalf("asserting the closing day's purchase into October: %v", err)
	}
	if err := f.cards.AssertMembership(ctx, f.card, lastDay, &oct.ID); err != nil {
		t.Fatalf("asserting 10-25 into October: %v", err)
	}
	if err := f.cards.AssertMembership(ctx, f.card, 9999, &oct.ID); !errors.Is(err, creditcard.ErrNotACardMovement) {
		t.Errorf("asserting a transaction of nothing = %v", err)
	}

	view, err := f.cards.Standing(ctx, f.card)
	if err != nil {
		t.Fatalf("Standing: %v", err)
	}
	for _, p := range view.Standing.Placed {
		if p.TransactionID == onClosing && (!p.Asserted || !p.ClosingDate.Equal(day(t, "2026-10-25"))) {
			t.Errorf("the closing day's purchase is placed %+v", p)
		}
	}
	r, ok := view.Standing.Reconcile(sep.ID)
	if !ok || r.LedgerOwedMinor != 0 || r.DifferenceMinor != 1000000 {
		t.Errorf("September reconciliation: ledger %d, difference %d", r.LedgerOwedMinor, r.DifferenceMinor)
	}

	// October cannot close on 10-24 while a movement of 10-25 is asserted into it.
	moved := creditcard.StatementInput{PeriodStart: "2026-09-26", ClosingDate: "2026-10-24", ClosingOwed: "10600.00"}
	if _, err := f.cards.UpdateStatement(ctx, f.card, oct.ID, moved); !errors.Is(err, creditcard.ErrAssertionAfterClosing) {
		t.Errorf("moving the closing before an asserted movement = %v", err)
	}
	// Withdrawn, the calendar decides again and the closing can move.
	if err := f.cards.AssertMembership(ctx, f.card, lastDay, nil); err != nil {
		t.Fatalf("withdrawing the assertion: %v", err)
	}
	if _, err := f.cards.UpdateStatement(ctx, f.card, oct.ID, moved); err != nil {
		t.Errorf("moving the closing after the withdrawal: %v", err)
	}
	// The minimum is corrected and every figure follows (E7).
	moved.MinimumPayment = "2600.00"
	updated, err := f.cards.UpdateStatement(ctx, f.card, oct.ID, moved)
	if err != nil || *updated.MinimumPaymentMinor != 260000 {
		t.Errorf("correcting the minimum = %+v, %v", updated, err)
	}
}

// The refund policy is a term of the card.
func TestRefundPolicyIsATerm(t *testing.T) {
	t.Parallel()
	f := newStatementFixture(t)
	ctx := t.Context()
	view, err := f.cards.Get(ctx, f.card)
	if err != nil || view.Card.RefundPolicy != creditcard.RefundNextStatement {
		t.Fatalf("a new card's policy is %q (%v)", view.Card.RefundPolicy, err)
	}
	updated, err := f.cards.UpdateTerms(ctx, f.card, creditcard.TermsInput{StatementDay: 25, RefundPolicy: "reduces_billed"})
	if err != nil || updated.RefundPolicy != creditcard.RefundReducesBilled {
		t.Errorf("UpdateTerms = %q, %v", updated.RefundPolicy, err)
	}
	if _, err := f.cards.UpdateTerms(ctx, f.card, creditcard.TermsInput{StatementDay: 25, RefundPolicy: "SOMETIMES"}); !errors.Is(err, creditcard.ErrInvalidInput) {
		t.Errorf("an unknown policy = %v", err)
	}
}
