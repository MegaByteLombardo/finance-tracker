package savings_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// --- helpers -----------------------------------------------------------------

func (e *env) goal(name string, kind savings.GoalType, target, date, share string) int64 {
	e.t.Helper()
	id, err := e.svc.CreateGoal(e.t.Context(), savings.GoalInput{
		Name: name, Type: kind, Target: target, TargetDate: date, Distribution: share,
	})
	if err != nil {
		e.t.Fatalf("creating goal %q: %v", name, err)
	}
	return id
}

func (e *env) getGoal(id int64) savings.Goal {
	e.t.Helper()
	g, err := e.svc.GetGoal(e.t.Context(), id)
	if err != nil {
		e.t.Fatalf("GetGoal(%d): %v", id, err)
	}
	return g
}

func (e *env) reserveFor(accountID, goalID int64, amount string) {
	e.t.Helper()
	if _, err := e.svc.Reserve(e.t.Context(), savings.ReserveInput{
		RequestKey: key(), Lines: []savings.ReserveLine{{AccountID: accountID, GoalID: &goalID, Amount: amount}},
	}); err != nil {
		e.t.Fatalf("reserving %s for goal %d: %v", amount, goalID, err)
	}
}

func ptrID(id int64) *int64 { return &id }

func sumLines(lines []savings.DistributionLine) int64 {
	var total int64
	for _, l := range lines {
		total += l.AmountMinor
	}
	return total
}

// --- distribution (TD-085) ---------------------------------------------------

// Confirmed $160, Emergency 50%, Travel 50%: $80 each, nothing unallocated.
func TestADistributionSplitsByShare(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	emergency := e.goal("Emergencias", savings.GoalPermanent, "1000", "", "50")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "50")

	lines, err := e.svc.SuggestDistribution(t.Context(), 16000)
	if err != nil {
		t.Fatalf("SuggestDistribution: %v", err)
	}
	if len(lines) != 3 || *lines[0].GoalID != emergency || lines[0].AmountMinor != 8000 ||
		*lines[1].GoalID != travel || lines[1].AmountMinor != 8000 || lines[2].GoalID != nil || lines[2].AmountMinor != 0 {
		t.Errorf("lines = %+v", lines)
	}
}

// Cents are never lost or created: the lines add up exactly, ties go to the
// earlier goal, a share below 100% leaves the rest unallocated, and a cap is
// never exceeded -- even for one or two cents.
func TestADistributionPreservesEveryCent(t *testing.T) {
	t.Parallel()
	third := []savings.Share{{ID: 1, BP: 3333, Remaining: 1 << 40}, {ID: 2, BP: 3333, Remaining: 1 << 40}, {ID: 3, BP: 3334, Remaining: 1 << 40}}

	cases := []struct {
		name   string
		amount int64
		shares []savings.Share
		want   []int64 // goals..., unallocated
	}{
		{"one cent in thirds", 1, third, []int64{0, 0, 1, 0}},
		{"two cents in thirds", 2, third, []int64{1, 0, 1, 0}},
		{"a dollar in thirds", 100, third, []int64{33, 33, 34, 0}},
		{"shares under 100%", 1000, []savings.Share{{ID: 1, BP: 2500, Remaining: 1 << 40}}, []int64{250, 750}},
		{"a funded goal gets nothing", 16000, []savings.Share{{ID: 1, BP: 5000, Remaining: 0}, {ID: 2, BP: 5000, Remaining: 1 << 40}}, []int64{0, 8000, 8000}},
		{"a cap leaves the excess unallocated", 16000, []savings.Share{{ID: 1, BP: 5000, Remaining: 3000}, {ID: 2, BP: 5000, Remaining: 1 << 40}}, []int64{3000, 8000, 5000}},
		{"one cent against a one-cent cap", 1, []savings.Share{{ID: 1, BP: 10000, Remaining: 0}}, []int64{0, 1}},
		{"nothing to distribute", 0, third, []int64{0, 0, 0, 0}},
		{"no goals", 777, nil, []int64{777}},
	}
	for _, c := range cases {
		lines, err := savings.Distribute(c.amount, c.shares)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(lines) != len(c.want) {
			t.Fatalf("%s: %d lines, want %d", c.name, len(lines), len(c.want))
		}
		for i, want := range c.want {
			if lines[i].AmountMinor != want {
				t.Errorf("%s: line %d = %d, want %d (%+v)", c.name, i, lines[i].AmountMinor, want, lines)
			}
		}
	}

	// Every amount from 0 to 5,000 cents, against odd shares and tight caps.
	shares := []savings.Share{{ID: 1, BP: 1234, Remaining: 37}, {ID: 2, BP: 4321, Remaining: 1 << 40}, {ID: 3, BP: 2222, Remaining: 501}}
	for amount := int64(0); amount <= 5000; amount++ {
		lines, err := savings.Distribute(amount, shares)
		if err != nil {
			t.Fatalf("amount %d: %v", amount, err)
		}
		if got := sumLines(lines); got != amount {
			t.Fatalf("amount %d distributed as %d", amount, got)
		}
		for i, sh := range shares {
			if lines[i].AmountMinor > sh.Remaining || lines[i].AmountMinor < 0 {
				t.Fatalf("amount %d: goal %d got %d past its cap %d", amount, sh.ID, lines[i].AmountMinor, sh.Remaining)
			}
		}
	}
}

