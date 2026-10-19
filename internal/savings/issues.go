package savings

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// M7.4: reconciliation. Issues are derived on every read from what the ledger
// and the fund say, and stored nowhere; only the user's acknowledgement of one
// is (TD-087). Nothing here changes a balance or resolves anything by itself.

// IssueKind names what is inconsistent.
type IssueKind string

const (
	// IssueOverallocated: an account holds less than is reserved on it --
	// after an expense, a transfer or a correction.
	IssueOverallocated IssueKind = "OVERALLOCATED"
	// IssueOverConfirmed: an income has more confirmed than its live
	// suggestion, or it is no longer eligible and money was confirmed for it.
	IssueOverConfirmed IssueKind = "OVER_CONFIRMED"
	// IssueSpendMismatch: a SPEND's expense was corrected -- it is no longer an
	// expense, moved to another account, or is now smaller than what it spent.
	IssueSpendMismatch IssueKind = "SPEND_MISMATCH"
	// IssueArchivedBacking: an archived account still backs reserved money.
	IssueArchivedBacking IssueKind = "ARCHIVED_BACKING"
)

// Acknowledgement is the user's note that they have seen an issue.
type Acknowledgement struct {
	Note string
	At   time.Time
}

// Issue is one inconsistency between the fund and the ledger.
type Issue struct {
	// Key names the issue and its amount: an acknowledgement covers exactly
	// what was seen, and a changed amount is a new issue.
	Key  string
	Kind IssueKind

	AccountID           *int64
	AccountName         string
	IncomeTransactionID *int64
	OperationID         *int64
	TransactionID       *int64

	// AmountMinor is the USD at stake.
	AmountMinor int64
	// Detail explains the issue in words.
	Detail string

	Acknowledged *Acknowledgement
}

// ListIssues returns every current issue, the unacknowledged first.
func (s *Service) ListIssues(ctx context.Context) ([]Issue, error) {
	var issues []Issue
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		var err error
		issues, err = s.issuesTx(ctx, exec)
		return err
	})
	return issues, err
}

func (s *Service) issuesTx(ctx context.Context, exec database.DBTX) ([]Issue, error) {
	repo := NewRepository(exec)
	issues := make([]Issue, 0)

	fund, err := s.fundTx(ctx, exec)
	if err != nil {
		return nil, err
	}
	for _, a := range fund.Accounts {
		id := a.AccountID
		if a.OverallocatedMinor > 0 {
			issues = append(issues, Issue{
				Key:  fmt.Sprintf("overallocated:account:%d:%d", id, a.OverallocatedMinor),
				Kind: IssueOverallocated, AccountID: &id, AccountName: a.AccountName,
				AmountMinor: a.OverallocatedMinor,
				Detail: fmt.Sprintf("%s tiene %s USD y hay %s USD reservados en ella.",
					a.AccountName, money.Format(a.BalanceMinor, 2), money.Format(a.ReservedMinor, 2)),
			})
		}
		if !a.IsActive && a.ReservedMinor > 0 {
			issues = append(issues, Issue{
				Key:  fmt.Sprintf("archived:account:%d:%d", id, a.ReservedMinor),
				Kind: IssueArchivedBacking, AccountID: &id, AccountName: a.AccountName,
				AmountMinor: a.ReservedMinor,
				Detail:      fmt.Sprintf("%s está archivada y todavía respalda ahorro.", a.AccountName),
			})
		}
	}

	confirmed, err := repo.ConfirmedByIncome(ctx)
	if err != nil {
		return nil, err
	}
	lives, err := repo.ListLiveSuggestions(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[int64]Suggestion, len(lives))
	for _, l := range lives {
		live[l.IncomeTransactionID] = l
	}
	for _, incomeID := range sortedKeys(confirmed) {
		amount := confirmed[incomeID]
		if amount <= 0 {
			continue
		}
		id := incomeID
		l, hasLive := live[incomeID]
		switch {
		case !hasLive:
			issues = append(issues, Issue{
				Key:  fmt.Sprintf("overconfirmed:income:%d:%d", id, amount),
				Kind: IssueOverConfirmed, IncomeTransactionID: &id, AmountMinor: amount,
				Detail: fmt.Sprintf("El ingreso ya no es elegible y tiene %s USD confirmados.", money.Format(amount, 2)),
			})
		case l.SuggestedUSDMinor != nil && amount > *l.SuggestedUSDMinor:
			excess := amount - *l.SuggestedUSDMinor
			issues = append(issues, Issue{
				Key:  fmt.Sprintf("overconfirmed:income:%d:%d", id, excess),
				Kind: IssueOverConfirmed, IncomeTransactionID: &id, AmountMinor: excess,
				Detail: fmt.Sprintf("Confirmado %s USD; la sugerencia vigente es %s USD.",
					money.Format(amount, 2), money.Format(*l.SuggestedUSDMinor, 2)),
			})
		}
	}

	spends, err := repo.ListOperations(ctx, `o.kind IN ('SPEND')`, 0)
	if err != nil {
		return nil, err
	}
	lrepo := ledger.NewRepository(exec)
	for _, op := range spends {
		spent := -op.TotalMinor
		if op.TransactionID == nil || len(op.Entries) == 0 {
			continue
		}
		detail, err := spendMismatch(ctx, lrepo, *op.TransactionID, op.Entries[0].AccountID, spent)
		if err != nil {
			return nil, err
		}
		if detail == "" {
			continue
		}
		opID, txID := op.ID, *op.TransactionID
		issues = append(issues, Issue{
			Key:  fmt.Sprintf("spend:operation:%d:%d", op.ID, spent),
			Kind: IssueSpendMismatch, OperationID: &opID, TransactionID: &txID,
			AccountName: op.Entries[0].AccountName, AmountMinor: spent, Detail: detail,
		})
	}

	acks, err := repo.Acknowledgements(ctx)
	if err != nil {
		return nil, err
	}
	for i := range issues {
		if a, ok := acks[issues[i].Key]; ok {
			a := a
			issues[i].Acknowledged = &a
		}
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return issues[i].Acknowledged == nil && issues[j].Acknowledged != nil
	})
	return issues, nil
}

