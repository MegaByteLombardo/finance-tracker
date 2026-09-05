package category

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Service holds the category use cases. It is the only surface a handler needs;
// nothing above it should see SQL.
//
// There is no Delete. Transactions reference categories permanently, and the
// schema backs that up with ON DELETE RESTRICT — SetActive retires a category
// from new use without erasing the history filed under it.
type Service struct {
	db   *sql.DB
	repo *Repository
}

// NewService builds the service over a pool. It keeps the *sql.DB so that
// operations needing atomicity can open their own transaction.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

// List returns categories ordered by type, then name.
//
// A transaction form calls it without IncludeInactive, so an archived category
// cannot be picked for new money; a management screen calls it with.
func (s *Service) List(ctx context.Context, in ListInput) ([]Category, error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, in)
}

// Get returns one category, active or not, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id int64) (Category, error) {
	return s.repo.Get(ctx, id)
}

// Create adds a category. It returns ErrConflict if that name is already used
// for that type.
//
// Two categories called "Comida" under EXPENSE are a data-entry mistake rather
// than a distinction — nothing downstream could tell the user which is which.
// The same name across the two types is legitimate: "Intereses" is both earned
// and paid.
//
// The existence check and the insert run in one transaction so that the
// conflict is determined rather than inferred. Inferring it from the driver's
// error is not possible in any case: modernc's Error.Code() reports the primary
// SQLite result code, so a duplicate key, a foreign key violation and a failed
// CHECK all arrive as SQLITE_CONSTRAINT. The unique index remains as the
// database's backstop; if it ever fires, something bypassed this check.
func (s *Service) Create(ctx context.Context, in CreateInput) (_ Category, err error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return Category{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Category{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back category creation: %w", rerr))
		}
	}()

	repo := NewRepository(tx)

	switch _, err := repo.FindByName(ctx, in.Type, in.Name, 0); {
	case err == nil:
		return Category{}, fmt.Errorf("category %q of type %s: %w", in.Name, in.Type, ErrConflict)
	case !errors.Is(err, ErrNotFound):
		return Category{}, fmt.Errorf("checking for an existing category %q: %w", in.Name, err)
	}

	now := database.Now()
	created, err := repo.Insert(ctx, Category{
		Name:      in.Name,
		Type:      in.Type,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return Category{}, err
	}

	if err := tx.Commit(); err != nil {
		return Category{}, fmt.Errorf("committing category creation: %w", err)
	}
	return created, nil
}

// UpdateMetadata changes the name.
//
// The type cannot be reached from here: it is not a field of
// UpdateMetadataInput and it appears in no UPDATE statement. Renaming still has
// to respect uniqueness, so the read, the check and the write share one
// transaction for the same reason Create does.
func (s *Service) UpdateMetadata(ctx context.Context, id int64, in UpdateMetadataInput) (_ Category, err error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return Category{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Category{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back category update: %w", rerr))
		}
	}()

	repo := NewRepository(tx)

	// The existing row supplies the type, which the caller cannot send.
	existing, err := repo.Get(ctx, id)
	if err != nil {
		return Category{}, err
	}

	switch _, err := repo.FindByName(ctx, existing.Type, in.Name, id); {
	case err == nil:
		return Category{}, fmt.Errorf("category %q of type %s: %w", in.Name, existing.Type, ErrConflict)
	case !errors.Is(err, ErrNotFound):
		return Category{}, fmt.Errorf("checking for an existing category %q: %w", in.Name, err)
	}

	if err := repo.UpdateMetadata(ctx, id, in.Name, database.Now()); err != nil {
		return Category{}, err
	}

	updated, err := repo.Get(ctx, id)
	if err != nil {
		return Category{}, err
	}

	if err := tx.Commit(); err != nil {
		return Category{}, fmt.Errorf("committing category update: %w", err)
	}
	return updated, nil
}

// SetActive retires a category from new use, or brings it back. Both directions
// are allowed: archiving is a reversible decision that destroys nothing, and
// transactions already filed under the category keep displaying it.
func (s *Service) SetActive(ctx context.Context, id int64, isActive bool) (Category, error) {
	if err := s.repo.SetActive(ctx, id, isActive, database.Now()); err != nil {
		return Category{}, err
	}
	return s.repo.Get(ctx, id)
}
