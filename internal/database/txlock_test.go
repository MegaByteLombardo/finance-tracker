package database_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// TD-075: a read-write transaction takes the write lock when it OPENS.
//
// Two pools over the same file stand in for two processes -- this application
// and a sqlite3 CLI session, or a second container. Within one pool the single
// connection already serialises everything, so only a second pool can show what
// BEGIN IMMEDIATE changes: the second writer waits at BEGIN, before it has read
// anything it could decide on, instead of failing at its first write after the
// decision was taken.
func TestReadWriteTransactionsBeginImmediate(t *testing.T) {
	t.Parallel()

	first, path := openTemp(t)
	second, err := database.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("opening a second pool: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("closing the second pool: %v", err)
		}
	})

	if _, err := first.ExecContext(t.Context(), "CREATE TABLE counter (n INTEGER NOT NULL) STRICT"); err != nil {
		t.Fatalf("creating a table: %v", err)
	}

	holder, err := first.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning the first transaction: %v", err)
	}
	// No statement has run in it yet. Under DEFERRED it would hold no lock at
	// all; under IMMEDIATE it already holds the write lock.

	began := make(chan error, 1)
	go func() {
		tx, err := second.BeginTx(context.Background(), nil)
		if err == nil {
			_, err = tx.ExecContext(context.Background(), "INSERT INTO counter (n) VALUES (1)")
			err = errors.Join(err, tx.Commit())
		}
		began <- err
	}()

	select {
	case err := <-began:
		t.Fatalf("the second writer proceeded while the first transaction was open (err=%v); BEGIN was not IMMEDIATE", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := holder.Commit(); err != nil {
		t.Fatalf("committing the first transaction: %v", err)
	}

	select {
	case err := <-began:
		if err != nil {
			t.Fatalf("the second writer failed after the lock was released: %v", err)
		}
	case <-time.After(time.Duration(database.BusyTimeoutMS) * time.Millisecond):
		t.Fatal("the second writer never proceeded after the first committed")
	}
}

// A ReadOnly transaction keeps a plain BEGIN, so a reader is never queued
// behind a writer in another process. WAL lets it read the last committed state.
func TestReadOnlyTransactionsDoNotTakeTheWriteLock(t *testing.T) {
	t.Parallel()

	first, path := openTemp(t)
	second, err := database.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("opening a second pool: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("closing the second pool: %v", err)
		}
	})

	holder, err := first.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning the writer: %v", err)
	}
	defer func() {
		if err := holder.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("rolling back the writer: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	reader, err := second.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("a read-only transaction waited for the writer: %v", err)
	}
	var one int
	if err := reader.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("reading inside the read-only transaction: %v", err)
	}
	if err := reader.Commit(); err != nil {
		t.Fatalf("closing the read-only transaction: %v", err)
	}
}
