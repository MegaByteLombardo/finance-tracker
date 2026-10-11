package fx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// Repository translates between Rate and SQL, one hand-written statement per
// method. It holds no business rules.
//
// It is a concrete struct over database.DBTX, not an interface (TD-021).
// Building it over DBTX is what lets a whole BCV publication be written inside
// one transaction.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or a transaction.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

// rateColumns is the one place the column list is written, so a projection and
// a scan cannot drift apart.
const rateColumns = `
	id, base_currency_code, quote_currency_code, effective_date, rate,
	source, is_active, fetched_at, created_at, updated_at`

// precedenceOrder sorts the observations of ONE date so the preferred source
// comes first.
//
// It is written as a CASE rather than as a lookup table because the ordering is
// a rule of this application, not data: MANUAL outranks BCV_OFFICIAL, and an
// unrecognised value -- which the column's CHECK makes unreachable -- sorts last
// rather than accidentally first.
const precedenceOrder = `
	CASE source WHEN 'MANUAL' THEN 0 WHEN 'BCV_OFFICIAL' THEN 1 ELSE 99 END`

// Insert writes a new observation.
func (r *Repository) Insert(ctx context.Context, rate Rate) (Rate, error) {
	var fetchedAt any
	if rate.FetchedAt != nil {
		fetchedAt = database.FormatTime(*rate.FetchedAt)
	}

	result, err := r.db.ExecContext(ctx, `
		INSERT INTO exchange_rates
		    (base_currency_code, quote_currency_code, effective_date, rate,
		     source, is_active, fetched_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rate.BaseCurrencyCode, rate.QuoteCurrencyCode,
		database.FormatDate(rate.EffectiveDate), rate.Value.String(),
		string(rate.Source), boolToInt(rate.IsActive), fetchedAt,
		database.FormatTime(rate.CreatedAt), database.FormatTime(rate.UpdatedAt))
	if err != nil {
		return Rate{}, fmt.Errorf("inserting the %s rate for %s/%s on %s: %w",
			rate.Source, rate.BaseCurrencyCode, rate.QuoteCurrencyCode,
			database.FormatDate(rate.EffectiveDate), err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Rate{}, fmt.Errorf("reading the id of the inserted rate: %w", err)
	}
	rate.ID = id
	return rate, nil
}

// Upsert writes an observation, replacing the one that already exists for the
// same pair, date and SOURCE.
//
// This is the provider path, and it differs from Insert on purpose. A second run
// of the scraper on the same day is the same publication being seen again, and a
// corrected publication for the same Fecha Valor is a correction of the same
// fact -- neither is a second observation. What is kept is the latest value and
// the latest fetched_at, which is precisely "what the source says now".
//
// Manual creation does NOT go through here: a person typing a rate that already
// exists is told so and offered the edit, rather than silently overwriting.
func (r *Repository) Upsert(ctx context.Context, rate Rate) (Rate, error) {
	var fetchedAt any
	if rate.FetchedAt != nil {
		fetchedAt = database.FormatTime(*rate.FetchedAt)
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO exchange_rates
		    (base_currency_code, quote_currency_code, effective_date, rate,
		     source, is_active, fetched_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (base_currency_code, quote_currency_code, effective_date, source)
		DO UPDATE SET rate       = excluded.rate,
		              is_active  = excluded.is_active,
		              fetched_at = excluded.fetched_at,
		              updated_at = excluded.updated_at`,
		rate.BaseCurrencyCode, rate.QuoteCurrencyCode,
		database.FormatDate(rate.EffectiveDate), rate.Value.String(),
		string(rate.Source), boolToInt(rate.IsActive), fetchedAt,
		database.FormatTime(rate.CreatedAt), database.FormatTime(rate.UpdatedAt))
	if err != nil {
		return Rate{}, fmt.Errorf("storing the %s rate for %s/%s on %s: %w",
			rate.Source, rate.BaseCurrencyCode, rate.QuoteCurrencyCode,
			database.FormatDate(rate.EffectiveDate), err)
	}

	// Read back rather than trusting LastInsertId, which names a new row even
	// when the statement took the DO UPDATE branch and changed an old one.
	return r.GetObservation(ctx, rate.BaseCurrencyCode, rate.QuoteCurrencyCode,
		rate.EffectiveDate, rate.Source)
}

