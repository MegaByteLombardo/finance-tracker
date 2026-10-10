package valuation_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/settings"
	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
)

// harness is the whole application's read path, wired the way cmd/web wires it.
//
// It is built over real SQLite with real migrations, so what these tests
// exercise is the actual chain -- movements summed by the ledger, rates resolved
// by fx, the base currency read from settings -- rather than a rehearsal of it.
type harness struct {
	db        *sql.DB
	accounts  *account.Service
	ledger    *ledger.Service
	rates     *fx.Service
	settings  *settings.Service
	valuation *valuation.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db := dbtest.New(t)
	rates := fx.NewService(db, nil, slog.New(slog.DiscardHandler))
	ledgerService := ledger.NewService(db)
	settingsService := settings.NewService(db)
	currencyService := currency.NewService(db)

	return &harness{
		db:        db,
		accounts:  account.NewService(db),
		ledger:    ledgerService,
		rates:     rates,
		settings:  settingsService,
		valuation: valuation.NewService(ledgerService, rates, settingsService, currencyService),
	}
}

// openAccount creates an account and gives it an opening balance, which is how
// money enters this application.
func (h *harness) openAccount(t *testing.T, name, currencyCode, amount string) account.Account {
	t.Helper()

	created, err := h.accounts.Create(t.Context(), account.CreateInput{
		Name:         name,
		Type:         account.Cash,
		CurrencyCode: currencyCode,
	})
	if err != nil {
		t.Fatalf("creating the account %q: %v", name, err)
	}

	if _, err := h.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID:     created.ID,
		Amount:        amount,
		EffectiveDate: "2026-09-01",
	}); err != nil {
		t.Fatalf("recording the opening balance of %q: %v", name, err)
	}
	return created
}

func (h *harness) seedOfficial(t *testing.T, base, quote, on, value string) fx.Rate {
	t.Helper()

	fetchedAt := database.Now()
	rate, err := fx.NewRepository(h.db).Insert(t.Context(), fx.Rate{
		BaseCurrencyCode:  base,
		QuoteCurrencyCode: quote,
		EffectiveDate:     mustDate(t, on),
		Value:             money.MustParseDecimal(value),
		Source:            fx.SourceBCVOfficial,
		IsActive:          true,
		FetchedAt:         &fetchedAt,
		CreatedAt:         fetchedAt,
		UpdatedAt:         fetchedAt,
	})
	if err != nil {
		t.Fatalf("seeding the official %s/%s rate: %v", base, quote, err)
	}
	return rate
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()

	parsed, err := database.ParseDate(s)
	if err != nil {
		t.Fatalf("parsing the date %q: %v", s, err)
	}
	return parsed
}

// snapshotLedger reads every movement, so a test can prove that valuing money
// did not move any.
func snapshotLedger(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), `
		SELECT id, transaction_id, account_id, amount_minor, created_at
		  FROM account_movements ORDER BY id`)
	if err != nil {
		t.Fatalf("reading movements: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("closing movement rows: %v", cerr)
		}
	}()

	var snapshot []string
	for rows.Next() {
		var id, transactionID, accountID, amount int64
		var createdAt string
		if err := rows.Scan(&id, &transactionID, &accountID, &amount, &createdAt); err != nil {
			t.Fatalf("scanning a movement: %v", err)
		}
		snapshot = append(snapshot, fmt.Sprintf("%s|%d|%d|%d|%d",
			createdAt, id, transactionID, accountID, amount))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating movements: %v", err)
	}
	return snapshot
}

