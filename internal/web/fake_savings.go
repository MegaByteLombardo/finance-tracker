package web

import (
	"context"
	"strconv"
	"sync"
)

// FakeSavings is the M7.1 stand-in for the savings service. Handler tests set
// what it returns and read back what it was asked to do; the savings rules
// themselves are tested against real SQLite in internal/savings.
type FakeSavings struct {
	mu sync.Mutex

	OverviewResult SavingsOverview
	Suggestions    []SavingsSuggestion
	Policy         SavingsPolicy
	Categories     []SavingsCategoryEligibility
	Plan           SavingsSyncPlan
	Details        map[int64]SavingsIncomeDetail

	// Err, when set, is returned by every read. ActionErr by every write.
	Err       error
	ActionErr error

	// What the handlers asked for.
	PolicySet       []SetSavingsPolicyInput
	EligibilitySet  map[int64]bool
	Overrides       map[int64]string
	Actions         []string // "value:12", "revalue:12", "dismiss:12"
	DismissReasons  []string
	Applied         [][]int64
	ValuePendingRun int

	fakeFundState
	fakeGoalState
}

// NewFakeSavings returns an empty stand-in: no policy, nothing suggested.
func NewFakeSavings() *FakeSavings {
	return &FakeSavings{
		Details:        make(map[int64]SavingsIncomeDetail),
		EligibilitySet: make(map[int64]bool),
		Overrides:      make(map[int64]string),
	}
}

func (f *FakeSavings) Overview(context.Context) (SavingsOverview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.OverviewResult, f.Err
}

func (f *FakeSavings) GetPolicy(context.Context) (SavingsPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Policy, f.Err
}

func (f *FakeSavings) SetPolicy(_ context.Context, in SetSavingsPolicyInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return f.ActionErr
	}
	f.PolicySet = append(f.PolicySet, in)
	return nil
}

func (f *FakeSavings) ListCategoryEligibility(context.Context) ([]SavingsCategoryEligibility, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Categories, f.Err
}

func (f *FakeSavings) SetCategoryEligibility(_ context.Context, categoryID int64, eligible bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return f.ActionErr
	}
	f.EligibilitySet[categoryID] = eligible
	return nil
}

func (f *FakeSavings) SetIncomeOverride(_ context.Context, transactionID int64, _ bool, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return f.ActionErr
	}
	f.Overrides[transactionID] = reason
	return nil
}

func (f *FakeSavings) ListSuggestions(context.Context) ([]SavingsSuggestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Suggestions, f.Err
}

func (f *FakeSavings) IncomeDetail(_ context.Context, transactionID int64) (SavingsIncomeDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return SavingsIncomeDetail{}, f.Err
	}
	d, ok := f.Details[transactionID]
	if !ok {
		return SavingsIncomeDetail{}, ErrSavingsIncomeNotFound
	}
	return d, nil
}

func (f *FakeSavings) action(name string, transactionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return f.ActionErr
	}
	f.Actions = append(f.Actions, name+":"+strconv.FormatInt(transactionID, 10))
	return nil
}

func (f *FakeSavings) ValueSuggestion(_ context.Context, transactionID int64) error {
	return f.action("value", transactionID)
}

func (f *FakeSavings) RevalueSuggestion(_ context.Context, transactionID int64) error {
	return f.action("revalue", transactionID)
}

func (f *FakeSavings) DismissSuggestion(_ context.Context, transactionID int64, reason string) error {
	if err := f.action("dismiss", transactionID); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DismissReasons = append(f.DismissReasons, reason)
	return nil
}

func (f *FakeSavings) ValuePending(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return 0, f.ActionErr
	}
	f.ValuePendingRun++
	return 2, nil
}

func (f *FakeSavings) PlanSync(context.Context) (SavingsSyncPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Plan, f.Err
}

func (f *FakeSavings) ApplySync(_ context.Context, transactionIDs []int64) (SavingsSyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return SavingsSyncResult{}, f.ActionErr
	}
	f.Applied = append(f.Applied, transactionIDs)
	return SavingsSyncResult{Created: len(transactionIDs)}, nil
}

