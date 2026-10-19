package savings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// --- operations and entries (M7.2) -------------------------------------------

// newOperation is what InsertOperation writes.
type newOperation struct {
	kind          OperationKind
	requestKey    string
	suggestionID  *int64
	transactionID *int64
	reason        *string
	isException   bool
	effectiveDate time.Time
	entries       []newEntry
	now           time.Time
}

type newEntry struct {
	accountID   int64
	goalID      *int64
	amountMinor int64
}

// InsertOperation writes an operation and its entries.
func (r *Repository) InsertOperation(ctx context.Context, in newOperation) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_operations
		    (kind, request_key, suggestion_id, transaction_id, reason, is_exception, effective_date, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(in.kind), in.requestKey, in.suggestionID, in.transactionID, in.reason,
		boolToInt(in.isException), database.FormatDate(in.effectiveDate), database.FormatTime(in.now))
	if err != nil {
		return 0, fmt.Errorf("inserting a %s operation: %w", in.kind, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading the id of the new operation: %w", err)
	}
	for _, e := range in.entries {
		if err := r.insertEntry(ctx, id, e, in.now); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// OperationIDByKey returns the operation a request key already produced.
func (r *Repository) OperationIDByKey(ctx context.Context, key string) (int64, bool, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM savings_operations WHERE request_key = ?`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("looking up request key: %w", err)
	}
	return id, true, nil
}

// ReservedByAccount returns every account's reserved balance.
func (r *Repository) ReservedByAccount(ctx context.Context) (map[int64]int64, error) {
	return r.sumBy(ctx, `SELECT account_id, SUM(amount_minor) FROM savings_entries GROUP BY account_id`)
}

// ConfirmedByIncome returns, per income, what RESERVE operations confirmed
// against any of its revisions.
func (r *Repository) ConfirmedByIncome(ctx context.Context) (map[int64]int64, error) {
	return r.sumBy(ctx, `
		SELECT s.income_transaction_id, SUM(e.amount_minor)
		  FROM savings_operations o
		  JOIN savings_entries e     ON e.operation_id = o.id
		  JOIN savings_suggestions s ON s.id = o.suggestion_id
		 WHERE o.kind IN ('RESERVE')
		 GROUP BY s.income_transaction_id`)
}

// ConfirmedBySuggestion returns, per revision, what RESERVE operations
// confirmed against it.
func (r *Repository) ConfirmedBySuggestion(ctx context.Context) (map[int64]int64, error) {
	return r.sumBy(ctx, `
		SELECT o.suggestion_id, SUM(e.amount_minor)
		  FROM savings_operations o
		  JOIN savings_entries e ON e.operation_id = o.id
		 WHERE o.kind IN ('RESERVE') AND o.suggestion_id IS NOT NULL
		 GROUP BY o.suggestion_id`)
}

func (r *Repository) sumBy(ctx context.Context, query string, args ...any) (_ map[int64]int64, err error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("summing savings entries: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing sum rows: %w", cerr))
		}
	}()
	out := make(map[int64]int64)
	for rows.Next() {
		var (
			key int64
			sum int64
		)
		if err := rows.Scan(&key, &sum); err != nil {
			return nil, fmt.Errorf("reading a sum: %w", err)
		}
		out[key] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating sums: %w", err)
	}
	return out, nil
}

// operationSelect reads operations with the income a confirmation answers to.
const operationSelect = `
	SELECT o.id, o.kind, o.request_key, o.suggestion_id, s.income_transaction_id, o.transaction_id,
	       o.reason, o.is_exception, o.effective_date, o.created_at
	  FROM savings_operations o
	  LEFT JOIN savings_suggestions s ON s.id = o.suggestion_id`

// ListOperations returns operations newest first, with their entries. A zero
// limit returns every operation.
func (r *Repository) ListOperations(ctx context.Context, where string, limit int, args ...any) ([]Operation, error) {
	query := operationSelect
	if where != "" {
		query += ` WHERE ` + where
	}
	query += ` ORDER BY o.effective_date DESC, o.id DESC`
	if limit > 0 {
		query += fmt.Sprintf(` LIMIT %d`, limit)
	}
	ops, err := r.scanOperations(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return ops, r.attachEntries(ctx, ops)
}

// GetOperation returns one operation with its entries.
func (r *Repository) GetOperation(ctx context.Context, id int64) (Operation, error) {
	ops, err := r.ListOperations(ctx, `o.id = ?`, 0, id)
	if err != nil {
		return Operation{}, err
	}
	if len(ops) == 0 {
		return Operation{}, fmt.Errorf("savings operation %d: %w", id, ErrNotFound)
	}
	return ops[0], nil
}

func (r *Repository) scanOperations(ctx context.Context, query string, args ...any) (_ []Operation, err error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing savings operations: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing operation rows: %w", cerr))
		}
	}()
	ops := make([]Operation, 0)
	for rows.Next() {
		var (
			op                       Operation
			kind, date, created      string
			suggestion, income, txID sql.NullInt64
			reason                   sql.NullString
			exception                int
		)
		if err := rows.Scan(&op.ID, &kind, &op.RequestKey, &suggestion, &income, &txID,
			&reason, &exception, &date, &created); err != nil {
			return nil, fmt.Errorf("reading a savings operation: %w", err)
		}
		op.Kind = OperationKind(kind)
		op.SuggestionID = nullInt(suggestion)
		op.IncomeTransactionID = nullInt(income)
		op.TransactionID = nullInt(txID)
		if reason.Valid {
			text := reason.String
			op.Reason = &text
		}
		op.IsException = exception == 1
		if op.EffectiveDate, err = database.ParseDate(date); err != nil {
			return nil, fmt.Errorf("operation %d effective_date: %w", op.ID, err)
		}
		if op.CreatedAt, err = database.ParseTime(created); err != nil {
			return nil, fmt.Errorf("operation %d created_at: %w", op.ID, err)
		}
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating operations: %w", err)
	}
	return ops, nil
}

// attachEntries loads the entries of the given operations in one query.
func (r *Repository) attachEntries(ctx context.Context, ops []Operation) (err error) {
	if len(ops) == 0 {
		return nil
	}
	ids := make([]string, len(ops))
	index := make(map[int64]int, len(ops))
	args := make([]any, len(ops))
	for i, op := range ops {
		ids[i] = "?"
		args[i] = op.ID
		index[op.ID] = i
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT e.operation_id, e.id, e.account_id, a.name, `+entryGoalColumns+`, e.amount_minor
		  FROM savings_entries e
		  JOIN accounts a ON a.id = e.account_id
		  `+entryGoalJoin+`
		 WHERE e.operation_id IN (`+strings.Join(ids, ",")+`)
		 ORDER BY e.operation_id, e.id`, args...)
	if err != nil {
		return fmt.Errorf("reading savings entries: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing entry rows: %w", cerr))
		}
	}()
	for rows.Next() {
		var (
			opID     int64
			e        Entry
			goalID   sql.NullInt64
			goalName sql.NullString
		)
		if err := rows.Scan(&opID, &e.ID, &e.AccountID, &e.AccountName, &goalID, &goalName, &e.AmountMinor); err != nil {
			return fmt.Errorf("reading a savings entry: %w", err)
		}
		e.GoalID = nullInt(goalID)
		if goalName.Valid {
			e.GoalName = goalName.String
		}
		i := index[opID]
		ops[i].Entries = append(ops[i].Entries, e)
		ops[i].TotalMinor += e.AmountMinor
	}
	return rows.Err()
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	value := v.Int64
	return &value
}

