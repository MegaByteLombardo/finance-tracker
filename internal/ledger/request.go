package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// ErrInvalidRequestKey means a form's idempotency token is missing or malformed.
// It is a programming error in the form that rendered it, not something the user
// typed.
var ErrInvalidRequestKey = errors.New("invalid request key")

// Request is what one submission of a form produced (TD-095): the transaction
// it recorded, the account it opened, or both.
type Request struct {
	Key           string
	TransactionID *int64
	AccountID     *int64
}

// ValidateRequestKey checks a token's shape: 16 to 64 characters of
// [0-9a-zA-Z_-], the rule the column's CHECK enforces as well.
func ValidateRequestKey(key string) error {
	if len(key) < 16 || len(key) > 64 {
		return fmt.Errorf("%w: it must be 16 to 64 characters long", ErrInvalidRequestKey)
	}
	for _, r := range key {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return fmt.Errorf("%w: %q is not allowed in it", ErrInvalidRequestKey, r)
		}
	}
	return nil
}

// ReplayedRequestTx returns what a request key already produced, if anything.
//
// It is read inside the transaction that would write -- BEGIN IMMEDIATE holds the
// write lock from the start (TD-075) -- so two submissions of one form cannot
// both find nothing and both record. The primary key is the backstop.
func ReplayedRequestTx(ctx context.Context, exec database.DBTX, key string) (Request, bool, error) {
	var (
		transactionID sql.NullInt64
		accountID     sql.NullInt64
	)
	err := exec.QueryRowContext(ctx,
		`SELECT transaction_id, account_id FROM operation_requests WHERE request_key = ?`, key).
		Scan(&transactionID, &accountID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Request{}, false, nil
	case err != nil:
		return Request{}, false, fmt.Errorf("looking up request key: %w", err)
	}
	request := Request{Key: key}
	if transactionID.Valid {
		id := transactionID.Int64
		request.TransactionID = &id
	}
	if accountID.Valid {
		id := accountID.Int64
		request.AccountID = &id
	}
	return request, true, nil
}

// RecordRequestTx remembers what a request produced, in the transaction that
// produced it, so that a replay finds it.
func RecordRequestTx(ctx context.Context, exec database.DBTX, request Request) error {
	if _, err := exec.ExecContext(ctx,
		`INSERT INTO operation_requests (request_key, transaction_id, account_id, created_at)
		 VALUES (?, ?, ?, ?)`,
		request.Key, request.TransactionID, request.AccountID, database.FormatTime(database.Now())); err != nil {
		return fmt.Errorf("recording request key: %w", err)
	}
	return nil
}
