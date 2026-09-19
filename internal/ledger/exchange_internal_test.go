package ledger

import (
	"errors"
	"testing"
)

// The execution rate is the one piece of arithmetic M5.5 adds, so it is tested
// directly rather than only through the service: these cases are about the
// orientation rule and about exactness, and reaching them through a database
// would bury both.

func TestExecutionRateDerivesTheRateExactly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string

		sourceCode        string
		sourceMinorUnit   int
		sourceAmountMinor int64

		destinationCode        string
		destinationMinorUnit   int
		destinationAmountMinor int64

		wantBase  string
		wantQuote string
		wantRate  string
		wantExact bool
	}{
		{
			// The acceptance case: 84,220.67 VES for 100.00 USD.
			name:     "buying dollars at a rate with four decimals",
			wantBase: "USD", wantQuote: "VES", wantRate: "842.2067", wantExact: true,
			sourceCode: "VES", sourceMinorUnit: 2, sourceAmountMinor: 8422067,
			destinationCode: "USD", destinationMinorUnit: 2, destinationAmountMinor: 10000,
		},
		{
			name:     "a whole rate keeps no decimals it does not need",
			wantBase: "USD", wantQuote: "VES", wantRate: "850", wantExact: true,
			sourceCode: "VES", sourceMinorUnit: 2, sourceAmountMinor: 8500000,
			destinationCode: "USD", destinationMinorUnit: 2, destinationAmountMinor: 10000,
		},
		{
			// Selling. The direction of the accounts reversed; the quote did
			// not, because it is still a price in bolívares per dollar.
			name:     "selling dollars is quoted the same way round",
			wantBase: "USD", wantQuote: "VES", wantRate: "845", wantExact: true,
			sourceCode: "USD", sourceMinorUnit: 2, sourceAmountMinor: 10000,
			destinationCode: "VES", destinationMinorUnit: 2, destinationAmountMinor: 8450000,
		},
		{
			name:     "a pair with no dollar is quoted in the cheaper currency",
			wantBase: "EUR", wantQuote: "VES", wantRate: "990", wantExact: true,
			sourceCode: "EUR", sourceMinorUnit: 2, sourceAmountMinor: 5000,
			destinationCode: "VES", destinationMinorUnit: 2, destinationAmountMinor: 4950000,
		},
		{
			// The same pair traded the other way. The orientation rule has to
			// give the same answer, or a buy and a sell could not be compared.
			name:     "and the same way round when that pair is traded back",
			wantBase: "EUR", wantQuote: "VES", wantRate: "990", wantExact: true,
			sourceCode: "VES", sourceMinorUnit: 2, sourceAmountMinor: 4950000,
			destinationCode: "EUR", destinationMinorUnit: 2, destinationAmountMinor: 5000,
		},
		{
			// Currencies with different scales. JPY has none, and the rate must
			// still come out of integer arithmetic exactly.
			name:     "currencies whose minor units differ",
			wantBase: "USD", wantQuote: "JPY", wantRate: "150", wantExact: true,
			sourceCode: "USD", sourceMinorUnit: 2, sourceAmountMinor: 10000,
			destinationCode: "JPY", destinationMinorUnit: 0, destinationAmountMinor: 15000,
		},
		{
			name:     "a rate with no finite decimal expansion is not exact",
			wantBase: "USD", wantQuote: "VES", wantRate: "33.333333333333", wantExact: false,
			sourceCode: "VES", sourceMinorUnit: 2, sourceAmountMinor: 10000,
			destinationCode: "USD", destinationMinorUnit: 2, destinationAmountMinor: 300,
		},
		{
			// Neither side is USD and the rate is exactly 1 in both directions,
			// so the tie-break decides: the destination is the base.
			name:     "an exact tie is broken by the destination",
			wantBase: "VES", wantQuote: "EUR", wantRate: "1", wantExact: true,
			sourceCode: "EUR", sourceMinorUnit: 2, sourceAmountMinor: 100,
			destinationCode: "VES", destinationMinorUnit: 2, destinationAmountMinor: 100,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rate, err := executionRate(
				tc.sourceCode, tc.sourceMinorUnit, tc.sourceAmountMinor,
				tc.destinationCode, tc.destinationMinorUnit, tc.destinationAmountMinor)
			if err != nil {
				t.Fatalf("deriving the rate: %v", err)
			}

			if rate.BaseCurrencyCode != tc.wantBase || rate.QuoteCurrencyCode != tc.wantQuote {
				t.Errorf("the rate is quoted 1 %s = x %s, want 1 %s = x %s",
					rate.BaseCurrencyCode, rate.QuoteCurrencyCode, tc.wantBase, tc.wantQuote)
			}
			if rate.RateFormatted != tc.wantRate {
				t.Errorf("the rate is %q, want %q", rate.RateFormatted, tc.wantRate)
			}
			if rate.Exact != tc.wantExact {
				t.Errorf("exact = %v, want %v", rate.Exact, tc.wantExact)
			}
			// RateFormatted must be exactly what the Decimal says, never a
			// second rendering that could drift from it.
			if rate.RateFormatted != rate.Rate.String() {
				t.Errorf("RateFormatted is %q but Rate is %q", rate.RateFormatted, rate.Rate.String())
			}
		})
	}
}