// A paused goal and a goal with no share take no part.
func TestOnlyActiveGoalsWithAShareAreSuggested(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	paused := e.goal("Mudanza", savings.GoalOneTime, "500", "", "40")
	e.goal("Laptop", savings.GoalOneTime, "500", "", "")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "60")
	if err := e.svc.PauseGoal(t.Context(), paused); err != nil {
		t.Fatalf("PauseGoal: %v", err)
	}
	lines, err := e.svc.SuggestDistribution(t.Context(), 10000)
	if err != nil {
		t.Fatalf("SuggestDistribution: %v", err)
	}
	if len(lines) != 2 || *lines[0].GoalID != travel || lines[0].AmountMinor != 6000 || lines[1].AmountMinor != 4000 {
		t.Errorf("lines = %+v", lines)
	}
}

func TestSharesCannotExceedAHundredPercent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	first := e.goal("Emergencias", savings.GoalPermanent, "1000", "", "70")
	if _, err := e.svc.CreateGoal(t.Context(), savings.GoalInput{Name: "Viaje", Type: savings.GoalOneTime, Target: "800", Distribution: "31"}); !errors.Is(err, savings.ErrDistributionExceeded) {
		t.Errorf("a share past 100%% returned %v", err)
	}
	e.goal("Viaje", savings.GoalOneTime, "800", "", "30")
	if err := e.svc.UpdateGoal(t.Context(), first, savings.GoalInput{Name: "Emergencias", Target: "1000", Distribution: "71"}); !errors.Is(err, savings.ErrDistributionExceeded) {
		t.Errorf("raising a share past 100%% returned %v", err)
	}
}

// Confirmed with a distribution: the lines name goals, the unallocated part
// names none, and together they are exactly what was confirmed.
func TestAConfirmationCanBeDistributedAmongGoals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	emergency := e.goal("Emergencias", savings.GoalPermanent, "1000", "", "50")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "25")
	income := e.income(e.usd, "800", "2026-10-05")

	lines, err := e.svc.SuggestDistribution(t.Context(), 16000)
	if err != nil {
		t.Fatalf("SuggestDistribution: %v", err)
	}
	in := savings.ReserveInput{RequestKey: key(), IncomeTransactionID: &income}
	for _, l := range lines {
		in.Lines = append(in.Lines, savings.ReserveLine{AccountID: e.usd, GoalID: l.GoalID, Amount: centsString(l.AmountMinor)})
	}
	op, err := e.svc.Reserve(t.Context(), in)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if op.TotalMinor != 16000 {
		t.Errorf("confirmed %d, want 16000", op.TotalMinor)
	}
	if g := e.getGoal(emergency); g.ReservedMinor != 8000 {
		t.Errorf("emergency holds %d", g.ReservedMinor)
	}
	if g := e.getGoal(travel); g.ReservedMinor != 4000 {
		t.Errorf("travel holds %d", g.ReservedMinor)
	}
	if f := e.fund(); f.ReservedMinor != 16000 || f.AllocatedMinor != 12000 || f.UnallocatedMinor != 4000 {
		t.Errorf("fund = %+v: allocated and unallocated must add up without counting twice", f)
	}
}

// --- goal lifecycle ------------------------------------------------------------

// Emergency target $1,000, reserved $1,000: FUNDED, never completed. $300
// withdrawn: $700, and it needs funding again. Refilled: funded again.
func TestAPermanentGoalIsFundedDrawnOnAndRefilled(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "2000")
	emergency := e.goal("Emergencias", savings.GoalPermanent, "1000", "", "")
	e.reserveFor(e.usd, emergency, "1000")

	g := e.getGoal(emergency)
	if g.Funding != savings.FundingFull || g.Status != savings.GoalActive || g.Progress.BasisPoints != 10000 {
		t.Errorf("funded goal = %s %s %d", g.Funding, g.Status, g.Progress.BasisPoints)
	}
	if err := e.svc.CompleteGoal(t.Context(), emergency, savings.CloseGoalInput{RequestKey: key(), Note: "x"}); !errors.Is(err, savings.ErrInvalidState) {
		t.Errorf("completing a permanent goal returned %v", err)
	}

	if _, err := e.svc.Release(t.Context(), savings.ReleaseInput{RequestKey: key(), AccountID: e.usd, GoalID: &emergency,
		Amount: "300", Reason: "Reparación"}); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}
	g = e.getGoal(emergency)
	if g.ReservedMinor != 70000 || g.RemainingMinor != 30000 || g.Funding != savings.FundingPartial {
		t.Errorf("after the withdrawal: %d reserved, %d remaining, %s", g.ReservedMinor, g.RemainingMinor, g.Funding)
	}
	e.reserveFor(e.usd, emergency, "300")
	if g := e.getGoal(emergency); g.Funding != savings.FundingFull {
		t.Errorf("refilled goal is %s", g.Funding)
	}
	if n := len(e.getGoal(emergency).Operations); n != 3 {
		t.Errorf("the goal's history has %d operations, want 3", n)
	}
}

