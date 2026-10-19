package savings

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// M7.3: goals -- what reserved money is for. A goal's balance is the sum of the
// entries that name it; its status is its lifecycle, never its money (TD-084).

var (
	// ErrGoalNotFound means no goal exists with that id.
	ErrGoalNotFound = errors.New("savings goal not found")

	// ErrGoalNotAccepting means the goal is paused or closed and takes no new
	// money.
	ErrGoalNotAccepting = errors.New("the savings goal does not accept money")

	// ErrDistributionExceeded means the open goals' shares would add up to more
	// than 100%.
	ErrDistributionExceeded = errors.New("the goals' shares would exceed 100%")
)

// GoalType is what kind of goal it is.
type GoalType string

const (
	// GoalPermanent is never completed: funded, drawn on, refilled.
	GoalPermanent GoalType = "PERMANENT"
	// GoalOneTime is completed when its purpose is met.
	GoalOneTime GoalType = "ONE_TIME"
)

// GoalStatus is a goal's lifecycle.
type GoalStatus string

const (
	GoalActive    GoalStatus = "ACTIVE"
	GoalPaused    GoalStatus = "PAUSED"
	GoalCompleted GoalStatus = "COMPLETED"
	GoalCancelled GoalStatus = "CANCELLED"
)

// Closed reports whether the goal's lifecycle has ended.
func (s GoalStatus) Closed() bool { return s == GoalCompleted || s == GoalCancelled }

// Funding is how a goal's reserved money compares with its target. Derived,
// never stored: a permanent goal drawn on goes back to PARTIAL by itself.
type Funding string

const (
	FundingNone    Funding = "UNFUNDED"
	FundingPartial Funding = "PARTIAL"
	FundingFull    Funding = "FUNDED"
	FundingOver    Funding = "OVERFUNDED"
)

// TargetRevision is one target a goal has had.
type TargetRevision struct {
	TargetUSDMinor int64
	CreatedAt      time.Time
}

// GoalAccount is the part of a goal's money one account backs.
type GoalAccount struct {
	AccountID     int64
	AccountName   string
	ReservedMinor int64
}

// Goal is a savings goal with its derived figures.
type Goal struct {
	ID   int64
	Name string
	Type GoalType

	// TargetUSDMinor is the current target; OriginalTargetUSDMinor the first
	// one, which a completion is compared against.
	TargetUSDMinor         int64
	OriginalTargetUSDMinor int64
	TargetDate             *time.Time
	DistributionBP         int64

	Status      GoalStatus
	ClosedAt    *time.Time
	ClosingNote *string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Derived on read.
	ReservedMinor  int64
	RemainingMinor int64 // max(0, target - reserved)
	Funding        Funding
	Progress       Ratio // reserved / target, in basis points

	// SuggestedContributionMinor is what would reach the target by its date in
	// equal monthly parts: remaining over the months left, rounded half to even.
	// Nil when the goal has no date, is funded, or is closed. A mechanical
	// figure, not advice.
	SuggestedContributionMinor *int64

	// Filled by GetGoal only.
	Accounts        []GoalAccount
	TargetRevisions []TargetRevision
	Operations      []Operation
}

// GoalInput creates or edits a goal.
type GoalInput struct {
	Name string
	Type GoalType // ignored on edit: the kind of a goal never changes
	// Target is a positive USD amount.
	Target string
	// TargetDate is "YYYY-MM-DD" or empty; only a one-time goal has one.
	TargetDate string
	// Distribution is the goal's share of each confirmed saving, as a
	// percentage with at most two decimals; empty is 0.
	Distribution string
}

// CloseGoalInput completes or cancels a goal.
type CloseGoalInput struct {
	RequestKey string
	Note       string
}

// ReallocateInput moves reserved money between goals of one account. A nil
// goal is the account's unallocated part.
type ReallocateInput struct {
	RequestKey string
	AccountID  int64
	FromGoalID *int64
	ToGoalID   *int64
	Amount     string
	Reason     string
}