// spendMismatch says why a SPEND no longer matches its expense, or "".
func spendMismatch(ctx context.Context, lrepo *ledger.Repository, txID, accountID, spent int64) (string, error) {
	tx, err := lrepo.GetTransaction(ctx, txID)
	if err != nil {
		return "", fmt.Errorf("reading transaction %d: %w", txID, err)
	}
	if tx.Type != ledger.Expense {
		return "El gasto que consumió este ahorro ya no es un gasto.", nil
	}
	m, err := lrepo.GetMovementFor(ctx, txID)
	if err != nil {
		return "", err
	}
	if m.AccountID != accountID {
		return "El gasto que consumió este ahorro se movió a otra cuenta.", nil
	}
	if -m.AmountMinor < spent {
		return fmt.Sprintf("El gasto es ahora de %s USD y consumió %s USD de ahorro.",
			money.Format(-m.AmountMinor, 2), money.Format(spent, 2)), nil
	}
	return "", nil
}

// AcknowledgeIssue records that the user has seen an issue. It changes no
// balance and does not make the issue disappear: it is still reported, marked
// as acknowledged, until what causes it is resolved.
func (s *Service) AcknowledgeIssue(ctx context.Context, key, note string) error {
	note, err := normalizeReason(note)
	if err != nil {
		return err
	}
	return s.inTx(ctx, "acknowledging a savings issue", func(exec database.DBTX) error {
		issues, err := s.issuesTx(ctx, exec)
		if err != nil {
			return err
		}
		for _, i := range issues {
			if i.Key == key {
				return NewRepository(exec).Acknowledge(ctx, key, note, database.Now())
			}
		}
		return fmt.Errorf("issue %q is not current: %w", key, ErrNotFound)
	})
}

// --- repository ---------------------------------------------------------------

// Acknowledgements returns every acknowledgement by issue key.
func (r *Repository) Acknowledgements(ctx context.Context) (_ map[string]Acknowledgement, err error) {
	rows, err := r.db.QueryContext(ctx, `SELECT issue_key, note, created_at FROM savings_issue_acknowledgements`)
	if err != nil {
		return nil, fmt.Errorf("reading acknowledgements: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing acknowledgement rows: %w", cerr))
		}
	}()
	out := make(map[string]Acknowledgement)
	for rows.Next() {
		var (
			key, note, at string
			a             Acknowledgement
		)
		if err := rows.Scan(&key, &note, &at); err != nil {
			return nil, fmt.Errorf("reading an acknowledgement: %w", err)
		}
		a.Note = note
		if a.At, err = database.ParseTime(at); err != nil {
			return nil, fmt.Errorf("acknowledgement %q: %w", key, err)
		}
		out[key] = a
	}
	return out, rows.Err()
}