// TestNetWorthAcceptanceScenario is M5's headline case, computed by hand in
// integer minor units and asserted exactly.
//
//	Base currency = USD
//
//	  Mercantil USD    1000.00 USD  ->  100000 minor
//	  Efectivo  USD     100.00 USD  ->   10000 minor
//	  Banesco   VES   50000.00 VES  -> 5000000 minor
//	  Efectivo  EUR     200.00 EUR  ->   20000 minor
//
//	Rates published 2026-09-11, in the BCV's own orientation:
//	  1 USD = 500 VES
//	  1 EUR = 550 VES
//
//	USD  identity                                     =  110000
//	VES  5000000 x (1/500)                            =   10000
//	EUR  cross: 550/500 = 11/10 ; 20000 x 11/10       =   22000
//	                                          TOTAL   =  142000  ->  $1,420.00
//
// Every figure is an integer. No float appears anywhere, including here.
func TestNetWorthAcceptanceScenario(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	h.openAccount(t, "Mercantil USD", "USD", "1000.00")
	h.openAccount(t, "Efectivo USD", "USD", "100.00")
	h.openAccount(t, "Banesco VES", "VES", "50000.00")
	h.openAccount(t, "Efectivo EUR", "EUR", "200.00")

	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")
	h.seedOfficial(t, "EUR", "VES", "2026-09-11", "550")

	before := snapshotLedger(t, h.db)

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}

	if !worth.Complete {
		t.Errorf("the valuation is incomplete: %+v", worth.Missing)
	}
	if worth.BaseCurrencyCode != "USD" {
		t.Errorf("base currency = %q, want USD", worth.BaseCurrencyCode)
	}
	if worth.TotalMinor != 142000 {
		t.Errorf("total = %d minor units, want 142000", worth.TotalMinor)
	}
	if worth.TotalFormatted != "1420.00" {
		t.Errorf("total = %q, want %q", worth.TotalFormatted, "1420.00")
	}

	components := map[string]valuation.Component{}
	for _, component := range worth.Components {
		components[component.CurrencyCode] = component
	}

	t.Run("the base currency is identity, with no rate and no row", func(t *testing.T) {
		usd := components["USD"]
		if usd.ConvertedMinor != 110000 || usd.BalanceMinor != 110000 {
			t.Errorf("USD: balance %d, converted %d, want 110000 and 110000",
				usd.BalanceMinor, usd.ConvertedMinor)
		}
		if usd.Resolution == nil || usd.Resolution.Method != fx.MethodIdentity {
			t.Errorf("USD was valued by %v, want identity", usd.Resolution)
		}
		if usd.AccountCount != 2 {
			t.Errorf("USD covers %d accounts, want 2", usd.AccountCount)
		}
	})

	t.Run("VES is the published rate, inverted exactly", func(t *testing.T) {
		ves := components["VES"]
		if ves.BalanceMinor != 5000000 {
			t.Errorf("VES balance = %d, want 5000000", ves.BalanceMinor)
		}
		if ves.ConvertedMinor != 10000 {
			t.Errorf("VES converted = %d, want 10000", ves.ConvertedMinor)
		}
		if ves.Resolution == nil || ves.Resolution.Method != fx.MethodInverted {
			t.Errorf("VES was valued by %v, want an inverted rate", ves.Resolution)
		}
		if ves.Resolution != nil && ves.Resolution.Rate().RatString() != "1/500" {
			t.Errorf("VES rate = %s, want exactly 1/500", ves.Resolution.Rate().RatString())
		}
	})

	t.Run("EUR is a cross through the bolivar", func(t *testing.T) {
		eur := components["EUR"]
		if eur.ConvertedMinor != 22000 {
			t.Errorf("EUR converted = %d, want 22000", eur.ConvertedMinor)
		}
		if eur.Resolution == nil || eur.Resolution.Method != fx.MethodCross {
			t.Errorf("EUR was valued by %v, want a cross", eur.Resolution)
		}
		if eur.Resolution != nil && len(eur.Resolution.Legs) != 2 {
			t.Errorf("the cross reports %d legs, want 2", len(eur.Resolution.Legs))
		}
	})

	t.Run("the components add up to the total exactly", func(t *testing.T) {
		var sum int64
		for _, component := range worth.Components {
			sum += component.ConvertedMinor
		}
		if sum != worth.TotalMinor {
			t.Errorf("the components sum to %d but the total is %d; a screen showing both would not add up",
				sum, worth.TotalMinor)
		}
	})

	t.Run("every component can say where its number came from", func(t *testing.T) {
		for _, component := range worth.Components {
			if !component.Valued {
				t.Errorf("%s was not valued", component.CurrencyCode)
				continue
			}
			if component.Resolution == nil {
				t.Errorf("%s has no resolution", component.CurrencyCode)
				continue
			}
			for _, leg := range component.Resolution.Legs {
				if leg.Observation.Source != fx.SourceBCVOfficial {
					t.Errorf("%s used a %s observation, want the published one",
						component.CurrencyCode, leg.Observation.Source)
				}
				if got := leg.Observation.EffectiveDate.Format("2006-01-02"); got != "2026-09-11" {
					t.Errorf("%s used an observation dated %s", component.CurrencyCode, got)
				}
			}
		}
	})

	t.Run("nothing in the ledger moved", func(t *testing.T) {
		after := snapshotLedger(t, h.db)
		if len(after) != len(before) {
			t.Fatalf("%d movements after valuing, want %d", len(after), len(before))
		}
		for i := range before {
			if after[i] != before[i] {
				t.Errorf("movement %d changed:\n before %s\n  after %s", i, before[i], after[i])
			}
		}
	})

	t.Run("nothing was persisted", func(t *testing.T) {
		// Two published observations, and not one derived, cached or
		// self-referencing row.
		var rates int
		if err := h.db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rates); err != nil {
			t.Fatalf("counting rates: %v", err)
		}
		if rates != 2 {
			t.Errorf("%d rates exist, want the 2 that were published", rates)
		}

		var tables int
		if err := h.db.QueryRowContext(t.Context(), `
			SELECT count(*) FROM sqlite_schema
			 WHERE type = 'table'
			   AND (name LIKE '%net_worth%' OR name LIKE '%valuation%' OR name LIKE '%converted%')`).
			Scan(&tables); err != nil {
			t.Fatalf("looking for cache tables: %v", err)
		}
		if tables != 0 {
			t.Errorf("%d tables look like a persisted valuation; the net worth is derived, always", tables)
		}
	})
}

