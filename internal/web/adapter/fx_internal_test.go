package adapter

import (
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

func TestAShortRateIsTwoDecimalsHalfToEven(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"886.22710000": "886.23",
		"39.8521":      "39.85",
		"40":           "40.00",
		"0.5":          "0.50",
		"1.125":        "1.12", // a tie goes to the even digit
		"1.135":        "1.14",
		"0.0049":       "0.00",
	} {
		if got := shortRate(money.MustParseDecimal(raw)); got != want {
			t.Errorf("shortRate(%s) = %q, want %q", raw, got, want)
		}
	}
}
