package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Repository translates between Settings and SQL.
//
// It is a concrete struct over database.DBTX, not an interface: there is one
// implementation, tests run against real SQLite, and an interface with a single
// implementation and no test double is indirection without a purpose (TD-021).
// Building it over DBTX is what lets the service read the currency and write the
// setting inside one transaction.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or a transaction.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

// settingsID is the only id this table ever holds. It is named rather than
// written as a bare 1 at three call sites, and the database enforces it with a
// CHECK so no code path can create a second row.
const settingsID = 1

// Get reads the settings row.
//
// A missing row is ErrMissing rather than a zero value: migration 00012 creates
// it, so its absence is an integrity failure and must not be silently papered
// over with a default that the rest of the application would then treat as the
// user's choice.
func (r *Repository) Get(ctx context.Context) (Settings, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT base_currency_code, created_at, updated_at
		  FROM app_settings
		 WHERE id = ?`, settingsID)

	var (
		s                  Settings
		createdAt, updated string
	)
	switch err := row.Scan(&s.BaseCurrencyCode, &createdAt, &updated); {
	case errors.Is(err, sql.ErrNoRows):
		return Settings{}, ErrMissing
	case err != nil:
		return Settings{}, fmt.Errorf("reading the application settings: %w", err)
	}

	var err error
	if s.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Settings{}, fmt.Errorf("reading the settings created_at: %w", err)
	}
	if s.UpdatedAt, err = database.ParseTime(updated); err != nil {
		return Settings{}, fmt.Errorf("reading the settings updated_at: %w", err)
	}
	return s, nil
}

// UpdateBaseCurrency writes the chosen currency.
//
// It reports ErrMissing when no row was affected, which distinguishes "the
// singleton is gone" from "the write succeeded and changed nothing" -- the
// latter is impossible here because updated_at always changes.
func (r *Repository) UpdateBaseCurrency(ctx context.Context, code string, updatedAt string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE app_settings
		   SET base_currency_code = ?, updated_at = ?
		 WHERE id = ?`, code, updatedAt, settingsID)
	if err != nil {
		return fmt.Errorf("updating the base currency: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the rows the settings update changed: %w", err)
	}
	if affected == 0 {
		return ErrMissing
	}
	return nil
}
