package currency_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

func ptr(s string) *string { return &s }

// Validation runs before anything touches the database, so these cases are
// exercised through Create against a real database and must fail without
// inserting anything.
func TestCreateRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := map[string]currency.CreateInput{
		"lowercase code is normalised then still too short": {
			Code: "us", Name: "Test", Symbol: ptr("$"), MinorUnit: 2,
		},
		"two-letter code":   {Code: "US", Name: "Test", Symbol: ptr("$"), MinorUnit: 2},
		"four-letter code":  {Code: "USDX", Name: "Test", Symbol: ptr("$"), MinorUnit: 2},
		"digits in code":    {Code: "US1", Name: "Test", Symbol: ptr("$"), MinorUnit: 2},
		"empty code":        {Code: "", Name: "Test", Symbol: ptr("$"), MinorUnit: 2},
		"blank name":        {Code: "AAA", Name: "   ", Symbol: ptr("$"), MinorUnit: 2},
		"overlong name":     {Code: "AAA", Name: strings.Repeat("x", 61), Symbol: ptr("$"), MinorUnit: 2},
		"empty symbol":      {Code: "AAA", Name: "Test", Symbol: ptr(""), MinorUnit: 2},
		"blank symbol":      {Code: "AAA", Name: "Test", Symbol: ptr("   "), MinorUnit: 2},
		"overlong symbol":   {Code: "AAA", Name: "Test", Symbol: ptr("123456789"), MinorUnit: 2},
		"negative minor":    {Code: "AAA", Name: "Test", Symbol: ptr("$"), MinorUnit: -1},
		"minor above ISO 4": {Code: "AAA", Name: "Test", Symbol: ptr("$"), MinorUnit: 5},
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc := currency.NewService(dbtest.New(t))
			got, err := svc.Create(t.Context(), in)
			if !errors.Is(err, currency.ErrInvalidInput) {
				t.Fatalf("Create(%+v) = %+v, %v; want ErrInvalidInput", in, got, err)
			}
		})
	}
}

// An empty symbol is a caller mistake, not a way to say "no symbol". Absence is
// expressed with nil, so the two must not be conflated.
func TestCreateDistinguishesAbsentSymbolFromEmptyOne(t *testing.T) {
	t.Parallel()

	svc := currency.NewService(dbtest.New(t))

	if _, err := svc.Create(t.Context(), currency.CreateInput{
		Code: "AAA", Name: "Empty symbol", Symbol: ptr(""), MinorUnit: 2,
	}); !errors.Is(err, currency.ErrInvalidInput) {
		t.Errorf("an empty symbol was accepted: %v", err)
	}

	created, err := svc.Create(t.Context(), currency.CreateInput{
		Code: "BBB", Name: "No symbol", Symbol: nil, MinorUnit: 0,
	})
	if err != nil {
		t.Fatalf("a currency with no symbol was rejected: %v", err)
	}
	if created.Symbol != nil {
		t.Errorf("Symbol = %q, want nil", *created.Symbol)
	}

	// And it must survive the round trip as absent rather than as "".
	fetched, err := svc.Get(t.Context(), "BBB")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.Symbol != nil {
		t.Errorf("Symbol after reload = %q, want nil", *fetched.Symbol)
	}
}

func TestCreateNormalisesInput(t *testing.T) {
	t.Parallel()

	svc := currency.NewService(dbtest.New(t))

	created, err := svc.Create(t.Context(), currency.CreateInput{
		Code: "  gbp  ", Name: "  Pound Sterling  ", Symbol: ptr("  £  "), MinorUnit: 2,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.Code != "GBP" {
		t.Errorf("Code = %q, want %q", created.Code, "GBP")
	}
	if created.Name != "Pound Sterling" {
		t.Errorf("Name = %q, want %q", created.Name, "Pound Sterling")
	}
	if created.Symbol == nil || *created.Symbol != "£" {
		t.Errorf("Symbol = %v, want %q", created.Symbol, "£")
	}
}

// Lookups accept whatever casing the caller has.
func TestGetNormalisesTheCode(t *testing.T) {
	t.Parallel()

	svc := currency.NewService(dbtest.New(t))

	for _, code := range []string{"USD", "usd", " Usd "} {
		got, err := svc.Get(t.Context(), code)
		if err != nil {
			t.Errorf("Get(%q): %v", code, err)
			continue
		}
		if got.Code != "USD" {
			t.Errorf("Get(%q).Code = %q, want %q", code, got.Code, "USD")
		}
	}
}
