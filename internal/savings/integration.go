package savings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// M7.4: the ledger meets the fund. An expense or a transfer is a real movement
// and is NEVER refused because of savings (TD-086). What this file does is show
// what a movement does to the reservations on its account, and apply -- in the
// same transaction as the movement -- what the user decided: consume a goal's
// money, release reserved money with a reason, or move the backing of reserved
// money to the transfer's destination.

// ErrNotLinkable means the transaction cannot carry the savings operation asked
// of it: a SPEND needs an EXPENSE in a USD account, a REBACK a USD TRANSFER.
var ErrNotLinkable = errors.New("the transaction cannot be linked to savings this way")

// ReserveSlice is the money reserved on one account for one goal, or -- with a
// nil goal -- unallocated on it.
type ReserveSlice struct {
	GoalID        *int64
	GoalName      string
	ReservedMinor int64
}

// AccountImpact is what an outflow of an amount from an account would do to the
// reservations on it.
type AccountImpact struct {
	// Applies is false for an account that is not a USD asset: savings are
	// never backed by it, and nothing is shown. A credit card is never one,
	// whatever its currency (TD-094).
	Applies     bool
	AccountName string

	BalanceMinor  int64
	ReservedMinor int64
	FreeMinor     int64

	// ShortfallMinor is how much of the outflow the free balance does not
	// cover: recording it as is would leave the account that much
	// overallocated. Zero when the free balance covers it.
	ShortfallMinor int64

	// Slices are the reservations on the account, per goal and unallocated,
	// that the outflow could use.
	Slices []ReserveSlice
}

// SavingsUse is an amount of one reservation slice, consumed or moved.
type SavingsUse struct {
	GoalID *int64
	Amount string
}

// ExpensePlan is what the user decided an expense does to savings.
type ExpensePlan struct {
	// RequestKey makes the expense and its savings operations idempotent
	// together: a replay returns the expense already recorded.
	RequestKey string

	// Spend is money the expense was FOR: a trip paid with the trip's goal.
	// Recorded as a SPEND linked to the expense; no reason needed.
	Spend []SavingsUse

	// Release is reserved money given back to cover an expense the free balance
	// did not -- an extraordinary use. Recorded as a RELEASE linked to the
	// expense; Reason is required.
	Release []SavingsUse
	Reason  string
}

// Empty reports whether the plan asks for nothing.
func (p ExpensePlan) Empty() bool { return len(p.Spend) == 0 && len(p.Release) == 0 }

// TransferPlan is what the user decided a transfer does to savings: reserved
// money whose backing moves from the source to the destination with it.
type TransferPlan struct {
	RequestKey string
	Move       []SavingsUse
}

// Empty reports whether the plan asks for nothing.
func (p TransferPlan) Empty() bool { return len(p.Move) == 0 }

// AccountImpact reports what an outflow of amount from an account would do to
// its reservations, without writing anything. An unreadable amount is treated
// as nothing yet: the form is still being filled in.
func (s *Service) AccountImpact(ctx context.Context, accountID int64, amount string) (AccountImpact, error) {
	var impact AccountImpact
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		a, err := account.NewRepository(exec).Get(ctx, accountID)
		if errors.Is(err, account.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("looking up account %d: %w", accountID, err)
		}
		if a.CurrencyCode != FundCurrency || a.Type.Nature() != account.Asset {
			return nil
		}
		fund, err := s.fundTx(ctx, exec)
		if err != nil {
			return err
		}
		avail, _ := fund.Account(accountID)
		impact = AccountImpact{
			Applies:       true,
			AccountName:   avail.AccountName,
			BalanceMinor:  avail.BalanceMinor,
			ReservedMinor: avail.ReservedMinor,
			FreeMinor:     avail.FreeMinor,
		}
		if outflow, err := parseUSD(amount); err == nil && outflow > 0 {
			impact.ShortfallMinor = min(max(outflow-max(avail.FreeMinor, 0), 0), max(avail.ReservedMinor, 0))
		}
		impact.Slices, err = s.slicesTx(ctx, exec, accountID)
		return err
	})
	return impact, err
}

