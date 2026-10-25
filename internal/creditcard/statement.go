package creditcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// M8.2: statements. A statement is what the issuer printed, a FACT: it is
// recorded as the bank stated it and never recomputed. Everything about what is
// owed, paid or overdue is derived from statements and the ledger on every read
// (obligations.go, TD-092).

// Statement is one statement as stored. Amounts are in the card currency's
// minor units.
type Statement struct {
	ID            int64
	CardAccountID int64

	PeriodStart time.Time
	ClosingDate time.Time
	// DueDate is nil when the statement does not print one.
	DueDate *time.Time

	// ClosingBalanceMinor follows the ledger's convention: negative when money
	// was owed at the closing, positive for a credit balance. It is the bank's
	// figure, B.
	ClosingBalanceMinor int64
	// MinimumPaymentMinor is nil when the statement does not state one.
	MinimumPaymentMinor *int64

	// What the bank reported besides, when it did. PreviousBalanceMinor follows
	// the ledger's sign convention; the others are magnitudes.
	PreviousBalanceMinor *int64
	PaymentsMinor        *int64
	DebitsMinor          *int64
	PastDueMinor         *int64

	CreatedAt time.Time
	UpdatedAt time.Time
}

// OwedMinor is what the statement says was owed at the closing, positive; a
// credit balance is negative.
func (s Statement) OwedMinor() int64 { return -s.ClosingBalanceMinor }

// StatementInput is a statement as the user copies it from the bank's
// document. It is a REPLACEMENT on edit: every field is sent every time.
//
// Balances are typed the way a statement prints them: what was OWED, positive
// ("18500.00"), and a credit balance as a negative amount ("-500.00"). They
// are stored in the ledger's convention, sign reversed. Every other amount is a
// magnitude. Amounts are read at the card currency's scale; an empty optional
// field means the statement does not say.
type StatementInput struct {
	PeriodStart string
	ClosingDate string
	DueDate     string

	ClosingOwed    string
	MinimumPayment string

	PreviousOwed string
	Payments     string
	Debits       string
	PastDue      string
}

// resolve validates the input at a currency's scale and builds the row.
func (in StatementInput) resolve(cardID int64, minorUnit int) (Statement, error) {
	s := Statement{CardAccountID: cardID}
	var err error
	if s.PeriodStart, err = statementDate("the period start", in.PeriodStart); err != nil {
		return Statement{}, err
	}
	if s.ClosingDate, err = statementDate("the closing date", in.ClosingDate); err != nil {
		return Statement{}, err
	}
	if s.PeriodStart.After(s.ClosingDate) {
		return Statement{}, fmt.Errorf("%w: the period starts after the closing date", ErrInvalidStatement)
	}
	if strings.TrimSpace(in.DueDate) != "" {
		due, err := statementDate("the due date", in.DueDate)
		if err != nil {
			return Statement{}, err
		}
		if !due.After(s.ClosingDate) {
			return Statement{}, fmt.Errorf("%w: payment falls due after the closing date", ErrInvalidStatement)
		}
		s.DueDate = &due
	}

	owed, err := statementAmount("the balance at the closing", in.ClosingOwed, minorUnit, true)
	if err != nil {
		return Statement{}, err
	}
	if owed == nil {
		return Statement{}, fmt.Errorf("%w: the balance at the closing is required", ErrInvalidStatement)
	}
	s.ClosingBalanceMinor = -*owed
	if s.MinimumPaymentMinor, err = statementAmount("the minimum payment", in.MinimumPayment, minorUnit, false); err != nil {
		return Statement{}, err
	}
	previous, err := statementAmount("the previous balance", in.PreviousOwed, minorUnit, true)
	if err != nil {
		return Statement{}, err
	}
	if previous != nil {
		v := -*previous
		s.PreviousBalanceMinor = &v
	}
	if s.PaymentsMinor, err = statementAmount("the payments", in.Payments, minorUnit, false); err != nil {
		return Statement{}, err
	}
	if s.DebitsMinor, err = statementAmount("the debits", in.Debits, minorUnit, false); err != nil {
		return Statement{}, err
	}
	if s.PastDueMinor, err = statementAmount("the past-due amount", in.PastDue, minorUnit, false); err != nil {
		return Statement{}, err
	}
	return s, nil
}

