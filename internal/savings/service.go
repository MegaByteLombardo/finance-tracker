package savings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Service holds the savings use cases.
type Service struct {
	db *sql.DB

	// now is the clock behind "today": which policy versions may still be
	// set, and whether an income's date has arrived. A field so a test can
	// state the date.
	now func() time.Time
}

// NewService builds the service over a pool.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now}
}

// today is the current business day on the Venezuelan calendar (TD-051).
func (s *Service) today() time.Time { return database.TodayAt(s.now()) }

// inTx runs fn in one read-write transaction. Every write in this package goes
// through here, and through it the transaction opens with BEGIN IMMEDIATE
// (TD-075): what fn reads is what it decides on, and no other process can
// write in between.
func (s *Service) inTx(ctx context.Context, what string, fn func(exec database.DBTX) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back %s: %w", what, rerr))
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing %s: %w", what, err)
	}
	return nil
}

// inReadTx runs fn in one read-only transaction, so that everything a screen
// shows comes from one consistent state of the file.
func (s *Service) inReadTx(ctx context.Context, fn func(exec database.DBTX) error) (err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("beginning a read: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("closing a read: %w", rerr))
		}
	}()
	return fn(tx)
}

// normalizeReason trims a free-text reason and bounds it like the schema does.
func normalizeReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if reason == "" {
		return "", fmt.Errorf("%w: a reason is required", ErrInvalidInput)
	}
	if len([]rune(reason)) > 255 {
		return "", fmt.Errorf("%w: the reason is longer than 255 characters", ErrInvalidInput)
	}
	return reason, nil
}
