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
	// ErrNatureChange means an edit would turn an asset into a liability or the
	// reverse (TD-089).
	//
	// A bank account reclassified as cash is a correction of where money sits.
	// A bank account reclassified as a credit card would turn every balance it
	// ever had from money held into money owed, and every movement into its
	// opposite in meaning, while no figure changed. Open the other kind of
	// account and archive this one instead.
	ErrNatureChange = errors.New("an account cannot change between an asset and a liability")
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
	// CreditCard is a revolving credit line (M8.1, TD-089): the first type whose
	// balance is money OWED rather than money held. A purchase lowers it, a
	// payment raises it, and a debt is a negative balance, so the ledger's
	// arithmetic is unchanged. Its terms -- statement day, limit -- live in
	// internal/creditcard; it is opened through internal/application, which
	// writes both in one transaction.
	CreditCard Type = "CREDIT_CARD"
)

// Types lists every valid type, in the order a UI would present them.
//
// CreditCard is last because it was appended: a caller rendering this list must
// not have its order change underneath it. Knowing a value does not make it
// offerable everywhere -- the generic account form offers AssetTypes, and a
// card is opened with its terms through its own form.
func Types() []Type { return []Type{Bank, Cash, Wallet, Other, CreditCard} }

// AssetTypes lists the types whose nature is Asset, in the order of Types.
func AssetTypes() []Type {
	var assets []Type
	for _, t := range Types() {
		if t.Nature() == Asset {
			assets = append(assets, t)
		}
	}
	return assets
}

// Nature is whether an account holds money or owes it (TD-089).
//
// It is DERIVED from Type and stored nowhere: a second column could only ever
// disagree with the first. Nothing infers it from a balance's sign -- a bank
// account can be overdrawn and a card can hold a credit, and neither changes
// what the account is -- and nothing infers it from a category or a name.
type Nature string

const (
	// Asset is an account whose balance is money the user holds.
	Asset Nature = "ASSET"
	// Liability is an account whose balance is money the user owes, as a
	// negative number. A future LOAN would be one too; none is built.
	Liability Nature = "LIABILITY"
)

// Nature returns whether accounts of this type hold or owe money.
//
// The switch names every type it knows and returns "" for anything else, so a
// type added later is neither an asset nor a liability until someone decides
// which it is (TD-045). A caller asking "is this an asset?" compares with Asset
// rather than testing for "not a liability".
func (t Type) Nature() Nature {
	switch t {
	case Bank, Cash, Wallet, Other:
		return Asset
	case CreditCard:
		return Liability
	default:
		return ""
	}
}

// Valid reports whether t is a known type.
func (t Type) Valid() bool {
	for _, known := range Types() {
		if t == known {
			return true
		}
	}
	return false
}

// Subtype refines Bank, and only Bank.
//
// It is a second, dependent axis rather than two more values in Type. Type
// classifies the container; whether that container is a current or a savings
// account is a fact that exists only for accounts held at an institution.
// Folding them into Type would have made "a savings account at Mercantil" and
// "cash" siblings in one enumeration, and left the other three types with no
// way to say which they are.
type Subtype string

const (
	// Checking is a current account: money kept for day-to-day use.
	Checking Subtype = "CHECKING"
	// Savings is a savings account at the same institution.
	Savings Subtype = "SAVINGS"
)

// Subtypes lists every valid subtype, in the order a UI would present them.
func Subtypes() []Subtype { return []Subtype{Checking, Savings} }

// SubtypesFor lists the subtypes valid for an account type: the two above for
// Bank, and none for anything else.
//
// A UI drives its subtype control from this. An empty result means the control
// must not be shown and must not be submitted, which is the same rule the
// validation below and the database's CHECK constraint enforce.
func SubtypesFor(t Type) []Subtype {
	if t == Bank {
		return Subtypes()
	}
	return nil
}

// Valid reports whether s is a known subtype.
func (s Subtype) Valid() bool {
	for _, known := range Subtypes() {
		if s == known {
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

	// Subtype is set if and only if Type is Bank. It is editable metadata: an
	// account genuinely reclassified from current to savings is corrected
	// rather than re-created.
	Subtype *Subtype

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
	Name string
	Type Type

	// Subtype is required when Type is Bank and must be nil otherwise.
	Subtype *Subtype

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
	Subtype     *Subtype
	Institution *string
}

func (in *CreateInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Type = Type(strings.ToUpper(strings.TrimSpace(string(in.Type))))
	in.Subtype = normalizeSubtype(in.Subtype)
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
	if err := validateSubtype(in.Type, in.Subtype); err != nil {
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
	in.Subtype = normalizeSubtype(in.Subtype)
	in.Institution = trimOptional(in.Institution)
}

func (in UpdateMetadataInput) validate() error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if err := validateType(in.Type); err != nil {
		return err
	}
	if err := validateSubtype(in.Type, in.Subtype); err != nil {
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

// validateSubtype enforces the dependency between the two columns: a bank
// account is a current or a savings account, and nothing else has a subtype at
// all.
//
// Both directions are errors rather than corrections. Silently dropping a
// subtype sent for a cash account would hide a caller's bug, and silently
// defaulting a missing one for a bank account would invent a fact about
// someone's money.
func validateSubtype(t Type, subtype *Subtype) error {
	if t != Bank {
		if subtype != nil {
			return fmt.Errorf("%w: only a %s account has a subtype, and this one is %s",
				ErrInvalidInput, Bank, t)
		}
		return nil
	}
	if subtype == nil {
		return fmt.Errorf("%w: a %s account needs a subtype, one of %v", ErrInvalidInput, Bank, Subtypes())
	}
	if !subtype.Valid() {
		return fmt.Errorf("%w: account subtype %q must be one of %v", ErrInvalidInput, *subtype, Subtypes())
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

// normalizeSubtype canonicalises the case and spacing of a supplied subtype so
// that "checking" and " CHECKING " address the same value. nil stays nil, and a
// value that normalises to something unknown is left for validation to reject.
func normalizeSubtype(s *Subtype) *Subtype {
	if s == nil {
		return nil
	}
	normalized := Subtype(strings.ToUpper(strings.TrimSpace(string(*s))))
	return &normalized
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