// TestManualOverrideChangesTheValuationAndArchivingRestoresIt is the second half
// of the acceptance scenario, with the numbers worked out by hand again:
//
//	MANUAL 1 USD = 400 VES on the same day
//	  VES  5000000 x (1/400)                     =  12500
//	  EUR  cross: 550/400 = 11/8 ; 20000 x 11/8  =  27500
//	  USD  identity                              = 110000
//	                                     TOTAL   = 150000  ->  $1,500.00
func TestManualOverrideChangesTheValuationAndArchivingRestoresIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	h.openAccount(t, "Mercantil USD", "USD", "1000.00")
	h.openAccount(t, "Efectivo USD", "USD", "100.00")
	h.openAccount(t, "Banesco VES", "VES", "50000.00")
	h.openAccount(t, "Efectivo EUR", "EUR", "200.00")

	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")
	h.seedOfficial(t, "EUR", "VES", "2026-09-11", "550")

	on := mustDate(t, "2026-09-11")

	worth, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.TotalMinor != 142000 {
		t.Fatalf("before the override the total is %d, want 142000", worth.TotalMinor)
	}

	override, err := h.rates.CreateManual(t.Context(), fx.CreateManualInput{
		BaseCurrencyCode:  "USD",
		QuoteCurrencyCode: "VES",
		Rate:              "400",
		EffectiveDate:     "2026-09-11",
	})
	if err != nil {
		t.Fatalf("creating the override: %v", err)
	}

	worth, err = h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.TotalMinor != 150000 {
		t.Errorf("with the override the total is %d, want 150000", worth.TotalMinor)
	}

	// And the interface can tell that it happened, which is what makes a
	// surprising net worth debuggable from the screen.
	var sawOverride bool
	for _, component := range worth.Components {
		if component.Resolution != nil && component.Resolution.UsesManualOverride() {
			sawOverride = true
		}
	}
	if !sawOverride {
		t.Error("no component reports that it used a manual override")
	}

	// Withdrawing the override restores the published rate, without destroying
	// the record that somebody disagreed with it.
	if _, err := h.rates.SetManualActive(t.Context(), override.ID, false); err != nil {
		t.Fatalf("archiving the override: %v", err)
	}

	worth, err = h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.TotalMinor != 142000 {
		t.Errorf("after archiving the override the total is %d, want 142000 again", worth.TotalMinor)
	}

	var rows int
	if err := h.db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rows); err != nil {
		t.Fatalf("counting rates: %v", err)
	}
	if rows != 3 {
		t.Errorf("%d rates exist, want 3: archiving must not delete the override", rows)
	}
}

