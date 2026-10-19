package savings_test

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
	"github.com/MegaByteLombardo/finance-tracker/internal/settings"
	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
)

// --- helpers -----------------------------------------------------------------

var keySeq atomic.Int64

// key returns a fresh idempotency token, as a form would carry.
func key() string { return fmt.Sprintf("test-key-%012d", keySeq.Add(1)) }

// open gives an account a starting balance dated before "today".
func (e *env) open(accountID int64, amount string) {
	e.t.Helper()
	if _, err := e.ledger.RecordOpeningBalance(e.t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID: accountID, Amount: amount, EffectiveDate: "2026-10-01",
	}); err != nil {
		e.t.Fatalf("opening balance: %v", err)
	}
}

func (e *env) reserve(accountID int64, amount string) savings.Operation {
	e.t.Helper()
	op, err := e.svc.Reserve(e.t.Context(), savings.ReserveInput{
		RequestKey: key(), Lines: []savings.ReserveLine{{AccountID: accountID, Amount: amount}},
	})
	if err != nil {
		e.t.Fatalf("reserving %s: %v", amount, err)
	}
	return op
}

func (e *env) confirm(incomeID, accountID int64, amount string) (savings.Operation, error) {
	e.t.Helper()
	return e.svc.Reserve(e.t.Context(), savings.ReserveInput{
		RequestKey: key(), IncomeTransactionID: &incomeID,
		Lines: []savings.ReserveLine{{AccountID: accountID, Amount: amount}},
	})
}

func (e *env) fund() savings.Fund {
	e.t.Helper()
	f, err := e.svc.GetFund(e.t.Context())
	if err != nil {
		e.t.Fatalf("GetFund: %v", err)
	}
	return f
}

func (e *env) availability(accountID int64) savings.AccountAvailability {
	e.t.Helper()
	a, ok := e.fund().Account(accountID)
	if !ok {
		e.t.Fatalf("account %d is not in the fund", accountID)
	}
	return a
}

func (e *env) netWorth() int64 {
	e.t.Helper()
	v := valuation.NewService(e.ledger, e.rates, settings.NewService(e.db), currency.NewService(e.db))
	nw, err := v.GetCurrentNetWorth(e.t.Context())
	if err != nil {
		e.t.Fatalf("net worth: %v", err)
	}
	return nw.TotalMinor
}

// --- Reserved vs unreserved --------------------------------------------------

// Balance $1,000, $800 reserved: $200 free. Reserving moves no money and does
// not change the net worth.
func TestReservingMarksMoneyWithoutMovingIt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000.00")
	before := e.netWorth()
	movements := e.count("account_movements")

	op := e.reserve(e.usd, "800.00")
	if op.Kind != savings.KindReserve || op.TotalMinor != 80000 || len(op.Entries) != 1 {
		t.Fatalf("operation = %+v", op)
	}

	a := e.availability(e.usd)
	if a.BalanceMinor != 100000 || a.ReservedMinor != 80000 || a.FreeMinor != 20000 || a.OverallocatedMinor != 0 {
		t.Errorf("availability = %+v, want 1000 / 800 / 200 / 0", a)
	}
	f := e.fund()
	if f.ReservedMinor != 80000 || f.UnallocatedMinor != 80000 || f.FreeMinor != 20000 {
		t.Errorf("fund = %+v", f)
	}
	if e.netWorth() != before {
		t.Errorf("the net worth moved from %d to %d by reserving", before, e.netWorth())
	}
	if e.count("account_movements") != movements || e.count("transactions") != 1 {
		t.Error("reserving wrote to the ledger")
	}
}

// Savings that predate M7 can be reserved with no income behind them.
func TestAManualReservationNeedsNoIncome(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "500.00")
	op := e.reserve(e.usd, "500.00")
	if op.SuggestionID != nil || op.IncomeTransactionID != nil {
		t.Errorf("a manual reservation names a suggestion: %+v", op)
	}
	if e.count("transactions") != 1 {
		t.Error("a manual reservation fabricated an income")
	}
}

