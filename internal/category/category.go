// Package category owns the categories income and expenses are grouped under.
//
// A category is created by the user, not seeded: a default set would be rows
// guessed on someone's behalf, most of which they would archive. It is never
// deleted either, because transactions reference it permanently — archiving
// retires it from new use without erasing why money moved in the past.
package category

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors this package returns. Callers distinguish them with errors.Is.
var (
	// ErrNotFound means no category exists with the given id.
	ErrNotFound = errors.New("category not found")
	// ErrInvalidInput means the input failed validation.
	ErrInvalidInput = errors.New("invalid category")
	// ErrConflict means a category with that name already exists for that type.
	ErrConflict = errors.New("category already exists")
)

// There is deliberately no ErrInactive. Being inactive is a fact about a
// category, not a failure of anything this package does: Get returns inactive
// categories and SetActive works in both directions. It becomes an error only
// in the ledger's context, when one is used for a new transaction, and the
// ledger raises its own error for it.

// Type says whether a category groups money coming in or money going out.
//
// It is immutable once set. Flipping a category from Income to Expense would
// reinterpret every transaction already filed under it — the same reason a
// currency's minor unit and an account's currency cannot change — so no use
// case accepts it and no UPDATE statement names it.
type Type string

const (
	// Income groups money coming in: salary, interest, a refund.
	Income Type = "INCOME"
	// Expense groups money going out.
	Expense Type = "EXPENSE"
)

// Types lists every valid type, in the order a UI would present them.
func Types() []Type { return []Type{Income, Expense} }

// Valid reports whether t is a known type.
func (t Type) Valid() bool {
	for _, known := range Types() {
		if t == known {
			return true
		}
	}
	return false
}

const maxNameLength = 80

// Category is a category as stored.
type Category struct {
	ID   int64
	Name string

	// Type is immutable. See the note on Type itself.
	Type Type

	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is the input to Service.Create.
type CreateInput struct {
	Name string
	Type Type
}

// UpdateMetadataInput is the input to Service.UpdateMetadata.
//
// Type is deliberately absent: there is no signature through which a category's
// type can be changed.
type UpdateMetadataInput struct {
	Name string
}

// ListInput filters Service.List.
//
// Both fields default to the widest sensible answer for a form: every active
// category, of both types.
type ListInput struct {
	// Type restricts the result to one type. nil returns both.
	Type *Type

	// IncludeInactive adds archived categories, which a management screen wants
	// and a transaction form must not.
	IncludeInactive bool
}

func (in *CreateInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Type = Type(strings.ToUpper(strings.TrimSpace(string(in.Type))))
}

func (in CreateInput) validate() error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	return validateType(in.Type)
}

func (in *UpdateMetadataInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
}

func (in UpdateMetadataInput) validate() error {
	return validateName(in.Name)
}

func (in *ListInput) normalize() {
	if in.Type == nil {
		return
	}
	normalized := Type(strings.ToUpper(strings.TrimSpace(string(*in.Type))))
	in.Type = &normalized
}

func (in ListInput) validate() error {
	if in.Type == nil {
		return nil
	}
	return validateType(*in.Type)
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
		return fmt.Errorf("%w: category type %q must be one of %v", ErrInvalidInput, t, Types())
	}
	return nil
}