func statementDate(what, raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("%w: %s is required, as YYYY-MM-DD", ErrInvalidStatement, what)
	}
	d, err := database.ParseDate(trimmed)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s %q is not a date in the form YYYY-MM-DD", ErrInvalidStatement, what, trimmed)
	}
	return d, nil
}

// statementAmount reads an optional amount; signed says whether a negative one
// is meaningful (a balance) or a mistake (a minimum, a sum of payments).
func statementAmount(what, raw string, minorUnit int, signed bool) (*int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	v, err := money.Parse(trimmed, minorUnit)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %q is not an amount in the card's currency", ErrInvalidStatement, what, trimmed)
	}
	if !signed && v < 0 {
		return nil, fmt.Errorf("%w: %s cannot be negative", ErrInvalidStatement, what)
	}
	return &v, nil
}

// sameFigures reports whether two statements state the same thing: what makes
// a second submission of one statement a replay rather than a conflict.
func sameFigures(a, b Statement) bool {
	return a.PeriodStart.Equal(b.PeriodStart) && a.ClosingDate.Equal(b.ClosingDate) &&
		equalDate(a.DueDate, b.DueDate) && a.ClosingBalanceMinor == b.ClosingBalanceMinor &&
		equalInt(a.MinimumPaymentMinor, b.MinimumPaymentMinor) &&
		equalInt(a.PreviousBalanceMinor, b.PreviousBalanceMinor) &&
		equalInt(a.PaymentsMinor, b.PaymentsMinor) && equalInt(a.DebitsMinor, b.DebitsMinor) &&
		equalInt(a.PastDueMinor, b.PastDueMinor)
}

