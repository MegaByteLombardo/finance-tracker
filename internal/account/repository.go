package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Repository translates between Account and SQL. It holds no business rules and
// performs no validation.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or an open transaction. Passing
// a *sql.Tx is what lets Service.Create check a currency and insert the account
// atomically.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

// columns is the read projection, listed explicitly so that adding a column to
// the table cannot silently change what Scan expects.
//
// There is no balance column, and there will not be one: balances are derived
// from the movement ledger, not stored alongside the account.
const columns = `id, name, account_type, account_subtype, currency_code, institution, is_active, created_at, updated_at`

// Get returns one account, or ErrNotFound.
func (r *Repository) Get(ctx context.Context, id int64) (Account, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM accounts WHERE id = ?`, id)

	a, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, fmt.Errorf("account %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Account{}, fmt.Errorf("reading account %d: %w", id, err)
	}
	return a, nil
}

// List returns accounts ordered by name.
func (r *Repository) List(ctx context.Context, includeInactive bool) (_ []Account, err error) {
	query := `SELECT ` + columns + ` FROM accounts`
	if !includeInactive {
		query += ` WHERE is_active = 1`
	}
	query += ` ORDER BY name, id`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing account rows: %w", cerr))
		}
	}()

	accounts := make([]Account, 0)
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reading account row: %w", err)
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating accounts: %w", err)
	}
	return accounts, nil
}

// Insert stores a new account and returns it with its assigned id.
func (r *Repository) Insert(ctx context.Context, a Account) (Account, error) {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO accounts (name, account_type, account_subtype, currency_code, institution,
		                       is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Name, string(a.Type), subtypeArg(a.Subtype), a.CurrencyCode, a.Institution, a.IsActive,
		database.FormatTime(a.CreatedAt), database.FormatTime(a.UpdatedAt))
	if err != nil {
		return Account{}, fmt.Errorf("inserting account %q: %w", a.Name, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Account{}, fmt.Errorf("reading the id of the new account %q: %w", a.Name, err)
	}
	a.ID = id
	return a, nil
}

// UpdateMetadata changes only the editable fields.
//
// currency_code is absent from this statement on purpose. An account's currency
// is its financial identity: changing it would reinterpret every movement ever
// recorded against it, so no SQL in this package names it outside the insert.
func (r *Repository) UpdateMetadata(
	ctx context.Context, id int64, name string, accountType Type, subtype *Subtype,
	institution *string, updatedAt time.Time,
) error {
	return r.exec(ctx, id,
		`UPDATE accounts
		    SET name = ?, account_type = ?, account_subtype = ?, institution = ?, updated_at = ?
		  WHERE id = ?`,
		name, string(accountType), subtypeArg(subtype), institution,
		database.FormatTime(updatedAt), id)
}

// subtypeArg turns an absent subtype into a SQL NULL. Passing a *Subtype
// directly would hand the driver a type it does not know.
func subtypeArg(s *Subtype) any {
	if s == nil {
		return nil
	}
	return string(*s)
}

// SetActive archives an account, or brings it back.
func (r *Repository) SetActive(ctx context.Context, id int64, isActive bool, updatedAt time.Time) error {
	return r.exec(ctx, id,
		`UPDATE accounts SET is_active = ?, updated_at = ? WHERE id = ?`,
		isActive, database.FormatTime(updatedAt), id)
}

// exec runs a statement that must affect exactly one account, turning "no rows
// matched" into ErrNotFound.
func (r *Repository) exec(ctx context.Context, id int64, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating account %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for account %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("account %d: %w", id, ErrNotFound)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (Account, error) {
	var (
		a                    Account
		accountType          string
		accountSubtype       sql.NullString
		institution          sql.NullString
		createdAt, updatedAt string
	)
	if err := s.Scan(&a.ID, &a.Name, &accountType, &accountSubtype, &a.CurrencyCode, &institution,
		&a.IsActive, &createdAt, &updatedAt); err != nil {
		return Account{}, err
	}

	a.Type = Type(accountType)

	// A NULL subtype becomes nil. Every non-bank account has one.
	if accountSubtype.Valid {
		value := Subtype(accountSubtype.String)
		a.Subtype = &value
	}

	// A NULL institution becomes nil, never "": the two mean different things.
	if institution.Valid {
		value := institution.String
		a.Institution = &value
	}

	var err error
	if a.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Account{}, fmt.Errorf("account %d created_at: %w", a.ID, err)
	}
	if a.UpdatedAt, err = database.ParseTime(updatedAt); err != nil {
		return Account{}, fmt.Errorf("account %d updated_at: %w", a.ID, err)
	}
	return a, nil
}
