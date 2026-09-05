// Package application holds workflows that span more than one domain.
//
// It exists for exactly one reason today: opening an account and recording its
// opening balance is a single user action that touches both account and ledger,
// and it must be atomic. That workflow cannot live in either package —
// account would have to import ledger, and ledger already imports account — so
// it lives here.
//
// This is not a general application layer and it is not a home for
// pass-through wrappers. A service belongs here when, and only when, it
// coordinates two domains inside one transaction. Everything else calls its
// domain service directly.
package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// Service holds the cross-domain workflows.
type Service struct {
	db *sql.DB
}

// NewService builds the service over a pool. It keeps the *sql.DB because
// coordinating two domains is precisely what needs its own transaction.
func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// OpenAccountInput is the input to Service.OpenAccount.
type OpenAccountInput struct {
	// Account is the account to create, exactly as account.Service.Create would
	// take it.
	Account account.CreateInput

	// OpeningBalance is the amount the account starts at, as a decimal string.
	//
	// nil or blank means no opening balance. An amount of zero is accepted and
	// also records nothing: an account with no movements already has a balance
	// of zero, so a zero-amount row would assert nothing.
	OpeningBalance *string

	// OpeningBalanceDate is "YYYY-MM-DD" and is required whenever
	// OpeningBalance names an amount. It is ignored otherwise.
	OpeningBalanceDate string
}

// OpenAccountResult is what OpenAccount produced.
type OpenAccountResult struct {
	Account account.Account

	// OpeningBalance is nil when none was recorded, either because none was
	// asked for or because the amount was zero.
	OpeningBalance *ledger.TransactionView
}

// OpenAccount creates an account and, optionally, records its opening balance —
// both in one transaction, or neither.
//
// This is what the "new account" form calls, always, even when the balance
// field is left empty: one code path rather than two, and no window in which an
// account exists that the user believes has a balance.
//
// The alternative a handler would otherwise write is not equivalent:
//
//	acct, err := accounts.Create(ctx, in)   // committed
//	if err == nil {
//	    ledgerSvc.RecordOpeningBalance(...) // may fail, independently
//	}
//
// That leaves an account with no starting balance and no error the user ever
// sees, and "fixing" it with a compensating delete is worse — the delete can
// fail too.
//
// Errors are the ones account and ledger raise, matched with errors.Is against
// their sentinels. This method declares none of its own: a caller has already
// named both domains in its input and receives values from both in its result,
// so these are the composition it asked for rather than a leaked internal.
func (s *Service) OpenAccount(ctx context.Context, in OpenAccountInput) (_ OpenAccountResult, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenAccountResult{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back account opening: %w", rerr))
		}
	}()

	// Both steps run through the SAME transaction. That is the whole point of
	// this method.
	created, err := account.CreateTx(ctx, tx, in.Account)
	if err != nil {
		return OpenAccountResult{}, err
	}

	var opening *ledger.TransactionView
	if amount := openingAmount(in.OpeningBalance); amount != "" {
		opening, err = ledger.RecordOpeningBalanceTx(ctx, tx, ledger.RecordOpeningBalanceInput{
			AccountID:     created.ID,
			Amount:        amount,
			EffectiveDate: in.OpeningBalanceDate,
		})
		if err != nil {
			return OpenAccountResult{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return OpenAccountResult{}, fmt.Errorf("committing account opening: %w", err)
	}
	return OpenAccountResult{Account: created, OpeningBalance: opening}, nil
}

// openingAmount reads the optional balance field.
//
// A form always posts a string, so an untouched field arrives as "". Both that
// and a nil pointer mean "no opening balance", and neither is an error: leaving
// the field empty is how a user says the account starts at zero.
//
// A value that is present but not a valid amount is NOT swallowed here — it is
// passed on so the ledger rejects it, because a user who typed something meant
// something.
func openingAmount(balance *string) string {
	if balance == nil {
		return ""
	}
	return strings.TrimSpace(*balance)
}