// slicesTx lists the positive reservations on an account, goals first by name,
// unallocated last.
func (s *Service) slicesTx(ctx context.Context, exec database.DBTX, accountID int64) ([]ReserveSlice, error) {
	repo := NewRepository(exec)
	byGoal, err := repo.sumBy(ctx, `
		SELECT COALESCE(goal_id, 0), SUM(amount_minor) FROM savings_entries
		 WHERE account_id = ? GROUP BY COALESCE(goal_id, 0)`, accountID)
	if err != nil {
		return nil, err
	}
	goals, err := repo.ListGoals(ctx, true)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(goals))
	for _, g := range goals {
		names[g.ID] = g.Name
	}
	slices := make([]ReserveSlice, 0, len(byGoal))
	for goalID, amount := range byGoal {
		if amount <= 0 {
			continue
		}
		slice := ReserveSlice{ReservedMinor: amount}
		if goalID != 0 {
			id := goalID
			slice.GoalID, slice.GoalName = &id, names[goalID]
		}
		slices = append(slices, slice)
	}
	sort.Slice(slices, func(i, j int) bool {
		a, b := slices[i], slices[j]
		if (a.GoalID == nil) != (b.GoalID == nil) {
			return b.GoalID == nil
		}
		return a.GoalName < b.GoalName
	})
	return slices, nil
}

// ReplayedTransactionTx returns the ledger transaction a request key already
// linked savings to, if any: the replay of an expense or a transfer submitted
// twice.
func (s *Service) ReplayedTransactionTx(ctx context.Context, exec database.DBTX, key string) (int64, bool, error) {
	if key == "" {
		return 0, false, nil
	}
	for _, suffix := range []string{spendSuffix, releaseSuffix, rebackSuffix} {
		var txID *int64
		err := exec.QueryRowContext(ctx,
			`SELECT transaction_id FROM savings_operations WHERE request_key = ?`, key+suffix).Scan(&txID)
		if err == nil && txID != nil {
			return *txID, true, nil
		}
		if err != nil && !isNoRows(err) {
			return 0, false, fmt.Errorf("looking up request key: %w", err)
		}
	}
	return 0, false, nil
}

const (
	spendSuffix   = "-spend"
	releaseSuffix = "-release"
	rebackSuffix  = "-reback"
)