// TestNetWorthOverAWeekend: no publication on a Sunday, and none needed.
func TestNetWorthOverAWeekend(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openAccount(t, "Banesco VES", "VES", "50000.00")

	// 2026-09-11 is a Friday.
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	for _, day := range []string{"2026-09-11", "2026-09-12", "2026-09-13"} {
		worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, day))
		if err != nil {
			t.Fatalf("GetNetWorthAt(%s): %v", day, err)
		}
		if !worth.Complete {
			t.Errorf("%s is incomplete: %+v", day, worth.Missing)
		}
		if worth.TotalMinor != 10000 {
			t.Errorf("on %s the total is %d, want 10000", day, worth.TotalMinor)
		}
	}
}

// TestNetWorthIgnoresARateThatHasNotTakenEffect is the look-ahead guard at the
// level a person would see it.
//
// The BCV publishes forward: on 2026-09-11 its page carried a Fecha Valor of
// 2026-09-15. Valuing today with that rate would be using information that did
// not exist yet, and the correct answer is an explicit gap.
func TestNetWorthIgnoresARateThatHasNotTakenEffect(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openAccount(t, "Banesco VES", "VES", "50000.00")
	h.seedOfficial(t, "USD", "VES", "2026-09-15", "500")

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.Complete {
		t.Error("a rate effective on 2026-09-15 was used to value 2026-09-11")
	}
	if worth.TotalMinor != 0 {
		t.Errorf("total = %d, want 0: nothing could be valued yet", worth.TotalMinor)
	}
	if len(worth.Missing) != 1 || worth.Missing[0].FromCurrencyCode != "VES" {
		t.Errorf("missing = %+v, want one entry for VES", worth.Missing)
	}

	// On its own effective date it applies.
	worth, err = h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-15"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if !worth.Complete || worth.TotalMinor != 10000 {
		t.Errorf("on 2026-09-15 the total is %d (complete=%t), want 10000 and complete",
			worth.TotalMinor, worth.Complete)
	}
}

