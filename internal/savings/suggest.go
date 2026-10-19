package savings

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// --- eligibility ------------------------------------------------------------

// rules is everything eligibility depends on, loaded once per operation.
type rules struct {
	versions   []PolicyVersion // oldest effective date first
	categories map[int64]bool
	overrides  map[int64]IncomeOverride
}

func loadRules(ctx context.Context, exec database.DBTX) (*rules, error) {
	repo := NewRepository(exec)
	versions, err := repo.ListPolicyVersions(ctx)
	if err != nil {
		return nil, err
	}
	categories, err := repo.CategoryRules(ctx)
	if err != nil {
		return nil, err
	}
	overrides, err := repo.Overrides(ctx)
	if err != nil {
		return nil, err
	}
	return &rules{versions: versions, categories: categories, overrides: overrides}, nil
}

// start is the first day any income is suggested from.
func (r *rules) start() (time.Time, bool) {
	if len(r.versions) == 0 {
		return time.Time{}, false
	}
	return r.versions[0].EffectiveFrom, true
}

// versionOn returns the version in force on an income's effective date: the
// latest one whose EffectiveFrom is on or before it (TD-077).
func (r *rules) versionOn(day time.Time) (PolicyVersion, bool) {
	var (
		found PolicyVersion
		ok    bool
	)
	for _, v := range r.versions {
		if v.EffectiveFrom.After(day) {
			break
		}
		found, ok = v, true
	}
	return found, ok
}

// decision is whether an income is saved from, under which version, and why.
type decision struct {
	eligible bool
	version  PolicyVersion
	reason   string
}

// decide applies the eligibility rules to one income, in this order: the
// policy must govern its date; an explicit override wins; otherwise its
// category's rule, eligible by default (TD-076); and a rate of zero suggests
// nothing. The income's description is never read.
func (r *rules) decide(item ledger.IncomeItem) decision {
	if len(r.versions) == 0 {
		return decision{reason: "No hay una política de ahorro configurada"}
	}
	version, ok := r.versionOn(item.Date)
	if !ok {
		return decision{reason: fmt.Sprintf("Es anterior al inicio de la política de ahorro (%s)",
			database.FormatDate(r.versions[0].EffectiveFrom))}
	}

	included := ""
	if o, has := r.overrides[item.TransactionID]; has {
		if !o.Eligible {
			return decision{version: version, reason: "Excluido manualmente: " + o.Reason}
		}
		included = "Incluido manualmente: " + o.Reason
	} else if eligible, explicit := r.categories[item.CategoryID]; explicit && !eligible {
		return decision{version: version,
			reason: fmt.Sprintf("La categoría «%s» está excluida del ahorro", item.CategoryName)}
	}

	if version.RateBP == 0 {
		return decision{version: version, reason: fmt.Sprintf("La tasa de ahorro vigente el %s es 0%%",
			database.FormatDate(item.Date))}
	}
	if included == "" {
		included = fmt.Sprintf("Categoría «%s», tasa %s%% vigente desde %s",
			item.CategoryName, money.Format(version.RateBP, 2), database.FormatDate(version.EffectiveFrom))
	}
	return decision{eligible: true, version: version, reason: included}
}

// --- planning ---------------------------------------------------------------

// planFor decides what a sync does to one income, without writing anything. It
// is the single rule behind both the preview and the sync itself, so the two
// cannot disagree.
//
//	not eligible, live revision      INVALIDATE
//	not eligible, no live revision   nothing
//	eligible, no live revision       CREATE
//	eligible, live revision differs  REVISE
//	eligible, live revision matches  nothing (this is what makes it idempotent)
func planFor(d decision, item *ledger.IncomeItem, live *Suggestion) (SyncAction, string) {
	if !d.eligible {
		if live != nil {
			return SyncInvalidate, d.reason
		}
		return "", ""
	}
	if live == nil {
		return SyncCreate, d.reason
	}
	if changes := changesSince(*live, *item, d.version); len(changes) > 0 {
		return SyncRevise, "El ingreso cambió: " + strings.Join(changes, ", ")
	}
	return "", ""
}