// ApplyExpensePlanTx records, through exec, what an expense does to savings:
// SPEND for money the expense was for, RELEASE (with a reason) for reserved
// money given back to cover it. exec must be the transaction that recorded the
// expense (internal/application).
//
// Every amount comes from a reservation on the expense's own account, and
// together they never exceed the expense. An expense in VES never consumes USD
// savings directly: the path is an FX_EXCHANGE, which the fund then shows as
// overallocated on the USD account until the user releases it (TD-086).
func (s *Service) ApplyExpensePlanTx(ctx context.Context, exec database.DBTX, transactionID int64, plan ExpensePlan) error {
	if plan.Empty() {
		return nil
	}
	if err := validateRequestKey(plan.RequestKey + releaseSuffix); err != nil {
		return err
	}
	// A release must explain itself; that is checked before any amount.
	var releaseReason string
	if len(plan.Release) > 0 {
		r, err := normalizeReason(plan.Reason)
		if err != nil {
			return fmt.Errorf("releasing savings for an expense: %w", err)
		}
		releaseReason = r
	}
	tx, err := ledger.NewRepository(exec).GetTransaction(ctx, transactionID)
	if err != nil {
		return fmt.Errorf("reading expense %d: %w", transactionID, err)
	}
	if tx.Type != ledger.Expense {
		return fmt.Errorf("%w: transaction %d is %s, not an expense", ErrNotLinkable, transactionID, tx.Type)
	}
	movement, err := ledger.NewRepository(exec).GetMovementFor(ctx, transactionID)
	if err != nil {
		return err
	}
	if err := checkFundAccount(ctx, exec, movement.AccountID, false); err != nil {
		return fmt.Errorf("%w: %w", ErrNotLinkable, err)
	}

	budget := -movement.AmountMinor
	spend, used, err := s.useEntries(ctx, exec, movement.AccountID, plan.Spend, budget)
	if err != nil {
		return err
	}
	release, _, err := s.useEntries(ctx, exec, movement.AccountID, plan.Release, budget-used, spend...)
	if err != nil {
		return err
	}

	repo := NewRepository(exec)
	now := database.Now()
	if len(spend) > 0 {
		var reason *string
		if strings.TrimSpace(plan.Reason) != "" && len(release) == 0 {
			r, err := normalizeReason(plan.Reason)
			if err != nil {
				return err
			}
			reason = &r
		}
		if _, err := repo.InsertOperation(ctx, newOperation{
			kind: KindSpend, requestKey: plan.RequestKey + spendSuffix, transactionID: &transactionID,
			reason: reason, effectiveDate: s.today(), entries: spend, now: now,
		}); err != nil {
			return err
		}
	}
	if len(release) > 0 {
		if _, err := repo.InsertOperation(ctx, newOperation{
			kind: KindRelease, requestKey: plan.RequestKey + releaseSuffix, transactionID: &transactionID,
			reason: &releaseReason, effectiveDate: s.today(), entries: release, now: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ApplyTransferPlanTx moves, through exec, the backing of reserved money from a
// transfer's source to its destination: one REBACK whose entries sum to zero
// per goal. Nothing enters or leaves the fund.
func (s *Service) ApplyTransferPlanTx(ctx context.Context, exec database.DBTX, transactionID int64, plan TransferPlan) error {
	if plan.Empty() {
		return nil
	}
	if err := validateRequestKey(plan.RequestKey + rebackSuffix); err != nil {
		return err
	}
	lrepo := ledger.NewRepository(exec)
	tx, err := lrepo.GetTransaction(ctx, transactionID)
	if err != nil {
		return fmt.Errorf("reading transfer %d: %w", transactionID, err)
	}
	if tx.Type != ledger.Transfer {
		return fmt.Errorf("%w: transaction %d is %s, not a transfer", ErrNotLinkable, transactionID, tx.Type)
	}
	movements, err := lrepo.ListMovementsFor(ctx, transactionID)
	if err != nil {
		return err
	}
	var source, destination ledger.Movement
	for _, m := range movements {
		if m.AmountMinor < 0 {
			source = m
		} else {
			destination = m
		}
	}
	if source.ID == 0 || destination.ID == 0 {
		return fmt.Errorf("%w: transfer %d has no source and destination", ErrNotLinkable, transactionID)
	}
	if err := checkFundAccount(ctx, exec, source.AccountID, false); err != nil {
		return fmt.Errorf("%w: %w", ErrNotLinkable, err)
	}
	if err := checkFundAccount(ctx, exec, destination.AccountID, true); err != nil {
		return fmt.Errorf("%w: %w", ErrNotLinkable, err)
	}

	out, _, err := s.useEntries(ctx, exec, source.AccountID, plan.Move, destination.AmountMinor)
	if err != nil {
		return err
	}
	entries := make([]newEntry, 0, 2*len(out))
	for _, e := range out {
		entries = append(entries, e, newEntry{accountID: destination.AccountID, goalID: e.goalID, amountMinor: -e.amountMinor})
	}
	_, err = NewRepository(exec).InsertOperation(ctx, newOperation{
		kind: KindReback, requestKey: plan.RequestKey + rebackSuffix, transactionID: &transactionID,
		effectiveDate: s.today(), entries: entries, now: database.Now(),
	})
	return err
}

// useEntries turns uses of reservation slices on an account into negative
// entries, refusing any that exceed its slice (after earlier entries of the
// same request) or that together exceed budget.
func (s *Service) useEntries(ctx context.Context, exec database.DBTX, accountID int64, uses []SavingsUse,
	budget int64, earlier ...newEntry,
) ([]newEntry, int64, error) {
	repo := NewRepository(exec)
	taken := make(map[int64]int64) // by goal id, 0 for unallocated
	for _, e := range earlier {
		taken[goalKey(e.goalID)] -= e.amountMinor
	}
	entries := make([]newEntry, 0, len(uses))
	var total int64
	for _, u := range uses {
		amount, err := parseUSD(u.Amount)
		if err != nil {
			return nil, 0, err
		}
		if amount <= 0 {
			return nil, 0, fmt.Errorf("%w: an amount of savings to use must be greater than zero", ErrInvalidInput)
		}
		reserved, err := repo.ReservedAt(ctx, accountID, u.GoalID)
		if err != nil {
			return nil, 0, err
		}
		k := goalKey(u.GoalID)
		if taken[k]+amount > reserved {
			return nil, 0, fmt.Errorf("%w: %s USD are reserved there and %s were asked for",
				ErrInsufficientReserved, money.Format(max(reserved-taken[k], 0), 2), money.Format(amount, 2))
		}
		taken[k] += amount
		total += amount
		entries = append(entries, newEntry{accountID: accountID, goalID: u.GoalID, amountMinor: -amount})
	}
	if total > budget {
		return nil, 0, fmt.Errorf("%w: %w: %s USD of savings for a movement of %s USD",
			ErrInvalidInput, ErrExceedsMovement, money.Format(total, 2), money.Format(budget, 2))
	}
	return entries, total, nil
}

func goalKey(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
