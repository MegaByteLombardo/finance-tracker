package savings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// Repository reads and writes the savings tables. It is a concrete struct over
// database.DBTX, not an interface (TD-021): tests use real SQLite.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or an open transaction.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

// liveStatuses is the SQL spelling of Status.Live, and the predicate of the
// partial unique index idx_savings_suggestions_live.
const liveStatuses = `('PENDING_VALUATION', 'OPEN', 'DISMISSED')`

// --- policy -----------------------------------------------------------------

// ListPolicyVersions returns every version, oldest effective date first, with
// the number of suggestions each governs.
func (r *Repository) ListPolicyVersions(ctx context.Context) (_ []PolicyVersion, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT v.id, v.rate_bp, v.effective_from, v.created_at, v.updated_at,
		       (SELECT count(*) FROM savings_suggestions s WHERE s.policy_version_id = v.id)
		  FROM savings_policy_versions v
		 ORDER BY v.effective_from`)
	if err != nil {
		return nil, fmt.Errorf("listing savings policy versions: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing policy version rows: %w", cerr))
		}
	}()

	versions := make([]PolicyVersion, 0)
	for rows.Next() {
		var (
			v                         PolicyVersion
			from, created, updatedRaw string
		)
		if err := rows.Scan(&v.ID, &v.RateBP, &from, &created, &updatedRaw, &v.SuggestionCount); err != nil {
			return nil, fmt.Errorf("reading a policy version: %w", err)
		}
		if v.EffectiveFrom, err = database.ParseDate(from); err != nil {
			return nil, fmt.Errorf("policy version %d effective_from: %w", v.ID, err)
		}
		if v.CreatedAt, err = database.ParseTime(created); err != nil {
			return nil, fmt.Errorf("policy version %d created_at: %w", v.ID, err)
		}
		if v.UpdatedAt, err = database.ParseTime(updatedRaw); err != nil {
			return nil, fmt.Errorf("policy version %d updated_at: %w", v.ID, err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating policy versions: %w", err)
	}
	return versions, nil
}

// InsertPolicyVersion appends a version.
func (r *Repository) InsertPolicyVersion(ctx context.Context, rateBP int64, from, now time.Time) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_policy_versions (rate_bp, effective_from, created_at, updated_at)
		VALUES (?, ?, ?, ?)`,
		rateBP, database.FormatDate(from), database.FormatTime(now), database.FormatTime(now)); err != nil {
		return fmt.Errorf("inserting a savings policy version: %w", err)
	}
	return nil
}

// CorrectPolicyVersion rewrites a version no suggestion uses. The caller has
// established that, in the same transaction; the statement re-checks it, so a
// version that gained a suggestion in between is never rewritten.
func (r *Repository) CorrectPolicyVersion(ctx context.Context, id, rateBP int64, from, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE savings_policy_versions
		   SET rate_bp = ?, effective_from = ?, updated_at = ?
		 WHERE id = ?
		   AND NOT EXISTS (SELECT 1 FROM savings_suggestions WHERE policy_version_id = ?)`,
		rateBP, database.FormatDate(from), database.FormatTime(now), id, id)
	if err != nil {
		return fmt.Errorf("correcting savings policy version %d: %w", id, err)
	}
	return expectOne(result, fmt.Errorf("savings policy version %d: %w", id, ErrPolicyVersionInUse))
}

// CountSuggestions returns how many suggestion rows exist, of any status.
func (r *Repository) CountSuggestions(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM savings_suggestions`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting savings suggestions: %w", err)
	}
	return n, nil
}

// --- eligibility ------------------------------------------------------------

