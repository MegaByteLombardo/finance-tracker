package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Service holds the account use cases. It is the only surface a future HTTP
// handler needs.
//
// There is no Delete, and no way to change an account's currency. Accounts are
// archived with SetActive: once movements exist, deleting one would orphan
// history or cascade destructively over it, and re-denominating one would
// reinterpret every movement recorded against it.
type Service struct {
	db   *sql.DB
	repo *Repository
}

// NewService builds the service over a pool.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

// List returns accounts ordered by name.
func (s *Service) List(ctx context.Context, includeInactive bool) ([]Account, error) {
	return s.repo.List(ctx, includeInactive)
}

// Get returns one account, active or not, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id int64) (Account, error) {
	return s.repo.Get(ctx, id)
}

// Create adds an account denominated in an existing, active currency.
//
// The currency lookup and the insert run inside one transaction, both through
// repositories built over the same *sql.Tx. That is what makes "no account is
// ever created with an inactive currency" an invariant rather than a hope: on
// separate connections the currency could be retired between the check and the
// insert. The foreign key guarantees the currency exists; only the transaction
// can guarantee it was still active.
//
// Currency errors are translated into this package's own, so that callers never
// need to import currency to interpret what Create returned.
//
// It creates ASSETS only. A credit card is an account plus its terms, and is
// opened through internal/application, which writes both in one transaction;
// created here it would be a card with no statement day.
func (s *Service) Create(ctx context.Context, in CreateInput) (_ Account, err error) {
	if err := RequireAsset(in.Type); err != nil {
		return Account{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back account creation: %w", rerr))
		}
	}()

	created, err := CreateTx(ctx, tx, in)
	if err != nil {
		return Account{}, err
	}

	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("committing account creation: %w", err)
	}
	return created, nil
}

// RequireAsset refuses a type whose nature is not Asset. It is the guard of
// every generic way of opening an account; the card has its own.
func RequireAsset(t Type) error {
	normalized := Type(strings.ToUpper(strings.TrimSpace(string(t))))
	if normalized.Valid() && normalized.Nature() != Asset {
		return fmt.Errorf("%w: a %s is opened with its terms, through its own form", ErrInvalidInput, normalized)
	}
	return nil
}

// CreateTx is Create without the transaction, so that a workflow spanning more
// than one domain can run it inside a transaction of its own.
//
// It exists for exactly one caller: opening an account and recording its
// opening balance must be atomic, and that workflow cannot live in this package
// or in the ledger without an import cycle. Everything else calls Create.
//
// exec must be an open transaction for the invariant below to mean anything.
// Passing a pool would let the currency be retired between the check and the
// insert.
func CreateTx(ctx context.Context, exec database.DBTX, in CreateInput) (Account, error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return Account{}, err
	}

	// Both repositories are built over the SAME executor.
	currencies := currency.NewRepository(exec)
	accounts := NewRepository(exec)

	cur, err := currencies.Get(ctx, in.CurrencyCode)
	switch {
	case errors.Is(err, currency.ErrNotFound):
		return Account{}, fmt.Errorf("currency %q: %w", in.CurrencyCode, ErrCurrencyNotFound)
	case err != nil:
		return Account{}, fmt.Errorf("looking up currency %q: %w", in.CurrencyCode, err)
	}
	if !cur.IsActive {
		// Decided here, on the field. A retired currency is a fact about the
		// currency, not an error the currency package raises.
		return Account{}, fmt.Errorf("currency %q: %w", in.CurrencyCode, ErrCurrencyInactive)
	}

	now := database.Now()
	return accounts.Insert(ctx, Account{
		Name:         in.Name,
		Type:         in.Type,
		Subtype:      in.Subtype,
		CurrencyCode: cur.Code,
		Institution:  in.Institution,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
}

// UpdateMetadata changes the name, type and institution.
//
// The currency cannot be reached from here: it is not a field of
// UpdateMetadataInput and it appears in no UPDATE statement.
//
// The type may change only within its nature: a bank account may become cash,
// but no asset may become a credit card or the reverse (ErrNatureChange,
// TD-089). The current type is read in the same transaction as the update, so
// the comparison is made against what is actually being overwritten.
func (s *Service) UpdateMetadata(ctx context.Context, id int64, in UpdateMetadataInput) (_ Account, err error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return Account{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the edit of account %d: %w", id, rerr))
		}
	}()

	repo := NewRepository(tx)
	current, err := repo.Get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	if current.Type.Nature() != in.Type.Nature() {
		return Account{}, fmt.Errorf("%w: %q is %s and cannot become %s",
			ErrNatureChange, current.Name, current.Type, in.Type)
	}

	if err := repo.UpdateMetadata(ctx, id, in.Name, in.Type, in.Subtype, in.Institution, database.Now()); err != nil {
		return Account{}, err
	}
	updated, err := repo.Get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("committing the edit of account %d: %w", id, err)
	}
	return updated, nil
}

// SetActive archives an account, or brings it back. Both directions are
// allowed: archiving is reversible and destroys nothing.
func (s *Service) SetActive(ctx context.Context, id int64, isActive bool) (Account, error) {
	if err := s.repo.SetActive(ctx, id, isActive, database.Now()); err != nil {
		return Account{}, err
	}
	return s.repo.Get(ctx, id)
}