// DistributionLine is one part of a suggested distribution. A nil goal is the
// fund's unallocated part.
type DistributionLine struct {
	GoalID      *int64
	GoalName    string
	AmountMinor int64
	// Capped is true when the goal received less than its share because it
	// needed less to reach its target (TD-085).
	Capped bool
}

// ListGoals returns goals with their derived figures.
func (s *Service) ListGoals(ctx context.Context, includeClosed bool) ([]Goal, error) {
	var goals []Goal
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		repo := NewRepository(exec)
		var err error
		if goals, err = repo.ListGoals(ctx, includeClosed); err != nil {
			return err
		}
		reserved, err := repo.ReservedByGoal(ctx)
		if err != nil {
			return err
		}
		for i := range goals {
			if err := s.derive(&goals[i], reserved[goals[i].ID]); err != nil {
				return err
			}
		}
		return nil
	})
	return goals, err
}

// GetGoal returns one goal with its accounts, target history and operations.
func (s *Service) GetGoal(ctx context.Context, id int64) (Goal, error) {
	var g Goal
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		repo := NewRepository(exec)
		var err error
		if g, err = repo.GetGoal(ctx, id); err != nil {
			return err
		}
		reserved, err := repo.ReservedByGoal(ctx)
		if err != nil {
			return err
		}
		if err := s.derive(&g, reserved[id]); err != nil {
			return err
		}
		byAccount, err := repo.ReservedByGoalAccount(ctx, id)
		if err != nil {
			return err
		}
		fund, err := s.fundTx(ctx, exec)
		if err != nil {
			return err
		}
		for _, a := range fund.Accounts {
			if amount, ok := byAccount[a.AccountID]; ok && amount != 0 {
				g.Accounts = append(g.Accounts, GoalAccount{AccountID: a.AccountID, AccountName: a.AccountName, ReservedMinor: amount})
			}
		}
		if g.TargetRevisions, err = repo.TargetRevisions(ctx, id); err != nil {
			return err
		}
		g.Operations, err = repo.ListOperations(ctx,
			`o.id IN (SELECT operation_id FROM savings_entries WHERE goal_id = ?)`, 0, id)
		return err
	})
	return g, err
}

// derive fills in a goal's figures from its reserved balance.
func (s *Service) derive(g *Goal, reserved int64) error {
	g.ReservedMinor = reserved
	g.RemainingMinor = max(g.TargetUSDMinor-reserved, 0)
	switch {
	case reserved <= 0:
		g.Funding = FundingNone
	case reserved < g.TargetUSDMinor:
		g.Funding = FundingPartial
	case reserved == g.TargetUSDMinor:
		g.Funding = FundingFull
	default:
		g.Funding = FundingOver
	}
	bp, defined, err := money.RatioBasisPoints(reserved, g.TargetUSDMinor)
	if err != nil {
		return fmt.Errorf("goal %d progress: %w", g.ID, err)
	}
	g.Progress = Ratio{BasisPoints: bp, Defined: defined}

	g.SuggestedContributionMinor = nil
	if g.TargetDate != nil && !g.Status.Closed() && g.RemainingMinor > 0 {
		months := monthsUntil(s.today(), *g.TargetDate)
		per, err := money.RoundHalfEven(big.NewRat(g.RemainingMinor, months))
		if err != nil {
			return fmt.Errorf("goal %d suggested contribution: %w", g.ID, err)
		}
		g.SuggestedContributionMinor = &per
	}
	return nil
}

// monthsUntil counts the calendar months from today's to the target's,
// inclusive of the target month and never less than one: a goal due this month
// or overdue needs everything now.
func monthsUntil(today, target time.Time) int64 {
	months := int64(target.Year()-today.Year())*12 + int64(target.Month()) - int64(today.Month())
	return max(months, 1)
}

// CreateGoal creates an active goal.
func (s *Service) CreateGoal(ctx context.Context, in GoalInput) (int64, error) {
	if in.Type != GoalPermanent && in.Type != GoalOneTime {
		return 0, fmt.Errorf("%w: a goal is permanent or one-time", ErrInvalidInput)
	}
	name, target, date, bp, err := parseGoalInput(in, in.Type)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.inTx(ctx, "creating a savings goal", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		if err := checkDistribution(ctx, repo, 0, bp); err != nil {
			return err
		}
		id, err = repo.InsertGoal(ctx, newGoal{
			name: name, goalType: in.Type, targetMinor: target, targetDate: date, distributionBP: bp,
			now: database.Now(),
		})
		return err
	})
	return id, err
}

