// Package currency owns the currencies a user's money can be denominated in.
//
// A currency is reference data: it is created once and then referred to. It is
// never deleted — accounts and, later, movements depend on it permanently — so
// it is retired by clearing IsActive instead.
package currency

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors this package returns. Callers distinguish them with errors.Is.
var (
	// ErrNotFound means no currency exists with the given code.
	ErrNotFound = errors.New("currency not found")
	// ErrInvalidInput means the input failed validation.
	ErrInvalidInput = errors.New("invalid currency")
	// ErrConflict means a currency with that code already exists.
	ErrConflict = errors.New("currency already exists")
)

// There is deliberately no ErrInactive. Being inactive is a fact about a
// currency (see Currency.IsActive), not a failure of any operation this
// package performs: Get returns inactive currencies and SetActive works in
// both directions. It only becomes an error in another domain's context —
// creating an account — and that domain raises its own error for it.

// Bounds enforced here and, identically, by CHECK constraints in the schema.
const (
	// MinMinorUnit and MaxMinorUnit span the real range of ISO 4217 exponents:
	// 0 (JPY, CLP), 2 (most), 3 (KWD, BHD) and 4 (CLF, UYW). Nothing in the
	// standard exceeds 4. Crypto-style precision is excluded on purpose: 18
	// decimals would overflow int64 for trivial amounts, so supporting it would
	// be a numeric representation decision, not a wider bound.
	MinMinorUnit = 0
	MaxMinorUnit = 4

	maxNameLength   = 60
	maxSymbolLength = 8
	codeLength      = 3
)

// Currency is a currency as stored.
type Currency struct {
	// Code is the ISO 4217 code and the primary key. It is immutable: no
	// operation in this package changes it, because renaming a currency would
	// rewrite the financial identity of everything denominated in it.
	Code string

	Name string

	// Symbol is presentation metadata and may be absent — not every currency
	// has a distinctive symbol in common use, and the UI falls back to Code.
	// It is never an identifier: "$" is shared by USD, CAD, AUD and others.
	Symbol *string

	// MinorUnit is the number of decimal places in the currency's minor unit.
	// It is immutable: changing it would silently reinterpret every amount ever
	// stored in this currency, turning 1.00 into 0.100.
	MinorUnit int

	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is the input to Service.Create.
type CreateInput struct {
	Code      string
	Name      string
	Symbol    *string // nil means the currency has no symbol
	MinorUnit int
}

// UpdateMetadataInput is the input to Service.UpdateMetadata.
//
// It carries only what may change. Code and MinorUnit are absent by design, so
// there is no signature through which they could be modified.
type UpdateMetadataInput struct {
	Name   string
	Symbol *string
}

// normalize puts the input into canonical form before validation, so that what
// is stored is always canonical regardless of how it was typed.
func (in *CreateInput) normalize() {
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	in.Name = strings.TrimSpace(in.Name)
	in.Symbol = trimOptional(in.Symbol)
}

func (in CreateInput) validate() error {
	if err := validateCode(in.Code); err != nil {
		return err
	}
	if err := validateName(in.Name); err != nil {
		return err
	}
	if err := validateSymbol(in.Symbol); err != nil {
		return err
	}
	if in.MinorUnit < MinMinorUnit || in.MinorUnit > MaxMinorUnit {
		return fmt.Errorf("%w: minor_unit %d is outside the ISO 4217 range %d-%d",
			ErrInvalidInput, in.MinorUnit, MinMinorUnit, MaxMinorUnit)
	}
	return nil
}

func (in *UpdateMetadataInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Symbol = trimOptional(in.Symbol)
}

func (in UpdateMetadataInput) validate() error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	return validateSymbol(in.Symbol)
}

func validateCode(code string) error {
	if len(code) != codeLength {
		return fmt.Errorf("%w: code %q must be exactly %d letters", ErrInvalidInput, code, codeLength)
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return fmt.Errorf("%w: code %q must contain only letters", ErrInvalidInput, code)
		}
	}
	return nil
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

// validateSymbol rejects a present-but-empty symbol.
//
// An empty string is not silently treated as "absent": a caller who sends ""
// believed they were sending a symbol, and swallowing that would hide their
// bug. Absence is expressed with nil, explicitly.
func validateSymbol(symbol *string) error {
	if symbol == nil {
		return nil
	}
	if *symbol == "" {
		return fmt.Errorf("%w: symbol must not be blank; use no symbol at all to omit it", ErrInvalidInput)
	}
	if len([]rune(*symbol)) > maxSymbolLength {
		return fmt.Errorf("%w: symbol must be at most %d characters", ErrInvalidInput, maxSymbolLength)
	}
	return nil
}

// trimOptional trims the value behind a pointer. nil stays nil; a value that
// trims to empty stays as an empty string so that validation can reject it
// rather than quietly turning it into an absent symbol.
func trimOptional(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	return &trimmed
}
