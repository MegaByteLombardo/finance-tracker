package paymentmethod

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Service holds the payment method use cases. It is the only surface a handler
// needs; nothing above it should see SQL.
//
// There is no Delete. Transactions reference payment methods permanently, and
// the schema backs that up with ON DELETE RESTRICT — SetActive retires a method
// from new use without erasing how past money moved.
type Service struct {
	db   *sql.DB
	repo *Repository
}

// NewService builds the service over a pool. It keeps the *sql.DB so that
// operations needing atomicity can open their own transaction.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

// List returns payment methods ordered by name.
//
// A transaction form calls it without IncludeInactive, so an archived method
// cannot be picked for new money; a management screen calls it with.
func (s *Service) List(ctx context.Context, in ListInput) ([]PaymentMethod, error) {
	return s.repo.List(ctx, in)
}

// Get returns one payment method, active or not, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id int64) (PaymentMethod, error) {
	return s.repo.Get(ctx, id)
}

// Create adds a payment method. It returns ErrConflict if the name is taken.
//
// Names are unique exactly, on the trimmed value — deliberately not
// case-insensitively. SQLite's NOCASE folds ASCII only, so claiming
// case-insensitive uniqueness would deliver it for "Zelle"/"zelle" and not for
// "Café"/"CAFÉ", and importing ICU to do it properly is a dependency this
// application will not carry for one index. categories made the opposite choice
// in M2 and keeps it; both are recorded in data-model.md.
//
// The existence check and the insert run in one transaction so that the conflict
// is determined rather than inferred from the driver's error, which cannot be
// classified reliably (TD-023). The unique index remains as the database's
// backstop; if it ever fires, something bypassed this check.
func (s *Service) Create(ctx context.Context, in CreateInput) (_ PaymentMethod, err error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return PaymentMethod{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back payment method creation: %w", rerr))
		}
	}()

	repo := NewRepository(tx)

	switch _, err := repo.FindByName(ctx, in.Name, 0); {
	case err == nil:
		return PaymentMethod{}, fmt.Errorf("payment method %q: %w", in.Name, ErrConflict)
	case !errors.Is(err, ErrNotFound):
		return PaymentMethod{}, fmt.Errorf("checking for an existing payment method %q: %w", in.Name, err)
	}

	now := database.Now()
	created, err := repo.Insert(ctx, PaymentMethod{
		Name:      in.Name,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return PaymentMethod{}, err
	}

	if err := tx.Commit(); err != nil {
		return PaymentMethod{}, fmt.Errorf("committing payment method creation: %w", err)
	}
	return created, nil
}

// UpdateMetadata renames a payment method.
//
// Renaming changes how every past transaction that used this method is
// displayed. See UpdateMetadataInput for why that is the intended behaviour and
// where its one real hazard is recorded.
func (s *Service) UpdateMetadata(ctx context.Context, id int64, in UpdateMetadataInput) (_ PaymentMethod, err error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return PaymentMethod{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back payment method update: %w", rerr))
		}
	}()

	repo := NewRepository(tx)

	if _, err := repo.Get(ctx, id); err != nil {
		return PaymentMethod{}, err
	}

	switch _, err := repo.FindByName(ctx, in.Name, id); {
	case err == nil:
		return PaymentMethod{}, fmt.Errorf("payment method %q: %w", in.Name, ErrConflict)
	case !errors.Is(err, ErrNotFound):
		return PaymentMethod{}, fmt.Errorf("checking for an existing payment method %q: %w", in.Name, err)
	}

	if err := repo.UpdateMetadata(ctx, id, in.Name, database.Now()); err != nil {
		return PaymentMethod{}, err
	}

	updated, err := repo.Get(ctx, id)
	if err != nil {
		return PaymentMethod{}, err
	}

	if err := tx.Commit(); err != nil {
		return PaymentMethod{}, fmt.Errorf("committing payment method update: %w", err)
	}
	return updated, nil
}

// SetActive retires a payment method from new use, or brings it back.
//
// Archiving destroys nothing: transactions already recorded against the method
// keep it and keep displaying it.
func (s *Service) SetActive(ctx context.Context, id int64, isActive bool) (PaymentMethod, error) {
	if err := s.repo.SetActive(ctx, id, isActive, database.Now()); err != nil {
		return PaymentMethod{}, err
	}
	return s.repo.Get(ctx, id)
}
