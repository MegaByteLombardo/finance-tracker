package creditcard_test

import (
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// The scenarios of plan R2.4 use whole units: 10.000 is 10000 here. The
// engine is unit-agnostic, so the numbers are the plan's, exactly.

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := database.ParseDate(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return d
}

func i64(v int64) *int64 { return &v }

func statement(t *testing.T, id int64, start, closing, due string, owed int64, minimum *int64) creditcard.Statement {
	t.Helper()
	s := creditcard.Statement{
		ID: id, CardAccountID: 1, PeriodStart: day(t, start), ClosingDate: day(t, closing),
		ClosingBalanceMinor: -owed, MinimumPaymentMinor: minimum,
	}
	if due != "" {
		d := day(t, due)
		s.DueDate = &d
	}
	return s
}

func purchase(t *testing.T, id int64, date string, amount int64) creditcard.Movement {
	return creditcard.Movement{TransactionID: id, Type: ledger.Expense, Date: day(t, date), AmountMinor: -amount}
}

func payment(t *testing.T, id int64, date string, amount int64) creditcard.Movement {
	return creditcard.Movement{TransactionID: id, Type: ledger.Transfer, Date: day(t, date), AmountMinor: amount}
}

func refund(t *testing.T, id int64, date string, amount int64) creditcard.Movement {
	return creditcard.Movement{TransactionID: id, Type: ledger.Refund, Date: day(t, date), AmountMinor: amount}
}

func card25() creditcard.Card {
	return creditcard.Card{AccountID: 1, StatementDay: 25, RefundPolicy: creditcard.RefundNextStatement}
}

// september and october are R2.4's common data.
func september(t *testing.T) creditcard.Statement {
	return statement(t, 1, "2026-08-26", "2026-09-25", "2026-10-15", 10000, i64(1000))
}

func october(t *testing.T, minimum int64) creditcard.Statement {
	s := statement(t, 2, "2026-09-26", "2026-10-25", "2026-11-14", 18500, i64(minimum))
	s.PastDueMinor = i64(700)
	return s
}

func cycleClosing(t *testing.T, st creditcard.Standing, closing string) creditcard.Cycle {
	t.Helper()
	for _, c := range st.Cycles {
		if c.ClosingDate.Equal(day(t, closing)) {
			return c
		}
	}
	t.Fatalf("no cycle closes on %s", closing)
	return creditcard.Cycle{}
}

func allocationOf(t *testing.T, st creditcard.Standing, id int64) creditcard.PaymentAllocation {
	t.Helper()
	for _, a := range st.Payments {
		if a.TransactionID == id {
			return a
		}
	}
	t.Fatalf("payment %d was not allocated", id)
	return creditcard.PaymentAllocation{}
}

type line struct {
	target creditcard.Target
	amount int64
}

func lines(a creditcard.PaymentAllocation) []line {
	out := make([]line, 0, len(a.Lines))
	for _, l := range a.Lines {
		out = append(out, line{l.Target, l.AmountMinor})
	}
	return out
}

// E1: two statements pending at once and a partial payment. One payment
// reduces two obligations without being counted twice.
func TestE1TwoPendingStatementsAndAPartialPayment(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{september(t), october(t, 2500)},
		Movements: []creditcard.Movement{
			purchase(t, 10, "2026-09-10", 10000),
			payment(t, 11, "2026-10-05", 300),
			purchase(t, 12, "2026-10-10", 8800),
			payment(t, 13, "2026-10-30", 1000),
		},
	}

	// After the due date of September: 700 of the minimum unmet, 9.700 of the
	// billed balance not paid.
	st := creditcard.Derive(facts, day(t, "2026-10-16"))
	sep := cycleClosing(t, st, "2026-09-25")
	if sep.MissedMinimumMinor == nil || *sep.MissedMinimumMinor != 700 ||
		sep.UnpaidAtDueMinor == nil || *sep.UnpaidAtDueMinor != 9700 || sep.Status != creditcard.StatusOverdue {
		t.Errorf("September on 10-16: missed %v, unpaid %v, status %s", sep.MissedMinimumMinor, sep.UnpaidAtDueMinor, sep.Status)
	}

	// On 10-28, October is in force: V = 700, its minimum 2.500 pending, 18.500 billed.
	st = creditcard.Derive(facts, day(t, "2026-10-28"))
	if st.PastDueMinor != 700 || !reflect.DeepEqual(st.PastDueCycles, []time.Time{day(t, "2026-09-25")}) ||
		st.MinimumPendingMinor == nil || *st.MinimumPendingMinor != 2500 || st.BilledPendingMinor != 18500 {
		t.Errorf("on 10-28: V %d %v, minimum %v, billed %d", st.PastDueMinor, st.PastDueCycles, st.MinimumPendingMinor, st.BilledPendingMinor)
	}
	if c, ok := st.Current(); !ok || !c.ClosingDate.Equal(day(t, "2026-10-25")) || c.Status != creditcard.StatusBilled {
		t.Errorf("the statement in force on 10-28 is %+v", c)
	}

	// The payment of 10-30: 700 to the overdue September, 300 to October's minimum.
	st = creditcard.Derive(facts, day(t, "2026-10-31"))
	a := allocationOf(t, st, 13)
	if got, want := lines(a), []line{{creditcard.TargetOverdue, 700}, {creditcard.TargetMinimum, 300}}; !reflect.DeepEqual(got, want) {
		t.Errorf("payment of 10-30 = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(a.Lines[0].Cycles, []time.Time{day(t, "2026-09-25")}) {
		t.Errorf("the overdue line names %v", a.Lines[0].Cycles)
	}
	if st.PastDueMinor != 0 || *st.MinimumPendingMinor != 1500 || st.BilledPendingMinor != 17500 {
		t.Errorf("after it: V %d, minimum %d, billed %d", st.PastDueMinor, *st.MinimumPendingMinor, st.BilledPendingMinor)
	}
	// The earlier payment went to September's minimum when it was made.
	if got, want := lines(allocationOf(t, st, 11)), []line{{creditcard.TargetMinimum, 300}}; !reflect.DeepEqual(got, want) {
		t.Errorf("payment of 10-05 = %v, want %v", got, want)
	}
	// The minimum of September, a fact of its cycle, stays missed.
	if sep := cycleClosing(t, st, "2026-09-25"); *sep.MissedMinimumMinor != 700 {
		t.Errorf("September's missed minimum moved to %d", *sep.MissedMinimumMinor)
	}
}

// E2: a payment in advance goes entirely to PREPAID, and the computed October
// already discounts it.
func TestE2PrepaidPayment(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{september(t)},
		Movements: []creditcard.Movement{
			purchase(t, 10, "2026-09-10", 10000),
			payment(t, 11, "2026-10-05", 10000),
			purchase(t, 12, "2026-10-12", 2000),
			payment(t, 13, "2026-10-20", 500),
		},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-21"))
	if got, want := lines(allocationOf(t, st, 13)), []line{{creditcard.TargetPrepaid, 500}}; !reflect.DeepEqual(got, want) {
		t.Errorf("payment of 10-20 = %v, want %v", got, want)
	}
	if st.PrepaidMinor != 500 || st.BilledPendingMinor != 0 {
		t.Errorf("prepaid %d, billed pending %d", st.PrepaidMinor, st.BilledPendingMinor)
	}
	if sep := cycleClosing(t, st, "2026-09-25"); sep.Status != creditcard.StatusPaid {
		t.Errorf("September is %s", sep.Status)
	}

	st = creditcard.Derive(facts, day(t, "2026-11-01"))
	oct := cycleClosing(t, st, "2026-10-25")
	if oct.Registered() || oct.OwedMinor != 1500 || oct.LedgerOwedMinor != 1500 {
		t.Errorf("computed October: registered %v, owed %d, ledger %d", oct.Registered(), oct.OwedMinor, oct.LedgerOwedMinor)
	}
}

// E3: a refund after the closing lowers the debt at once; what it does to the
// billed balance is the card's policy; the minimum is never touched.
func TestE3RefundAfterTheClosing(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{september(t)},
		Movements: []creditcard.Movement{
			purchase(t, 10, "2026-08-20", 2000),
			purchase(t, 11, "2026-09-10", 8000),
			refund(t, 12, "2026-10-02", 2000),
		},
	}
	before := creditcard.Derive(facts, day(t, "2026-10-01"))
	st := creditcard.Derive(facts, day(t, "2026-10-05"))
	if before.DebtMinor-st.DebtMinor != 2000 {
		t.Errorf("the refund lowered the debt by %d", before.DebtMinor-st.DebtMinor)
	}
	if st.BilledPendingMinor != 10000 || st.BilledNetOfRefundsMinor != 8000 || st.RefundsAfterClosingMinor != 2000 ||
		*st.MinimumPendingMinor != 1000 {
		t.Errorf("NEXT_STATEMENT: billed %d, net %d, refunds %d, minimum %d",
			st.BilledPendingMinor, st.BilledNetOfRefundsMinor, st.RefundsAfterClosingMinor, *st.MinimumPendingMinor)
	}

	facts.Card.RefundPolicy = creditcard.RefundReducesBilled
	st = creditcard.Derive(facts, day(t, "2026-10-05"))
	if st.BilledPendingMinor != 8000 || *st.MinimumPendingMinor != 1000 {
		t.Errorf("REDUCES_BILLED: billed %d, minimum %d", st.BilledPendingMinor, *st.MinimumPendingMinor)
	}

	// A refund is never a payment: after the due date the minimum is still unmet.
	st = creditcard.Derive(facts, day(t, "2026-10-20"))
	if st.PastDueMinor != 1000 || len(st.Payments) != 0 {
		t.Errorf("after the due date: V %d, payments %v", st.PastDueMinor, st.Payments)
	}
}

