package database

import (
	"context"
	"database/sql"
)

// DBTX is the subset of *sql.DB that a repository needs. Both *sql.DB and
// *sql.Tx satisfy it.
//
// This is not a repository abstraction and it does not exist to be mocked —
// tests run against real SQLite. It exists so that a repository can be built
// over either a pool or an open transaction, which is what lets a service
// perform a read and a dependent write inside one transaction. Creating an
// account, for example, must check that its currency is active and insert the
// row atomically; without this, the check and the insert would run on separate
// connections and the invariant would only hold by luck.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
