package paymentmethod

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Repository translates between PaymentMethod and SQL. It holds no business
// rules and performs no validation.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or an open transaction.
// Passing a *sql.Tx is what lets Service.Create check for a duplicate name and
// insert the row atomically.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

// columns is the read projection, listed explicitly so that adding a column to
// the table cannot silently change what Scan expects.
const columns = `id, name, is_active, created_at, updated_at`

// Get returns one payment method, or ErrNotFound.
func (r *Repository) Get(ctx context.Context, id int64) (PaymentMethod, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM payment_methods WHERE id = ?`, id)

	m, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentMethod{}, fmt.Errorf("payment method %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("reading payment method %d: %w", id, err)
	}
	return m, nil
}

// List returns payment methods ordered by name, then id.
func (r *Repository) List(ctx context.Context, in ListInput) (_ []PaymentMethod, err error) {
	query := `SELECT ` + columns + ` FROM payment_methods`
	if !in.IncludeInactive {
		query += ` WHERE is_active = 1`
	}
	query += ` ORDER BY name, id`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing payment methods: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing payment method rows: %w", cerr))
		}
	}()

	methods := make([]PaymentMethod, 0)
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reading payment method row: %w", err)
		}
		methods = append(methods, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating payment methods: %w", err)
	}
	return methods, nil
}

// FindByName looks a method up the way the unique index sees it: by exact name.
//
// It exists so that a duplicate is determined rather than inferred from a driver
// error, which cannot be classified reliably (TD-023). Excluding an id lets an
// update check for a clash with rows other than itself; pass 0 to exclude
// nothing.
func (r *Repository) FindByName(ctx context.Context, name string, excludeID int64) (PaymentMethod, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM payment_methods WHERE name = ? AND id <> ?`, name, excludeID)

	m, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentMethod{}, fmt.Errorf("payment method %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("looking up payment method %q: %w", name, err)
	}
	return m, nil
}

// Insert stores a new payment method and returns it with its assigned id.
func (r *Repository) Insert(ctx context.Context, m PaymentMethod) (PaymentMethod, error) {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO payment_methods (name, is_active, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		m.Name, m.IsActive, database.FormatTime(m.CreatedAt), database.FormatTime(m.UpdatedAt))
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("inserting payment method %q: %w", m.Name, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("reading the id of the new payment method %q: %w", m.Name, err)
	}
	m.ID = id
	return m, nil
}

// UpdateMetadata changes only the name.
func (r *Repository) UpdateMetadata(ctx context.Context, id int64, name string, updatedAt time.Time) error {
	return r.exec(ctx, id,
		`UPDATE payment_methods SET name = ?, updated_at = ? WHERE id = ?`,
		name, database.FormatTime(updatedAt), id)
}

// SetActive archives a payment method, or brings it back.
func (r *Repository) SetActive(ctx context.Context, id int64, isActive bool, updatedAt time.Time) error {
	return r.exec(ctx, id,
		`UPDATE payment_methods SET is_active = ?, updated_at = ? WHERE id = ?`,
		isActive, database.FormatTime(updatedAt), id)
}

// exec runs a statement that must affect exactly one row, turning "no rows
// matched" into ErrNotFound.
func (r *Repository) exec(ctx context.Context, id int64, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating payment method %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for payment method %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("payment method %d: %w", id, ErrNotFound)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (PaymentMethod, error) {
	var (
		m                    PaymentMethod
		createdAt, updatedAt string
	)
	if err := s.Scan(&m.ID, &m.Name, &m.IsActive, &createdAt, &updatedAt); err != nil {
		return PaymentMethod{}, err
	}

	var err error
	if m.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return PaymentMethod{}, fmt.Errorf("payment method %d created_at: %w", m.ID, err)
	}
	if m.UpdatedAt, err = database.ParseTime(updatedAt); err != nil {
		return PaymentMethod{}, fmt.Errorf("payment method %d updated_at: %w", m.ID, err)
	}
	return m, nil
}