// changesSince names what differs between an income now and what its live
// revision recorded. The description is not among them: it does not take part
// in the arithmetic.
func changesSince(live Suggestion, item ledger.IncomeItem, version PolicyVersion) []string {
	var changes []string
	if live.IncomeAmountMinor != item.AmountMinor || live.IncomeCurrencyCode != item.CurrencyCode {
		changes = append(changes, "importe")
	}
	if !live.IncomeEffectiveDate.Equal(item.Date) {
		changes = append(changes, "fecha")
	}
	if live.IncomeAccountID != item.AccountID {
		changes = append(changes, "cuenta")
	}
	if live.IncomeCategoryID != item.CategoryID {
		changes = append(changes, "categoría")
	}
	if live.PolicyVersionID != version.ID {
		changes = append(changes, "versión de la política")
	}
	return changes
}

// --- valuation --------------------------------------------------------------

// syncContext carries what one operation loads once and reuses for every
// income it touches: the rules, the reference rates and USD's minor unit. It
// reads only through exec -- never the pool (see the package comment).
type syncContext struct {
	exec  database.DBTX
	today time.Time
	rules *rules

	book         *fx.Book
	usdMinorUnit *int
}

func (s *Service) newSyncContext(ctx context.Context, exec database.DBTX) (*syncContext, error) {
	r, err := loadRules(ctx, exec)
	if err != nil {
		return nil, err
	}
	return &syncContext{exec: exec, today: s.today(), rules: r}, nil
}

// rateBook loads, on first use and with one query, every active observation in
// effect on or before today. It answers any income date up to today with the
// same rules fx.Service.Resolve applies (TD-066) -- latest effective_date on or
// before the date, never later -- and was never given anything dated after
// today to be tempted by.
func (sc *syncContext) rateBook(ctx context.Context) (*fx.Book, error) {
	if sc.book == nil {
		rates, err := fx.NewRepository(sc.exec).ListActiveThrough(ctx, sc.today)
		if err != nil {
			return nil, err
		}
		sc.book = fx.NewBook(rates)
	}
	return sc.book, nil
}

func (sc *syncContext) usdScale(ctx context.Context) (int, error) {
	if sc.usdMinorUnit == nil {
		usd, err := currency.NewRepository(sc.exec).Get(ctx, FundCurrency)
		if err != nil {
			return 0, fmt.Errorf("looking up %s, the savings currency: %w", FundCurrency, err)
		}
		sc.usdMinorUnit = &usd.MinorUnit
	}
	return *sc.usdMinorUnit, nil
}

// valuate converts an income into USD at the reference rate in force on the
// income's date and applies the policy rate.
//
// Both figures are ONE rounding, half to even, of an exact value: the income in
// USD cents, and the income in USD cents times rate_bp over 10,000. The
// suggestion is not derived from the rounded income, which would round twice.
//
// It returns no valuation, and the reason, when the rate cannot be known: the
// income is dated after today (TD-081), or no observation connects its currency
// with USD on its date. Never a zero, never a later rate.
func (sc *syncContext) valuate(ctx context.Context, amountMinor int64, currencyCode string, minorUnit int,
	day time.Time, rateBP int64,
) (*valuation, PendingReason, error) {
	usdScale, err := sc.usdScale(ctx)
	if err != nil {
		return nil, "", err
	}

	resolution := fx.Resolution{Method: fx.MethodIdentity}
	if currencyCode != FundCurrency {
		if day.After(sc.today) {
			return nil, PendingFutureDate, nil
		}
		book, err := sc.rateBook(ctx)
		if err != nil {
			return nil, "", err
		}
		resolution, err = book.Resolve(currencyCode, FundCurrency, day)
		if errors.Is(err, fx.ErrRateUnavailable) || errors.Is(err, fx.ErrNotFound) {
			return nil, PendingNoRate, nil
		}
		if err != nil {
			return nil, "", fmt.Errorf("resolving %s/%s on %s: %w",
				currencyCode, FundCurrency, database.FormatDate(day), err)
		}
	}

	exact, err := money.ConvertMinorExact(amountMinor, minorUnit, usdScale, resolution.Rate())
	if err != nil {
		return nil, "", fmt.Errorf("converting the income into %s: %w", FundCurrency, err)
	}
	incomeUSD, err := money.RoundHalfEven(exact)
	if err != nil {
		return nil, "", fmt.Errorf("rounding the income in %s: %w", FundCurrency, err)
	}
	share := new(big.Rat).Mul(exact, new(big.Rat).SetFrac(big.NewInt(rateBP), bpDenominator))
	suggested, err := money.RoundHalfEven(share)
	if err != nil {
		return nil, "", fmt.Errorf("rounding the suggested saving: %w", err)
	}
	return &valuation{
		incomeUSDMinor:    incomeUSD,
		suggestedUSDMinor: suggested,
		method:            resolution.Method,
		legs:              resolution.Legs,
	}, "", nil
}

