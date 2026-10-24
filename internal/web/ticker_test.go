package web

import (
	"context"
	"errors"
	"html"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// tickerRecap is the recap of a month in progress, with a liability, a second
// currency valued through a published rate and a category of each direction.
func tickerRecap() Recap {
	r := sampleRecap()
	r.InProgress = true
	r.Closing = NetWorth{
		BaseCurrencyCode: "USD", Complete: true,
		TotalMinor: 320000, TotalFormatted: "3200.00",
		LiabilitiesMinor: 45000, LiabilitiesFormatted: "450.00",
		Accounts: []NetWorthAccount{
			{AccountName: "Mercantil USD", Nature: "ASSET", IsActive: true, CurrencyCode: "USD"},
			{AccountName: "Visa", Nature: "LIABILITY", IsActive: true, CurrencyCode: "USD"},
		},
		Components: []NetWorthComponent{
			{CurrencyCode: "USD", BalanceMinor: 300000, BalanceFormatted: "3000.00", Valued: true},
			{CurrencyCode: "VES", BalanceMinor: 797000, BalanceFormatted: "7970.00", Valued: true,
				Resolution: &RateResolutionView{Legs: []RateLegView{
					{Pair: "USD/VES", Rate: "39.85210000", RateShort: "39.85", SourceLabel: "BCV"},
				}}},
			{CurrencyCode: "EUR", BalanceMinor: 0, BalanceFormatted: "0.00"},
		},
	}
	r.ExpenseDailyAverageChange.Percent = Ratio{BasisPoints: 2631, Defined: true, Formatted: "26.31"}
	return r
}

func tickerRouter(t *testing.T, r *Recap, err error) (*FakeServices, func(path string) (int, string)) {
	t.Helper()

	fs := NewFakeServices()
	svcs := fs.BuildServices()
	if r != nil {
		fs.Analytics.SetRecap(*r)
	}
	if err != nil {
		fs.Analytics.SetError(err)
	}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), svcs, DefaultConfig())
	return fs, func(path string) (int, string) {
		rec := get(t, router, path)
		return rec.Code, html.UnescapeString(body(t, rec))
	}
}

func seedTickerTransactions(t *testing.T, fs *FakeServices) {
	t.Helper()
	ctx := context.Background()

	acct, err := fs.CreateAccount(ctx, CreateAccountInput{Name: "Efectivo USD", Type: AccountTypeCash, CurrencyCode: "USD"})
	if err != nil {
		t.Fatalf("creating an account: %v", err)
	}
	food, err := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Farmacia", Type: CategoryTypeExpense})
	if err != nil {
		t.Fatalf("creating a category: %v", err)
	}
	if _, err := fs.RecordExpense(ctx, RecordExpenseInput{
		AccountID: acct.ID, CategoryID: food.ID, PaymentMethodID: 3, Amount: "32.10",
		EffectiveDate: "2026-10-20", Description: OptionalString("Farmatodo"),
	}); err != nil {
		t.Fatalf("recording an expense: %v", err)
	}
}

func TestTheTickerShowsTheMonthAsComputed(t *testing.T) {
	t.Parallel()

	r := tickerRecap()
	fs, fetch := tickerRouter(t, &r, nil)
	seedTickerTransactions(t, fs)

	status, page := fetch("/ticker")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	for _, phrase := range []string{
		`data-tape-group="NWTH"`, "3,200.00", "+900.00 (+39.13%)", // net worth and its change
		`data-tape-group="DEBT"`, "450.00", // the card's debt
		"+900.00", "-200.00", // net cash flow and its change
		"45.00%", "-10.00 pts", // savings rate, in points
		"36.67", "USD/día", "+26.31%", // daily spending
		"USD/VES", "39.85", "[BCV]", // the rate that valued VES
		"CASH.VES", "7,970.00", // a balance in its own currency
		"COMIDA", "OCIO", "VIAJES", "[NUEVA]", // the three largest movements
		"TX Farmatodo", "-32.10", "[FARMACIA]", // a recent transaction
		"/transactions/",
	} {
		if !strings.Contains(page, phrase) {
			t.Errorf("the ticker does not show %q", phrase)
		}
	}

	// A zero balance is not a holding worth a tape, and only three categories
	// are shown.
	for _, absent := range []string{"CASH.EUR", "ALQUILER"} {
		if strings.Contains(page, absent) {
			t.Errorf("the ticker shows %q", absent)
		}
	}
	// Rising spending reads bad; a rising net worth reads good.
	if !strings.Contains(page, `data-tape-group="BURN" data-tone="bad"`) {
		t.Error("rising daily spending is not marked bad")
	}
	if !strings.Contains(page, `data-tape-group="NWTH" data-tone="good"`) {
		t.Error("a rising net worth is not marked good")
	}
	// A shrinking cash flow reads bad, and an expense's own figure is red.
	if !strings.Contains(page, `data-tape-group="CFLOW" data-tone="bad"`) {
		t.Error("a falling net cash flow is not marked bad")
	}
	if !strings.Contains(page, `data-tone="bad">-32.10`) {
		t.Error("an expense's amount is not marked bad")
	}
}

