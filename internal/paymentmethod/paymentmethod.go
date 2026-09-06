// Package paymentmethod owns the channels money moves through.
//
// A payment method describes how an operation with the outside world happened:
// a bank transfer, a card at a point of sale, cash. It is deliberately not a
// transfer between the user's own accounts — a method named "Bank transfer"
// says how money reached or left one account, whereas moving money between two
// accounts we own is a different thing entirely and is not in this milestone.
//
// M2 held these four values in a CHECK constraint. Real use produced "Pago
// móvil", "Zelle" and "Débito", and adding one meant a migration, so they became
// rows instead. They are created by the user and never deleted: transactions
// reference them permanently, so a method is archived to retire it.
package paymentmethod

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors this package returns. Callers distinguish them with errors.Is.
var (
	// ErrNotFound means no payment method exists with the given id.
	ErrNotFound = errors.New("payment method not found")
	// ErrInvalidInput means the input failed validation.
	ErrInvalidInput = errors.New("invalid payment method")
	// ErrConflict means a payment method with that name already exists.
	ErrConflict = errors.New("payment method already exists")
)

// There is deliberately no ErrInactive. Being archived is a fact about a
// method, not a failure of anything this package does: Get returns archived
// methods and SetActive works both ways. It only becomes an error in the
// ledger's context, and the ledger raises its own error for it.

const maxNameLength = 60

// PaymentMethod is a payment method as stored.
//
// It has a name and nothing else. A code, a sort order and an icon were all
// considered and left out: none has a consumer, and the presentation order is
// alphabetical.
type PaymentMethod struct {
	ID   int64
	Name string

	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is the input to Service.Create.
type CreateInput struct {
	Name string
}

// UpdateMetadataInput is the input to Service.UpdateMetadata.
//
// Renaming a method changes how every past transaction that used it is
// displayed. That is intended: a payment method is a label for a channel, not a
// fact captured at the moment of the transaction, so correcting its wording
// should follow the history — the same choice category.Name already makes.
//
// What must not be done is reusing a row for a different meaning, renaming
// "Zelle" to "PayPal" rather than creating a second method, because that
// silently rewrites what past transactions claim. No schema can prevent it; it
// is recorded as a rule in data-model.md.
type UpdateMetadataInput struct {
	Name string
}

// ListInput filters Service.List.
type ListInput struct {
	// IncludeInactive adds archived methods, which a management screen wants and
	// a transaction form must not.
	IncludeInactive bool
}

func (in *CreateInput) normalize() { in.Name = strings.TrimSpace(in.Name) }

func (in CreateInput) validate() error { return validateName(in.Name) }

func (in *UpdateMetadataInput) normalize() { in.Name = strings.TrimSpace(in.Name) }

func (in UpdateMetadataInput) validate() error { return validateName(in.Name) }

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be blank", ErrInvalidInput)
	}
	if len([]rune(name)) > maxNameLength {
		return fmt.Errorf("%w: name must be at most %d characters", ErrInvalidInput, maxNameLength)
	}
	return nil
}