// A reservation never exceeds an account's free balance, and only a USD,
// active account can back one.
func TestAReservationIsBackedByFreeUSD(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	e.open(e.usd, "1000.00")
	e.open(e.ves, "1000.00")
	e.reserve(e.usd, "800.00")

	for name, c := range map[string]struct {
		account int64
		amount  string
		want    error
	}{
		"more than free":   {e.usd, "200.01", savings.ErrInsufficientFree},
		"a VES account":    {e.ves, "10", savings.ErrNotUSDAccount},
		"no account":       {9999, "10", savings.ErrAccountNotFound},
		"zero":             {e.usd, "0", savings.ErrInvalidInput},
		"three decimals":   {e.usd, "1.001", savings.ErrInvalidInput},
		"an empty balance": {e.usdTwo, "1", savings.ErrInsufficientFree},
	} {
		_, err := e.svc.Reserve(ctx, savings.ReserveInput{
			RequestKey: key(), Lines: []savings.ReserveLine{{AccountID: c.account, Amount: c.amount}},
		})
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	e.reserve(e.usd, "200.00") // exactly the free balance is fine

	if _, err := account.NewService(e.db).SetActive(ctx, e.usdSavings, false); err != nil {
		t.Fatalf("archiving: %v", err)
	}
	e.open(e.usdTwo, "50")
	_, err := e.svc.Reserve(ctx, savings.ReserveInput{
		RequestKey: key(), Lines: []savings.ReserveLine{{AccountID: e.usdSavings, Amount: "1"}},
	})
	if !errors.Is(err, savings.ErrAccountInactive) {
		t.Errorf("an archived account backed new savings: %v", err)
	}
	if _, err := e.svc.Reserve(ctx, savings.ReserveInput{RequestKey: "short",
		Lines: []savings.ReserveLine{{AccountID: e.usdTwo, Amount: "1"}}}); !errors.Is(err, savings.ErrInvalidRequestKey) {
		t.Errorf("a malformed key was accepted: %v", err)
	}
}

// --- Partial confirmation -----------------------------------------------------

// Suggested $160, confirmed $100: $60 remain, on the same suggestion. Confirming
// past it needs a recorded exception.
func TestASuggestionIsConfirmedInPartsAndNeverPastItWithoutAnException(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	e.policy("20", "2026-10-01")
	income := e.income(e.usd, "800", "2026-10-05")

	if _, err := e.confirm(income, e.usd, "100.00"); err != nil {
		t.Fatalf("confirming $100: %v", err)
	}
	s := e.live(income)
	if s.IncomeConfirmedUSDMinor != 10000 || s.RemainingUSDMinor != 6000 || s.Confirmation != savings.ConfirmationPartial {
		t.Errorf("after $100: confirmed %d remaining %d state %s", s.IncomeConfirmedUSDMinor, s.RemainingUSDMinor, s.Confirmation)
	}
	if e.count("savings_suggestions") != 1 {
		t.Error("a partial confirmation created a second suggestion")
	}

	if _, err := e.confirm(income, e.usd, "60.01"); !errors.Is(err, savings.ErrExceedsSuggestion) {
		t.Errorf("confirming past the suggestion returned %v", err)
	}
	if _, err := e.confirm(income, e.usd, "60.00"); err != nil {
		t.Fatalf("confirming the rest: %v", err)
	}
	if s := e.live(income); s.Confirmation != savings.ConfirmationFull || s.RemainingUSDMinor != 0 {
		t.Errorf("after $160: %s, remaining %d", s.Confirmation, s.RemainingUSDMinor)
	}

	_, err := e.svc.Reserve(ctx, savings.ReserveInput{RequestKey: key(), IncomeTransactionID: &income,
		Lines: []savings.ReserveLine{{AccountID: e.usd, Amount: "40"}}, Exception: true})
	if !errors.Is(err, savings.ErrInvalidInput) {
		t.Errorf("an exception without a reason returned %v", err)
	}
	op, err := e.svc.Reserve(ctx, savings.ReserveInput{RequestKey: key(), IncomeTransactionID: &income,
		Lines: []savings.ReserveLine{{AccountID: e.usd, Amount: "40"}}, Exception: true, Reason: "Bono extra"})
	if err != nil {
		t.Fatalf("an exception with a reason: %v", err)
	}
	if !op.IsException || *op.Reason != "Bono extra" {
		t.Errorf("the exception was not recorded: %+v", op)
	}
	if s := e.live(income); s.OverConfirmedUSDMinor != 4000 {
		t.Errorf("over-confirmed %d, want 4000", s.OverConfirmedUSDMinor)
	}
	if err := e.svc.DismissSuggestion(ctx, income, ""); !errors.Is(err, savings.ErrInvalidState) {
		t.Errorf("dismissing a fully confirmed suggestion returned %v", err)
	}
}

// Only an open, valued suggestion can be confirmed.
func TestAPendingOrDismissedSuggestionCannotBeConfirmed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	e.policy("20", "2026-10-01")
	pending := e.income(e.ves, "4000", "2026-10-05")
	if _, err := e.confirm(pending, e.usd, "1"); !errors.Is(err, savings.ErrInvalidState) {
		t.Errorf("confirming a suggestion with no rate returned %v", err)
	}
	income := e.income(e.usd, "100", "2026-10-05")
	if _, err := e.confirm(income, e.usd, "5"); err != nil {
		t.Fatalf("partial: %v", err)
	}
	if err := e.svc.DismissSuggestion(t.Context(), income, "No este mes"); err != nil {
		t.Fatalf("dismissing the remainder: %v", err)
	}
	if _, err := e.confirm(income, e.usd, "5"); !errors.Is(err, savings.ErrInvalidState) {
		t.Errorf("confirming a dismissed suggestion returned %v", err)
	}
	if s := e.live(income); s.IncomeConfirmedUSDMinor != 500 || s.RemainingUSDMinor != 1500 {
		t.Errorf("dismissing changed what was confirmed: %+v", s)
	}
}