// Get returns one observation by id, archived or not.
func (r *Repository) Get(ctx context.Context, id int64) (Rate, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+rateColumns+` FROM exchange_rates WHERE id = ?`, id)

	rate, err := scanRate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Rate{}, fmt.Errorf("exchange rate %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Rate{}, fmt.Errorf("reading exchange rate %d: %w", id, err)
	}
	return rate, nil
}

// GetObservation returns the observation identified by its natural key.
func (r *Repository) GetObservation(
	ctx context.Context, base, quote string, on time.Time, source Source,
) (Rate, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+rateColumns+`
		  FROM exchange_rates
		 WHERE base_currency_code = ? AND quote_currency_code = ?
		   AND effective_date = ? AND source = ?`,
		base, quote, database.FormatDate(on), string(source))

	rate, err := scanRate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Rate{}, fmt.Errorf("%s rate for %s/%s on %s: %w",
			source, base, quote, database.FormatDate(on), ErrNotFound)
	}
	if err != nil {
		return Rate{}, fmt.Errorf("reading the %s rate for %s/%s on %s: %w",
			source, base, quote, database.FormatDate(on), err)
	}
	return rate, nil
}

// ListActiveThrough returns every active observation whose effective_date is on
// or before the given date, oldest first.
//
// It is the only read the resolver needs (TD-066). Which observation applies to
// which pair on which day is decided in Go, by Book, so that a single date and a
// five-year series are resolved by the same code. The predicate is still the
// lookup policy's first half: an observation dated after the date has not taken
// effect, and is not even read.
//
// Archived observations are excluded, which is how archiving a manual override
// restores the official rate underneath it.
//
// At the scale of this application -- one row per day per pair per source -- a
// decade of daily publications is a few thousand rows, read in one statement.
func (r *Repository) ListActiveThrough(ctx context.Context, on time.Time) (_ []Rate, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+rateColumns+`
		  FROM exchange_rates
		 WHERE is_active = 1
		   AND effective_date <= ?
		 ORDER BY effective_date, id`,
		database.FormatDate(on))
	if err != nil {
		return nil, fmt.Errorf("listing the rates in effect on %s: %w", database.FormatDate(on), err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing rate rows: %w", cerr))
		}
	}()

	rates := make([]Rate, 0)
	for rows.Next() {
		rate, serr := scanRate(rows)
		if serr != nil {
			return nil, fmt.Errorf("reading a rate row: %w", serr)
		}
		rates = append(rates, rate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rates: %w", err)
	}
	return rates, nil
}