// CategoryRules returns every explicit category rule, by category id.
func (r *Repository) CategoryRules(ctx context.Context) (_ map[int64]bool, err error) {
	rows, err := r.db.QueryContext(ctx, `SELECT category_id, is_eligible FROM savings_category_rules`)
	if err != nil {
		return nil, fmt.Errorf("listing savings category rules: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing category rule rows: %w", cerr))
		}
	}()

	rules := make(map[int64]bool)
	for rows.Next() {
		var (
			id       int64
			eligible int
		)
		if err := rows.Scan(&id, &eligible); err != nil {
			return nil, fmt.Errorf("reading a category rule: %w", err)
		}
		rules[id] = eligible == 1
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating category rules: %w", err)
	}
	return rules, nil
}

// SetCategoryRule records whether a category's income is saved from.
func (r *Repository) SetCategoryRule(ctx context.Context, categoryID int64, eligible bool, now time.Time) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_category_rules (category_id, is_eligible, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (category_id) DO UPDATE
		   SET is_eligible = excluded.is_eligible, updated_at = excluded.updated_at`,
		categoryID, boolToInt(eligible), database.FormatTime(now), database.FormatTime(now)); err != nil {
		return fmt.Errorf("setting the savings rule of category %d: %w", categoryID, err)
	}
	return nil
}

// Overrides returns every income override, by transaction id.
func (r *Repository) Overrides(ctx context.Context) (_ map[int64]IncomeOverride, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT transaction_id, is_eligible, reason, updated_at FROM savings_income_overrides`)
	if err != nil {
		return nil, fmt.Errorf("listing savings income overrides: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing override rows: %w", cerr))
		}
	}()

	overrides := make(map[int64]IncomeOverride)
	for rows.Next() {
		var id int64
		o, err := scanOverride(rows, &id)
		if err != nil {
			return nil, err
		}
		overrides[id] = o
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating overrides: %w", err)
	}
	return overrides, nil
}