// E4: interest and a fee dated on the closing are debits of that cycle, an
// obligation like any other.
func TestE4ChargesAreDebitsOfTheirCycle(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card: card25(),
		Movements: []creditcard.Movement{
			purchase(t, 10, "2026-10-10", 10000),
			{TransactionID: 11, Type: ledger.Expense, Date: day(t, "2026-10-25"), AmountMinor: -300},
			{TransactionID: 12, Type: ledger.Expense, Date: day(t, "2026-10-25"), AmountMinor: -100},
		},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-26"))
	oct := cycleClosing(t, st, "2026-10-25")
	if oct.DebitsMinor != 10400 || oct.BilledMinor != 10400 || oct.Registered() {
		t.Errorf("October: debits %d, billed %d", oct.DebitsMinor, oct.BilledMinor)
	}
}

// E5: a credit balance bills nothing; every payment is prepaid or a credit.
func TestE5CreditBalance(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{statement(t, 1, "2026-08-26", "2026-09-25", "2026-10-15", -500, i64(0))},
		Movements: []creditcard.Movement{
			purchase(t, 10, "2026-10-03", 300),
			payment(t, 11, "2026-10-05", 1000),
		},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-06"))
	sep := cycleClosing(t, st, "2026-09-25")
	if sep.BilledMinor != 0 || sep.Status != creditcard.StatusPaid {
		t.Errorf("September: billed %d, %s", sep.BilledMinor, sep.Status)
	}
	if got, want := lines(allocationOf(t, st, 11)), []line{{creditcard.TargetPrepaid, 300}, {creditcard.TargetCreditBalance, 700}}; !reflect.DeepEqual(got, want) {
		t.Errorf("payment = %v, want %v", got, want)
	}
}

