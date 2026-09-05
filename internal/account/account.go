// Package account owns the accounts a user's money sits in.
//
// An account is denominated in exactly one currency. A bank offering both USD
// and VES under a single contract is modelled as two accounts, which keeps the
// future ledger single-currency by construction and makes cross-currency
// movement explicit rather than an invisible change of composition.
package account

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors this package returns. Callers distinguish them with errors.Is.
//
// The currency errors are declared here, not re-exported from the currency
// package, so that a handler dealing with accounts never has to import currency
// to interpret what account operations return. That this package consults
// currency internally is an implementation detail and must not leak.
var (
	// ErrNotFound means no account exists with the given id.
	ErrNotFound = errors.New("account not found")
	// ErrInvalidInput means the input failed validation.
	ErrInvalidInput = errors.New("invalid account")
	// ErrConflict means the account clashes with one that already exists.
	ErrConflict = errors.New("account already exists")
	// ErrCurrencyNotFound means the requested currency does not exist.
	ErrCurrencyNotFound = errors.New("account currency not found")
	// ErrCurrencyInactive means the requested currency exists but has been
	// retired, so it cannot denominate a new account.
	ErrCurrencyInactive = errors.New("account currency is inactive")
)

// Type classifies the logical container the money sits in.
//
// It describes *where* the money is, and nothing else. Purpose — "savings",
// "emergency fund", "day-to-day" — is a separate axis: one can save in a bank
// account, in cash or in a wallet, so folding it in here would force a false
// choice between two facts that are simultaneously true. If a purpose dimension
// is ever needed it arrives as its own decision, not as more values here.
type Type string

const (
	// Bank is any account held at an institution. It covers what elsewhere gets
	// split into current and savings accounts; the institution itself is
	// recorded separately.
	Bank Type = "BANK"
	// Cash is physical money held outside any institution.
	Cash Type = "CASH"
	// Wallet is a digital balance that is not a bank account, such as PayPal.
	Wallet Type = "WALLET"
	// Other is anything that fits none of the above.
	Other Type = "OTHER"
)

// Types lists every valid type, in the order a UI would present them.
func Types() []Type { return []Type{Bank, Cash, Wallet, Other} }

// Valid reports whether t is a known type.
func (t Type) Valid() bool {
	for _, known := range Types() {
		if t == known {
			return true
		}
	}
	return false
}

const (
	maxNameLength        = 120
	maxInstitutionLength = 120
)

// Account is an account as stored.
type Account struct {
	ID   int64
	Name string
	Type Type

	// CurrencyCode is immutable. Once movements exist, changing it would
	// reinterpret the entire history of the account, so there is no operation
	// that modifies it: archive the account and create a new one instead.
	CurrencyCode string

	// Institution is absent for cash held outside any institution, which is
	// different from an empty name.
	Institution *string

	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is the input to Service.Create.
type CreateInput struct {
	Name         string
	Type         Type
	CurrencyCode string
	Institution  *string // nil means no institution, as for cash
}

// UpdateMetadataInput is the input to Service.UpdateMetadata.
//
// CurrencyCode is deliberately absent: there is no signature through which an
// account's currency can be changed.
type UpdateMetadataInput struct {
	Name        string
	Type        Type
	Institution *string
}

func (in *CreateInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Type = Type(strings.ToUpper(strings.TrimSpace(string(in.Type))))
	in.CurrencyCode = strings.ToUpper(strings.TrimSpace(in.CurrencyCode))
	in.Institution = trimOptional(in.Institution)
}

func (in CreateInput) validate() error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if err := validateType(in.Type); err != nil {
		return err
	}
	if in.CurrencyCode == "" {
		return fmt.Errorf("%w: currency code must not be blank", ErrInvalidInput)
	}
	return validateInstitution(in.Institution)
}

func (in *UpdateMetadataInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Type = Type(strings.ToUpper(strings.TrimSpace(string(in.Type))))
	in.Institution = trimOptional(in.Institution)
}

func (in UpdateMetadataInput) validate() error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if err := validateType(in.Type); err != nil {
		return err
	}
	return validateInstitution(in.Institution)
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be blank", ErrInvalidInput)
	}
	if len([]rune(name)) > maxNameLength {
		return fmt.Errorf("%w: name must be at most %d characters", ErrInvalidInput, maxNameLength)
	}
	return nil
}

func validateType(t Type) error {
	if !t.Valid() {
		return fmt.Errorf("%w: account type %q must be one of %v", ErrInvalidInput, t, Types())
	}
	return nil
}

// validateInstitution rejects a present-but-empty institution. Absence is
// expressed with nil, so an empty string is a caller mistake rather than a way
// to say "none".
func validateInstitution(institution *string) error {
	if institution == nil {
		return nil
	}
	if *institution == "" {
		return fmt.Errorf("%w: institution must not be blank; omit it entirely instead", ErrInvalidInput)
	}
	if len([]rune(*institution)) > maxInstitutionLength {
		return fmt.Errorf("%w: institution must be at most %d characters",
			ErrInvalidInput, maxInstitutionLength)
	}
	return nil
}

// trimOptional trims the value behind a pointer. nil stays nil; a value that
// trims to empty stays empty so validation can reject it.
func trimOptional(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	return &trimmed
}