func equalInt(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalDate(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// --- repository ---------------------------------------------------------------

const statementColumns = `id, card_account_id, period_start, closing_date, due_date,
                          closing_balance_minor, minimum_payment_minor, previous_balance_minor,
                          payments_minor, debits_minor, past_due_minor, created_at, updated_at`

// GetStatement returns one statement of a card, or ErrStatementNotFound.
func (r *Repository) GetStatement(ctx context.Context, cardID, statementID int64) (Statement, error) {
	s, err := scanStatement(r.db.QueryRowContext(ctx,
		`SELECT `+statementColumns+` FROM credit_statements WHERE id = ? AND card_account_id = ?`,
		statementID, cardID))
	if errors.Is(err, sql.ErrNoRows) {
		return Statement{}, fmt.Errorf("statement %d of card %d: %w", statementID, cardID, ErrStatementNotFound)
	}
	if err != nil {
		return Statement{}, fmt.Errorf("reading statement %d: %w", statementID, err)
	}
	return s, nil
}

// StatementClosingOn returns the card's statement closing on a day, and false
// when there is none.
func (r *Repository) StatementClosingOn(ctx context.Context, cardID int64, closing time.Time) (Statement, bool, error) {
	s, err := scanStatement(r.db.QueryRowContext(ctx,
		`SELECT `+statementColumns+` FROM credit_statements WHERE card_account_id = ? AND closing_date = ?`,
		cardID, database.FormatDate(closing)))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Statement{}, false, nil
	case err != nil:
		return Statement{}, false, fmt.Errorf("reading the statement of card %d closing %s: %w",
			cardID, database.FormatDate(closing), err)
	}
	return s, true, nil
}

// ListStatements returns every statement of a card, oldest closing first.
func (r *Repository) ListStatements(ctx context.Context, cardID int64) (_ []Statement, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+statementColumns+` FROM credit_statements WHERE card_account_id = ? ORDER BY closing_date`, cardID)
	if err != nil {
		return nil, fmt.Errorf("listing the statements of card %d: %w", cardID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing statement rows: %w", cerr))
		}
	}()
	statements := make([]Statement, 0)
	for rows.Next() {
		s, err := scanStatement(rows)
		if err != nil {
			return nil, fmt.Errorf("reading a statement: %w", err)
		}
		statements = append(statements, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating statements: %w", err)
	}
	return statements, nil
}

// InsertStatement stores a statement and returns its id.
func (r *Repository) InsertStatement(ctx context.Context, s Statement) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO credit_statements (card_account_id, period_start, closing_date, due_date,
		                               closing_balance_minor, minimum_payment_minor, previous_balance_minor,
		                               payments_minor, debits_minor, past_due_minor, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.CardAccountID, database.FormatDate(s.PeriodStart), database.FormatDate(s.ClosingDate), optionalDate(s.DueDate),
		s.ClosingBalanceMinor, s.MinimumPaymentMinor, s.PreviousBalanceMinor,
		s.PaymentsMinor, s.DebitsMinor, s.PastDueMinor,
		database.FormatTime(s.CreatedAt), database.FormatTime(s.UpdatedAt))
	if err != nil {
		return 0, fmt.Errorf("inserting a statement of card %d: %w", s.CardAccountID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading the new statement's id: %w", err)
	}
	return id, nil
}

// UpdateStatement replaces a statement's figures. created_at is untouched.
func (r *Repository) UpdateStatement(ctx context.Context, s Statement) error {
	if _, err := r.db.ExecContext(ctx, `
		UPDATE credit_statements
		   SET period_start = ?, closing_date = ?, due_date = ?, closing_balance_minor = ?,
		       minimum_payment_minor = ?, previous_balance_minor = ?, payments_minor = ?,
		       debits_minor = ?, past_due_minor = ?, updated_at = ?
		 WHERE id = ? AND card_account_id = ?`,
		database.FormatDate(s.PeriodStart), database.FormatDate(s.ClosingDate), optionalDate(s.DueDate),
		s.ClosingBalanceMinor, s.MinimumPaymentMinor, s.PreviousBalanceMinor, s.PaymentsMinor,
		s.DebitsMinor, s.PastDueMinor, database.FormatTime(s.UpdatedAt), s.ID, s.CardAccountID); err != nil {
		return fmt.Errorf("updating statement %d: %w", s.ID, err)
	}
	return nil
}

// Assertions returns the asserted statement of every movement of a card that
// has one, by transaction id.
func (r *Repository) Assertions(ctx context.Context, cardID int64) (_ map[int64]int64, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.transaction_id, a.statement_id
		  FROM credit_statement_assignments a
		  JOIN credit_statements s ON s.id = a.statement_id
		 WHERE s.card_account_id = ?`, cardID)
	if err != nil {
		return nil, fmt.Errorf("listing the assertions of card %d: %w", cardID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing assertion rows: %w", cerr))
		}
	}()
	out := make(map[int64]int64)
	for rows.Next() {
		var tx, statement int64
		if err := rows.Scan(&tx, &statement); err != nil {
			return nil, fmt.Errorf("reading an assertion: %w", err)
		}
		out[tx] = statement
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating assertions: %w", err)
	}
	return out, nil
}

// LatestAssertedDate returns the latest effective date of the movements
// asserted into a statement, and false when none is.
func (r *Repository) LatestAssertedDate(ctx context.Context, statementID int64) (time.Time, bool, error) {
	var latest sql.NullString
	if err := r.db.QueryRowContext(ctx, `
		SELECT MAX(t.effective_date)
		  FROM credit_statement_assignments a
		  JOIN transactions t ON t.id = a.transaction_id
		 WHERE a.statement_id = ?`, statementID).Scan(&latest); err != nil {
		return time.Time{}, false, fmt.Errorf("reading the assertions of statement %d: %w", statementID, err)
	}
	if !latest.Valid {
		return time.Time{}, false, nil
	}
	d, err := database.ParseDate(latest.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("statement %d: %w", statementID, err)
	}
	return d, true, nil
}

// SetAssertion records that a movement belongs to a statement, replacing any
// earlier assertion about it.
func (r *Repository) SetAssertion(ctx context.Context, transactionID, statementID int64, at time.Time) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO credit_statement_assignments (transaction_id, statement_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT (transaction_id) DO UPDATE SET statement_id = excluded.statement_id, created_at = excluded.created_at`,
		transactionID, statementID, database.FormatTime(at)); err != nil {
		return fmt.Errorf("asserting the statement of transaction %d: %w", transactionID, err)
	}
	return nil
}