// rateChanged reports whether the reference rate in force on a valued
// suggestion's date is no longer the one it used: a re-published Fecha Valor, a
// manual override added, corrected or archived, a rate that is now missing.
func (sc *syncContext) rateChanged(ctx context.Context, s Suggestion) (bool, error) {
	if s.FXMethod == nil || *s.FXMethod == fx.MethodIdentity {
		return false, nil
	}
	book, err := sc.rateBook(ctx)
	if err != nil {
		return false, err
	}
	now, err := book.Resolve(s.IncomeCurrencyCode, FundCurrency, s.IncomeEffectiveDate)
	if errors.Is(err, fx.ErrRateUnavailable) || errors.Is(err, fx.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolving the current rate of suggestion %d: %w", s.ID, err)
	}
	if now.Method != *s.FXMethod || len(now.Legs) != len(s.Legs) {
		return true, nil
	}
	for i, leg := range now.Legs {
		was := s.Legs[i]
		if leg.Observation.ID != was.ExchangeRateID || leg.Inverted != was.Inverted ||
			!leg.Observation.Value.Equal(was.Rate) {
			return true, nil
		}
	}
	return false, nil
}

// pendingReason says why a live revision has no valuation.
func (sc *syncContext) pendingReason(s Suggestion) PendingReason {
	if s.Status != StatusPendingValuation {
		return ""
	}
	if s.IncomeCurrencyCode != FundCurrency && s.IncomeEffectiveDate.After(sc.today) {
		return PendingFutureDate
	}
	return PendingNoRate
}

// --- sync -------------------------------------------------------------------

// SyncIncomeTx brings one transaction's suggestion in line with what it is now,
// through exec -- which must be an open transaction, the same one that recorded
// or corrected the income (internal/application). It is what makes "every
// eligible income has exactly one live suggestion, computed from its current
// facts" hold at every commit.
//
// It is idempotent: run twice, the second run finds the live revision matching
// and does nothing. A confirmed reservation (M7.2) is never touched by it; a
// revision is closed and a new one opened, and the money already set aside
// stays exactly where it is.
func (s *Service) SyncIncomeTx(ctx context.Context, exec database.DBTX, transactionID int64) (SyncAction, error) {
	sc, err := s.newSyncContext(ctx, exec)
	if err != nil {
		return "", err
	}
	return sc.syncIncome(ctx, transactionID)
}