// TestNetWorthReportsAMissingRateRatherThanGuessing is the rule from the
// contract: never zero, never a partial total presented as whole.
func TestNetWorthReportsAMissingRateRatherThanGuessing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if _, err := h.db.ExecContext(t.Context(), `
		INSERT INTO currencies (code, name, symbol, minor_unit, is_active, created_at, updated_at)
		VALUES ('JPY','Yen japonés','¥',0,1,'2026-09-01T00:00:00.000Z','2026-09-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seeding JPY: %v", err)
	}

	h.openAccount(t, "Mercantil USD", "USD", "1000.00")
	h.openAccount(t, "Ahorro Japón", "JPY", "50000")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("a missing rate must not be an error: %v", err)
	}

	if worth.Complete {
		t.Error("the valuation claims to be complete with no JPY rate")
	}
	if worth.TotalMinor != 100000 {
		t.Errorf("total = %d, want 100000: the USD that could be valued, and nothing invented",
			worth.TotalMinor)
	}

	if len(worth.Missing) != 1 {
		t.Fatalf("missing = %+v, want exactly one entry", worth.Missing)
	}
	missing := worth.Missing[0]
	if missing.FromCurrencyCode != "JPY" || missing.ToCurrencyCode != "USD" {
		t.Errorf("missing pair = %s/%s, want JPY/USD", missing.FromCurrencyCode, missing.ToCurrencyCode)
	}
	if missing.BalanceMinor != 50000 {
		t.Errorf("the missing entry reports %d, want the 50000 that is sitting unvalued", missing.BalanceMinor)
	}

	// The unvalued currency is still listed with its own balance intact: the
	// user holds that money whether or not this application can price it.
	var jpy valuation.Component
	for _, component := range worth.Components {
		if component.CurrencyCode == "JPY" {
			jpy = component
		}
	}
	if jpy.CurrencyCode == "" {
		t.Fatal("JPY is missing from the components entirely")
	}
	if jpy.Valued {
		t.Error("JPY reports itself as valued")
	}
	if jpy.ConvertedMinor != 0 || jpy.Resolution != nil {
		t.Error("an unvalued component carries a converted amount")
	}
	if jpy.BalanceMinor != 50000 {
		t.Errorf("JPY balance = %d, want 50000", jpy.BalanceMinor)
	}
}

// TestChangingTheBaseCurrencyChangesNothingButThePresentation is the settings
// invariant, asserted where it can actually be checked.
func TestChangingTheBaseCurrencyChangesNothingButThePresentation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	h.openAccount(t, "Mercantil USD", "USD", "1000.00")
	h.openAccount(t, "Banesco VES", "VES", "50000.00")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	on := mustDate(t, "2026-09-11")
	before := snapshotLedger(t, h.db)

	inUSD, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	// 100000 USD minor + 5000000 VES / 500 = 100000 + 10000 = 110000.
	if inUSD.TotalMinor != 110000 {
		t.Fatalf("in USD the total is %d, want 110000", inUSD.TotalMinor)
	}

	if _, err := h.settings.SetBaseCurrency(t.Context(), "VES"); err != nil {
		t.Fatalf("changing the valuation base: %v", err)
	}

	inVES, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if inVES.BaseCurrencyCode != "VES" {
		t.Errorf("base currency = %q, want VES", inVES.BaseCurrencyCode)
	}
	// 5000000 VES + 100000 USD minor x 500 = 5000000 + 50000000 = 55000000.
	if inVES.TotalMinor != 55000000 {
		t.Errorf("in VES the total is %d, want 55000000", inVES.TotalMinor)
	}

	// The balances themselves are untouched in both readings.
	for _, worth := range []valuation.NetWorth{inUSD, inVES} {
		for _, component := range worth.Components {
			switch component.CurrencyCode {
			case "USD":
				if component.BalanceMinor != 100000 {
					t.Errorf("the USD balance reads %d in the %s valuation",
						component.BalanceMinor, worth.BaseCurrencyCode)
				}
			case "VES":
				if component.BalanceMinor != 5000000 {
					t.Errorf("the VES balance reads %d in the %s valuation",
						component.BalanceMinor, worth.BaseCurrencyCode)
				}
			}
		}
	}

	after := snapshotLedger(t, h.db)
	if len(after) != len(before) {
		t.Fatalf("%d movements after changing the base currency, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("changing the base currency rewrote movement %d:\n before %s\n  after %s",
				i, before[i], after[i])
		}
	}
}

// TestArchivedAccountsAreStillValued is TD-064, which superseded M5's rule:
// archiving is a matter of visibility, not a financial event, so the money in an
// archived account is still the user's and still counts.
func TestArchivedAccountsAreStillValued(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	h.openAccount(t, "Mercantil USD", "USD", "1000.00")
	retired := h.openAccount(t, "Cuenta vieja", "USD", "250.00")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	on := mustDate(t, "2026-09-11")

	before, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if before.TotalMinor != 125000 {
		t.Fatalf("total = %d, want 125000", before.TotalMinor)
	}

	if _, err := h.accounts.SetActive(t.Context(), retired.ID, false); err != nil {
		t.Fatalf("archiving the account: %v", err)
	}

	after, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if after.TotalMinor != 125000 {
		t.Errorf("total = %d after archiving, want 125000: archiving is not a financial event", after.TotalMinor)
	}
	if after.ArchivedWithBalance != 1 {
		t.Errorf("ArchivedWithBalance = %d, want 1 so the interface can warn", after.ArchivedWithBalance)
	}
	var listed bool
	for _, line := range after.Accounts {
		if line.AccountID == retired.ID {
			listed = true
			if line.IsActive || line.ConvertedMinor != 25000 {
				t.Errorf("the archived account's line = %+v, want inactive and worth 25000", line)
			}
		}
	}
	if !listed {
		t.Error("the archived account holding 250.00 is missing from the breakdown")
	}
}

// TestAnArchivedCurrencyIsStillValued is the reachable state the data model
// documents: archiving a currency retires it from new accounts, it does not make
// the money in existing ones stop existing.
func TestAnArchivedCurrencyIsStillValued(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	h.openAccount(t, "Banesco VES", "VES", "50000.00")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	if _, err := h.db.ExecContext(t.Context(),
		"UPDATE currencies SET is_active = 0 WHERE code = 'VES'"); err != nil {
		t.Fatalf("archiving VES: %v", err)
	}

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if !worth.Complete {
		t.Errorf("an account in an archived currency stopped being valued: %+v", worth.Missing)
	}
	if worth.TotalMinor != 10000 {
		t.Errorf("total = %d, want 10000", worth.TotalMinor)
	}
}

func TestNetWorthWithNoAccountsIsZeroAndComplete(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if !worth.Complete {
		t.Error("an empty application reports an incomplete valuation")
	}
	if worth.TotalMinor != 0 {
		t.Errorf("total = %d, want 0", worth.TotalMinor)
	}
	if len(worth.Components) != 0 {
		t.Errorf("%d components, want none", len(worth.Components))
	}
	if worth.TotalFormatted != "0.00" {
		t.Errorf("total = %q, want %q", worth.TotalFormatted, "0.00")
	}
}

// TestNegativeBalancesAreValued: a transfer may leave an account overdrawn
// (TD-043), so a negative balance is a legitimate state and must convert by the
// same symmetric rule as a positive one.
func TestNegativeBalancesAreValued(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	h.openAccount(t, "Mercantil USD", "USD", "1000.00")
	h.openAccount(t, "Banesco VES", "VES", "-50000.00")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	// 100000 - 10000 = 90000.
	if worth.TotalMinor != 90000 {
		t.Errorf("total = %d, want 90000", worth.TotalMinor)
	}
	for _, component := range worth.Components {
		if component.CurrencyCode == "VES" && component.ConvertedMinor != -10000 {
			t.Errorf("the VES component converted to %d, want -10000", component.ConvertedMinor)
		}
	}
}

// TestCurrentNetWorthUsesTheVenezuelanDate guards against the UTC date bug at
// the level that matters: which day's rates get used.
func TestCurrentNetWorthUsesTheVenezuelanDate(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openAccount(t, "Banesco VES", "VES", "50000.00")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	// The valuation date must be today in Caracas. Asserting the exact day the
	// suite runs on would be a test of the calendar, so this asserts the
	// property instead: it agrees with database.Today, which is Venezuelan.
	worth, err := h.valuation.GetCurrentNetWorth(t.Context())
	if err != nil {
		t.Fatalf("GetCurrentNetWorth: %v", err)
	}
	if !worth.ValuationDate.Equal(database.Today()) {
		t.Errorf("valuation date = %s, want today in Venezuela, %s",
			database.FormatDate(worth.ValuationDate), database.FormatDate(database.Today()))
	}
}

func TestValuationFailsLoudlyWhenTheBaseCurrencyIsGone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	// Only reachable by editing the database by hand, which is exactly why it
	// must be an error rather than a silently empty result.
	if _, err := h.db.ExecContext(t.Context(), "DELETE FROM app_settings"); err != nil {
		t.Fatalf("removing the settings row: %v", err)
	}

	if _, err := h.valuation.GetCurrentNetWorth(t.Context()); !errors.Is(err, valuation.ErrBaseCurrencyUnavailable) {
		t.Errorf("error = %v, want ErrBaseCurrencyUnavailable", err)
	}
}

// Compile-time proof that the valuation service needs nothing but a context to
// answer, which is what makes it safe to call from a handler.
var _ = func(ctx context.Context, s *valuation.Service) (valuation.NetWorth, error) {
	return s.GetCurrentNetWorth(ctx)
}