// ClearAssertion withdraws the assertion about a movement: its membership is
// the calendar's again. Like unlinking a refund, it corrects an assertion and
// changes no amount.
func (r *Repository) ClearAssertion(ctx context.Context, transactionID int64) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM credit_statement_assignments WHERE transaction_id = ?`, transactionID); err != nil {
		return fmt.Errorf("withdrawing the assertion about transaction %d: %w", transactionID, err)
	}
	return nil
}

// Movements returns every movement of a card, with its transaction's type and
// date: what the obligation engine classifies. Oldest first, by date and id.
func (r *Repository) Movements(ctx context.Context, cardID int64) (_ []Movement, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.transaction_id, t.transaction_type, t.effective_date, t.description, m.amount_minor
		  FROM account_movements m
		  JOIN transactions t ON t.id = m.transaction_id
		 WHERE m.account_id = ?
		 ORDER BY t.effective_date, m.transaction_id`, cardID)
	if err != nil {
		return nil, fmt.Errorf("listing the movements of card %d: %w", cardID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing movement rows: %w", cerr))
		}
	}()
	out := make([]Movement, 0)
	for rows.Next() {
		var (
			m           Movement
			txType      string
			date        string
			description sql.NullString
		)
		if err := rows.Scan(&m.TransactionID, &txType, &date, &description, &m.AmountMinor); err != nil {
			return nil, fmt.Errorf("reading a card movement: %w", err)
		}
		m.Type = ledger.TransactionType(txType)
		if m.Date, err = database.ParseDate(date); err != nil {
			return nil, fmt.Errorf("movement of transaction %d: %w", m.TransactionID, err)
		}
		if description.Valid {
			d := description.String
			m.Description = &d
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating card movements: %w", err)
	}
	return out, nil
}

func optionalDate(d *time.Time) any {
	if d == nil {
		return nil
	}
	return database.FormatDate(*d)
}

func scanStatement(s scanner) (Statement, error) {
	var (
		st                   Statement
		start, closing       string
		due                  sql.NullString
		minimum, previous    sql.NullInt64
		payments, debits     sql.NullInt64
		pastDue              sql.NullInt64
		createdAt, updatedAt string
	)
	if err := s.Scan(&st.ID, &st.CardAccountID, &start, &closing, &due, &st.ClosingBalanceMinor,
		&minimum, &previous, &payments, &debits, &pastDue, &createdAt, &updatedAt); err != nil {
		return Statement{}, err
	}
	var err error
	if st.PeriodStart, err = database.ParseDate(start); err != nil {
		return Statement{}, fmt.Errorf("statement %d period_start: %w", st.ID, err)
	}
	if st.ClosingDate, err = database.ParseDate(closing); err != nil {
		return Statement{}, fmt.Errorf("statement %d closing_date: %w", st.ID, err)
	}
	if due.Valid {
		d, err := database.ParseDate(due.String)
		if err != nil {
			return Statement{}, fmt.Errorf("statement %d due_date: %w", st.ID, err)
		}
		st.DueDate = &d
	}
	st.MinimumPaymentMinor = nullInt(minimum)
	st.PreviousBalanceMinor = nullInt(previous)
	st.PaymentsMinor = nullInt(payments)
	st.DebitsMinor = nullInt(debits)
	st.PastDueMinor = nullInt(pastDue)
	if st.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Statement{}, fmt.Errorf("statement %d created_at: %w", st.ID, err)
	}
	if st.UpdatedAt, err = database.ParseTime(updatedAt); err != nil {
		return Statement{}, fmt.Errorf("statement %d updated_at: %w", st.ID, err)
	}
	return st, nil
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}