// DailyFundDeltas returns, per financial date and operation kind, the signed
// sum of entries -- the one read the fund's history needs, whatever its length.
func (r *Repository) DailyFundDeltas(ctx context.Context) (_ []FundDelta, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT o.effective_date, o.kind, SUM(e.amount_minor)
		  FROM savings_operations o
		  JOIN savings_entries e ON e.operation_id = o.id
		 GROUP BY o.effective_date, o.kind
		 ORDER BY o.effective_date, o.kind`)
	if err != nil {
		return nil, fmt.Errorf("reading the fund's daily deltas: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing delta rows: %w", cerr))
		}
	}()
	out := make([]FundDelta, 0)
	for rows.Next() {
		var (
			d          FundDelta
			date, kind string
		)
		if err := rows.Scan(&date, &kind, &d.AmountMinor); err != nil {
			return nil, fmt.Errorf("reading a fund delta: %w", err)
		}
		if d.Date, err = database.ParseDate(date); err != nil {
			return nil, fmt.Errorf("fund delta date: %w", err)
		}
		d.Kind = OperationKind(kind)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Entries name no goal until M7.3 adds the column.
const (
	entryGoalColumns = `NULL, NULL`
	entryGoalJoin    = ``
)

func (r *Repository) insertEntry(ctx context.Context, operationID int64, e newEntry, now time.Time) error {
	if e.goalID != nil {
		return fmt.Errorf("%w: goals do not exist yet", ErrInvalidInput)
	}
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_entries (operation_id, account_id, amount_minor, created_at)
		VALUES (?, ?, ?, ?)`,
		operationID, e.accountID, e.amountMinor, database.FormatTime(now)); err != nil {
		return fmt.Errorf("inserting an entry of operation %d: %w", operationID, err)
	}
	return nil
}

// AllocatedTotal is reserved money assigned to a goal. Until M7.3 there are
// no goals, so it is zero.
func (r *Repository) AllocatedTotal(context.Context) (int64, error) { return 0, nil }

// ReservedAt is what is reserved on an account. Until M7.3 there are no goals,
// so it is the whole of the account's reservation.
func (r *Repository) ReservedAt(ctx context.Context, accountID int64, goalID *int64) (int64, error) {
	if goalID != nil {
		return 0, fmt.Errorf("%w: goals do not exist yet", ErrInvalidInput)
	}
	var sum int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM savings_entries WHERE account_id = ?`, accountID).Scan(&sum); err != nil {
		return 0, fmt.Errorf("reading the reservation of account %d: %w", accountID, err)
	}
	return sum, nil
}
