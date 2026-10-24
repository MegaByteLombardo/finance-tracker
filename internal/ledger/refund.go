package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// A REFUND is recorded and corrected only here, in a transaction the caller
// owns (internal/application commits it with its request key). The rules a
// refund shares with an expense -- account, category, channel, date, amount at
// the currency's scale -- are the ones recordTx and updateTx already apply; what
// this file adds is the sign and the link to the expense it gives money back
// for.

// RecordRefundTx records money given back for an expense: one POSITIVE movement
// filed under an EXPENSE category, and -- when the user names it -- the link to
// the expense it refunds.
func RecordRefundTx(ctx context.Context, exec database.DBTX, in RecordRefundInput) (TransactionView, error) {
	view, err := recordTx(ctx, exec, recordRequest{
		txType:          Refund,
		accountID:       in.AccountID,
		amount:          in.Amount,
		categoryID:      &in.CategoryID,
		paymentMethodID: &in.PaymentMethodID,
		description:     in.Description,
		effectiveDate:   in.EffectiveDate,
	})
	if err != nil {
		return TransactionView{}, err
	}
	if in.OriginalTransactionID != nil {
		if err := setRefundLink(ctx, exec, view.ID, nil, in.OriginalTransactionID); err != nil {
			return TransactionView{}, err
		}
	}
	return NewRepository(exec).GetView(ctx, view.ID)
}

// UpdateRefundTx corrects a refund: every field, the link included, is
// replaced. The guard names the one type it accepts (TD-045).
func UpdateRefundTx(ctx context.Context, exec database.DBTX, id int64, in UpdateRefundInput) (TransactionView, error) {
	previous, err := refundLinkOf(ctx, exec, id)
	if err != nil {
		return TransactionView{}, err
	}

	if _, err := updateTx(ctx, exec, id, func(_ context.Context, _ database.DBTX, current Transaction) (recordRequest, error) {
		if current.Type != Refund {
			return recordRequest{}, fmt.Errorf(
				"%w: transaction %d is %s, not a refund", ErrTypeChangeNotAllowed, id, current.Type)
		}
		return recordRequest{
			txType:          Refund,
			accountID:       in.AccountID,
			amount:          in.Amount,
			categoryID:      &in.CategoryID,
			paymentMethodID: &in.PaymentMethodID,
			description:     in.Description,
			effectiveDate:   in.EffectiveDate,
		}, nil
	}, in.AllowCurrencyChange); err != nil {
		return TransactionView{}, err
	}

	if err := setRefundLink(ctx, exec, id, previous, in.OriginalTransactionID); err != nil {
		return TransactionView{}, err
	}
	return NewRepository(exec).GetView(ctx, id)
}

// setRefundLink makes a refund name the original it should, and checks every
// expense whose refunds changed: the one it names now and the one it named
// before. Removing a link is a correction of an assertion, not the deletion of
// a record: no amount, balance or movement is touched.
func setRefundLink(ctx context.Context, exec database.DBTX, refundID int64, previous, next *int64) error {
	switch {
	case next == nil && previous == nil:
		return nil
	case next == nil:
		if _, err := exec.ExecContext(ctx,
			`DELETE FROM transaction_refunds WHERE refund_transaction_id = ?`, refundID); err != nil {
			return fmt.Errorf("unlinking refund %d: %w", refundID, err)
		}
	case previous == nil:
		if _, err := exec.ExecContext(ctx,
			`INSERT INTO transaction_refunds (refund_transaction_id, original_transaction_id, created_at)
			 VALUES (?, ?, ?)`, refundID, *next, database.FormatTime(database.Now())); err != nil {
			return fmt.Errorf("linking refund %d to transaction %d: %w", refundID, *next, err)
		}
	default:
		if _, err := exec.ExecContext(ctx,
			`UPDATE transaction_refunds SET original_transaction_id = ? WHERE refund_transaction_id = ?`,
			*next, refundID); err != nil {
			return fmt.Errorf("relinking refund %d to transaction %d: %w", refundID, *next, err)
		}
	}

	for _, original := range []*int64{next, previous} {
		if original == nil {
			continue
		}
		if err := checkRefundsOf(ctx, exec, *original); err != nil {
			return err
		}
	}
	return nil
}