// --- Idempotency -----------------------------------------------------------------

// The same form submitted twice reserves once and answers with the same
// operation.
func TestASubmittedTwiceConfirmationReservesOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	income := e.income(e.usd, "800", "2026-10-05")
	in := savings.ReserveInput{RequestKey: key(), IncomeTransactionID: &income,
		Lines: []savings.ReserveLine{{AccountID: e.usd, Amount: "160"}}}

	first, err := e.svc.Reserve(t.Context(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := e.svc.Reserve(t.Context(), in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ID != second.ID || e.count("savings_operations") != 1 || e.fund().ReservedMinor != 16000 {
		t.Errorf("a replay reserved again: %d vs %d, %d operations", first.ID, second.ID, e.count("savings_operations"))
	}

	rel := savings.ReleaseInput{RequestKey: key(), AccountID: e.usd, Amount: "10", Reason: "Imprevisto"}
	a, err := e.svc.Release(t.Context(), rel)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	b, err := e.svc.Release(t.Context(), rel)
	if err != nil || a.ID != b.ID || e.fund().ReservedMinor != 15000 {
		t.Errorf("a replayed release released again (%v)", err)
	}
}

// --- Concurrency -------------------------------------------------------------------

// Two reservations of $600 against $1,000 free, at once: exactly one wins.
func TestConcurrentReservationsNeverExceedTheFreeBalance(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")

	var (
		wg       sync.WaitGroup
		ok, fail atomic.Int64
	)
	start := make(chan struct{})
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.svc.Reserve(t.Context(), savings.ReserveInput{
				RequestKey: key(), Lines: []savings.ReserveLine{{AccountID: e.usd, Amount: "600"}},
			})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, savings.ErrInsufficientFree):
				fail.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || fail.Load() != 5 {
		t.Errorf("%d succeeded and %d were refused, want 1 and 5", ok.Load(), fail.Load())
	}
	if a := e.availability(e.usd); a.ReservedMinor != 60000 || a.FreeMinor != 40000 {
		t.Errorf("availability = %+v", a)
	}
}

// --- Release ------------------------------------------------------------------------

func TestReleasingNeedsAReasonAndCannotExceedTheReservation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	e.open(e.usd, "1000")
	e.reserve(e.usd, "800")

	if _, err := e.svc.Release(ctx, savings.ReleaseInput{RequestKey: key(), AccountID: e.usd, Amount: "100"}); !errors.Is(err, savings.ErrInvalidInput) {
		t.Errorf("a release without a reason returned %v", err)
	}
	if _, err := e.svc.Release(ctx, savings.ReleaseInput{RequestKey: key(), AccountID: e.usd, Amount: "800.01", Reason: "x"}); !errors.Is(err, savings.ErrInsufficientReserved) {
		t.Errorf("releasing more than reserved returned %v", err)
	}
	op, err := e.svc.Release(ctx, savings.ReleaseInput{RequestKey: key(), AccountID: e.usd, Amount: "300", Reason: "Reparación del carro"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if op.TotalMinor != -30000 || op.Kind != savings.KindRelease {
		t.Errorf("operation = %+v", op)
	}
	if a := e.availability(e.usd); a.ReservedMinor != 50000 || a.FreeMinor != 50000 {
		t.Errorf("availability = %+v", a)
	}

	// An archived account still releases what it backs.
	if _, err := account.NewService(e.db).SetActive(ctx, e.usd, false); err != nil {
		t.Fatalf("archiving: %v", err)
	}
	if _, err := e.svc.Release(ctx, savings.ReleaseInput{RequestKey: key(), AccountID: e.usd, Amount: "500", Reason: "Cierro la cuenta"}); err != nil {
		t.Errorf("an archived account could not release: %v", err)
	}
}

// --- Overallocation ---------------------------------------------------------------

// An expense the free balance does not cover leaves the reservation
// overallocated: shown, never corrected automatically, and the expense is
// recorded regardless.
func TestAnExpenseBeyondTheFreeBalanceLeavesTheFundOverallocated(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	e.reserve(e.usd, "800")

	if _, err := e.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: e.usd, CategoryID: e.food, Amount: "300", PaymentMethodID: bankTransfer, EffectiveDate: "2026-10-10",
	}); err != nil {
		t.Fatalf("an expense was refused because of savings: %v", err)
	}
	a := e.availability(e.usd)
	if a.BalanceMinor != 70000 || a.ReservedMinor != 80000 || a.FreeMinor != -10000 || a.OverallocatedMinor != 10000 {
		t.Errorf("availability = %+v, want 700 / 800 / -100 / 100", a)
	}
	if f := e.fund(); f.OverallocatedMinor != 10000 || f.FreeMinor != 0 {
		t.Errorf("fund = %+v", f)
	}
}

// --- Integrity across revisions (F) ---------------------------------------------

// A confirmed suggestion revised or invalidated by a change of its income keeps
// its reservations: nothing is duplicated, deleted or reconciled automatically,
// and the new revision accepts no more than what remains of it.
func TestReservationsSurviveRevisionsAndInvalidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	income := e.income(e.usd, "800", "2026-10-05")
	if _, err := e.confirm(income, e.usd, "160"); err != nil {
		t.Fatalf("confirming: %v", err)
	}

	e.edit(income, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Income, CategoryID: e.salary,
		Amount: "500", EffectiveDate: "2026-10-05"})
	s := e.live(income)
	if s.Revision != 2 || suggested(t, s) != 10000 {
		t.Fatalf("revision %d at %d", s.Revision, suggested(t, s))
	}
	if s.IncomeConfirmedUSDMinor != 16000 || s.OverConfirmedUSDMinor != 6000 || s.RemainingUSDMinor != 0 {
		t.Errorf("after lowering the income: %+v", s)
	}
	if e.fund().ReservedMinor != 16000 || e.count("savings_operations") != 1 {
		t.Error("the revision touched the reservation")
	}
	if _, err := e.confirm(income, e.usd, "1"); !errors.Is(err, savings.ErrExceedsSuggestion) {
		t.Errorf("confirming beyond the new revision returned %v", err)
	}

	e.edit(income, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Expense, CategoryID: e.food,
		Amount: "500", EffectiveDate: "2026-10-05"})
	if e.fund().ReservedMinor != 16000 {
		t.Error("invalidating the income released its reservation")
	}
	revs := e.revisions(income)
	if revs[0].ConfirmedUSDMinor != 16000 || revs[1].ConfirmedUSDMinor != 0 {
		t.Errorf("per-revision confirmations: %d, %d", revs[0].ConfirmedUSDMinor, revs[1].ConfirmedUSDMinor)
	}
}

// --- Metrics (B) and dates (D) ------------------------------------------------------

// Suggested, confirmed, gross contributions, withdrawals, net change and the
// reserved balance are six different figures. An income of September confirmed
// in October counts as confirmed in September and as a contribution in October.
func TestTheSixSavingsFiguresAreKeptApart(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	e.policy("20", "2026-09-01")
	september := e.income(e.usd, "500", "2026-09-30")
	october := e.income(e.usd, "800", "2026-10-05")
	if _, err := e.confirm(september, e.usd, "100"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := e.confirm(october, e.usd, "60"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	e.open(e.usd, "200")
	e.reserve(e.usd, "50") // manual: a contribution, not a confirmation
	if _, err := e.svc.Release(ctx, savings.ReleaseInput{RequestKey: key(), AccountID: e.usd, Amount: "30", Reason: "x"}); err != nil {
		t.Fatalf("release: %v", err)
	}

	o, err := e.svc.Overview(ctx) // October
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	checks := map[string][2]int64{
		"suggested":           {o.SuggestedUSDMinor, 16000},
		"confirmed":           {o.ConfirmedUSDMinor, 6000},
		"open":                {o.OpenUSDMinor, 10000},
		"gross contributions": {o.GrossContributionsMinor, 21000},
		"withdrawals":         {o.WithdrawalsMinor, 3000},
		"net change":          {o.NetChangeMinor, 18000},
		"reserved now":        {o.Fund.ReservedMinor, 18000},
		"progress bp":         {o.ConfirmationProgress.BasisPoints, 3750},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %d, want %d", name, c[0], c[1])
		}
	}

	ops, err := e.svc.ListOperations(ctx, 0)
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(ops) != 4 || ops[len(ops)-1].IncomeTransactionID == nil || *ops[len(ops)-1].IncomeTransactionID != september {
		t.Errorf("history = %+v", ops)
	}
	for _, op := range ops {
		if op.EffectiveDate.Format("2006-01-02") != "2026-10-15" {
			t.Errorf("operation %d dated %s, want the business day it happened", op.ID, op.EffectiveDate)
		}
	}
}