// UpdateGoal edits an open goal. A new target is appended to its history: the
// original budget is never overwritten silently.
func (s *Service) UpdateGoal(ctx context.Context, id int64, in GoalInput) error {
	return s.inTx(ctx, "updating a savings goal", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		current, err := repo.GetGoal(ctx, id)
		if err != nil {
			return err
		}
		name, target, date, bp, err := parseGoalInput(in, current.Type)
		if err != nil {
			return err
		}
		if err := checkDistribution(ctx, repo, id, bp); err != nil {
			return err
		}
		now := database.Now()
		if err := repo.UpdateGoal(ctx, id, name, target, date, bp, now); err != nil {
			return err
		}
		if target != current.TargetUSDMinor {
			return repo.AddTargetRevision(ctx, id, target, now)
		}
		return nil
	})
}

// PauseGoal stops an active goal from receiving money; its balance stays.
func (s *Service) PauseGoal(ctx context.Context, id int64) error {
	return s.inTx(ctx, "pausing a savings goal", func(exec database.DBTX) error {
		return NewRepository(exec).SetGoalStatus(ctx, id, []GoalStatus{GoalActive}, GoalPaused, nil, database.Now())
	})
}

// ResumeGoal reactivates a paused goal.
func (s *Service) ResumeGoal(ctx context.Context, id int64) error {
	return s.inTx(ctx, "resuming a savings goal", func(exec database.DBTX) error {
		return NewRepository(exec).SetGoalStatus(ctx, id, []GoalStatus{GoalPaused}, GoalActive, nil, database.Now())
	})
}

// CompleteGoal closes a one-time goal whose purpose was met. Whatever is still
// reserved for it moves, in the same transaction, to the fund's unallocated
// part on the same accounts -- it stays reserved and is never released
// silently. Its history is kept.
func (s *Service) CompleteGoal(ctx context.Context, id int64, in CloseGoalInput) error {
	return s.closeGoal(ctx, id, in, GoalCompleted)
}

// CancelGoal closes a goal that will not be pursued. Its money moves to the
// unallocated part exactly as on completion.
func (s *Service) CancelGoal(ctx context.Context, id int64, in CloseGoalInput) error {
	return s.closeGoal(ctx, id, in, GoalCancelled)
}

func (s *Service) closeGoal(ctx context.Context, id int64, in CloseGoalInput, to GoalStatus) error {
	if err := validateRequestKey(in.RequestKey); err != nil {
		return err
	}
	note, err := normalizeReason(in.Note)
	if err != nil {
		return err
	}
	return s.inTx(ctx, "closing a savings goal", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		if _, replay, err := repo.OperationIDByKey(ctx, in.RequestKey); err != nil || replay {
			return err
		}
		g, err := repo.GetGoal(ctx, id)
		if err != nil {
			return err
		}
		if to == GoalCompleted && g.Type != GoalOneTime {
			return fmt.Errorf("%w: a permanent goal is never completed; it stays funded or is cancelled", ErrInvalidState)
		}
		byAccount, err := repo.ReservedByGoalAccount(ctx, id)
		if err != nil {
			return err
		}
		var entries []newEntry
		for _, accountID := range sortedKeys(byAccount) {
			amount := byAccount[accountID]
			if amount == 0 {
				continue
			}
			entries = append(entries,
				newEntry{accountID: accountID, goalID: &id, amountMinor: -amount},
				newEntry{accountID: accountID, goalID: nil, amountMinor: amount})
		}
		now := database.Now()
		if len(entries) > 0 {
			verb := "completada"
			if to == GoalCancelled {
				verb = "cancelada"
			}
			reason := fmt.Sprintf("Meta %s: %s", verb, g.Name)
			if _, err := repo.InsertOperation(ctx, newOperation{
				kind: KindReallocate, requestKey: in.RequestKey, reason: &reason,
				effectiveDate: s.today(), entries: entries, now: now,
			}); err != nil {
				return err
			}
		}
		return repo.SetGoalStatus(ctx, id, []GoalStatus{GoalActive, GoalPaused}, to, &note, now)
	})
}