// refundLinkOf returns the expense a refund names, or nil.
func refundLinkOf(ctx context.Context, exec database.DBTX, refundID int64) (*int64, error) {
	var original int64
	err := exec.QueryRowContext(ctx,
		`SELECT original_transaction_id FROM transaction_refunds WHERE refund_transaction_id = ?`,
		refundID).Scan(&original)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading the link of refund %d: %w", refundID, err)
	}
	return &original, nil
}

// RefundedMinor returns how much of an expense the refunds linked to it give
// back, in the expense's currency. It is what a refund form shows as already
// refunded; it is derived on every read.
func RefundedMinor(ctx context.Context, exec database.DBTX, originalID int64) (int64, error) {
	var total int64
	if err := exec.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(m.amount_minor), 0)
		  FROM transaction_refunds r
		  JOIN account_movements m ON m.transaction_id = r.refund_transaction_id
		 WHERE r.original_transaction_id = ?`, originalID).Scan(&total); err != nil {
		return 0, fmt.Errorf("summing the refunds of transaction %d: %w", originalID, err)
	}
	return total, nil
}

// checkRefundsOf verifies that the refunds naming an expense still can: the
// expense exists, is an EXPENSE, sits in the same account as every one of its
// refunds, and is at least as large as they add up to.
//
// It runs after every write that could break one of those -- linking a refund,
// editing a refund, editing the expense itself -- inside the same transaction,
// so it judges what is about to be committed. A transaction no refund names
// costs one query and passes.
func checkRefundsOf(ctx context.Context, exec database.DBTX, originalID int64) (err error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT r.refund_transaction_id, rm.account_id, rm.amount_minor
		  FROM transaction_refunds r
		  JOIN account_movements rm ON rm.transaction_id = r.refund_transaction_id
		 WHERE r.original_transaction_id = ?
		 ORDER BY r.refund_transaction_id, rm.id`, originalID)
	if err != nil {
		return fmt.Errorf("reading the refunds of transaction %d: %w", originalID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing refund rows: %w", cerr))
		}
	}()

	type refund struct{ accountID, amountMinor int64 }
	var refunds []refund
	for rows.Next() {
		var (
			id int64
			r  refund
		)
		if err := rows.Scan(&id, &r.accountID, &r.amountMinor); err != nil {
			return fmt.Errorf("reading a refund of transaction %d: %w", originalID, err)
		}
		refunds = append(refunds, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating the refunds of transaction %d: %w", originalID, err)
	}
	if len(refunds) == 0 {
		return nil
	}

	repo := NewRepository(exec)
	original, err := repo.GetTransaction(ctx, originalID)
	switch {
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("%w: transaction %d does not exist", ErrRefundOriginal, originalID)
	case err != nil:
		return err
	}
	if original.Type != Expense {
		return fmt.Errorf("%w: transaction %d is %s", ErrRefundOriginal, originalID, original.Type)
	}
	movement, err := repo.GetMovementFor(ctx, originalID)
	if err != nil {
		return err
	}

	var total int64
	for _, r := range refunds {
		if r.accountID != movement.AccountID {
			return fmt.Errorf("%w: expense %d is in another account", ErrRefundOriginal, originalID)
		}
		total += r.amountMinor
	}
	if spent := -movement.AmountMinor; total > spent {
		cur, err := currencyOfAccount(ctx, exec, movement.AccountID)
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s refunded for an expense of %s", ErrRefundExceedsOriginal,
			money.Format(total, cur), money.Format(spent, cur))
	}
	return nil
}

// currencyOfAccount returns the minor unit of an account's currency, for a
// message that states amounts the way the user typed them.
func currencyOfAccount(ctx context.Context, exec database.DBTX, accountID int64) (int, error) {
	var minorUnit int
	if err := exec.QueryRowContext(ctx, `
		SELECT c.minor_unit FROM accounts a JOIN currencies c ON c.code = a.currency_code
		 WHERE a.id = ?`, accountID).Scan(&minorUnit); err != nil {
		return 0, fmt.Errorf("reading the currency of account %d: %w", accountID, err)
	}
	return minorUnit, nil
}
