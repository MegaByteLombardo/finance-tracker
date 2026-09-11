package ledger

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

// White-box tests for the two things that cannot be reached from outside the
// package: the transaction boundary under a mid-write failure, and the read
// path's response to a stored transfer that is already broken.
//
// Neither mocks the driver. The first decorates a REAL *sql.Tx so that one of
// its statements fails; the second writes genuinely malformed rows with SQL and
// then reads them back the way the application would.

// failingExec is a database.DBTX that passes everything through to a real
// executor until the nth ExecContext, which it fails instead.
//
// This is not a mock of SQLite: every statement before and after the chosen one
// really runs, against a real open transaction, and the rollback that follows is
// SQLite's. What it injects is the only thing a test otherwise cannot produce —
// a write that fails after another write has already succeeded.
type failingExec struct {
	database.DBTX

	failOn int // 1-based index of the ExecContext that fails
	execs  int
	err    error
}

func (f *failingExec) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	f.execs++
	if f.execs == f.failOn {
		return nil, f.err
	}
	return f.DBTX.ExecContext(ctx, query, args...)
}

// A transfer writes three rows: the transaction, then the source movement, then
// the destination movement. If any of them fails, none of them may survive —
// a transfer with a debit and no credit is money destroyed.
func TestRecordTransferRollsBackAPartialWrite(t *testing.T) {
	t.Parallel()

	// 1 = the transaction, 2 = the source movement, 3 = the destination
	// movement. The last is the one that matters most: by then two rows are
	// already in the transaction.
	for _, failOn := range []int{1, 2, 3} {
		t.Run(execName(failOn), func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			ctx := t.Context()
			seedTwoUSDAccounts(t, db)

			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("beginning transaction: %v", err)
			}

			injected := errors.New("disk went away")
			exec := &failingExec{DBTX: tx, failOn: failOn, err: injected}

			_, err = recordTransferTx(ctx, exec, transferRequest{
				sourceAccountID:      1,
				destinationAccountID: 2,
				amount:               "250.00",
				effectiveDate:        "2026-09-06",
			})
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v, want the injected failure", err)
			}

			if err := tx.Rollback(); err != nil {
				t.Fatalf("rolling back: %v", err)
			}

			// Nothing survived. Not a stray transaction, not a lone movement.
			assertLedgerIsEmpty(t, db)
		})
	}
}

func execName(failOn int) string {
	switch failOn {
	case 1:
		return "the transaction fails"
	case 2:
		return "the source movement fails"
	default:
		return "the destination movement fails"
	}
}

// The same guarantee at the level SQLite provides it: a transaction that is
// rolled back leaves nothing behind, including a half-written transfer.
func TestARolledBackTransferLeavesNothing(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	seedTwoUSDAccounts(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("beginning transaction: %v", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO transactions (id, transaction_type, category_id, payment_method_id,
		                          description, effective_date, created_at, updated_at)
		VALUES (1,'TRANSFER',NULL,NULL,NULL,'2026-09-06',
		        '2026-09-06T10:00:00.000Z','2026-09-06T10:00:00.000Z')`); err != nil {
		t.Fatalf("inserting the transaction: %v", err)
	}
	// Only the debit, as if the credit had failed.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account_movements (id, transaction_id, account_id, amount_minor, created_at)
		VALUES (1,1,1,-25000,'2026-09-06T10:00:00.000Z')`); err != nil {
		t.Fatalf("inserting the source movement: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rolling back: %v", err)
	}

	assertLedgerIsEmpty(t, db)
}

// --- the read path refuses to present a broken transfer ---------------------

// Every case below is unreachable through the service: RecordTransfer and
// UpdateTransfer establish the shape inside one database transaction, and
// nothing deletes a movement. They are written directly with SQL because the
// point is what the READ path does when the data is already wrong — a listing
// that renders half a transfer as though it were whole is a ledger lying
// quietly, and that is worse than an error.
func TestTheReadPathRefusesAMalformedTransfer(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"no destination movement": `
			INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
			VALUES (1,1,1,-25000,'2026-09-06T10:00:00.000Z')`,

		"no source movement": `
			INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
			VALUES (1,1,2,25000,'2026-09-06T10:00:00.000Z')`,

		"both movements negative": `
			INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
			VALUES (1,1,1,-25000,'2026-09-06T10:00:00.000Z'),
			       (2,1,2,-25000,'2026-09-06T10:00:00.000Z')`,

		"both movements positive": `
			INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
			VALUES (1,1,1,25000,'2026-09-06T10:00:00.000Z'),
			       (2,1,2,25000,'2026-09-06T10:00:00.000Z')`,

		"amounts do not cancel out": `
			INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
			VALUES (1,1,1,-25000,'2026-09-06T10:00:00.000Z'),
			       (2,1,2, 30000,'2026-09-06T10:00:00.000Z')`,

		"both ends are the same account": `
			INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
			VALUES (1,1,1,-25000,'2026-09-06T10:00:00.000Z'),
			       (2,1,1, 25000,'2026-09-06T10:00:00.000Z')`,

		"three movements": `
			INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
			VALUES (1,1,1,-25000,'2026-09-06T10:00:00.000Z'),
			       (2,1,2, 15000,'2026-09-06T10:00:00.000Z'),
			       (3,1,2, 10000,'2026-09-06T10:00:00.000Z')`,
	}

	for name, movements := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			ctx := t.Context()
			seedTwoUSDAccounts(t, db)
			seedBareTransfer(t, db)

			if _, err := db.ExecContext(ctx, movements); err != nil {
				t.Fatalf("writing the malformed movements: %v", err)
			}

			repo := NewRepository(db)

			if _, err := repo.GetView(ctx, 1); !errors.Is(err, ErrInconsistentTransfer) {
				t.Errorf("GetView = %v, want ErrInconsistentTransfer", err)
			}

			// The listing must refuse too, rather than skipping the row or
			// showing one side of it.
			if _, err := repo.ListViews(ctx, 50); !errors.Is(err, ErrInconsistentTransfer) {
				t.Errorf("ListViews = %v, want ErrInconsistentTransfer", err)
			}
		})
	}
}