// E6: a purchase recorded late changes the ledger, never the bank's figure.
func TestE6RetroactivePurchase(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{september(t)},
		Movements:  []creditcard.Movement{purchase(t, 10, "2026-09-10", 7000)},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-01"))
	r, ok := st.Reconcile(1)
	if !ok || r.DifferenceMinor != 3000 {
		t.Fatalf("before: difference %d (%v)", r.DifferenceMinor, ok)
	}
	facts.Movements = append(facts.Movements, purchase(t, 11, "2026-08-20", 3000))
	st = creditcard.Derive(facts, day(t, "2026-10-01"))
	r, _ = st.Reconcile(1)
	sep := cycleClosing(t, st, "2026-09-25")
	if r.DifferenceMinor != 0 || sep.BilledMinor != 10000 {
		t.Errorf("after: difference %d, billed %d", r.DifferenceMinor, sep.BilledMinor)
	}

	// Unregistered, the computed September rises by the purchase.
	facts.Statements = nil
	st = creditcard.Derive(facts, day(t, "2026-10-01"))
	if sep := cycleClosing(t, st, "2026-09-25"); sep.BilledMinor != 10000 || sep.Registered() {
		t.Errorf("computed September billed %d", sep.BilledMinor)
	}
}

// E7: a corrected minimum re-derives everything; 700 + 300 still holds.
func TestE7CorrectedStatement(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{september(t), october(t, 2600)},
		Movements:  []creditcard.Movement{payment(t, 11, "2026-10-05", 300), payment(t, 13, "2026-10-30", 1000)},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-31"))
	a := allocationOf(t, st, 13)
	if got, want := lines(a), []line{{creditcard.TargetOverdue, 700}, {creditcard.TargetMinimum, 300}}; !reflect.DeepEqual(got, want) {
		t.Errorf("payment = %v, want %v", got, want)
	}
	if a.Thresholds.OverdueMinor != 700 || a.Thresholds.MinimumMinor != 2600 || a.Clamped {
		t.Errorf("thresholds %+v, clamped %v", a.Thresholds, a.Clamped)
	}
}

