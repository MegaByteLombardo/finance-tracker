// Package application holds workflows that span more than one domain.
//
// Two workflows live here today, each a single user action that touches two
// domains and must be atomic:
//
//   - Opening an account and recording its opening balance (account + ledger).
//     account would have to import ledger, and ledger already imports account.
//   - Recording or correcting an income and bringing its savings suggestion in
//     line (ledger + savings, M7). savings reads the ledger, so the ledger
//     cannot call it; an income committed without its suggestion, or a
//     suggestion computed from an income that then failed to commit, would each
//     be a state no screen could explain.
//   - Opening a credit card, and recording what happens on one (account +
//     creditcard + ledger, M8.1): the card's terms, its charges and the request
//     key that makes a form submission idempotent commit with the ledger's
//     rows. See card.go.
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
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// Service holds the cross-domain workflows.
type Service struct {
	db      *sql.DB
	savings *savings.Service
}

// NewService builds the service over a pool. It keeps the *sql.DB because
// coordinating two domains is precisely what needs its own transaction.
func NewService(db *sql.DB, savingsService *savings.Service) *Service {
	return &Service{db: db, savings: savingsService}
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

	// The generic form opens assets. A credit card is opened with its terms,
	// through OpenCreditCard; opened here it would have no statement day.
	if err := account.RequireAsset(in.Account.Type); err != nil {
		return OpenAccountResult{}, err
	}

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

// RecordIncome records an income and its savings suggestion in ONE transaction
// (TD-080).
//
// Everything runs through that transaction -- the ledger's write, the savings
// rules, the reference rates -- and nothing reaches for the pool: it has one
// connection, and this transaction holds it.
//
// A missing exchange rate is not a failure: the suggestion is recorded as
// pending its valuation and the income commits. Only a real error -- the
// ledger refusing the income, the database failing -- rolls both back.
func (s *Service) RecordIncome(ctx context.Context, in ledger.RecordIncomeInput) (ledger.TransactionView, error) {
	var view ledger.TransactionView
	err := s.inTx(ctx, "recording an income", func(exec database.DBTX) error {
		var err error
		if view, err = ledger.RecordIncomeTx(ctx, exec, in); err != nil {
			return err
		}
		_, err = s.savings.SyncIncomeTx(ctx, exec, view.ID)
		return err
	})
	if err != nil {
		return ledger.TransactionView{}, err
	}
	return view, nil
}

// UpdateTransaction corrects an income or an expense and brings its savings
// suggestion in line, in ONE transaction (TD-080).
//
// Any correction can matter to savings, not only an income's: an expense
// corrected INTO an income becomes eligible, and an income corrected into an
// expense stops being one. The suggestion follows -- revised, invalidated or
// created -- and money already reserved against it (M7.2) is never touched.
//
// A card charge is refused here: it is corrected through UpdateCardCharge, which
// keeps its kind and its account in step (creditcard.ErrChargeEditedAsCharge).
func (s *Service) UpdateTransaction(
	ctx context.Context, id int64, in ledger.UpdateTransactionInput,
) (ledger.TransactionView, error) {
	var view ledger.TransactionView
	err := s.inTx(ctx, "correcting a transaction", func(exec database.DBTX) error {
		isCharge, err := creditcard.IsChargeTx(ctx, exec, id)
		if err != nil {
			return err
		}
		if isCharge {
			return fmt.Errorf("%w: transaction %d", creditcard.ErrChargeEditedAsCharge, id)
		}
		if view, err = ledger.UpdateTransactionTx(ctx, exec, id, in); err != nil {
			return err
		}
		_, err = s.savings.SyncIncomeTx(ctx, exec, id)
		return err
	})
	if err != nil {
		return ledger.TransactionView{}, err
	}
	return view, nil
}

// RecordExpense records an expense and, in the same transaction, what it does
// to savings (M7.4, TD-086): money of a goal it was for, or reserved money
// released -- with a reason -- to cover it. With an empty plan it is exactly
// the ledger's RecordExpense.
//
// The expense is never refused because of savings. An expense the free balance
// does not cover, recorded without a plan, leaves the account overallocated --
// an issue the savings screens show until the user resolves it.
//
// A plan's request key makes the whole action idempotent: submitted twice, it
// returns the expense already recorded and writes nothing.
func (s *Service) RecordExpense(
	ctx context.Context, in ledger.RecordExpenseInput, plan savings.ExpensePlan,
) (ledger.TransactionView, error) {
	var view ledger.TransactionView
	err := s.inTx(ctx, "recording an expense", func(exec database.DBTX) error {
		if !plan.Empty() {
			if id, replay, err := s.savings.ReplayedTransactionTx(ctx, exec, plan.RequestKey); err != nil {
				return err
			} else if replay {
				var gerr error
				view, gerr = ledger.NewRepository(exec).GetView(ctx, id)
				return gerr
			}
		}
		var err error
		if view, err = ledger.RecordExpenseTx(ctx, exec, in); err != nil {
			return err
		}
		return s.savings.ApplyExpensePlanTx(ctx, exec, view.ID, plan)
	})
	if err != nil {
		return ledger.TransactionView{}, err
	}
	return view, nil
}

// RecordTransfer records a transfer and, in the same transaction, moves the
// backing of the reserved money the user chose from its source to its
// destination (M7.4). A transfer never consumes savings by itself.
func (s *Service) RecordTransfer(
	ctx context.Context, in ledger.RecordTransferInput, plan savings.TransferPlan,
) (ledger.TransactionView, error) {
	var view ledger.TransactionView
	err := s.inTx(ctx, "recording a transfer", func(exec database.DBTX) error {
		if !plan.Empty() {
			if id, replay, err := s.savings.ReplayedTransactionTx(ctx, exec, plan.RequestKey); err != nil {
				return err
			} else if replay {
				var gerr error
				view, gerr = ledger.NewRepository(exec).GetView(ctx, id)
				return gerr
			}
		}
		var err error
		if view, err = ledger.RecordTransferTx(ctx, exec, in); err != nil {
			return err
		}
		return s.savings.ApplyTransferPlanTx(ctx, exec, view.ID, plan)
	})
	if err != nil {
		return ledger.TransactionView{}, err
	}
	return view, nil
}

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
