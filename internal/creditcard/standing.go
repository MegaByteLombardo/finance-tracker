package creditcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// M8.2: the use cases over statements. Recording a statement and asserting a
// membership write to this domain only; what they mean is derived by Derive on
// the next read.

// StandingView is a card with its obligations as of today.
type StandingView struct {
	View
	Standing Standing
}

// Standing returns a card with its obligations as of today.
func (s *Service) Standing(ctx context.Context, accountID int64) (StandingView, error) {
	return standingTx(ctx, s.db, accountID, s.today())
}

func standingTx(ctx context.Context, exec database.DBTX, accountID int64, today time.Time) (StandingView, error) {
	view, err := viewTx(ctx, exec, accountID, today)
	if err != nil {
		return StandingView{}, err
	}
	facts, err := factsTx(ctx, exec, view.Card)
	if err != nil {
		return StandingView{}, err
	}
	return StandingView{View: view, Standing: Derive(facts, today)}, nil
}

// factsTx reads everything the obligations of a card are derived from: three
// queries, whatever the card's history.
func factsTx(ctx context.Context, exec database.DBTX, card Card) (Facts, error) {
	repo := NewRepository(exec)
	statements, err := repo.ListStatements(ctx, card.AccountID)
	if err != nil {
		return Facts{}, err
	}
	movements, err := repo.Movements(ctx, card.AccountID)
	if err != nil {
		return Facts{}, err
	}
	assertions, err := repo.Assertions(ctx, card.AccountID)
	if err != nil {
		return Facts{}, err
	}
	return Facts{Card: card, Statements: statements, Movements: movements, Assertions: assertions}, nil
}

// Reconcile returns a card's standing and the reconciliation of one of its
// statements, or ErrStatementNotFound.
func (s *Service) Reconcile(ctx context.Context, accountID, statementID int64) (StandingView, Reconciliation, error) {
	view, err := s.Standing(ctx, accountID)
	if err != nil {
		return StandingView{}, Reconciliation{}, err
	}
	r, ok := view.Standing.Reconcile(statementID)
	if !ok {
		return StandingView{}, Reconciliation{}, fmt.Errorf("statement %d of card %d: %w", statementID, accountID, ErrStatementNotFound)
	}
	return view, r, nil
}

// GetStatement returns one statement of a card.
func (s *Service) GetStatement(ctx context.Context, accountID, statementID int64) (Statement, error) {
	if _, err := RequireCardTx(ctx, s.db, accountID); err != nil {
		return Statement{}, err
	}
	return NewRepository(s.db).GetStatement(ctx, accountID, statementID)
}

// NextStatementDates proposes the period and closing of the next statement to
// record: the day after the last one recorded, through the calendar's closing
// that follows; with none recorded, the last cycle the calendar closed before
// today. The user corrects them to what the document says.
func (s *Service) NextStatementDates(ctx context.Context, accountID int64) (periodStart, closing time.Time, err error) {
	if _, err := RequireCardTx(ctx, s.db, accountID); err != nil {
		return time.Time{}, time.Time{}, err
	}
	card, err := NewRepository(s.db).Get(ctx, accountID)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	statements, err := NewRepository(s.db).ListStatements(ctx, accountID)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if n := len(statements); n > 0 {
		start := statements[n-1].ClosingDate.AddDate(0, 0, 1)
		return start, CalendarClosingFor(card.StatementDay, start), nil
	}
	today := s.today()
	closing = CalendarClosingFor(card.StatementDay, today)
	if !closing.Before(today) {
		closing = ClosingDate(card.StatementDay, closing.Year(), closing.Month()-1)
	}
	previous := ClosingDate(card.StatementDay, closing.Year(), closing.Month()-1)
	return previous.AddDate(0, 0, 1), closing, nil
}

// RecordStatement records a statement the bank emitted. Submitting the same
// statement twice returns the one already recorded: a card has one statement
// per closing day, so the closing is the statement's own idempotency key. A
// different statement closing the same day is ErrStatementExists.
func (s *Service) RecordStatement(ctx context.Context, accountID int64, in StatementInput) (Statement, error) {
	var out Statement
	err := s.inTx(ctx, "recording a statement", func(exec database.DBTX) error {
		statement, err := s.resolveStatementTx(ctx, exec, accountID, in)
		if err != nil {
			return err
		}
		repo := NewRepository(exec)
		existing, found, err := repo.StatementClosingOn(ctx, accountID, statement.ClosingDate)
		if err != nil {
			return err
		}
		if found {
			if !sameFigures(existing, statement) {
				return fmt.Errorf("%w: %s", ErrStatementExists, database.FormatDate(statement.ClosingDate))
			}
			out = existing
			return nil
		}
		if err := checkOverlapTx(ctx, exec, statement); err != nil {
			return err
		}
		now := database.Now()
		statement.CreatedAt, statement.UpdatedAt = now, now
		if statement.ID, err = repo.InsertStatement(ctx, statement); err != nil {
			return err
		}
		out = statement
		return nil
	})
	return out, err
}

