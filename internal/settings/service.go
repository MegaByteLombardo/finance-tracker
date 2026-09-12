package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Service holds the settings use cases: read them, and change the base currency
// of valuation.
//
// There is no Create and no Delete. The row is created by migration 00012 and a
// CHECK makes a second one impossible, so the only operation is an update.
type Service struct {
	db   *sql.DB
	repo *Repository
}

// NewService builds the service over a pool.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

// Get returns the application settings.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	return s.repo.Get(ctx)
}

// SetBaseCurrency changes the currency the consolidated net worth is expressed
// in.
//
// The currency lookup and the write run inside ONE transaction, through
// repositories built over the same *sql.Tx. That is what makes "the base
// currency was active when it was chosen" an invariant rather than a race: on
// separate connections the currency could be archived between the check and the
// update. It is the same construction account.CreateTx uses, for the same
// reason.
//
// What this does NOT do is touch the ledger. No transaction is rewritten, no
// movement is touched, and no balance changes -- a fact this method's test
// asserts by comparing every movement before and after, because it is the single
// most important thing about this operation and the easiest to break later.
func (s *Service) SetBaseCurrency(ctx context.Context, code string) (_ Settings, err error) {
	code = normalizeCode(code)
	if verr := validateCode(code); verr != nil {
		return Settings{}, fmt.Errorf("%w: %s", ErrInvalidInput, verr)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the settings update: %w", rerr))
		}
	}()

	currencies := currency.NewRepository(tx)
	repo := NewRepository(tx)

	cur, err := currencies.Get(ctx, code)
	switch {
	case errors.Is(err, currency.ErrNotFound):
		return Settings{}, fmt.Errorf("currency %q: %w", code, ErrCurrencyNotFound)
	case err != nil:
		return Settings{}, fmt.Errorf("looking up currency %q: %w", code, err)
	}
	if !cur.IsActive {
		// Decided here, on the field, exactly as account.CreateTx decides it.
		// Being archived is a fact about the currency, not an error the currency
		// package raises.
		return Settings{}, fmt.Errorf("currency %q: %w", code, ErrCurrencyInactive)
	}

	if err := repo.UpdateBaseCurrency(ctx, cur.Code, database.FormatTime(database.Now())); err != nil {
		return Settings{}, err
	}

	updated, err := repo.Get(ctx)
	if err != nil {
		return Settings{}, err
	}

	if err := tx.Commit(); err != nil {
		return Settings{}, fmt.Errorf("committing the settings update: %w", err)
	}
	return updated, nil
}
