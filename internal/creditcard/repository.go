package creditcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Repository translates between Card and SQL. It holds no business rules and
// performs no validation.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or an open transaction.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

const columns = `account_id, product_name, credit_limit_minor, statement_day, due_days_after_statement,
                 payment_processing_days, denomination, denomination_confirmed, default_payment_method_id,
                 created_at, updated_at`

// Get returns one card's terms, or ErrNotFound.
func (r *Repository) Get(ctx context.Context, accountID int64) (Card, error) {
	c, err := scan(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM credit_cards WHERE account_id = ?`, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return Card{}, fmt.Errorf("account %d: %w", accountID, ErrNotFound)
	}
	if err != nil {
		return Card{}, fmt.Errorf("reading the terms of card %d: %w", accountID, err)
	}
	return c, nil
}

// List returns every card's terms, by account id.
func (r *Repository) List(ctx context.Context) (_ []Card, err error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM credit_cards ORDER BY account_id`)
	if err != nil {
		return nil, fmt.Errorf("listing cards: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing card rows: %w", cerr))
		}
	}()
	cards := make([]Card, 0)
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reading card row: %w", err)
		}
		cards = append(cards, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating cards: %w", err)
	}
	return cards, nil
}

// Insert stores a card's terms.
func (r *Repository) Insert(ctx context.Context, c Card) error {
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO credit_cards (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.AccountID, c.ProductName, c.CreditLimitMinor, c.StatementDay, c.DueDaysAfterStatement,
		c.PaymentProcessingDays, string(c.Denomination), c.DenominationConfirmed, c.DefaultPaymentMethodID,
		database.FormatTime(c.CreatedAt), database.FormatTime(c.UpdatedAt)); err != nil {
		return fmt.Errorf("inserting the terms of card %d: %w", c.AccountID, err)
	}
	return nil
}

// Update replaces a card's terms. created_at is untouched.
func (r *Repository) Update(ctx context.Context, c Card) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE credit_cards
		   SET product_name = ?, credit_limit_minor = ?, statement_day = ?, due_days_after_statement = ?,
		       payment_processing_days = ?, denomination = ?, denomination_confirmed = ?,
		       default_payment_method_id = ?, updated_at = ?
		 WHERE account_id = ?`,
		c.ProductName, c.CreditLimitMinor, c.StatementDay, c.DueDaysAfterStatement,
		c.PaymentProcessingDays, string(c.Denomination), c.DenominationConfirmed, c.DefaultPaymentMethodID,
		database.FormatTime(c.UpdatedAt), c.AccountID)
	if err != nil {
		return fmt.Errorf("updating the terms of card %d: %w", c.AccountID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for card %d: %w", c.AccountID, err)
	}
	if affected == 0 {
		return fmt.Errorf("account %d: %w", c.AccountID, ErrNotFound)
	}
	return nil
}

// ChargeKindOf returns the kind of a charge, and false when the transaction is
// not one.
func (r *Repository) ChargeKindOf(ctx context.Context, transactionID int64) (ChargeKind, bool, error) {
	var kind string
	err := r.db.QueryRowContext(ctx,
		`SELECT kind FROM credit_charges WHERE transaction_id = ?`, transactionID).Scan(&kind)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("reading the charge of transaction %d: %w", transactionID, err)
	}
	return ChargeKind(kind), true, nil
}

// InsertCharge records that the issuer charged a transaction as a kind.
func (r *Repository) InsertCharge(ctx context.Context, transactionID int64, kind ChargeKind, at time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO credit_charges (transaction_id, kind, created_at) VALUES (?, ?, ?)`,
		transactionID, string(kind), database.FormatTime(at)); err != nil {
		return fmt.Errorf("recording the charge of transaction %d: %w", transactionID, err)
	}
	return nil
}

// UpdateChargeKind corrects what a charge was charged as.
func (r *Repository) UpdateChargeKind(ctx context.Context, transactionID int64, kind ChargeKind) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE credit_charges SET kind = ? WHERE transaction_id = ?`, string(kind), transactionID); err != nil {
		return fmt.Errorf("correcting the charge of transaction %d: %w", transactionID, err)
	}
	return nil
}

// ChargesOfAccount returns the kind of every charge on an account, by
// transaction id: what a card's movement list needs to label its rows.
func (r *Repository) ChargesOfAccount(ctx context.Context, accountID int64) (_ map[int64]ChargeKind, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.transaction_id, c.kind
		  FROM credit_charges c
		  JOIN account_movements m ON m.transaction_id = c.transaction_id
		 WHERE m.account_id = ?`, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing the charges of account %d: %w", accountID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing charge rows: %w", cerr))
		}
	}()
	charges := make(map[int64]ChargeKind)
	for rows.Next() {
		var (
			id   int64
			kind string
		)
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, fmt.Errorf("reading a charge: %w", err)
		}
		charges[id] = ChargeKind(kind)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating charges: %w", err)
	}
	return charges, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (Card, error) {
	var (
		c                    Card
		product              sql.NullString
		limit                sql.NullInt64
		dueDays              sql.NullInt64
		method               sql.NullInt64
		denomination         string
		createdAt, updatedAt string
	)
	if err := s.Scan(&c.AccountID, &product, &limit, &c.StatementDay, &dueDays,
		&c.PaymentProcessingDays, &denomination, &c.DenominationConfirmed, &method,
		&createdAt, &updatedAt); err != nil {
		return Card{}, err
	}
	c.Denomination = Denomination(denomination)
	if product.Valid {
		value := product.String
		c.ProductName = &value
	}
	if limit.Valid {
		value := limit.Int64
		c.CreditLimitMinor = &value
	}
	if dueDays.Valid {
		value := int(dueDays.Int64)
		c.DueDaysAfterStatement = &value
	}
	if method.Valid {
		value := method.Int64
		c.DefaultPaymentMethodID = &value
	}
	var err error
	if c.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Card{}, fmt.Errorf("card %d created_at: %w", c.AccountID, err)
	}
	if c.UpdatedAt, err = database.ParseTime(updatedAt); err != nil {
		return Card{}, fmt.Errorf("card %d updated_at: %w", c.AccountID, err)
	}
	return c, nil
}