func TestTheTickerAsksForTheMonthInProgress(t *testing.T) {
	t.Parallel()

	r := tickerRecap()
	fs, fetch := tickerRouter(t, &r, nil)
	if status, _ := fetch("/ticker"); status != 200 {
		t.Fatalf("status = %d", status)
	}
	if len(fs.Analytics.RecapRequests) != 1 {
		t.Fatalf("recap requests = %d, want 1", len(fs.Analytics.RecapRequests))
	}
	req := fs.Analytics.RecapRequests[0]
	if req.Granularity != RecapMonth || !req.HasAnchor {
		t.Errorf("recap request = %+v, want the month anchored at today", req)
	}
}

func TestTheTickerKeepsWhatItCanReadWhenTheMonthFails(t *testing.T) {
	t.Parallel()

	fs, fetch := tickerRouter(t, nil, errors.New("valuation exploded"))
	seedTickerTransactions(t, fs)

	status, page := fetch("/ticker")
	if status != 200 {
		t.Fatalf("status = %d, want 200: the tape never breaks a page", status)
	}
	if strings.Contains(page, `data-tape-group="NWTH"`) {
		t.Error("the ticker shows a net worth it could not compute")
	}
	if !strings.Contains(page, "TX Farmatodo") {
		t.Error("the ticker dropped the transactions, which need no valuation")
	}
}

func TestAnEmptyTickerSaysSo(t *testing.T) {
	t.Parallel()

	_, fetch := tickerRouter(t, nil, errors.New("no data"))
	status, page := fetch("/ticker")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(page, "SIN DATOS") || strings.Contains(page, "data-tape-run") {
		t.Errorf("an empty ticker should say so and render no run:\n%s", page)
	}
}

func TestEveryPageCarriesTheTickerBay(t *testing.T) {
	t.Parallel()

	_, fetch := tickerRouter(t, nil, nil)
	for _, path := range []string{"/", "/accounts", "/settings", "/mobile"} {
		status, page := fetch(path)
		if status != 200 {
			t.Fatalf("%s: status = %d", path, status)
		}
		for _, phrase := range []string{`hx-get="/ticker"`, "/static/js/tape.js"} {
			if !strings.Contains(page, phrase) {
				t.Errorf("%s does not carry %q", path, phrase)
			}
		}
	}
}

func TestTheTickerSettingsListTheWholeCatalogue(t *testing.T) {
	t.Parallel()

	_, fetch := tickerRouter(t, nil, nil)
	status, page := fetch("/settings/ticker")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	for _, entry := range TickerCatalogue() {
		if !strings.Contains(page, `data-key="`+entry.Key+`"`) {
			t.Errorf("the settings do not list %s", entry.Key)
		}
	}
	for _, color := range []string{"amber", "green", "cyan", "red", "white"} {
		if !strings.Contains(page, `value="`+color+`"`) {
			t.Errorf("the settings do not offer the %s display", color)
		}
	}
}

func TestATickerTransactionIsNamedByItsType(t *testing.T) {
	t.Parallel()

	transfer := tickerTransaction(TransactionView{
		ID: 7, Type: TransactionTypeTransfer, CurrencyCode: "USD",
		Transfer: &TransferDetail{SourceAccountName: "A", DestinationAccountName: "B", AmountFormatted: "10.00"},
	})
	if transfer.Code != "TX A → B" || transfer.Note != "TRANSFERENCIA" || transfer.Tone != TickerToneNeutral ||
		transfer.Href != "/transfers/7/edit" {
		t.Errorf("transfer = %+v", transfer)
	}

	income := tickerTransaction(TransactionView{
		ID: 8, Type: TransactionTypeIncome, AccountName: "Mercantil", CurrencyCode: "USD",
		AmountMinor: 210000, AmountFormatted: "2100.00",
	})
	if income.Value != "+2,100.00" || income.Tone != TickerToneGood || income.Code != "TX Mercantil" {
		t.Errorf("income = %+v", income)
	}
}