// A transfer with no movements at all has nothing to anchor on, so the read path
// reports it as missing rather than as inconsistent. Either way it is not
// presented, and the orphan is visible to the invariant every ledger test
// asserts.
func TestATransferWithNoMovementsIsNotFound(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	seedTwoUSDAccounts(t, db)
	seedBareTransfer(t, db)

	if _, err := NewRepository(db).GetView(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetView = %v, want ErrNotFound", err)
	}
}

// A well-formed transfer written by hand reads back correctly, which is what
// makes the failures above meaningful rather than a blanket refusal.
func TestAWellFormedTransferReadsBack(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	seedTwoUSDAccounts(t, db)
	seedBareTransfer(t, db)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
		VALUES (1,1,1,-25000,'2026-09-06T10:00:00.000Z'),
		       (2,1,2, 25000,'2026-09-06T10:00:00.000Z')`); err != nil {
		t.Fatalf("writing the movements: %v", err)
	}

	view, err := NewRepository(db).GetView(ctx, 1)
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if view.Transfer == nil {
		t.Fatal("no transfer detail")
	}
	if view.Transfer.SourceAccountID != 1 || view.Transfer.DestinationAccountID != 2 {
		t.Errorf("detail = %d -> %d, want 1 -> 2",
			view.Transfer.SourceAccountID, view.Transfer.DestinationAccountID)
	}
	if view.Transfer.AmountMinor != 25000 || view.Transfer.AmountFormatted != "250.00" {
		t.Errorf("amount = %d/%q, want 25000/\"250.00\"",
			view.Transfer.AmountMinor, view.Transfer.AmountFormatted)
	}
	// Even if the rows had been written destination-first, the sign decides
	// which side is which.
	if view.Transfer.SourceMovementID != 1 || view.Transfer.DestinationMovementID != 2 {
		t.Errorf("movement ids = %d/%d, want 1/2",
			view.Transfer.SourceMovementID, view.Transfer.DestinationMovementID)
	}
}

// The anchor is the lowest-numbered movement, not the negative one, so a
// transfer whose credit happens to have the lower id still reads correctly.
func TestATransferReadsCorrectlyWhateverOrderItsMovementsAreIn(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	seedTwoUSDAccounts(t, db)
	seedBareTransfer(t, db)

	// Credit first: id 1 is the positive movement.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
		VALUES (1,1,2, 25000,'2026-09-06T10:00:00.000Z'),
		       (2,1,1,-25000,'2026-09-06T10:00:00.000Z')`); err != nil {
		t.Fatalf("writing the movements: %v", err)
	}

	view, err := NewRepository(db).GetView(ctx, 1)
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if view.Transfer.SourceAccountID != 1 || view.Transfer.SourceMovementID != 2 {
		t.Errorf("source = account %d / movement %d, want account 1 / movement 2",
			view.Transfer.SourceAccountID, view.Transfer.SourceMovementID)
	}
	if view.Transfer.DestinationAccountID != 2 || view.Transfer.DestinationMovementID != 1 {
		t.Errorf("destination = account %d / movement %d, want account 2 / movement 1",
			view.Transfer.DestinationAccountID, view.Transfer.DestinationMovementID)
	}
	if view.Transfer.AmountMinor != 25000 {
		t.Errorf("amount = %d, want 25000", view.Transfer.AmountMinor)
	}
}

// --- fixtures ---------------------------------------------------------------

func seedTwoUSDAccounts(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO accounts (id,name,account_type,account_subtype,currency_code,institution,is_active,created_at,updated_at)
		VALUES (1,'Mercantil USD','BANK','CHECKING','USD','Mercantil',1,'2026-09-01T10:00:00.000Z','2026-09-01T10:00:00.000Z'),
		       (2,'Efectivo USD','CASH',NULL,'USD',NULL,1,'2026-09-01T10:00:01.000Z','2026-09-01T10:00:01.000Z')`); err != nil {
		t.Fatalf("seeding accounts: %v", err)
	}
}

// seedBareTransfer writes the transaction row with no movements, so that each
// test can attach whatever malformed set it is about.
func seedBareTransfer(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO transactions (id,transaction_type,category_id,payment_method_id,
		                          description,effective_date,created_at,updated_at)
		VALUES (1,'TRANSFER',NULL,NULL,NULL,'2026-09-06',
		        '2026-09-06T10:00:00.000Z','2026-09-06T10:00:00.000Z')`); err != nil {
		t.Fatalf("seeding the transfer: %v", err)
	}
}

func assertLedgerIsEmpty(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()

	var transactions, movements int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM transactions").Scan(&transactions); err != nil {
		t.Fatalf("counting transactions: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM account_movements").Scan(&movements); err != nil {
		t.Fatalf("counting movements: %v", err)
	}
	if transactions != 0 || movements != 0 {
		t.Errorf("%d transaction(s) and %d movement(s) survived, want none", transactions, movements)
	}
}