// Reallocate moves reserved money between two goals on one account, or between
// a goal and the account's unallocated part. The entries sum to zero: nothing
// enters or leaves the fund.
func (s *Service) Reallocate(ctx context.Context, in ReallocateInput) (Operation, error) {
	if err := validateRequestKey(in.RequestKey); err != nil {
		return Operation{}, err
	}
	if sameGoal(in.FromGoalID, in.ToGoalID) {
		return Operation{}, fmt.Errorf("%w: origin and destination are the same", ErrInvalidInput)
	}
	amount, err := parseUSD(in.Amount)
	if err != nil {
		return Operation{}, err
	}
	if amount <= 0 {
		return Operation{}, fmt.Errorf("%w: an amount to move must be greater than zero", ErrInvalidInput)
	}
	var reason *string
	if strings.TrimSpace(in.Reason) != "" {
		r, err := normalizeReason(in.Reason)
		if err != nil {
			return Operation{}, err
		}
		reason = &r
	}
	var op Operation
	err = s.inTx(ctx, "reallocating savings", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		if id, replay, err := repo.OperationIDByKey(ctx, in.RequestKey); err != nil {
			return err
		} else if replay {
			op, err = repo.GetOperation(ctx, id)
			return err
		}
		if err := checkFundAccount(ctx, exec, in.AccountID, false); err != nil {
			return err
		}
		if in.FromGoalID != nil {
			if _, err := repo.GetGoal(ctx, *in.FromGoalID); err != nil {
				return err
			}
		}
		if in.ToGoalID != nil {
			if err := s.checkGoalAcceptsMoney(ctx, exec, *in.ToGoalID); err != nil {
				return err
			}
		}
		available, err := repo.ReservedAt(ctx, in.AccountID, in.FromGoalID)
		if err != nil {
			return err
		}
		if amount > available {
			return fmt.Errorf("%w: %s USD are there and %s were asked for",
				ErrInsufficientReserved, money.Format(max(available, 0), 2), money.Format(amount, 2))
		}
		id, err := repo.InsertOperation(ctx, newOperation{
			kind: KindReallocate, requestKey: in.RequestKey, reason: reason, effectiveDate: s.today(),
			entries: []newEntry{
				{accountID: in.AccountID, goalID: in.FromGoalID, amountMinor: -amount},
				{accountID: in.AccountID, goalID: in.ToGoalID, amountMinor: amount},
			},
			now: database.Now(),
		})
		if err != nil {
			return err
		}
		op, err = repo.GetOperation(ctx, id)
		return err
	})
	return op, err
}

// SuggestDistribution proposes how to split an amount among the active goals
// by their shares (TD-085). It writes nothing; the user may change every line.
func (s *Service) SuggestDistribution(ctx context.Context, amountMinor int64) ([]DistributionLine, error) {
	var lines []DistributionLine
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		repo := NewRepository(exec)
		goals, err := repo.ListGoals(ctx, false)
		if err != nil {
			return err
		}
		reserved, err := repo.ReservedByGoal(ctx)
		if err != nil {
			return err
		}
		shares := make([]goalShare, 0, len(goals))
		for _, g := range goals {
			if g.Status != GoalActive || g.DistributionBP == 0 {
				continue
			}
			shares = append(shares, goalShare{
				id: g.ID, name: g.Name, bp: g.DistributionBP,
				remaining: max(g.TargetUSDMinor-reserved[g.ID], 0),
			})
		}
		lines, err = distribute(amountMinor, shares)
		return err
	})
	return lines, err
}

// goalShare is one goal as distribute sees it.
type goalShare struct {
	id        int64
	name      string
	bp        int64
	remaining int64
}

