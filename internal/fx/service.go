package fx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Service holds the exchange-rate use cases.
//
// Manual entry is a use case here rather than an implementation of RateProvider,
// and that is a deliberate shape. A provider FETCHES: it has a network, a
// document and a publication date of its own. A person typing a rate has none of
// those, and its source is not theirs to choose -- the backend sets MANUAL, so a
// form submission cannot claim to be the Banco Central.
type Service struct {
	db       *sql.DB
	provider RateProvider
	logger   *slog.Logger

	// fetching serialises calls to the external source.
	//
	// Two things can start a fetch: the button in the interface and the daily
	// refresher. Without this they could run at once and write the same
	// publication twice, and the second write would be doing nothing useful
	// while holding the single SQLite connection. The refresher uses TryLock and
	// skips rather than queueing, because a fetch that is already running is
	// exactly what its tick wanted.
	fetching sync.Mutex
}

// NewService builds the service over a pool.
//
// provider may be nil, which is a supported configuration and not a degraded
// one: with the external source switched off, manual entry, lookup and valuation
// all work unchanged. Nothing about this application's core requires a network.
func NewService(db *sql.DB, provider RateProvider, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{db: db, provider: provider, logger: logger}
}

// HasProvider reports whether an automatic source is configured, so the
// interface can hide a button that could not do anything.
func (s *Service) HasProvider() bool { return s.provider != nil }

// ProviderName is the source an automatic refresh would write under.
func (s *Service) ProviderName() Source {
	if s.provider == nil {
		return ""
	}
	return s.provider.Name()
}

// List returns observations, newest effective date first.
func (s *Service) List(ctx context.Context, in ListInput) ([]Rate, error) {
	in.BaseCurrencyCode = normalizeCode(in.BaseCurrencyCode)
	in.QuoteCurrencyCode = normalizeCode(in.QuoteCurrencyCode)
	return NewRepository(s.db).List(ctx, in)
}

// Get returns one observation, archived or not, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id int64) (Rate, error) {
	return NewRepository(s.db).Get(ctx, id)
}