func TestExecutionRateRefusesWhatIsNotAnExchange(t *testing.T) {
	t.Parallel()

	if _, err := executionRate("VES", 2, 0, "USD", 2, 10000); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("a zero source returned %v, want ErrInvalidAmount", err)
	}
	if _, err := executionRate("VES", 2, 8500000, "USD", 2, -10000); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("a negative destination returned %v, want ErrInvalidAmount", err)
	}
	if _, err := executionRate("VES", 2, 8500000, "VES", 2, 10000); !errors.Is(err, ErrSameCurrency) {
		t.Errorf("one currency on both sides returned %v, want ErrSameCurrency", err)
	}
	if _, err := executionRate("VES", 9, 8500000, "USD", 2, 10000); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("an impossible minor unit returned %v, want ErrInvalidAmount", err)
	}
}

// splitExchangeBySign is the one implementation of what a stored exchange looks
// like, and the difference from splitTransfer is what this pins down: the two
// magnitudes are never compared.
func TestSplitExchangeBySign(t *testing.T) {
	t.Parallel()

	source, destination, err := splitExchangeBySign(1,
		exchangeSide{movementID: 10, accountID: 1, amountMinor: 8422067},
		exchangeSide{movementID: 11, accountID: 2, amountMinor: -10000})
	if err != nil {
		t.Fatalf("splitting a positive-first exchange: %v", err)
	}
	if source.movementID != 11 || destination.movementID != 10 {
		t.Errorf("the source is movement %d and the destination %d, want 11 and 10",
			source.movementID, destination.movementID)
	}

	rejected := map[string][2]exchangeSide{
		"both negative": {
			{movementID: 10, accountID: 1, amountMinor: -1},
			{movementID: 11, accountID: 2, amountMinor: -1},
		},
		"both positive": {
			{movementID: 10, accountID: 1, amountMinor: 1},
			{movementID: 11, accountID: 2, amountMinor: 1},
		},
		"one account": {
			{movementID: 10, accountID: 1, amountMinor: -1},
			{movementID: 11, accountID: 1, amountMinor: 1},
		},
	}
	for name, sides := range rejected {
		if _, _, err := splitExchangeBySign(1, sides[0], sides[1]); !errors.Is(err, ErrInconsistentExchange) {
			t.Errorf("%s returned %v, want ErrInconsistentExchange", name, err)
		}
	}

	// Equal and opposite magnitudes are the shape of a TRANSFER, and they are
	// accepted here: two accounts in two currencies that happen to hold the same
	// number of minor units is an ordinary exchange at a rate of one.
	if _, _, err := splitExchangeBySign(1,
		exchangeSide{movementID: 10, accountID: 1, amountMinor: -10000},
		exchangeSide{movementID: 11, accountID: 2, amountMinor: 10000}); err != nil {
		t.Errorf("two equal magnitudes were refused: %v", err)
	}
}

// The currency rule is the read path's half, because only it has joined the
// currencies.
func TestExchangeDetailFromRefusesOneCurrency(t *testing.T) {
	t.Parallel()

	_, err := exchangeDetailFrom(1,
		exchangeSide{movementID: 10, accountID: 1, amountMinor: -8500000, currencyCode: "VES", minorUnit: 2},
		exchangeSide{movementID: 11, accountID: 2, amountMinor: 8500000, currencyCode: "VES", minorUnit: 2})
	if !errors.Is(err, ErrInconsistentExchange) {
		t.Errorf("two movements in one currency returned %v, want ErrInconsistentExchange", err)
	}
}