// List returns observations, newest effective date first.
func (r *Repository) List(ctx context.Context, in ListInput) (_ []Rate, err error) {
	query := `SELECT ` + rateColumns + ` FROM exchange_rates WHERE 1 = 1`
	args := make([]any, 0, 4)

	if in.BaseCurrencyCode != "" && in.QuoteCurrencyCode != "" {
		query += ` AND base_currency_code = ? AND quote_currency_code = ?`
		args = append(args, in.BaseCurrencyCode, in.QuoteCurrencyCode)
	}
	if !in.IncludeArchived {
		query += ` AND is_active = 1`
	}

	limit := in.Limit
	switch {
	case limit <= 0:
		limit = defaultListSize
	case limit > maxListSize:
		limit = maxListSize
	}
	query += ` ORDER BY effective_date DESC, base_currency_code, quote_currency_code, ` +
		precedenceOrder + `, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing exchange rates: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing rate rows: %w", cerr))
		}
	}()

	rates := make([]Rate, 0)
	for rows.Next() {
		rate, serr := scanRate(rows)
		if serr != nil {
			return nil, fmt.Errorf("reading a rate row: %w", serr)
		}
		rates = append(rates, rate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating exchange rates: %w", err)
	}
	return rates, nil
}

// UpdateValue rewrites an observation's rate.
//
// Only the value and updated_at are named. The pair, the date and the source are
// the identity of the observation, and the absence of a statement that could
// change them is what protects them -- the same mechanism that protects
// currencies.code and categories.category_type (TD-022).
func (r *Repository) UpdateValue(ctx context.Context, id int64, value money.Decimal, updatedAt time.Time) error {
	return r.execOne(ctx, id,
		`UPDATE exchange_rates SET rate = ?, updated_at = ? WHERE id = ?`,
		value.String(), database.FormatTime(updatedAt), id)
}

// SetActive archives or restores an observation.
func (r *Repository) SetActive(ctx context.Context, id int64, isActive bool, updatedAt time.Time) error {
	return r.execOne(ctx, id,
		`UPDATE exchange_rates SET is_active = ?, updated_at = ? WHERE id = ?`,
		boolToInt(isActive), database.FormatTime(updatedAt), id)
}

// LastFetchedAt returns when an automatic source was last seen successfully, or
// a zero time if it never was.
//
// It reads the maximum fetched_at rather than keeping a separate "last run"
// table: the observations themselves already record when they were seen, and a
// second place to record it is a second place to be wrong.
func (r *Repository) LastFetchedAt(ctx context.Context, source Source) (time.Time, error) {
	var fetched sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT max(fetched_at) FROM exchange_rates WHERE source = ?`, string(source)).
		Scan(&fetched)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the last %s fetch: %w", source, err)
	}
	if !fetched.Valid {
		return time.Time{}, nil
	}

	at, err := database.ParseTime(fetched.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the last %s fetch timestamp: %w", source, err)
	}
	return at, nil
}

// execOne runs a statement that must affect exactly one row, and turns "no rows"
// into ErrNotFound rather than reporting success.
func (r *Repository) execOne(ctx context.Context, id int64, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating exchange rate %d: %w", id, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the rows changed for exchange rate %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("exchange rate %d: %w", id, ErrNotFound)
	}
	return nil
}

// scanner is what both *sql.Row and *sql.Rows satisfy, so one scan function
// serves the single-row and multi-row paths.
type scanner interface {
	Scan(dest ...any) error
}

func scanRate(s scanner) (Rate, error) {
	var (
		rate                         Rate
		effectiveDate, value, source string
		isActive                     int
		fetchedAt                    sql.NullString
		createdAt, updatedAt         string
	)

	if err := s.Scan(&rate.ID, &rate.BaseCurrencyCode, &rate.QuoteCurrencyCode,
		&effectiveDate, &value, &source, &isActive, &fetchedAt,
		&createdAt, &updatedAt); err != nil {
		return Rate{}, err
	}

	var err error
	if rate.EffectiveDate, err = database.ParseDate(effectiveDate); err != nil {
		return Rate{}, fmt.Errorf("reading the effective date of rate %d: %w", rate.ID, err)
	}
	// A stored value that will not parse is an integrity failure, not a rate of
	// zero. Reporting it is the only way anyone would ever find out.
	if rate.Value, err = money.ParseDecimal(value); err != nil {
		return Rate{}, fmt.Errorf("reading the value of rate %d: %w", rate.ID, err)
	}
	if rate.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Rate{}, fmt.Errorf("reading the created_at of rate %d: %w", rate.ID, err)
	}
	if rate.UpdatedAt, err = database.ParseTime(updatedAt); err != nil {
		return Rate{}, fmt.Errorf("reading the updated_at of rate %d: %w", rate.ID, err)
	}
	if fetchedAt.Valid {
		at, perr := database.ParseTime(fetchedAt.String)
		if perr != nil {
			return Rate{}, fmt.Errorf("reading the fetched_at of rate %d: %w", rate.ID, perr)
		}
		rate.FetchedAt = &at
	}

	rate.Source = Source(source)
	rate.IsActive = isActive != 0
	return rate, nil
}

// boolToInt spells a Go bool the way a STRICT SQLite column holds one.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