func (sc *syncContext) syncIncome(ctx context.Context, transactionID int64) (SyncAction, error) {
	repo := NewRepository(sc.exec)

	live, err := liveOrNil(ctx, repo, transactionID)
	if err != nil {
		return "", err
	}

	item, err := ledger.NewRepository(sc.exec).GetIncome(ctx, transactionID)
	isIncome := err == nil
	if err != nil && !errors.Is(err, ledger.ErrNotFound) {
		return "", fmt.Errorf("reading income %d: %w", transactionID, err)
	}

	d := decision{reason: "La transacción ya no es un ingreso"}
	if isIncome {
		d = sc.rules.decide(item)
	}

	action, reason := planFor(d, &item, live)
	now := database.Now()
	switch action {
	case "":
		return "", nil

	case SyncInvalidate:
		return action, repo.SetStatus(ctx, live.ID, StatusInvalidated, reason, now)

	case SyncCreate, SyncRevise:
		revision := 0
		if live != nil {
			// Closed BEFORE the next revision is written: the partial unique
			// index admits one live revision per income, never two.
			if err := repo.SetStatus(ctx, live.ID, StatusSuperseded, reason, now); err != nil {
				return "", err
			}
			revision = live.Revision + 1
		} else if revision, err = repo.NextRevision(ctx, transactionID); err != nil {
			return "", err
		}

		v, _, err := sc.valuate(ctx, item.AmountMinor, item.CurrencyCode, item.MinorUnit, item.Date, d.version.RateBP)
		if err != nil {
			return "", err
		}
		if _, err := repo.InsertSuggestion(ctx, newSuggestion{
			transactionID: transactionID,
			revision:      revision,
			policyVersion: d.version.ID,
			accountID:     item.AccountID,
			categoryID:    item.CategoryID,
			amountMinor:   item.AmountMinor,
			currencyCode:  item.CurrencyCode,
			effectiveDate: item.Date,
			valuation:     v,
			now:           now,
		}); err != nil {
			return "", err
		}
		return action, nil

	default:
		return "", fmt.Errorf("unknown sync action %q", action)
	}
}