// distribute splits an amount among goals by their shares, exactly (TD-085):
//
//  1. each goal's exact share is amount × bp / 10000;
//  2. a share above what the goal still needs is capped at that need -- a
//     funded goal receives nothing;
//  3. whatever the capped shares and the unassigned basis points leave is the
//     unallocated part;
//  4. money.Allocate turns the exact parts into cents by largest remainder,
//     ties to the earlier goal, so the lines add up to the amount EXACTLY.
//
// A capped part is a whole number of cents, so its remainder is zero and it can
// never receive the extra cent that would take it past its cap.
func distribute(amountMinor int64, shares []goalShare) ([]DistributionLine, error) {
	if amountMinor < 0 {
		return nil, fmt.Errorf("%w: a negative amount cannot be distributed", ErrInvalidInput)
	}
	total := new(big.Rat).SetInt64(amountMinor)
	parts := make([]*big.Rat, 0, len(shares)+1)
	capped := make([]bool, len(shares))
	assigned := new(big.Rat)
	for i, sh := range shares {
		part := new(big.Rat).Mul(total, new(big.Rat).SetFrac(big.NewInt(sh.bp), bpDenominator))
		if limit := new(big.Rat).SetInt64(sh.remaining); part.Cmp(limit) > 0 {
			part, capped[i] = limit, true
		}
		parts = append(parts, part)
		assigned.Add(assigned, part)
	}
	parts = append(parts, new(big.Rat).Sub(total, assigned))

	cents, err := money.Allocate(amountMinor, parts)
	if err != nil {
		return nil, fmt.Errorf("allocating %d cents: %w", amountMinor, err)
	}
	lines := make([]DistributionLine, 0, len(parts))
	for i, sh := range shares {
		id := sh.id
		lines = append(lines, DistributionLine{GoalID: &id, GoalName: sh.name, AmountMinor: cents[i], Capped: capped[i]})
	}
	lines = append(lines, DistributionLine{AmountMinor: cents[len(cents)-1]})
	return lines, nil
}

// checkGoalAcceptsMoney verifies a goal exists and is active.
func (s *Service) checkGoalAcceptsMoney(ctx context.Context, exec database.DBTX, goalID int64) error {
	g, err := NewRepository(exec).GetGoal(ctx, goalID)
	if err != nil {
		return err
	}
	if g.Status != GoalActive {
		return fmt.Errorf("%w: %q is %s", ErrGoalNotAccepting, g.Name, g.Status)
	}
	return nil
}

// checkDistribution refuses shares that would add up to more than 100%.
func checkDistribution(ctx context.Context, repo *Repository, goalID, bp int64) error {
	others, err := repo.DistributionTotal(ctx, goalID)
	if err != nil {
		return err
	}
	if others+bp > money.BasisPointsPerUnit {
		return fmt.Errorf("%w: the other goals already take %s%%", ErrDistributionExceeded, money.Format(others, 2))
	}
	return nil
}

func parseGoalInput(in GoalInput, goalType GoalType) (string, int64, *time.Time, int64, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 80 {
		return "", 0, nil, 0, fmt.Errorf("%w: a goal needs a name of up to 80 characters", ErrInvalidInput)
	}
	target, err := parseUSD(in.Target)
	if err != nil {
		return "", 0, nil, 0, err
	}
	if target <= 0 {
		return "", 0, nil, 0, fmt.Errorf("%w: the target must be greater than zero", ErrInvalidInput)
	}
	var date *time.Time
	if raw := strings.TrimSpace(in.TargetDate); raw != "" {
		if goalType != GoalOneTime {
			return "", 0, nil, 0, fmt.Errorf("%w: only a one-time goal has a date", ErrInvalidInput)
		}
		d, err := database.ParseDate(raw)
		if err != nil {
			return "", 0, nil, 0, fmt.Errorf("%w: the date must be YYYY-MM-DD", ErrInvalidInput)
		}
		date = &d
	}
	var bp int64
	if raw := strings.TrimSpace(in.Distribution); raw != "" {
		if bp, err = parseRate(raw); err != nil {
			return "", 0, nil, 0, err
		}
	}
	return name, target, date, bp, nil
}

func sameGoal(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// sortedKeys returns a map's keys in ascending order, for deterministic writes.
func sortedKeys(m map[int64]int64) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
