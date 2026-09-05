package currency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Service holds the currency use cases. It is the only surface a future HTTP
// handler needs; nothing above it should see SQL.
//
// There is no Delete. Currencies are referenced permanently by accounts and,
// later, by movements, so removing one is not an operation this application
// offers — SetActive retires a currency without destroying history, and the
// schema backs that up with ON DELETE RESTRICT.
type Service struct {
	db   *sql.DB
	repo *Repository
}

// NewService builds the service over a pool. It keeps the *sql.DB so that
// operations needing atomicity can open their own transaction.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

// List returns currencies ordered by code.
func (s *Service) List(ctx context.Context, includeInactive bool) ([]Currency, error) {
	return s.repo.List(ctx, includeInactive)
}

// Get returns one currency, active or not, or ErrNotFound.
func (s *Service) Get(ctx context.Context, code string) (Currency, error) {
	code, err := normalizeCode(code)
	if err != nil {
		return Currency{}, err
	}
	return s.repo.Get(ctx, code)
}

// Create adds a currency. It returns ErrConflict if the code is already taken.
//
// The existence check and the insert run in one transaction so that the
// conflict is determined rather than inferred. Inferring it from the driver's
// error is not possible here in any case: modernc's Error.Code() reports the
// primary SQLite result code, so a duplicate key, a foreign key violation and a
// failed CHECK all arrive as SQLITE_CONSTRAINT and cannot be told apart without
// reading the message text. The primary key remains as the database's backstop:
// if it ever fires, something bypassed this check and that is a bug, not a
// conflict to report as one.
func (s *Service) Create(ctx context.Context, in CreateInput) (_ Currency, err error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return Currency{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Currency{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back currency creation: %w", rerr))
		}
	}()

	repo := NewRepository(tx)

	switch _, err := repo.Get(ctx, in.Code); {
	case err == nil:
		return Currency{}, fmt.Errorf("currency %q: %w", in.Code, ErrConflict)
	case !errors.Is(err, ErrNotFound):
		return Currency{}, fmt.Errorf("checking for an existing currency %q: %w", in.Code, err)
	}

	now := database.Now()
	created := Currency{
		Code:      in.Code,
		Name:      in.Name,
		Symbol:    in.Symbol,
		MinorUnit: in.MinorUnit,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repo.Insert(ctx, created); err != nil {
		return Currency{}, err
	}

	if err := tx.Commit(); err != nil {
		return Currency{}, fmt.Errorf("committing currency creation: %w", err)
	}
	return created, nil
}

// UpdateMetadata changes the name and symbol.
//
// Code and MinorUnit cannot be reached from here: they are not fields of
// UpdateMetadataInput and they appear in no UPDATE statement. Both are
// immutable — renaming a code would rewrite the identity of every account
// denominated in it, and changing the minor unit would reinterpret every amount
// ever stored in that currency.
func (s *Service) UpdateMetadata(ctx context.Context, code string, in UpdateMetadataInput) (Currency, error) {
	code, err := normalizeCode(code)
	if err != nil {
		return Currency{}, err
	}
	in.normalize()
	if err := in.validate(); err != nil {
		return Currency{}, err
	}

	if err := s.repo.UpdateMetadata(ctx, code, in.Name, in.Symbol, database.Now()); err != nil {
		return Currency{}, err
	}
	return s.repo.Get(ctx, code)
}

// SetActive retires a currency from new use, or brings it back. Both directions
// are allowed: archiving is a reversible decision that destroys nothing.
func (s *Service) SetActive(ctx context.Context, code string, isActive bool) (Currency, error) {
	code, err := normalizeCode(code)
	if err != nil {
		return Currency{}, err
	}

	if err := s.repo.SetActive(ctx, code, isActive, database.Now()); err != nil {
		return Currency{}, err
	}
	return s.repo.Get(ctx, code)
}

// normalizeCode canonicalises a lookup code so that "usd", " USD " and "USD"
// all address the same row.
func normalizeCode(code string) (string, error) {
	in := CreateInput{Code: code}
	in.normalize()
	if err := validateCode(in.Code); err != nil {
		return "", err
	}
	return in.Code, nil
}