// Acknowledge records or updates an acknowledgement.
func (r *Repository) Acknowledge(ctx context.Context, key, note string, now time.Time) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_issue_acknowledgements (issue_key, note, created_at) VALUES (?, ?, ?)
		ON CONFLICT (issue_key) DO UPDATE SET note = excluded.note, created_at = excluded.created_at`,
		key, note, database.FormatTime(now)); err != nil {
		return fmt.Errorf("acknowledging %q: %w", key, err)
	}
	return nil
}

// --- goal cost and history ----------------------------------------------------

// LinkedExpense is an expense that spent a goal's money.
type LinkedExpense struct {
	TransactionID int64
	Date          time.Time
	Description   *string
	// AmountMinor is the whole expense, in USD cents; SpentMinor the part of it
	// the goal paid.
	AmountMinor int64
	SpentMinor  int64
}

// GoalMonth is one month of a goal's money.
type GoalMonth struct {
	Month        time.Time
	InMinor      int64
	OutMinor     int64
	ClosingMinor int64
}

// FundMonth is one month of the fund: what came in, what went out, the net and
// the balance at its close. Reallocations move nothing in or out.
type FundMonth struct {
	Month              time.Time
	ContributionsMinor int64
	WithdrawalsMinor   int64
	NetMinor           int64
	ClosingMinor       int64
}

// linkedExpensesTx returns the expenses that spent a goal's money, oldest first.
func linkedExpensesTx(ctx context.Context, exec database.DBTX, goalID int64) (_ []LinkedExpense, err error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT o.transaction_id, -SUM(e.amount_minor)
		  FROM savings_operations o
		  JOIN savings_entries e ON e.operation_id = o.id
		 WHERE o.kind IN ('SPEND') AND e.goal_id = ?
		 GROUP BY o.transaction_id
		 ORDER BY o.transaction_id`, goalID)
	if err != nil {
		return nil, fmt.Errorf("reading the expenses of goal %d: %w", goalID, err)
	}
	type spent struct{ tx, amount int64 }
	var list []spent
	for rows.Next() {
		var s spent
		if err := rows.Scan(&s.tx, &s.amount); err != nil {
			return nil, errors.Join(fmt.Errorf("reading a goal expense: %w", err), rows.Close())
		}
		list = append(list, s)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("iterating goal expenses: %w", err)
	}
	lrepo := ledger.NewRepository(exec)
	out := make([]LinkedExpense, 0, len(list))
	for _, s := range list {
		tx, err := lrepo.GetTransaction(ctx, s.tx)
		if err != nil {
			return nil, err
		}
		m, err := lrepo.GetMovementFor(ctx, s.tx)
		if err != nil {
			return nil, err
		}
		out = append(out, LinkedExpense{
			TransactionID: s.tx, Date: tx.EffectiveDate, Description: tx.Description,
			AmountMinor: -m.AmountMinor, SpentMinor: s.amount,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// goalMonthsTx folds a goal's entries into months, from its first to this one.
func (s *Service) goalMonthsTx(ctx context.Context, exec database.DBTX, goalID int64) (_ []GoalMonth, err error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT o.effective_date,
		       SUM(CASE WHEN e.amount_minor > 0 THEN e.amount_minor ELSE 0 END),
		       SUM(CASE WHEN e.amount_minor < 0 THEN -e.amount_minor ELSE 0 END)
		  FROM savings_operations o
		  JOIN savings_entries e ON e.operation_id = o.id
		 WHERE e.goal_id = ?
		 GROUP BY o.effective_date
		 ORDER BY o.effective_date`, goalID)
	if err != nil {
		return nil, fmt.Errorf("reading the history of goal %d: %w", goalID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing history rows: %w", cerr))
		}
	}()
	var months []GoalMonth
	var balance int64
	for rows.Next() {
		var (
			date    string
			in, out int64
		)
		if err := rows.Scan(&date, &in, &out); err != nil {
			return nil, fmt.Errorf("reading a goal day: %w", err)
		}
		day, err := database.ParseDate(date)
		if err != nil {
			return nil, err
		}
		month := firstOfMonth(day)
		months = extendMonths(months, month, balance)
		last := &months[len(months)-1]
		last.InMinor += in
		last.OutMinor += out
		balance += in - out
		last.ClosingMinor = balance
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(months) > 0 {
		months = extendMonths(months, firstOfMonth(s.today()), balance)
	}
	return months, nil
}

// extendMonths appends empty months up to and including month, each closing at
// balance.
func extendMonths(months []GoalMonth, month time.Time, balance int64) []GoalMonth {
	if len(months) == 0 {
		return append(months, GoalMonth{Month: month, ClosingMinor: balance})
	}
	for next := months[len(months)-1].Month.AddDate(0, 1, 0); !next.After(month); next = next.AddDate(0, 1, 0) {
		months = append(months, GoalMonth{Month: next, ClosingMinor: balance})
	}
	return months
}

// FundHistory returns the fund month by month, from its first operation to this
// month, from one read of the daily deltas.
func (s *Service) FundHistory(ctx context.Context) ([]FundMonth, error) {
	deltas, err := NewRepository(s.db).DailyFundDeltas(ctx)
	if err != nil {
		return nil, err
	}
	var months []FundMonth
	var balance int64
	add := func(month time.Time) {
		if len(months) == 0 {
			months = append(months, FundMonth{Month: month, ClosingMinor: balance})
			return
		}
		for next := months[len(months)-1].Month.AddDate(0, 1, 0); !next.After(month); next = next.AddDate(0, 1, 0) {
			months = append(months, FundMonth{Month: next, ClosingMinor: balance})
		}
	}
	for _, d := range deltas {
		add(firstOfMonth(d.Date))
		last := &months[len(months)-1]
		switch d.Kind {
		case KindReserve:
			last.ContributionsMinor += d.AmountMinor
		case KindRelease, KindSpend:
			last.WithdrawalsMinor -= d.AmountMinor
		case KindReallocate, KindReback:
		}
		balance += d.AmountMinor
		last.NetMinor = last.ContributionsMinor - last.WithdrawalsMinor
		last.ClosingMinor = balance
	}
	if len(months) > 0 {
		add(firstOfMonth(s.today()))
	}
	return months, nil
}

func firstOfMonth(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
}