// A one-time goal funded is FUNDED, not COMPLETED. Completing it is explicit;
// what it still holds moves to the unallocated part, never released silently.
func TestAOneTimeGoalIsCompletedExplicitly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "2027-03-01", "")
	e.reserveFor(e.usd, travel, "850")

	if g := e.getGoal(travel); g.Funding != savings.FundingOver || g.Status != savings.GoalActive {
		t.Errorf("funded: %s %s", g.Funding, g.Status)
	}
	k := key()
	if err := e.svc.CompleteGoal(t.Context(), travel, savings.CloseGoalInput{RequestKey: k, Note: "Viaje hecho"}); err != nil {
		t.Fatalf("CompleteGoal: %v", err)
	}
	if err := e.svc.CompleteGoal(t.Context(), travel, savings.CloseGoalInput{RequestKey: k, Note: "Viaje hecho"}); err != nil {
		t.Errorf("replaying the completion failed: %v", err)
	}
	g := e.getGoal(travel)
	if g.Status != savings.GoalCompleted || g.ReservedMinor != 0 || g.ClosedAt == nil || *g.ClosingNote != "Viaje hecho" {
		t.Errorf("completed goal = %+v", g)
	}
	if len(g.Operations) != 2 {
		t.Errorf("the completed goal kept %d operations, want its history of 2", len(g.Operations))
	}
	if f := e.fund(); f.ReservedMinor != 85000 || f.UnallocatedMinor != 85000 {
		t.Errorf("fund after completion = %+v: the money must stay reserved, unallocated", f)
	}
	if _, err := e.svc.Reserve(t.Context(), savings.ReserveInput{RequestKey: key(),
		Lines: []savings.ReserveLine{{AccountID: e.usd, GoalID: &travel, Amount: "1"}}}); !errors.Is(err, savings.ErrGoalNotAccepting) {
		t.Errorf("a completed goal accepted money: %v", err)
	}
}

// A cancelled goal with money: the money moves to the unallocated part, the
// goal keeps its history. A paused goal keeps its balance and takes no money.
func TestCancellingAndPausingGoals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	e.open(e.usdTwo, "1000")
	move := e.goal("Mudanza", savings.GoalOneTime, "500", "", "")
	e.reserveFor(e.usd, move, "100")
	e.reserveFor(e.usdTwo, move, "50")

	if err := e.svc.PauseGoal(t.Context(), move); err != nil {
		t.Fatalf("PauseGoal: %v", err)
	}
	if _, err := e.svc.Reserve(t.Context(), savings.ReserveInput{RequestKey: key(),
		Lines: []savings.ReserveLine{{AccountID: e.usd, GoalID: &move, Amount: "1"}}}); !errors.Is(err, savings.ErrGoalNotAccepting) {
		t.Errorf("a paused goal accepted money: %v", err)
	}
	if g := e.getGoal(move); g.ReservedMinor != 15000 || g.Status != savings.GoalPaused {
		t.Errorf("paused goal = %+v", g)
	}
	if err := e.svc.CancelGoal(t.Context(), move, savings.CloseGoalInput{RequestKey: key()}); !errors.Is(err, savings.ErrInvalidInput) {
		t.Errorf("cancelling without a note returned %v", err)
	}
	if err := e.svc.CancelGoal(t.Context(), move, savings.CloseGoalInput{RequestKey: key(), Note: "Ya no me mudo"}); err != nil {
		t.Fatalf("CancelGoal: %v", err)
	}
	if a := e.availability(e.usd); a.ReservedMinor != 10000 {
		t.Errorf("cancelling moved money between accounts: %+v", a)
	}
	if f := e.fund(); f.AllocatedMinor != 0 || f.UnallocatedMinor != 15000 {
		t.Errorf("fund = %+v", f)
	}
	if err := e.svc.ResumeGoal(t.Context(), move); !errors.Is(err, savings.ErrInvalidState) {
		t.Errorf("resuming a cancelled goal returned %v", err)
	}
}

