// Package settings owns the application's own configuration: the choices a user
// makes from inside Finance Tracker about their own money, as opposed to the
// deployment settings internal/config reads from the environment.
//
// There is exactly one of them today, and exactly one row holding it. The base
// currency of valuation is here rather than in an environment variable because
// it must be a foreign key to a row in currencies: an environment variable
// cannot be one, so a typo in it would surface at valuation time rather than at
// the moment of the choice.
//
// This is deliberately not a preferences system. There is one user, one process
// and one database file, so there are no profiles, no scopes and no key/value
// bag that unrelated settings can accumulate in. A second setting arrives as a
// second column when a second setting actually exists.
package settings

import (
	"errors"
	"strings"
	"time"
)

// Errors this package returns. Callers distinguish them with errors.Is.
var (
	// ErrInvalidInput means the input failed validation before the database was
	// consulted: a blank or malformed currency code.
	ErrInvalidInput = errors.New("invalid settings")

	// ErrCurrencyNotFound means the requested base currency does not exist.
	//
	// currency's own errors are translated into these two so that a handler for
	// settings never has to import the currency package to interpret what it
	// was returned. The same rule account and ledger follow.
	ErrCurrencyNotFound = errors.New("base currency not found")

	// ErrCurrencyInactive means the requested base currency exists but has been
	// archived, so it may not be CHOSEN.
	//
	// It is not an error for the settings to already point at an archived
	// currency: archiving one retires it from new use without invalidating what
	// already refers to it, exactly as it does for accounts. The rule applies at
	// the moment of the choice, which is why it lives in the service rather than
	// in the foreign key.
	ErrCurrencyInactive = errors.New("base currency is inactive")

	// ErrMissing means the singleton settings row is not there.
	//
	// It is an integrity failure, not an ordinary state: migration 00012 creates
	// the row, and a CHECK makes a second one impossible, so its absence means
	// the database was tampered with or restored from something that is not one
	// of ours. It warrants a 500, never a form message.
	ErrMissing = errors.New("the settings row is missing")
)

// codeLength is the length of an ISO 4217 code, duplicated from currency for the
// same reason money duplicates the minor-unit bounds: so that validation here
// does not need to reach into the catalogue. A test asserts the two agree.
const codeLength = 3

// Settings is the application's configuration as stored. One row, always id 1.
type Settings struct {
	// BaseCurrencyCode is the currency the consolidated net worth is expressed
	// in.
	//
	// Changing it changes NOTHING about the ledger. No transaction is rewritten,
	// no movement is touched and no balance moves: each account keeps its own
	// currency and its own amounts. It decides only how those amounts are
	// valued for presentation.
	BaseCurrencyCode string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// normalizeCode puts a currency code into canonical form, so that what is stored
// is canonical regardless of how it was typed.
func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// validateCode enforces the shape currencies.code enforces, before the database
// is asked. It exists so that "usd " and "US" get different, specific answers
// rather than both arriving as a foreign key failure.
func validateCode(code string) error {
	if code == "" {
		return errors.New("a base currency is required")
	}
	if len(code) != codeLength {
		return errors.New("a currency code is exactly three letters")
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return errors.New("a currency code contains only letters")
		}
	}
	return nil
}