// M7.2 stand-ins. FundResult and Operations are what the reads return;
// Reserved and Released record what the handlers asked for.
type fakeFundState struct {
	FundResult SavingsFund
	Operations []SavingsOperation
	Reserved   []SavingsReserveInput
	Released   []SavingsReleaseInput
}

func (f *FakeSavings) GetFund(context.Context) (SavingsFund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.FundResult, f.Err
}

func (f *FakeSavings) Reserve(_ context.Context, in SavingsReserveInput) (SavingsOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return SavingsOperation{}, f.ActionErr
	}
	f.Reserved = append(f.Reserved, in)
	return SavingsOperation{ID: int64(len(f.Reserved)), Kind: "RESERVE"}, nil
}

func (f *FakeSavings) Release(_ context.Context, in SavingsReleaseInput) (SavingsOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return SavingsOperation{}, f.ActionErr
	}
	f.Released = append(f.Released, in)
	return SavingsOperation{ID: int64(len(f.Released)), Kind: "RELEASE"}, nil
}

func (f *FakeSavings) ListOperations(context.Context, int) ([]SavingsOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Operations, f.Err
}

// M7.3 stand-ins.
type fakeGoalState struct {
	Goals         []SavingsGoal
	Distribution  []SavingsDistributionLine
	CreatedGoals  []SavingsGoalInput
	UpdatedGoals  map[int64]SavingsGoalInput
	GoalActions   []string // "pause:3", "complete:3:note", ...
	Reallocations []SavingsReallocateInput
}

func (f *FakeSavings) ListGoals(_ context.Context, includeClosed bool) ([]SavingsGoal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SavingsGoal, 0, len(f.Goals))
	for _, g := range f.Goals {
		if includeClosed || !g.Closed {
			out = append(out, g)
		}
	}
	return out, f.Err
}

func (f *FakeSavings) GetGoal(_ context.Context, id int64) (SavingsGoal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return SavingsGoal{}, f.Err
	}
	for _, g := range f.Goals {
		if g.ID == id {
			return g, nil
		}
	}
	return SavingsGoal{}, ErrSavingsGoalNotFound
}

func (f *FakeSavings) CreateGoal(_ context.Context, in SavingsGoalInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return 0, f.ActionErr
	}
	f.CreatedGoals = append(f.CreatedGoals, in)
	return int64(100 + len(f.CreatedGoals)), nil
}

func (f *FakeSavings) UpdateGoal(_ context.Context, id int64, in SavingsGoalInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return f.ActionErr
	}
	if f.UpdatedGoals == nil {
		f.UpdatedGoals = make(map[int64]SavingsGoalInput)
	}
	f.UpdatedGoals[id] = in
	return nil
}

func (f *FakeSavings) goalAction(name string, id int64, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return f.ActionErr
	}
	action := name + ":" + strconv.FormatInt(id, 10)
	if note != "" {
		action += ":" + note
	}
	f.GoalActions = append(f.GoalActions, action)
	return nil
}

func (f *FakeSavings) PauseGoal(_ context.Context, id int64) error {
	return f.goalAction("pause", id, "")
}
func (f *FakeSavings) ResumeGoal(_ context.Context, id int64) error {
	return f.goalAction("resume", id, "")
}

func (f *FakeSavings) CompleteGoal(_ context.Context, id int64, in SavingsCloseGoalInput) error {
	return f.goalAction("complete", id, in.Note)
}

func (f *FakeSavings) CancelGoal(_ context.Context, id int64, in SavingsCloseGoalInput) error {
	return f.goalAction("cancel", id, in.Note)
}

func (f *FakeSavings) Reallocate(_ context.Context, in SavingsReallocateInput) (SavingsOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ActionErr != nil {
		return SavingsOperation{}, f.ActionErr
	}
	f.Reallocations = append(f.Reallocations, in)
	return SavingsOperation{Kind: "REALLOCATE"}, nil
}

func (f *FakeSavings) SuggestDistribution(context.Context, string) ([]SavingsDistributionLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Distribution, f.Err
}