// Moving money between goals changes nothing in the fund's total.
func TestReallocatingBetweenGoals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	emergency := e.goal("Emergencias", savings.GoalPermanent, "1000", "", "")
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "")
	e.reserveFor(e.usd, emergency, "500")
	e.reserve(e.usd, "100")

	if _, err := e.svc.Reallocate(t.Context(), savings.ReallocateInput{RequestKey: key(), AccountID: e.usd,
		FromGoalID: &emergency, ToGoalID: &travel, Amount: "200"}); err != nil {
		t.Fatalf("goal to goal: %v", err)
	}
	if _, err := e.svc.Reallocate(t.Context(), savings.ReallocateInput{RequestKey: key(), AccountID: e.usd,
		ToGoalID: &travel, Amount: "100"}); err != nil {
		t.Fatalf("unallocated to goal: %v", err)
	}
	if e.getGoal(emergency).ReservedMinor != 30000 || e.getGoal(travel).ReservedMinor != 30000 {
		t.Error("the goals do not hold what was moved")
	}
	if f := e.fund(); f.ReservedMinor != 60000 || f.UnallocatedMinor != 0 {
		t.Errorf("fund = %+v", f)
	}
	for name, in := range map[string]savings.ReallocateInput{
		"more than there":    {RequestKey: key(), AccountID: e.usd, FromGoalID: &emergency, ToGoalID: &travel, Amount: "300.01"},
		"to the same goal":   {RequestKey: key(), AccountID: e.usd, FromGoalID: &travel, ToGoalID: &travel, Amount: "1"},
		"from empty account": {RequestKey: key(), AccountID: e.usdTwo, FromGoalID: &travel, Amount: "1"},
	} {
		if _, err := e.svc.Reallocate(t.Context(), in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	o, err := e.svc.Overview(t.Context())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if o.GrossContributionsMinor != 60000 || o.WithdrawalsMinor != 0 {
		t.Errorf("reallocations counted as contributions or withdrawals: %+v", o)
	}
}

// A goal costing more or less than planned keeps its original budget: a new
// target is appended, never overwritten.
func TestChangingATargetKeepsTheOriginal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	travel := e.goal("Viaje", savings.GoalOneTime, "800", "", "")
	if err := e.svc.UpdateGoal(t.Context(), travel, savings.GoalInput{Name: "Viaje a Mérida", Target: "1000"}); err != nil {
		t.Fatalf("UpdateGoal: %v", err)
	}
	g := e.getGoal(travel)
	if g.TargetUSDMinor != 100000 || g.OriginalTargetUSDMinor != 80000 || len(g.TargetRevisions) != 2 || g.Name != "Viaje a Mérida" {
		t.Errorf("goal = %+v", g)
	}
	if err := e.svc.UpdateGoal(t.Context(), travel, savings.GoalInput{Name: "Viaje", Target: "1000", TargetDate: "2027-01-01"}); err != nil {
		t.Fatalf("UpdateGoal: %v", err)
	}
	if n := len(e.getGoal(travel).TargetRevisions); n != 2 {
		t.Errorf("an unchanged target added a revision: %d", n)
	}
}

// A goal with no date has no suggested contribution; one with a date spreads
// what remains over the months left, and one due or overdue needs it all now.
func TestASuggestedContributionSpreadsWhatRemains(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(e.usd, "1000")
	noDate := e.goal("Laptop", savings.GoalOneTime, "1200", "", "")
	dated := e.goal("Viaje", savings.GoalOneTime, "1200", "2027-01-31", "")
	overdue := e.goal("Regalo", savings.GoalOneTime, "100", "2026-09-01", "")
	e.reserveFor(e.usd, dated, "300")

	if g := e.getGoal(noDate); g.SuggestedContributionMinor != nil {
		t.Errorf("a goal without a date suggests %d", *g.SuggestedContributionMinor)
	}
	// $900 remain over October-to-January: three months.
	if g := e.getGoal(dated); g.SuggestedContributionMinor == nil || *g.SuggestedContributionMinor != 30000 {
		t.Errorf("dated goal suggests %v", g.SuggestedContributionMinor)
	}
	if g := e.getGoal(overdue); g.SuggestedContributionMinor == nil || *g.SuggestedContributionMinor != 10000 {
		t.Errorf("overdue goal suggests %v", g.SuggestedContributionMinor)
	}
	if _, err := e.svc.CreateGoal(t.Context(), savings.GoalInput{Name: "Emergencias", Type: savings.GoalPermanent, Target: "1000", TargetDate: "2027-01-01"}); !errors.Is(err, savings.ErrInvalidInput) {
		t.Errorf("a permanent goal with a date returned %v", err)
	}
}

// centsString writes non-negative cents as a USD decimal string.
func centsString(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }
