package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// Repository translates between the ledger's models and SQL. It holds no
// business rules and performs no validation.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or an open transaction.
// Passing a *sql.Tx is what lets a service write a transaction and its movement
// atomically.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

// InsertTransaction stores the event and returns it with its assigned id.
func (r *Repository) InsertTransaction(ctx context.Context, t Transaction) (Transaction, error) {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO transactions (transaction_type, category_id, payment_method_id, description,
		                           effective_date, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(t.Type), t.CategoryID, t.PaymentMethodID, t.Description,
		database.FormatDate(t.EffectiveDate),
		database.FormatTime(t.CreatedAt), database.FormatTime(t.UpdatedAt))
	if err != nil {
		return Transaction{}, fmt.Errorf("inserting %s transaction: %w", t.Type, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Transaction{}, fmt.Errorf("reading the id of the new transaction: %w", err)
	}
	t.ID = id
	return t, nil
}

// InsertMovement stores the impact on an account and returns it with its
// assigned id.
func (r *Repository) InsertMovement(ctx context.Context, m Movement) (Movement, error) {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO account_movements (transaction_id, account_id, amount_minor, created_at)
		 VALUES (?, ?, ?, ?)`,
		m.TransactionID, m.AccountID, m.AmountMinor, database.FormatTime(m.CreatedAt))
	if err != nil {
		return Movement{}, fmt.Errorf("inserting movement for transaction %d: %w", m.TransactionID, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Movement{}, fmt.Errorf("reading the id of the new movement: %w", err)
	}
	m.ID = id
	return m, nil
}

// viewQuery joins a movement to everything a listing row displays.
//
// One query rather than a lookup per row: a listing of 50 transactions would
// otherwise issue 200 further queries for their accounts, currencies, categories
// and payment methods. The category and payment method joins are LEFT because an
// opening balance has neither.
const viewQuery = `
	SELECT t.id, t.transaction_type, t.effective_date, t.description,
	       t.payment_method_id, pm.name,
	       t.category_id, cat.name, cat.category_type,
	       m.id, m.account_id, a.name, a.account_type, a.account_subtype, a.institution,
	       a.currency_code, cur.symbol, cur.minor_unit,
	       m.amount_minor, t.created_at
	  FROM account_movements m
	  JOIN transactions t   ON t.id   = m.transaction_id
	  JOIN accounts a       ON a.id   = m.account_id
	  JOIN currencies cur   ON cur.code = a.currency_code
	  LEFT JOIN categories cat     ON cat.id = t.category_id
	  LEFT JOIN payment_methods pm ON pm.id  = t.payment_method_id`

// GetView returns the row for one transaction, or ErrNotFound.
func (r *Repository) GetView(ctx context.Context, id int64) (TransactionView, error) {
	row := r.db.QueryRowContext(ctx, viewQuery+` WHERE t.id = ? ORDER BY m.id LIMIT 1`, id)

	v, err := scanView(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TransactionView{}, fmt.Errorf("transaction %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return TransactionView{}, fmt.Errorf("reading transaction %d: %w", id, err)
	}
	return v, nil
}

// ListViews returns the most recent movements, newest first.
//
// The order is effective_date DESC, id DESC. The second key is what makes it
// total: several transactions routinely share a date, and without it their
// relative order would be undefined between calls, so a row could appear to
// move between two loads of the same page.
func (r *Repository) ListViews(ctx context.Context, limit int) (_ []TransactionView, err error) {
	rows, err := r.db.QueryContext(ctx,
		viewQuery+` ORDER BY t.effective_date DESC, t.id DESC, m.id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing transactions: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing transaction rows: %w", cerr))
		}
	}()

	views := make([]TransactionView, 0)
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, fmt.Errorf("reading transaction row: %w", err)
		}
		views = append(views, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating transactions: %w", err)
	}
	return views, nil
}

// balanceQuery derives every balance from the movements.
//
// The LEFT JOIN with COALESCE is what gives an account with no movements a
// balance of zero rather than dropping it from the result: a newly opened
// account must appear on the dashboard.
//
// SUM over an INTEGER column returns an integer, and SQLite raises "integer
// overflow" rather than silently switching to floating point if the total
// exceeds int64 — which is the behaviour this application wants.
const balanceQuery = `
	SELECT a.id, a.name, a.account_type, a.account_subtype, a.institution, a.is_active,
	       a.currency_code, cur.symbol, cur.minor_unit,
	       COALESCE(SUM(m.amount_minor), 0)
	  FROM accounts a
	  JOIN currencies cur ON cur.code = a.currency_code
	  LEFT JOIN account_movements m ON m.account_id = a.id`

const balanceGrouping = `
	 GROUP BY a.id, a.name, a.account_type, a.account_subtype, a.institution, a.is_active,
	          a.currency_code, cur.symbol, cur.minor_unit`

// GetBalance returns one account's balance, or ErrAccountNotFound.
func (r *Repository) GetBalance(ctx context.Context, accountID int64) (AccountBalance, error) {
	row := r.db.QueryRowContext(ctx, balanceQuery+` WHERE a.id = ?`+balanceGrouping, accountID)

	b, err := scanBalance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountBalance{}, fmt.Errorf("account %d: %w", accountID, ErrAccountNotFound)
	}
	if err != nil {
		return AccountBalance{}, fmt.Errorf("reading the balance of account %d: %w", accountID, err)
	}
	return b, nil
}

// ListBalances returns every account with its balance, ordered by currency
// code, then name, then id.
//
// That ordering is part of the contract: it lets a dashboard render its
// per-currency sections in one pass, breaking whenever the currency changes,
// with no sorting or grouping in the template.
func (r *Repository) ListBalances(ctx context.Context, includeInactive bool) (_ []AccountBalance, err error) {
	query := balanceQuery
	if !includeInactive {
		query += ` WHERE a.is_active = 1`
	}
	query += balanceGrouping + ` ORDER BY a.currency_code, a.name, a.id`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing balances: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing balance rows: %w", cerr))
		}
	}()

	balances := make([]AccountBalance, 0)
	for rows.Next() {
		b, err := scanBalance(rows)
		if err != nil {
			return nil, fmt.Errorf("reading balance row: %w", err)
		}
		balances = append(balances, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating balances: %w", err)
	}
	return balances, nil
}

// ListCurrencyTotals sums the balances of each currency's accounts.
//
// count(DISTINCT a.id) rather than count(*): the LEFT JOIN produces one row per
// movement, so counting rows would report the number of movements.
func (r *Repository) ListCurrencyTotals(ctx context.Context, includeInactive bool) (_ []CurrencyTotal, err error) {
	query := `
		SELECT cur.code, cur.name, cur.symbol, cur.minor_unit,
		       count(DISTINCT a.id), COALESCE(SUM(m.amount_minor), 0)
		  FROM accounts a
		  JOIN currencies cur ON cur.code = a.currency_code
		  LEFT JOIN account_movements m ON m.account_id = a.id`
	if !includeInactive {
		query += ` WHERE a.is_active = 1`
	}
	query += ` GROUP BY cur.code, cur.name, cur.symbol, cur.minor_unit ORDER BY cur.code`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing currency totals: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing currency total rows: %w", cerr))
		}
	}()

	totals := make([]CurrencyTotal, 0)
	for rows.Next() {
		var (
			total  CurrencyTotal
			symbol sql.NullString
		)
		if err := rows.Scan(&total.CurrencyCode, &total.CurrencyName, &symbol, &total.MinorUnit,
			&total.AccountCount, &total.TotalMinor); err != nil {
			return nil, fmt.Errorf("reading currency total row: %w", err)
		}
		if symbol.Valid {
			value := symbol.String
			total.CurrencySymbol = &value
		}
		total.TotalFormatted = money.Format(total.TotalMinor, total.MinorUnit)
		totals = append(totals, total)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating currency totals: %w", err)
	}
	return totals, nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanView(s scanner) (TransactionView, error) {
	var (
		v               TransactionView
		transactionType string
		effectiveDate   string
		description     sql.NullString
		paymentMethodID sql.NullInt64
		paymentMethod   sql.NullString
		categoryID      sql.NullInt64
		categoryName    sql.NullString
		categoryType    sql.NullString
		accountType     string
		accountSubtype  sql.NullString
		institution     sql.NullString
		currencySymbol  sql.NullString
		createdAt       string
	)

	if err := s.Scan(&v.ID, &transactionType, &effectiveDate, &description,
		&paymentMethodID, &paymentMethod,
		&categoryID, &categoryName, &categoryType,
		&v.MovementID, &v.AccountID, &v.AccountName, &accountType, &accountSubtype, &institution,
		&v.CurrencyCode, &currencySymbol, &v.MinorUnit,
		&v.AmountMinor, &createdAt); err != nil {
		return TransactionView{}, err
	}

	v.Type = TransactionType(transactionType)
	v.AccountType = account.Type(accountType)

	if description.Valid {
		value := description.String
		v.Description = &value
	}
	if paymentMethodID.Valid {
		value := paymentMethodID.Int64
		v.PaymentMethodID = &value
	}
	if paymentMethod.Valid {
		value := paymentMethod.String
		v.PaymentMethodName = &value
	}
	if categoryID.Valid {
		value := categoryID.Int64
		v.CategoryID = &value
	}
	if categoryName.Valid {
		value := categoryName.String
		v.CategoryName = &value
	}
	if categoryType.Valid {
		value := category.Type(categoryType.String)
		v.CategoryType = &value
	}
	if accountSubtype.Valid {
		value := account.Subtype(accountSubtype.String)
		v.AccountSubtype = &value
	}
	if institution.Valid {
		value := institution.String
		v.Institution = &value
	}
	if currencySymbol.Valid {
		value := currencySymbol.String
		v.CurrencySymbol = &value
	}

	// Formatted here, once, so that no caller is ever tempted to do it with a
	// float.
	v.AmountFormatted = money.Format(v.AmountMinor, v.MinorUnit)

	var err error
	if v.EffectiveDate, err = database.ParseDate(effectiveDate); err != nil {
		return TransactionView{}, fmt.Errorf("transaction %d effective_date: %w", v.ID, err)
	}
	if v.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return TransactionView{}, fmt.Errorf("transaction %d created_at: %w", v.ID, err)
	}
	return v, nil
}

func scanBalance(s scanner) (AccountBalance, error) {
	var (
		b              AccountBalance
		accountType    string
		accountSubtype sql.NullString
		institution    sql.NullString
		currencySymbol sql.NullString
	)

	if err := s.Scan(&b.AccountID, &b.AccountName, &accountType, &accountSubtype, &institution,
		&b.IsActive, &b.CurrencyCode, &currencySymbol, &b.MinorUnit, &b.BalanceMinor); err != nil {
		return AccountBalance{}, err
	}

	b.AccountType = account.Type(accountType)

	if accountSubtype.Valid {
		value := account.Subtype(accountSubtype.String)
		b.AccountSubtype = &value
	}
	if institution.Valid {
		value := institution.String
		b.Institution = &value
	}
	if currencySymbol.Valid {
		value := currencySymbol.String
		b.CurrencySymbol = &value
	}

	b.BalanceFormatted = money.Format(b.BalanceMinor, b.MinorUnit)
	return b, nil
}

// transactionColumns is the read projection for the stored event.
const transactionColumns = `id, transaction_type, category_id, payment_method_id, description,
                            effective_date, created_at, updated_at`

// GetTransaction returns the stored event, or ErrNotFound.
//
// Editing needs this as well as the view: the view is a joined listing row, and
// an update has to know what the transaction currently IS before deciding what
// it may become.
func (r *Repository) GetTransaction(ctx context.Context, id int64) (Transaction, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+transactionColumns+` FROM transactions WHERE id = ?`, id)

	var (
		t               Transaction
		transactionType string
		effectiveDate   string
		createdAt       string
		updatedAt       string
		categoryID      sql.NullInt64
		paymentMethodID sql.NullInt64
		description     sql.NullString
	)
	err := row.Scan(&t.ID, &transactionType, &categoryID, &paymentMethodID, &description,
		&effectiveDate, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, fmt.Errorf("transaction %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Transaction{}, fmt.Errorf("reading transaction %d: %w", id, err)
	}

	t.Type = TransactionType(transactionType)
	if categoryID.Valid {
		value := categoryID.Int64
		t.CategoryID = &value
	}
	if paymentMethodID.Valid {
		value := paymentMethodID.Int64
		t.PaymentMethodID = &value
	}
	if description.Valid {
		value := description.String
		t.Description = &value
	}
	if t.EffectiveDate, err = database.ParseDate(effectiveDate); err != nil {
		return Transaction{}, fmt.Errorf("transaction %d effective_date: %w", id, err)
	}
	if t.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Transaction{}, fmt.Errorf("transaction %d created_at: %w", id, err)
	}
	if t.UpdatedAt, err = database.ParseTime(updatedAt); err != nil {
		return Transaction{}, fmt.Errorf("transaction %d updated_at: %w", id, err)
	}
	return t, nil
}

// GetMovementFor returns the movement belonging to a transaction.
//
// M3 writes exactly one movement per transaction, which is what lets editing
// work on "the" movement. When transfers arrive a transaction will have two, and
// this becomes the wrong shape -- deliberately so: it will fail to compile
// rather than silently edit whichever one came first.
func (r *Repository) GetMovementFor(ctx context.Context, transactionID int64) (Movement, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, transaction_id, account_id, amount_minor, created_at
		   FROM account_movements WHERE transaction_id = ? ORDER BY id LIMIT 1`, transactionID)

	var (
		m         Movement
		createdAt string
	)
	err := row.Scan(&m.ID, &m.TransactionID, &m.AccountID, &m.AmountMinor, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Movement{}, fmt.Errorf("transaction %d has no movement: %w", transactionID, ErrNotFound)
	}
	if err != nil {
		return Movement{}, fmt.Errorf("reading the movement of transaction %d: %w", transactionID, err)
	}

	if m.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Movement{}, fmt.Errorf("movement %d created_at: %w", m.ID, err)
	}
	return m, nil
}

// UpdateTransaction rewrites the editable fields of an event.
//
// transaction_type is among them: correcting an expense entered as income is a
// plausible mistake and M3 supports it. created_at is not -- when a row was
// written does not change because its contents were corrected.
func (r *Repository) UpdateTransaction(ctx context.Context, t Transaction) error {
	return r.execOne(ctx, t.ID,
		`UPDATE transactions
		    SET transaction_type = ?, category_id = ?, payment_method_id = ?,
		        description = ?, effective_date = ?, updated_at = ?
		  WHERE id = ?`,
		string(t.Type), t.CategoryID, t.PaymentMethodID, t.Description,
		database.FormatDate(t.EffectiveDate), database.FormatTime(t.UpdatedAt), t.ID)
}

// UpdateMovement rewrites which account a movement lands in and for how much.
//
// created_at is untouched: the movement records the same event it always did.
func (r *Repository) UpdateMovement(ctx context.Context, id, accountID, amountMinor int64) error {
	return r.execOne(ctx, id,
		`UPDATE account_movements SET account_id = ?, amount_minor = ? WHERE id = ?`,
		accountID, amountMinor, id)
}

// execOne runs a statement that must affect exactly one row.
func (r *Repository) execOne(ctx context.Context, id int64, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating row %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for row %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("row %d: %w", id, ErrNotFound)
	}
	return nil
}
