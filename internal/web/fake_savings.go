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
