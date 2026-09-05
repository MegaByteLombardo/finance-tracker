package currency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Repository translates between Currency and SQL. It holds no business rules
// and performs no validation: if it is handed invalid data that is a bug, and
// the database's CHECK constraints will say so.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or an open transaction. Passing
// a *sql.Tx is what lets a service read and write atomically.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

// columns is the read projection, listed explicitly so that adding a column to
// the table cannot silently change what Scan expects.
const columns = `code, name, symbol, minor_unit, is_active, created_at, updated_at`

// Get returns one currency, or ErrNotFound.
func (r *Repository) Get(ctx context.Context, code string) (Currency, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM currencies WHERE code = ?`, code)

	c, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Currency{}, fmt.Errorf("currency %q: %w", code, ErrNotFound)
	}
	if err != nil {
		return Currency{}, fmt.Errorf("reading currency %q: %w", code, err)
	}
	return c, nil
}

// List returns currencies ordered by code.
func (r *Repository) List(ctx context.Context, includeInactive bool) (_ []Currency, err error) {
	query := `SELECT ` + columns + ` FROM currencies`
	if !includeInactive {
		query += ` WHERE is_active = 1`
	}
	query += ` ORDER BY code`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing currencies: %w", err)
	}
	defer func() {
		// Closing frees the connection, so its failure matters even when the
		// rows were read successfully. It is joined rather than dropped.
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing currency rows: %w", cerr))
		}
	}()

	currencies := make([]Currency, 0)
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reading currency row: %w", err)
		}
		currencies = append(currencies, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating currencies: %w", err)
	}
	return currencies, nil
}

// Insert stores a new currency.
func (r *Repository) Insert(ctx context.Context, c Currency) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO currencies (code, name, symbol, minor_unit, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.Code, c.Name, c.Symbol, c.MinorUnit, c.IsActive,
		database.FormatTime(c.CreatedAt), database.FormatTime(c.UpdatedAt))
	if err != nil {
		return fmt.Errorf("inserting currency %q: %w", c.Code, err)
	}
	return nil
}

// UpdateMetadata changes only the editable fields.
//
// code and minor_unit are absent from this statement on purpose: they are
// immutable, and the way to guarantee that is for no SQL to name them.
func (r *Repository) UpdateMetadata(
	ctx context.Context, code, name string, symbol *string, updatedAt time.Time,
) error {
	return r.exec(ctx, code,
		`UPDATE currencies SET name = ?, symbol = ?, updated_at = ? WHERE code = ?`,
		name, symbol, database.FormatTime(updatedAt), code)
}

// SetActive retires a currency from new use, or brings it back.
func (r *Repository) SetActive(ctx context.Context, code string, isActive bool, updatedAt time.Time) error {
	return r.exec(ctx, code,
		`UPDATE currencies SET is_active = ?, updated_at = ? WHERE code = ?`,
		isActive, database.FormatTime(updatedAt), code)
}

// exec runs a statement that must affect exactly one currency, turning "no rows
// matched" into ErrNotFound.
func (r *Repository) exec(ctx context.Context, code, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating currency %q: %w", code, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for currency %q: %w", code, err)
	}
	if affected == 0 {
		return fmt.Errorf("currency %q: %w", code, ErrNotFound)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (Currency, error) {
	var (
		c                    Currency
		symbol               sql.NullString
		createdAt, updatedAt string
	)
	if err := s.Scan(&c.Code, &c.Name, &symbol, &c.MinorUnit, &c.IsActive, &createdAt, &updatedAt); err != nil {
		return Currency{}, err
	}

	// A NULL symbol becomes nil, never "": the two mean different things and
	// the distinction must survive the round trip.
	if symbol.Valid {
		value := symbol.String
		c.Symbol = &value
	}

	var err error
	if c.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Currency{}, fmt.Errorf("currency %q created_at: %w", c.Code, err)
	}
	if c.UpdatedAt, err = database.ParseTime(updatedAt); err != nil {
		return Currency{}, fmt.Errorf("currency %q updated_at: %w", c.Code, err)
	}
	return c, nil
}