// V is the largest unmet minimum, never the sum: one payment raises every P_j.
func TestPastDueIsTheMaximum(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card: card25(),
		Statements: []creditcard.Statement{
			statement(t, 1, "2026-07-26", "2026-08-25", "2026-09-15", 5000, i64(500)),
			september(t),
		},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-20"))
	if st.PastDueMinor != 1000 || len(st.PastDueCycles) != 2 {
		t.Errorf("V = %d over %v, want 1000 over two cycles", st.PastDueMinor, st.PastDueCycles)
	}
}

// An unknown minimum removes the minimum segment; an unknown due date never
// makes anything overdue, and an estimated one does.
func TestUnknownMinimumAndDueDate(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{statement(t, 1, "2026-08-26", "2026-09-25", "", 10000, nil)},
		Movements:  []creditcard.Movement{payment(t, 11, "2026-10-05", 300)},
	}
	st := creditcard.Derive(facts, day(t, "2026-12-01"))
	if st.MinimumPendingMinor != nil || st.PastDueMinor != 0 {
		t.Errorf("minimum %v, V %d", st.MinimumPendingMinor, st.PastDueMinor)
	}
	if sep := cycleClosing(t, st, "2026-09-25"); sep.Status != creditcard.StatusDueUnknown || sep.DueDate != nil {
		t.Errorf("September is %s, due %v", sep.Status, sep.DueDate)
	}
	if got, want := lines(allocationOf(t, st, 11)), []line{{creditcard.TargetBilled, 300}}; !reflect.DeepEqual(got, want) {
		t.Errorf("payment = %v, want %v", got, want)
	}

	// With a minimum and the terms' due days, the due date is estimated and
	// does make the cycle overdue.
	facts.Statements[0].MinimumPaymentMinor = i64(1000)
	days := 20
	facts.Card.DueDaysAfterStatement = &days
	st = creditcard.Derive(facts, day(t, "2026-10-16"))
	sep := cycleClosing(t, st, "2026-09-25")
	if !sep.DueEstimated || !sep.DueDate.Equal(day(t, "2026-10-15")) || sep.Status != creditcard.StatusOverdue || st.PastDueMinor != 700 {
		t.Errorf("estimated due %v (%v), status %s, V %d", sep.DueDate, sep.DueEstimated, sep.Status, st.PastDueMinor)
	}
}

// With no statement in force, a payment is prepaid up to what the open cycle
// owes and a credit balance beyond it.
func TestNoStatementInForce(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:      card25(),
		Movements: []creditcard.Movement{purchase(t, 10, "2026-10-05", 400), payment(t, 11, "2026-10-10", 1000)},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-12"))
	if _, ok := st.Current(); ok || st.BilledPendingMinor != 0 || st.MinimumPendingMinor != nil {
		t.Errorf("a statement is in force: %+v", st)
	}
	a := allocationOf(t, st, 11)
	if got, want := lines(a), []line{{creditcard.TargetPrepaid, 400}, {creditcard.TargetCreditBalance, 600}}; !reflect.DeepEqual(got, want) || a.Against != nil {
		t.Errorf("payment = %v against %v, want %v", got, a.Against, want)
	}
	if st.CreditBalanceMinor != 600 || st.Open().Status != creditcard.StatusOpen {
		t.Errorf("credit %d, open %s", st.CreditBalanceMinor, st.Open().Status)
	}
}