// CreateManual records a rate a person typed.
//
// The source is MANUAL and is set here, never taken from the input: there is no
// Source field on CreateManualInput, so there is no signature through which a
// submission could claim to be an official publication.
//
// A rate that already exists for the same pair, date and source is a CONFLICT
// rather than an overwrite. That is the opposite of what the provider path does,
// and the difference is intentional: a second scraper run is the same
// publication being seen again, whereas a person typing a value that is already
// there has not been told what is already there.
//
// Both currencies must EXIST. Neither has to be active, and that is not an
// oversight -- an active account denominated in an archived currency is a
// reachable state, and it still has to be valued.
func (s *Service) CreateManual(ctx context.Context, in CreateManualInput) (_ Rate, err error) {
	base := normalizeCode(in.BaseCurrencyCode)
	quote := normalizeCode(in.QuoteCurrencyCode)
	if err := validatePair(base, quote); err != nil {
		return Rate{}, err
	}

	value, err := parseRate(in.Rate)
	if err != nil {
		return Rate{}, err
	}
	effectiveDate, err := parseEffectiveDate(in.EffectiveDate)
	if err != nil {
		return Rate{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rate{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the manual rate: %w", rerr))
		}
	}()

	if err := requireCurrencies(ctx, tx, base, quote); err != nil {
		return Rate{}, err
	}

	repo := NewRepository(tx)

	// Asked as a query, in the same transaction, rather than inferred from a
	// constraint failure. SQLite reports a duplicate key, a foreign key
	// violation and a failed CHECK all as SQLITE_CONSTRAINT, so classifying by
	// error code would be guessing (TD-023).
	switch _, err := repo.GetObservation(ctx, base, quote, effectiveDate, SourceManual); {
	case err == nil:
		return Rate{}, fmt.Errorf("a manual rate for %s/%s on %s already exists: %w",
			base, quote, database.FormatDate(effectiveDate), ErrConflict)
	case !errors.Is(err, ErrNotFound):
		return Rate{}, err
	}

	now := database.Now()
	created, err := repo.Insert(ctx, Rate{
		BaseCurrencyCode:  base,
		QuoteCurrencyCode: quote,
		EffectiveDate:     effectiveDate,
		Value:             value,
		Source:            SourceManual,
		IsActive:          true,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		return Rate{}, err
	}

	if err := tx.Commit(); err != nil {
		return Rate{}, fmt.Errorf("committing the manual rate: %w", err)
	}
	return created, nil
}

// UpdateManual corrects the value of a manual observation.
//
// Only the value changes. The pair and the effective date are the IDENTITY of an
// observation: changing them would not correct this one, it would name a
// different one, so the input carries no field for them.
//
// An automatic observation is refused with ErrNotEditable. A BCV_OFFICIAL row
// records what was published, and editing it would make this application claim
// the source said something it did not. The way to disagree with a publication
// is a MANUAL observation for the same day, which wins by precedence and can
// itself be corrected or archived.
func (s *Service) UpdateManual(ctx context.Context, id int64, in UpdateManualInput) (_ Rate, err error) {
	value, err := parseRate(in.Rate)
	if err != nil {
		return Rate{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rate{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the rate update: %w", rerr))
		}
	}()

	repo := NewRepository(tx)

	current, err := repo.Get(ctx, id)
	if err != nil {
		return Rate{}, err
	}
	// The guard names what it ACCEPTS, not what it excludes, so that a source
	// added later is refused by default rather than walking through (TD-045).
	if current.Source != SourceManual {
		return Rate{}, fmt.Errorf("exchange rate %d comes from %s: %w",
			id, current.Source, ErrNotEditable)
	}

	if err := repo.UpdateValue(ctx, id, value, database.Now()); err != nil {
		return Rate{}, err
	}
	updated, err := repo.Get(ctx, id)
	if err != nil {
		return Rate{}, err
	}

	if err := tx.Commit(); err != nil {
		return Rate{}, fmt.Errorf("committing the rate update: %w", err)
	}
	return updated, nil
}

// SetManualActive archives or restores a manual observation.
//
// Archiving is how a manual override is withdrawn. Valuation ignores archived
// rows, so the official rate underneath becomes the one that applies again --
// without destroying the record that somebody once disagreed with it.
//
// There is no delete, here or anywhere else in this application.
func (s *Service) SetManualActive(ctx context.Context, id int64, isActive bool) (_ Rate, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rate{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the rate archive: %w", rerr))
		}
	}()

	repo := NewRepository(tx)

	current, err := repo.Get(ctx, id)
	if err != nil {
		return Rate{}, err
	}
	if current.Source != SourceManual {
		return Rate{}, fmt.Errorf("exchange rate %d comes from %s: %w",
			id, current.Source, ErrNotEditable)
	}

	if err := repo.SetActive(ctx, id, isActive, database.Now()); err != nil {
		return Rate{}, err
	}
	updated, err := repo.Get(ctx, id)
	if err != nil {
		return Rate{}, err
	}

	if err := tx.Commit(); err != nil {
		return Rate{}, fmt.Errorf("committing the rate archive: %w", err)
	}
	return updated, nil
}

// RefreshFromProvider fetches the external source now and stores what it
// published.
//
// It is what the button in the interface calls and what the daily refresher
// calls. Every failure is returned; none of them is fatal to anything.
//
// # All or nothing
//
// A publication is ONE document with ONE Fecha Valor. Either all of its usable
// observations are stored or none are, inside a single transaction. Storing half
// of one would let a valuation combine one currency's rate from this publication
// with another currency's rate from a previous one, producing a total that looks
// perfectly ordinary and is a mixture of two days.
//
// # Currencies this application does not have
//
// The BCV publishes several pairs. Ones whose currencies are not in the
// currencies table are DROPPED, and counted in the result, never created.
// Scraping an external document must not be able to bring new entities into this
// database: which currencies exist is the user's decision.
func (s *Service) RefreshFromProvider(ctx context.Context) (RefreshResult, error) {
	if s.provider == nil {
		return RefreshResult{}, fmt.Errorf("no automatic rate source is configured: %w",
			ErrProviderUnavailable)
	}

	s.fetching.Lock()
	defer s.fetching.Unlock()

	return s.refreshLocked(ctx)
}

// tryRefreshFromProvider is RefreshFromProvider for the scheduler: if a fetch is
// already running it reports so instead of queueing behind it.
func (s *Service) tryRefreshFromProvider(ctx context.Context) (RefreshResult, bool, error) {
	if s.provider == nil {
		return RefreshResult{}, false, fmt.Errorf("no automatic rate source is configured: %w",
			ErrProviderUnavailable)
	}
	if !s.fetching.TryLock() {
		return RefreshResult{}, false, nil
	}
	defer s.fetching.Unlock()

	result, err := s.refreshLocked(ctx)
	return result, true, err
}

func (s *Service) refreshLocked(ctx context.Context) (_ RefreshResult, err error) {
	started := time.Now()

	publication, err := s.provider.Fetch(ctx)
	if err != nil {
		// Logged here because this is the only place that knows both the
		// provider and how long it took, and because a fetch failure is
		// frequently the ONLY warning anyone gets that the source changed shape.
		// The body is never logged: it is 150 KB of somebody else's HTML.
		s.logger.Warn("exchange rate refresh failed",
			slog.String("provider", string(s.provider.Name())),
			slog.Duration("duration", time.Since(started)),
			slog.Any("error", err))
		return RefreshResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the rate refresh: %w", rerr))
		}
	}()

	known, err := knownCurrencies(ctx, tx)
	if err != nil {
		return RefreshResult{}, err
	}

	repo := NewRepository(tx)
	now := database.Now()

	result := RefreshResult{
		Source:        publication.Source,
		EffectiveDate: publication.EffectiveDate,
		FetchedAt:     publication.FetchedAt,
	}

	for _, observation := range publication.Observations {
		if _, ok := known[observation.BaseCurrencyCode]; !ok {
			result.Skipped = append(result.Skipped, observation.BaseCurrencyCode)
			continue
		}
		if _, ok := known[observation.QuoteCurrencyCode]; !ok {
			result.Skipped = append(result.Skipped, observation.QuoteCurrencyCode)
			continue
		}

		fetchedAt := publication.FetchedAt
		stored, serr := repo.Upsert(ctx, Rate{
			BaseCurrencyCode:  observation.BaseCurrencyCode,
			QuoteCurrencyCode: observation.QuoteCurrencyCode,
			EffectiveDate:     publication.EffectiveDate,
			Value:             observation.Rate,
			Source:            publication.Source,
			IsActive:          true,
			FetchedAt:         &fetchedAt,
			CreatedAt:         now,
			UpdatedAt:         now,
		})
		if serr != nil {
			return RefreshResult{}, serr
		}
		result.Rates = append(result.Rates, stored)
		result.Stored++
	}

	if err := tx.Commit(); err != nil {
		return RefreshResult{}, fmt.Errorf("committing the rate refresh: %w", err)
	}

	s.logger.Info("exchange rates refreshed",
		slog.String("provider", string(publication.Source)),
		slog.String("effective_date", database.FormatDate(publication.EffectiveDate)),
		slog.Int("rates", result.Stored),
		slog.Int("skipped", len(result.Skipped)),
		slog.Duration("duration", time.Since(started)))

	return result, nil
}

// requireCurrencies checks that both sides of a pair exist, translating
// currency's error into this package's own so that a handler for rates never has
// to import the currency catalogue to interpret what it was returned.
func requireCurrencies(ctx context.Context, exec database.DBTX, codes ...string) error {
	currencies := currency.NewRepository(exec)
	for _, code := range codes {
		switch _, err := currencies.Get(ctx, code); {
		case errors.Is(err, currency.ErrNotFound):
			return fmt.Errorf("currency %q: %w", code, ErrCurrencyNotFound)
		case err != nil:
			return fmt.Errorf("looking up currency %q: %w", code, err)
		}
	}
	return nil
}

// knownCurrencies is the set of codes this application has, used to decide which
// of a publication's observations may be stored.
func knownCurrencies(ctx context.Context, exec database.DBTX) (map[string]struct{}, error) {
	// Archived currencies are included on purpose. An account may still be
	// active in one, and refusing to record the rate that values it would make
	// that account permanently unvaluable.
	list, err := currency.NewRepository(exec).List(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("listing currencies: %w", err)
	}

	known := make(map[string]struct{}, len(list))
	for _, c := range list {
		known[c.Code] = struct{}{}
	}
	return known, nil
}