func liveOrNil(ctx context.Context, repo *Repository, transactionID int64) (*Suggestion, error) {
	live, err := repo.LiveSuggestion(ctx, transactionID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &live, nil
}

// getIncome reads one income and translates the ledger's "not found" -- which
// also covers a transaction that is not an INCOME.
func getIncome(ctx context.Context, exec database.DBTX, transactionID int64) (ledger.IncomeItem, error) {
	item, err := ledger.NewRepository(exec).GetIncome(ctx, transactionID)
	if errors.Is(err, ledger.ErrNotFound) {
		return ledger.IncomeItem{}, fmt.Errorf("transaction %d: %w", transactionID, ErrIncomeNotFound)
	}
	if err != nil {
		return ledger.IncomeItem{}, fmt.Errorf("reading income %d: %w", transactionID, err)
	}
	return item, nil
}

// --- actions on one income's live suggestion ---------------------------------

// ValueSuggestion values an income's live revision that is waiting for a rate.
//
// The SAME row becomes OPEN: resolving a pending valuation never creates a
// second suggestion. The rate used is the one in force on the income's date,
// found now -- typically a rate entered or published after the income was
// recorded, but dated on or before it. The income is brought in line first, so
// a correction made outside the application is not valued at its old figures.
func (s *Service) ValueSuggestion(ctx context.Context, transactionID int64) error {
	return s.inTx(ctx, "valuing a savings suggestion", func(exec database.DBTX) error {
		sc, err := s.newSyncContext(ctx, exec)
		if err != nil {
			return err
		}
		if _, err := sc.syncIncome(ctx, transactionID); err != nil {
			return err
		}
		repo := NewRepository(exec)
		live, err := repo.LiveSuggestion(ctx, transactionID)
		if err != nil {
			return err
		}
		if live.Status != StatusPendingValuation {
			return fmt.Errorf("%w: the suggestion is %s, not waiting for a rate", ErrInvalidState, live.Status)
		}
		v, pending, err := sc.valuate(ctx, live.IncomeAmountMinor, live.IncomeCurrencyCode, live.IncomeMinorUnit,
			live.IncomeEffectiveDate, live.RateBP)
		if err != nil {
			return err
		}
		switch {
		case v != nil:
			return repo.ValueSuggestion(ctx, live.ID, *v, database.Now())
		case pending == PendingFutureDate:
			return fmt.Errorf("%w: it is dated %s", ErrNotYetEffective, database.FormatDate(live.IncomeEffectiveDate))
		default:
			return fmt.Errorf("%w: %s/%s on %s", ErrRateUnavailable,
				live.IncomeCurrencyCode, FundCurrency, database.FormatDate(live.IncomeEffectiveDate))
		}
	})
}

// ValuePending values every live revision whose rate can now be found, in one
// transaction, and reports how many it valued. The rest stay pending.
func (s *Service) ValuePending(ctx context.Context) (int, error) {
	valued := 0
	err := s.inTx(ctx, "valuing pending savings suggestions", func(exec database.DBTX) error {
		valued = 0
		sc, err := s.newSyncContext(ctx, exec)
		if err != nil {
			return err
		}
		repo := NewRepository(exec)
		lives, err := repo.ListLiveSuggestions(ctx)
		if err != nil {
			return err
		}
		now := database.Now()
		for _, live := range lives {
			if live.Status != StatusPendingValuation {
				continue
			}
			v, _, err := sc.valuate(ctx, live.IncomeAmountMinor, live.IncomeCurrencyCode, live.IncomeMinorUnit,
				live.IncomeEffectiveDate, live.RateBP)
			if err != nil {
				return err
			}
			if v == nil {
				continue
			}
			if err := repo.ValueSuggestion(ctx, live.ID, *v, now); err != nil {
				return err
			}
			valued++
		}
		return nil
	})
	return valued, err
}

// RevalueSuggestion re-values an income whose rate has changed since its
// suggestion was made.
//
// Never automatic (TD-078): the live revision is closed as SUPERSEDED and the
// next one is valued at the rate in force on the income's date now. Any money
// already reserved against the income (M7.2) stays where it is; the new figure
// only changes what remains pending.
func (s *Service) RevalueSuggestion(ctx context.Context, transactionID int64) error {
	return s.inTx(ctx, "revaluing a savings suggestion", func(exec database.DBTX) error {
		sc, err := s.newSyncContext(ctx, exec)
		if err != nil {
			return err
		}
		// A correction of the income re-values it already.
		if action, err := sc.syncIncome(ctx, transactionID); err != nil || action != "" {
			return err
		}
		repo := NewRepository(exec)
		live, err := repo.LiveSuggestion(ctx, transactionID)
		if err != nil {
			return err
		}
		if live.Status != StatusOpen && live.Status != StatusDismissed {
			return fmt.Errorf("%w: the suggestion is %s and has no valuation to revise", ErrInvalidState, live.Status)
		}
		changed, err := sc.rateChanged(ctx, live)
		if err != nil {
			return err
		}
		if !changed {
			return ErrRateUnchanged
		}

		now := database.Now()
		if err := repo.SetStatus(ctx, live.ID, StatusSuperseded,
			fmt.Sprintf("Revalorada: cambió la tasa vigente el %s", database.FormatDate(live.IncomeEffectiveDate)),
			now); err != nil {
			return err
		}
		v, _, err := sc.valuate(ctx, live.IncomeAmountMinor, live.IncomeCurrencyCode, live.IncomeMinorUnit,
			live.IncomeEffectiveDate, live.RateBP)
		if err != nil {
			return err
		}
		_, err = repo.InsertSuggestion(ctx, newSuggestion{
			transactionID: transactionID,
			revision:      live.Revision + 1,
			policyVersion: live.PolicyVersionID,
			accountID:     live.IncomeAccountID,
			categoryID:    live.IncomeCategoryID,
			amountMinor:   live.IncomeAmountMinor,
			currencyCode:  live.IncomeCurrencyCode,
			effectiveDate: live.IncomeEffectiveDate,
			valuation:     v,
			now:           now,
		})
		return err
	})
}

// DismissSuggestion declines an income's open suggestion. The revision stays
// live, so the income is not suggested from again unless it changes; the
// decision and its reason are kept.
func (s *Service) DismissSuggestion(ctx context.Context, transactionID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Descartada por el usuario"
	}
	reason, err := normalizeReason(reason)
	if err != nil {
		return err
	}
	return s.inTx(ctx, "dismissing a savings suggestion", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		live, err := repo.LiveSuggestion(ctx, transactionID)
		if err != nil {
			return err
		}
		if live.Status != StatusOpen {
			return fmt.Errorf("%w: only an open suggestion can be dismissed; this one is %s",
				ErrInvalidState, live.Status)
		}
		confirmed, err := repo.ConfirmedByIncome(ctx)
		if err != nil {
			return err
		}
		if confirmed[transactionID] >= *live.SuggestedUSDMinor {
			return fmt.Errorf("%w: the suggestion is already confirmed in full", ErrInvalidState)
		}
		return repo.SetStatus(ctx, live.ID, StatusDismissed, reason, database.Now())
	})
}