// A minimum below what is overdue is bounded so the line stays monotone, and
// the payment says so.
func TestClampedThresholds(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{september(t), october(t, 500)},
		Movements:  []creditcard.Movement{payment(t, 11, "2026-10-05", 300), payment(t, 13, "2026-10-30", 1000)},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-31"))
	a := allocationOf(t, st, 13)
	if !a.Clamped || a.Thresholds.MinimumMinor != 700 {
		t.Errorf("clamped %v, thresholds %+v", a.Clamped, a.Thresholds)
	}
	if got, want := lines(a), []line{{creditcard.TargetOverdue, 700}, {creditcard.TargetBilled, 300}}; !reflect.DeepEqual(got, want) {
		t.Errorf("payment = %v, want %v", got, want)
	}
	found := false
	for _, w := range st.Warnings {
		found = found || (w.Kind == creditcard.WarningThresholdsClamped && w.TransactionID == 13)
	}
	if !found {
		t.Errorf("no warning for the clamped payment: %v", st.Warnings)
	}
}

// The calendar: the 25th, a year change, the 31st in February and a leap year.
func TestCalendar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		statementDay int
		date, want   string
	}{
		{25, "2026-10-25", "2026-10-25"},
		{25, "2026-10-26", "2026-11-25"},
		{25, "2026-12-26", "2027-01-25"},
		{25, "2026-12-25", "2026-12-25"},
		{31, "2026-02-10", "2026-02-28"},
		{31, "2028-02-10", "2028-02-29"},
		{31, "2026-03-01", "2026-03-31"},
		{30, "2026-02-28", "2026-02-28"},
		{30, "2026-03-01", "2026-03-30"},
		{29, "2027-02-28", "2027-02-28"},
		{1, "2026-01-01", "2026-01-01"},
		{1, "2026-01-02", "2026-02-01"},
	} {
		if got := creditcard.CalendarClosingFor(tc.statementDay, day(t, tc.date)); !got.Equal(day(t, tc.want)) {
			t.Errorf("day %d, %s belongs to the cycle closing %s, want %s",
				tc.statementDay, tc.date, database.FormatDate(got), tc.want)
		}
	}
}

// A registered statement replaces the calendar's nearest closing, so a bank
// that closed a day early does not open a one-day cycle; an assertion moves a
// movement between statements and an impossible one is reported.
func TestMembership(t *testing.T) {
	t.Parallel()
	early := statement(t, 1, "2026-09-26", "2026-10-23", "2026-11-12", 0, nil)
	nov := statement(t, 2, "2026-10-24", "2026-11-25", "", 0, nil)
	jan := statement(t, 3, "2026-12-26", "2027-01-25", "", 0, nil)
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{early, nov, jan},
		Movements: []creditcard.Movement{
			purchase(t, 10, "2026-10-23", 100), // the early closing day: October
			purchase(t, 11, "2026-10-24", 100), // the next day: November, no 10-25 cycle
			purchase(t, 12, "2026-10-22", 100), // asserted into November
			payment(t, 13, "2026-10-24", 100),  // asserted into October, which closed before it
			purchase(t, 14, "2026-12-24", 100), // asserted across the year into January
		},
		Assertions: map[int64]int64{12: 2, 13: 1, 14: 3, 99: 2},
	}
	st := creditcard.Derive(facts, day(t, "2027-02-01"))
	for _, c := range st.Cycles {
		if c.ClosingDate.Equal(day(t, "2026-10-25")) {
			t.Error("a one-day calendar cycle closing 2026-10-25 survived beside the early statement")
		}
	}
	want := map[int64]struct {
		closing  string
		asserted bool
	}{
		10: {"2026-10-23", false}, 11: {"2026-11-25", false}, 12: {"2026-11-25", true},
		13: {"2026-11-25", false}, 14: {"2027-01-25", true},
	}
	for _, p := range st.Placed {
		w := want[p.TransactionID]
		if !p.ClosingDate.Equal(day(t, w.closing)) || p.Asserted != w.asserted {
			t.Errorf("transaction %d belongs to %s (asserted %v), want %s (%v)",
				p.TransactionID, database.FormatDate(p.ClosingDate), p.Asserted, w.closing, w.asserted)
		}
	}
	kinds := map[int64]creditcard.WarningKind{}
	for _, w := range st.Warnings {
		kinds[w.TransactionID] = w.Kind
	}
	if kinds[13] != creditcard.WarningAssertionBeforeMovement || kinds[99] != creditcard.WarningAssertionOrphan {
		t.Errorf("warnings = %v", st.Warnings)
	}
}