// Override returns one income's override, if it has one.
func (r *Repository) Override(ctx context.Context, transactionID int64) (*IncomeOverride, error) {
	var id int64
	o, err := scanOverride(r.db.QueryRowContext(ctx,
		`SELECT transaction_id, is_eligible, reason, updated_at
		   FROM savings_income_overrides WHERE transaction_id = ?`, transactionID), &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// SetOverride records an explicit exception for one income.
func (r *Repository) SetOverride(ctx context.Context, transactionID int64, eligible bool, reason string, now time.Time) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_income_overrides (transaction_id, is_eligible, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (transaction_id) DO UPDATE
		   SET is_eligible = excluded.is_eligible, reason = excluded.reason, updated_at = excluded.updated_at`,
		transactionID, boolToInt(eligible), reason, database.FormatTime(now), database.FormatTime(now)); err != nil {
		return fmt.Errorf("setting the savings override of income %d: %w", transactionID, err)
	}
	return nil
}

func scanOverride(s scanner, id *int64) (IncomeOverride, error) {
	var (
		o        IncomeOverride
		eligible int
		updated  string
	)
	if err := s.Scan(id, &eligible, &o.Reason, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IncomeOverride{}, err
		}
		return IncomeOverride{}, fmt.Errorf("reading an income override: %w", err)
	}
	o.Eligible = eligible == 1
	var err error
	if o.UpdatedAt, err = database.ParseTime(updated); err != nil {
		return IncomeOverride{}, fmt.Errorf("override of income %d updated_at: %w", *id, err)
	}
	return o, nil
}

// --- suggestions ------------------------------------------------------------

const suggestionSelect = `
	SELECT s.id, s.income_transaction_id, s.revision, s.policy_version_id, v.rate_bp, v.effective_from,
	       s.income_account_id, a.name, s.income_category_id, c.name,
	       s.income_amount_minor, s.income_currency_code, cur.minor_unit, s.income_effective_date,
	       t.description,
	       s.income_usd_minor, s.suggested_usd_minor, s.fx_method, s.valued_at,
	       s.status, s.status_reason, s.created_at, s.updated_at
	  FROM savings_suggestions s
	  JOIN savings_policy_versions v ON v.id = s.policy_version_id
	  JOIN accounts a                ON a.id = s.income_account_id
	  JOIN categories c              ON c.id = s.income_category_id
	  JOIN currencies cur            ON cur.code = s.income_currency_code
	  JOIN transactions t            ON t.id = s.income_transaction_id`

// LiveSuggestion returns an income's live revision, or ErrNotFound.
func (r *Repository) LiveSuggestion(ctx context.Context, transactionID int64) (Suggestion, error) {
	s, err := scanSuggestion(r.db.QueryRowContext(ctx,
		suggestionSelect+` WHERE s.income_transaction_id = ? AND s.status IN `+liveStatuses, transactionID))
	if errors.Is(err, sql.ErrNoRows) {
		return Suggestion{}, fmt.Errorf("income %d has no live suggestion: %w", transactionID, ErrNotFound)
	}
	if err != nil {
		return Suggestion{}, err
	}
	legs, err := r.legsWhere(ctx, `suggestion_id = ?`, s.ID)
	if err != nil {
		return Suggestion{}, err
	}
	s.Legs = legs[s.ID]
	return s, nil
}

// ListLiveSuggestions returns every live revision, newest income first.
func (r *Repository) ListLiveSuggestions(ctx context.Context) ([]Suggestion, error) {
	list, err := r.listSuggestions(ctx,
		suggestionSelect+` WHERE s.status IN `+liveStatuses+` ORDER BY s.income_effective_date DESC, s.id DESC`)
	if err != nil {
		return nil, err
	}
	legs, err := r.legsWhere(ctx,
		`suggestion_id IN (SELECT id FROM savings_suggestions WHERE status IN `+liveStatuses+`)`)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Legs = legs[list[i].ID]
	}
	return list, nil
}

// ListRevisions returns every revision of one income, oldest first.
func (r *Repository) ListRevisions(ctx context.Context, transactionID int64) ([]Suggestion, error) {
	list, err := r.listSuggestions(ctx,
		suggestionSelect+` WHERE s.income_transaction_id = ? ORDER BY s.revision`, transactionID)
	if err != nil {
		return nil, err
	}
	legs, err := r.legsWhere(ctx,
		`suggestion_id IN (SELECT id FROM savings_suggestions WHERE income_transaction_id = ?)`, transactionID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Legs = legs[list[i].ID]
	}
	return list, nil
}

// NextRevision returns the revision number the next suggestion about an income
// takes: one more than the highest it has ever had.
func (r *Repository) NextRevision(ctx context.Context, transactionID int64) (int, error) {
	var highest int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision), 0) FROM savings_suggestions WHERE income_transaction_id = ?`,
		transactionID).Scan(&highest); err != nil {
		return 0, fmt.Errorf("reading the revisions of income %d: %w", transactionID, err)
	}
	return highest + 1, nil
}

// newSuggestion is what InsertSuggestion writes.
type newSuggestion struct {
	transactionID int64
	revision      int
	policyVersion int64
	accountID     int64
	categoryID    int64
	amountMinor   int64
	currencyCode  string
	effectiveDate time.Time
	valuation     *valuation
	now           time.Time
}

// InsertSuggestion writes a live revision -- OPEN when it carries a valuation,
// PENDING_VALUATION when it does not -- and the legs of its valuation.
func (r *Repository) InsertSuggestion(ctx context.Context, in newSuggestion) (int64, error) {
	status := StatusPendingValuation
	var (
		incomeUSD, suggested, method, valuedAt any
	)
	if in.valuation != nil {
		status = StatusOpen
		incomeUSD = in.valuation.incomeUSDMinor
		suggested = in.valuation.suggestedUSDMinor
		method = string(in.valuation.method)
		valuedAt = database.FormatTime(in.now)
	}

	result, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_suggestions
		    (income_transaction_id, revision, policy_version_id, income_account_id, income_category_id,
		     income_amount_minor, income_currency_code, income_effective_date,
		     income_usd_minor, suggested_usd_minor, fx_method, valued_at,
		     status, status_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		in.transactionID, in.revision, in.policyVersion, in.accountID, in.categoryID,
		in.amountMinor, in.currencyCode, database.FormatDate(in.effectiveDate),
		incomeUSD, suggested, method, valuedAt,
		string(status), database.FormatTime(in.now), database.FormatTime(in.now))
	if err != nil {
		return 0, fmt.Errorf("inserting revision %d of the suggestion for income %d: %w",
			in.revision, in.transactionID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading the id of the new suggestion: %w", err)
	}
	if in.valuation != nil {
		if err := r.insertLegs(ctx, id, in.valuation.legs, in.now); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// ValueSuggestion gives a PENDING_VALUATION revision its valuation, in place:
// the same row becomes OPEN, so resolving a pending suggestion can never create
// a second one.
func (r *Repository) ValueSuggestion(ctx context.Context, id int64, v valuation, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE savings_suggestions
		   SET income_usd_minor = ?, suggested_usd_minor = ?, fx_method = ?, valued_at = ?,
		       status = 'OPEN', updated_at = ?
		 WHERE id = ? AND status = 'PENDING_VALUATION'`,
		v.incomeUSDMinor, v.suggestedUSDMinor, string(v.method), database.FormatTime(now),
		database.FormatTime(now), id)
	if err != nil {
		return fmt.Errorf("valuing suggestion %d: %w", id, err)
	}
	if err := expectOne(result, fmt.Errorf("suggestion %d is not waiting for a valuation: %w", id, ErrInvalidState)); err != nil {
		return err
	}
	return r.insertLegs(ctx, id, v.legs, now)
}

// SetStatus moves a live revision to another status with its reason. It never
// touches a closed revision: history is not rewritten.
func (r *Repository) SetStatus(ctx context.Context, id int64, status Status, reason string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE savings_suggestions
		   SET status = ?, status_reason = ?, updated_at = ?
		 WHERE id = ? AND status IN `+liveStatuses,
		string(status), reason, database.FormatTime(now), id)
	if err != nil {
		return fmt.Errorf("setting suggestion %d to %s: %w", id, status, err)
	}
	return expectOne(result, fmt.Errorf("suggestion %d is no longer live: %w", id, ErrInvalidState))
}

func (r *Repository) insertLegs(ctx context.Context, suggestionID int64, legs []fx.Leg, now time.Time) error {
	for i, leg := range legs {
		o := leg.Observation
		if _, err := r.db.ExecContext(ctx, `
			INSERT INTO savings_suggestion_rate_legs
			    (suggestion_id, leg, exchange_rate_id, base_currency_code, quote_currency_code,
			     rate, effective_date, source, inverted, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			suggestionID, i+1, o.ID, o.BaseCurrencyCode, o.QuoteCurrencyCode,
			o.Value.String(), database.FormatDate(o.EffectiveDate), string(o.Source),
			boolToInt(leg.Inverted), database.FormatTime(now)); err != nil {
			return fmt.Errorf("recording leg %d of suggestion %d: %w", i+1, suggestionID, err)
		}
	}
	return nil
}

// legsWhere loads the legs of every suggestion matching a predicate over
// savings_suggestion_rate_legs, in one query, by suggestion id.
func (r *Repository) legsWhere(ctx context.Context, predicate string, args ...any) (_ map[int64][]RateLeg, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT suggestion_id, exchange_rate_id, base_currency_code, quote_currency_code,
		       rate, effective_date, source, inverted
		  FROM savings_suggestion_rate_legs
		 WHERE `+predicate+`
		 ORDER BY suggestion_id, leg`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading suggestion rate legs: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing rate leg rows: %w", cerr))
		}
	}()

	legs := make(map[int64][]RateLeg)
	for rows.Next() {
		var (
			suggestionID int64
			leg          RateLeg
			rate, date   string
			source       string
			inverted     int
		)
		if err := rows.Scan(&suggestionID, &leg.ExchangeRateID, &leg.BaseCurrencyCode, &leg.QuoteCurrencyCode,
			&rate, &date, &source, &inverted); err != nil {
			return nil, fmt.Errorf("reading a rate leg: %w", err)
		}
		if leg.Rate, err = money.ParseDecimal(rate); err != nil {
			return nil, fmt.Errorf("rate leg of suggestion %d: %w", suggestionID, err)
		}
		if leg.EffectiveDate, err = database.ParseDate(date); err != nil {
			return nil, fmt.Errorf("rate leg of suggestion %d effective_date: %w", suggestionID, err)
		}
		leg.Source = fx.Source(source)
		leg.Inverted = inverted == 1
		legs[suggestionID] = append(legs[suggestionID], leg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rate legs: %w", err)
	}
	return legs, nil
}

func (r *Repository) listSuggestions(ctx context.Context, query string, args ...any) (_ []Suggestion, err error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing savings suggestions: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing suggestion rows: %w", cerr))
		}
	}()

	list := make([]Suggestion, 0)
	for rows.Next() {
		s, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating suggestions: %w", err)
	}
	return list, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSuggestion(s scanner) (Suggestion, error) {
	var (
		out                          Suggestion
		policyFrom, incomeDate       string
		description                  sql.NullString
		incomeUSD, suggested         sql.NullInt64
		method, valuedAt, reason     sql.NullString
		status, createdRaw, updatedR string
	)
	if err := s.Scan(&out.ID, &out.IncomeTransactionID, &out.Revision, &out.PolicyVersionID, &out.RateBP, &policyFrom,
		&out.IncomeAccountID, &out.IncomeAccountName, &out.IncomeCategoryID, &out.IncomeCategoryName,
		&out.IncomeAmountMinor, &out.IncomeCurrencyCode, &out.IncomeMinorUnit, &incomeDate,
		&description,
		&incomeUSD, &suggested, &method, &valuedAt,
		&status, &reason, &createdRaw, &updatedR); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Suggestion{}, err
		}
		return Suggestion{}, fmt.Errorf("reading a savings suggestion: %w", err)
	}

	var err error
	if out.PolicyEffectiveFrom, err = database.ParseDate(policyFrom); err != nil {
		return Suggestion{}, fmt.Errorf("suggestion %d policy date: %w", out.ID, err)
	}
	if out.IncomeEffectiveDate, err = database.ParseDate(incomeDate); err != nil {
		return Suggestion{}, fmt.Errorf("suggestion %d income date: %w", out.ID, err)
	}
	if description.Valid {
		text := description.String
		out.IncomeDescription = &text
	}
	if incomeUSD.Valid {
		value := incomeUSD.Int64
		out.IncomeUSDMinor = &value
	}
	if suggested.Valid {
		value := suggested.Int64
		out.SuggestedUSDMinor = &value
	}
	if method.Valid {
		value := fx.Method(method.String)
		out.FXMethod = &value
	}
	if valuedAt.Valid {
		at, err := database.ParseTime(valuedAt.String)
		if err != nil {
			return Suggestion{}, fmt.Errorf("suggestion %d valued_at: %w", out.ID, err)
		}
		out.ValuedAt = &at
	}
	out.Status = Status(status)
	if reason.Valid {
		text := reason.String
		out.StatusReason = &text
	}
	if out.CreatedAt, err = database.ParseTime(createdRaw); err != nil {
		return Suggestion{}, fmt.Errorf("suggestion %d created_at: %w", out.ID, err)
	}
	if out.UpdatedAt, err = database.ParseTime(updatedR); err != nil {
		return Suggestion{}, fmt.Errorf("suggestion %d updated_at: %w", out.ID, err)
	}
	return out, nil
}

// expectOne turns "no row matched" into the given error. A conditional UPDATE
// that matched nothing is how a stale status is detected, without classifying
// any SQLite error.
func expectOne(result sql.Result, none error) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected: %w", err)
	}
	if n != 1 {
		return none
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