// UpdateStatement corrects a statement: a figure copied wrong, a date. Every
// derived figure follows on the next read (E7). Moving the closing date before
// a movement asserted into it is refused: nothing is billed before it happens.
func (s *Service) UpdateStatement(ctx context.Context, accountID, statementID int64, in StatementInput) (Statement, error) {
	var out Statement
	err := s.inTx(ctx, "correcting a statement", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		current, err := repo.GetStatement(ctx, accountID, statementID)
		if err != nil {
			return err
		}
		statement, err := s.resolveStatementTx(ctx, exec, accountID, in)
		if err != nil {
			return err
		}
		statement.ID, statement.CreatedAt, statement.UpdatedAt = current.ID, current.CreatedAt, database.Now()
		if !statement.ClosingDate.Equal(current.ClosingDate) {
			other, found, err := repo.StatementClosingOn(ctx, accountID, statement.ClosingDate)
			if err != nil {
				return err
			}
			if found && other.ID != statementID {
				return fmt.Errorf("%w: %s", ErrStatementExists, database.FormatDate(statement.ClosingDate))
			}
		}
		latest, asserted, err := repo.LatestAssertedDate(ctx, statementID)
		if err != nil {
			return err
		}
		if asserted && latest.After(statement.ClosingDate) {
			return fmt.Errorf("%w: a movement of %s is asserted into this statement, which would close on %s",
				ErrAssertionAfterClosing, database.FormatDate(latest), database.FormatDate(statement.ClosingDate))
		}
		if err := checkOverlapTx(ctx, exec, statement); err != nil {
			return err
		}
		if err := repo.UpdateStatement(ctx, statement); err != nil {
			return err
		}
		out = statement
		return nil
	})
	return out, err
}

// AssertMembership records that the bank billed a movement of the card in a
// statement -- or, with a nil statement, withdraws the assertion so that the
// calendar decides again. The movement must move this card, and the statement
// must close on or after the movement's date.
func (s *Service) AssertMembership(ctx context.Context, accountID, transactionID int64, statementID *int64) error {
	return s.inTx(ctx, "asserting a statement membership", func(exec database.DBTX) error {
		if _, err := RequireCardTx(ctx, exec, accountID); err != nil {
			return err
		}
		date, err := cardMovementDateTx(ctx, exec, accountID, transactionID)
		if err != nil {
			return err
		}
		repo := NewRepository(exec)
		if statementID == nil {
			return repo.ClearAssertion(ctx, transactionID)
		}
		statement, err := repo.GetStatement(ctx, accountID, *statementID)
		if err != nil {
			return err
		}
		if statement.ClosingDate.Before(date) {
			return fmt.Errorf("%w: the movement is of %s and the statement closed on %s",
				ErrAssertionAfterClosing, database.FormatDate(date), database.FormatDate(statement.ClosingDate))
		}
		return repo.SetAssertion(ctx, transactionID, statement.ID, database.Now())
	})
}

// cardMovementDateTx returns the date of a transaction that moves a card, or
// ErrNotACardMovement.
func cardMovementDateTx(ctx context.Context, exec database.DBTX, accountID, transactionID int64) (time.Time, error) {
	var date string
	err := exec.QueryRowContext(ctx, `
		SELECT t.effective_date
		  FROM account_movements m
		  JOIN transactions t ON t.id = m.transaction_id
		 WHERE m.transaction_id = ? AND m.account_id = ?`, transactionID, accountID).Scan(&date)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, fmt.Errorf("%w: transaction %d", ErrNotACardMovement, transactionID)
	case err != nil:
		return time.Time{}, fmt.Errorf("reading transaction %d: %w", transactionID, err)
	}
	return database.ParseDate(date)
}

// resolveStatementTx validates a statement against its card: the account is a
// card, the amounts are read at its currency's scale, and the statement has
// closed -- a bank does not emit one before its closing date.
func (s *Service) resolveStatementTx(ctx context.Context, exec database.DBTX, accountID int64, in StatementInput) (Statement, error) {
	acct, err := RequireCardTx(ctx, exec, accountID)
	if err != nil {
		return Statement{}, err
	}
	cur, err := currency.NewRepository(exec).Get(ctx, acct.CurrencyCode)
	if err != nil {
		return Statement{}, fmt.Errorf("looking up currency %q of card %d: %w", acct.CurrencyCode, accountID, err)
	}
	statement, err := in.resolve(accountID, cur.MinorUnit)
	if err != nil {
		return Statement{}, err
	}
	if statement.ClosingDate.After(s.today()) {
		return Statement{}, fmt.Errorf("%w: the statement closes on %s, after today",
			ErrInvalidStatement, database.FormatDate(statement.ClosingDate))
	}
	return statement, nil
}

// checkOverlapTx refuses a statement whose period shares a day with another
// statement of the same card.
func checkOverlapTx(ctx context.Context, exec database.DBTX, statement Statement) error {
	others, err := NewRepository(exec).ListStatements(ctx, statement.CardAccountID)
	if err != nil {
		return err
	}
	for _, o := range others {
		if o.ID == statement.ID {
			continue
		}
		if !statement.PeriodStart.After(o.ClosingDate) && !o.PeriodStart.After(statement.ClosingDate) {
			return fmt.Errorf("%w: the statement closing on %s covers %s to %s",
				ErrStatementOverlap, database.FormatDate(o.ClosingDate),
				database.FormatDate(o.PeriodStart), database.FormatDate(o.ClosingDate))
		}
	}
	return nil
}

func (s *Service) inTx(ctx context.Context, what string, fn func(exec database.DBTX) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back %s: %w", what, rerr))
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing %s: %w", what, err)
	}
	return nil
}