// A future-dated movement takes no part in any figure before its date.
func TestFutureMovementsAreLeftOut(t *testing.T) {
	t.Parallel()
	facts := creditcard.Facts{
		Card:       card25(),
		Statements: []creditcard.Statement{september(t)},
		Movements:  []creditcard.Movement{payment(t, 11, "2026-10-20", 1000)},
	}
	st := creditcard.Derive(facts, day(t, "2026-10-10"))
	if len(st.Payments) != 0 || st.BalanceMinor != 0 || *st.MinimumPendingMinor != 1000 {
		t.Errorf("a payment of 10-20 counted on 10-10: %+v", st)
	}
}

// I5: whatever the facts, every payment is partitioned exactly: its lines are
// positive, in the order of the line, and add up to its amount.
func TestAllocationIsAPartition(t *testing.T) {
	t.Parallel()
	order := map[creditcard.Target]int{
		creditcard.TargetOverdue: 0, creditcard.TargetMinimum: 1, creditcard.TargetBilled: 2,
		creditcard.TargetPrepaid: 3, creditcard.TargetCreditBalance: 4,
	}
	rng := rand.New(rand.NewPCG(8, 2))
	start := day(t, "2026-01-01")
	for run := 0; run < 400; run++ {
		facts := creditcard.Facts{
			Card:       creditcard.Card{AccountID: 1, StatementDay: 1 + rng.IntN(31), RefundPolicy: creditcard.RefundPolicies()[rng.IntN(2)]},
			Assertions: map[int64]int64{},
		}
		if rng.IntN(2) == 0 {
			days := 1 + rng.IntN(30)
			facts.Card.DueDaysAfterStatement = &days
		}
		// Statements on some of twelve consecutive calendar closings.
		var previous time.Time
		for m := 0; m < 12; m++ {
			closing := creditcard.ClosingDate(facts.Card.StatementDay, 2026, time.Month(1+m))
			if rng.IntN(2) == 0 {
				periodStart := closing.AddDate(0, -1, 1)
				if !previous.IsZero() {
					periodStart = previous.AddDate(0, 0, 1)
				}
				s := creditcard.Statement{
					ID: int64(m + 1), CardAccountID: 1, PeriodStart: periodStart, ClosingDate: closing,
					ClosingBalanceMinor: -int64(rng.IntN(20000) - 2000),
				}
				if rng.IntN(3) > 0 {
					s.MinimumPaymentMinor = i64(int64(rng.IntN(3000)))
				}
				if rng.IntN(3) > 0 {
					due := closing.AddDate(0, 0, 1+rng.IntN(25))
					s.DueDate = &due
				}
				facts.Statements = append(facts.Statements, s)
			}
			previous = closing
		}
		for id := int64(1); id <= 40; id++ {
			m := creditcard.Movement{TransactionID: id, Date: start.AddDate(0, 0, rng.IntN(365))}
			switch rng.IntN(4) {
			case 0:
				m.Type, m.AmountMinor = ledger.Transfer, int64(1+rng.IntN(6000))
			case 1:
				m.Type, m.AmountMinor = ledger.FXExchange, int64(1+rng.IntN(6000))
			case 2:
				m.Type, m.AmountMinor = ledger.Refund, int64(1+rng.IntN(800))
			default:
				m.Type, m.AmountMinor = ledger.Expense, -int64(1+rng.IntN(4000))
			}
			facts.Movements = append(facts.Movements, m)
			if len(facts.Statements) > 0 && rng.IntN(6) == 0 {
				facts.Assertions[id] = facts.Statements[rng.IntN(len(facts.Statements))].ID
			}
		}
		asOf := start.AddDate(0, 0, 30+rng.IntN(370))
		st := creditcard.Derive(facts, asOf)
		for _, a := range st.Payments {
			var sum int64
			last := -1
			for _, l := range a.Lines {
				if l.AmountMinor <= 0 || order[l.Target] <= last {
					t.Fatalf("run %d: payment %d has lines %+v", run, a.TransactionID, a.Lines)
				}
				last = order[l.Target]
				sum += l.AmountMinor
			}
			if sum != a.AmountMinor {
				t.Fatalf("run %d: payment %d of %d was allocated %d: %+v", run, a.TransactionID, a.AmountMinor, sum, a.Lines)
			}
			th := a.Thresholds
			if th.OverdueMinor > th.MinimumMinor || th.MinimumMinor > th.BilledMinor || th.UnbilledMinor < 0 {
				t.Fatalf("run %d: thresholds out of order: %+v", run, th)
			}
		}
	}
}
