package account_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

func ptr(s string) *string { return &s }

// checking returns a pointer to the CHECKING subtype, which every BANK account
// in these tests needs and no other type may have.
func checking() *account.Subtype { s := account.Checking; return &s }

func subtype(s account.Subtype) *account.Subtype { return &s }

// fixture returns an account service and the database behind it, so tests can
// assert on rows directly when the point is what did or did not get written.
func fixture(t *testing.T) (*account.Service, *currency.Service, *sql.DB) {
	t.Helper()

	db := dbtest.New(t)
	return account.NewService(db), currency.NewService(db), db
}

func TestCreateGetList(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, account.CreateInput{
		Name:         "Mercantil USD",
		Type:         account.Bank,
		Subtype:      checking(),
		CurrencyCode: "USD",
		Institution:  ptr("Mercantil"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Error("the new account has no id")
	}
	if !created.IsActive {
		t.Error("a newly created account is not active")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("timestamps were not set on create")
	}

	fetched, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched != created {
		// Account is comparable except for the Institution pointer, so compare
		// the fields that matter individually when this fires.
		if fetched.Name != created.Name || fetched.Type != created.Type ||
			fetched.CurrencyCode != created.CurrencyCode || fetched.ID != created.ID {
			t.Errorf("Get returned %+v, want %+v", fetched, created)
		}
	}
	if !fetched.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt round trip: got %v, want %v", fetched.CreatedAt, created.CreatedAt)
	}

	accounts, err := svc.List(ctx, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(accounts) != 1 {
		t.Errorf("List returned %d accounts, want 1", len(accounts))
	}
}

func TestCreateAcceptsEveryType(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	for _, accountType := range account.Types() {
		// Only a bank account carries a subtype, and it must carry one.
		var st *account.Subtype
		if accountType == account.Bank {
			st = checking()
		}

		created, err := svc.Create(t.Context(), account.CreateInput{
			Name:         "Account " + string(accountType),
			Type:         accountType,
			Subtype:      st,
			CurrencyCode: "USD",
		})
		if err != nil {
			t.Errorf("Create with type %q: %v", accountType, err)
			continue
		}
		if created.Type != accountType {
			t.Errorf("Type = %q, want %q", created.Type, accountType)
		}
	}
}

// Cash held outside any institution genuinely has none.
func TestCreateAcceptsAbsentInstitution(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	created, err := svc.Create(t.Context(), account.CreateInput{
		Name: "Efectivo USD", Type: account.Cash, CurrencyCode: "USD", Institution: nil,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Institution != nil {
		t.Errorf("Institution = %q, want nil", *created.Institution)
	}

	fetched, err := svc.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.Institution != nil {
		t.Errorf("Institution after reload = %q, want nil", *fetched.Institution)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := map[string]account.CreateInput{
		"blank name":        {Name: "   ", Type: account.Bank, CurrencyCode: "USD"},
		"empty name":        {Name: "", Type: account.Bank, CurrencyCode: "USD"},
		"overlong name":     {Name: strings.Repeat("x", 121), Type: account.Bank, CurrencyCode: "USD"},
		"empty type":        {Name: "Test", Type: "", CurrencyCode: "USD"},
		"unknown type":      {Name: "Test", Type: "CRYPTO", CurrencyCode: "USD"},
		"CHECKING is gone":  {Name: "Test", Type: "CHECKING", CurrencyCode: "USD"},
		"SAVINGS is gone":   {Name: "Test", Type: "SAVINGS", CurrencyCode: "USD"},
		"blank currency":    {Name: "Test", Type: account.Bank, Subtype: checking(), CurrencyCode: "   "},
		"empty institution": {Name: "Test", Type: account.Bank, Subtype: checking(), CurrencyCode: "USD", Institution: ptr("")},
		"blank institution": {Name: "Test", Type: account.Bank, Subtype: checking(), CurrencyCode: "USD", Institution: ptr("   ")},
		"overlong institution": {
			Name: "Test", Type: account.Bank, Subtype: checking(), CurrencyCode: "USD",
			Institution: ptr(strings.Repeat("x", 121)),
		},

		// The subtype rule, in both directions. Neither is silently corrected:
		// dropping a subtype sent for cash would hide a caller's bug, and
		// defaulting a missing one for a bank account would invent a fact.
		"bank without subtype": {Name: "Test", Type: account.Bank, CurrencyCode: "USD"},
		"bank with unknown subtype": {
			Name: "Test", Type: account.Bank, Subtype: subtype("PREMIUM"), CurrencyCode: "USD",
		},
		"bank with empty subtype": {
			Name: "Test", Type: account.Bank, Subtype: subtype(""), CurrencyCode: "USD",
		},
		"cash with subtype": {
			Name: "Test", Type: account.Cash, Subtype: checking(), CurrencyCode: "USD",
		},
		"wallet with subtype": {
			Name: "Test", Type: account.Wallet, Subtype: subtype(account.Savings), CurrencyCode: "USD",
		},
		"other with subtype": {
			Name: "Test", Type: account.Other, Subtype: checking(), CurrencyCode: "USD",
		},
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, _, _ := fixture(t)
			if _, err := svc.Create(t.Context(), in); !errors.Is(err, account.ErrInvalidInput) {
				t.Fatalf("Create(%+v) = %v; want ErrInvalidInput", in, err)
			}
		})
	}
}

func TestCreateNormalisesInput(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	created, err := svc.Create(t.Context(), account.CreateInput{
		Name:         "  Mercantil USD  ",
		Type:         "  bank  ",
		Subtype:      subtype("  savings  "),
		CurrencyCode: "  usd  ",
		Institution:  ptr("  Mercantil  "),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.Name != "Mercantil USD" {
		t.Errorf("Name = %q, want %q", created.Name, "Mercantil USD")
	}
	if created.Type != account.Bank {
		t.Errorf("Type = %q, want %q", created.Type, account.Bank)
	}
	if created.Subtype == nil || *created.Subtype != account.Savings {
		t.Errorf("Subtype = %v, want %q", created.Subtype, account.Savings)
	}
	if created.CurrencyCode != "USD" {
		t.Errorf("CurrencyCode = %q, want %q", created.CurrencyCode, "USD")
	}
	if created.Institution == nil || *created.Institution != "Mercantil" {
		t.Errorf("Institution = %v, want %q", created.Institution, "Mercantil")
	}
}

func TestCreateWithUnknownCurrency(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	_, err := svc.Create(t.Context(), account.CreateInput{
		Name: "Test", Type: account.Bank, Subtype: checking(), CurrencyCode: "ZZZ",
	})
	if !errors.Is(err, account.ErrCurrencyNotFound) {
		t.Fatalf("Create with an unknown currency = %v, want ErrCurrencyNotFound", err)
	}

	// The error must be this package's own. A handler for accounts should never
	// have to import currency to understand what account operations return.
	if errors.Is(err, currency.ErrNotFound) {
		t.Error("the currency package's error leaked through account.Service.Create")
	}
}

// The invariant this protects: no account is ever created denominated in a
// retired currency. The check and the insert share one transaction, so the
// failure must leave nothing behind.
func TestCreateWithInactiveCurrency(t *testing.T) {
	t.Parallel()

	accounts, currencies, db := fixture(t)
	ctx := t.Context()

	if _, err := currencies.SetActive(ctx, "EUR", false); err != nil {
		t.Fatalf("archiving the currency: %v", err)
	}

	_, err := accounts.Create(ctx, account.CreateInput{
		Name: "Ahorros EUR", Type: account.Bank, Subtype: subtype(account.Savings), CurrencyCode: "EUR",
	})
	if !errors.Is(err, account.ErrCurrencyInactive) {
		t.Fatalf("Create with an inactive currency = %v, want ErrCurrencyInactive", err)
	}

	// Nothing was written: this is what proves the transaction rolled back
	// rather than the check merely happening to run first.
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&count); err != nil {
		t.Fatalf("counting accounts: %v", err)
	}
	if count != 0 {
		t.Errorf("%d account rows exist after a rejected create, want 0", count)
	}
}

// Archiving a currency must not invalidate accounts that already use it: the
// state "active account, inactive currency" is legitimate and reachable.
func TestArchivingACurrencyLeavesExistingAccountsAlone(t *testing.T) {
	t.Parallel()

	accounts, currencies, _ := fixture(t)
	ctx := t.Context()

	created, err := accounts.Create(ctx, account.CreateInput{
		Name: "Cuenta EUR", Type: account.Bank, Subtype: checking(), CurrencyCode: "EUR",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := currencies.SetActive(ctx, "EUR", false); err != nil {
		t.Fatalf("archiving the currency: %v", err)
	}

	fetched, err := accounts.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("the account became unreadable after its currency was archived: %v", err)
	}
	if !fetched.IsActive {
		t.Error("archiving a currency deactivated an existing account")
	}
}

func TestGetUnknownAccount(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	if _, err := svc.Get(t.Context(), 4242); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("Get of an unknown account = %v, want ErrNotFound", err)
	}
}

// The editable fields change; the currency does not.
//
// UpdatedAt is not asserted to have strictly increased: timestamps have
// millisecond precision, so two legitimate operations can share one.
func TestUpdateMetadataLeavesCurrencyUntouched(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, account.CreateInput{
		Name: "Mercantil USD", Type: account.Bank, Subtype: checking(),
		CurrencyCode: "USD", Institution: ptr("Mercantil"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := svc.UpdateMetadata(ctx, created.ID, account.UpdateMetadataInput{
		Name: "Efectivo USD", Type: account.Cash, Institution: nil,
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	if updated.Name != "Efectivo USD" {
		t.Errorf("Name = %q, want %q", updated.Name, "Efectivo USD")
	}
	if updated.Type != account.Cash {
		t.Errorf("Type = %q, want %q", updated.Type, account.Cash)
	}
	if updated.Institution != nil {
		t.Errorf("Institution = %q, want nil", *updated.Institution)
	}

	// The immutable parts.
	if updated.CurrencyCode != created.CurrencyCode {
		t.Errorf("CurrencyCode changed from %q to %q", created.CurrencyCode, updated.CurrencyCode)
	}
	if updated.ID != created.ID {
		t.Errorf("ID changed from %d to %d", created.ID, updated.ID)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt changed from %v to %v", created.CreatedAt, updated.CreatedAt)
	}
	if updated.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero after an update")
	}
}

func TestUpdateMetadataUnknownAccount(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	_, err := svc.UpdateMetadata(t.Context(), 4242, account.UpdateMetadataInput{
		Name: "Nope", Type: account.Other,
	})
	if !errors.Is(err, account.ErrNotFound) {
		t.Errorf("UpdateMetadata of an unknown account = %v, want ErrNotFound", err)
	}
}

func TestSetActiveArchivesAndRestores(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, account.CreateInput{
		Name: "Ahorros USD", Type: account.Bank, Subtype: subtype(account.Savings), CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	archived, err := svc.SetActive(ctx, created.ID, false)
	if err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	if archived.IsActive {
		t.Error("the account is still active after being archived")
	}

	// It still exists: archiving must never destroy the row.
	if _, err := svc.Get(ctx, created.ID); err != nil {
		t.Errorf("an archived account is no longer readable: %v", err)
	}

	active, err := svc.List(ctx, false)
	if err != nil {
		t.Fatalf("List(false): %v", err)
	}
	if len(active) != 0 {
		t.Errorf("List(includeInactive=false) returned %d accounts, want 0", len(active))
	}

	all, err := svc.List(ctx, true)
	if err != nil {
		t.Fatalf("List(true): %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List(includeInactive=true) returned %d accounts, want 1", len(all))
	}

	restored, err := svc.SetActive(ctx, created.ID, true)
	if err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	if !restored.IsActive {
		t.Error("the account was not restored")
	}
}

func TestSetActiveUnknownAccount(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	if _, err := svc.SetActive(t.Context(), 4242, false); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("SetActive on an unknown account = %v, want ErrNotFound", err)
	}
}

// A cancelled context must stop the operation rather than let it run, including
// the transactional path.
func TestOperationsRespectContextCancellation(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := svc.List(ctx, true); err == nil {
		t.Error("List succeeded with a cancelled context")
	}
	if _, err := svc.Get(ctx, 1); err == nil {
		t.Error("Get succeeded with a cancelled context")
	}
	if _, err := svc.Create(ctx, account.CreateInput{
		Name: "Test", Type: account.Bank, Subtype: checking(), CurrencyCode: "USD",
	}); err == nil {
		t.Error("Create succeeded with a cancelled context")
	}
}

// The subtype is editable metadata, so reclassifying an account is a normal
// edit. What must not be possible is an account left in a state where its type
// and subtype disagree.
func TestUpdateMetadataEnforcesTheSubtypeRule(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, account.CreateInput{
		Name: "Mercantil USD", Type: account.Bank, Subtype: checking(),
		CurrencyCode: "USD", Institution: ptr("Mercantil"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Current account becomes a savings account at the same bank.
	updated, err := svc.UpdateMetadata(ctx, created.ID, account.UpdateMetadataInput{
		Name: "Mercantil Ahorros", Type: account.Bank,
		Subtype: subtype(account.Savings), Institution: ptr("Mercantil"),
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if updated.Subtype == nil || *updated.Subtype != account.Savings {
		t.Errorf("Subtype = %v, want %q", updated.Subtype, account.Savings)
	}

	// Dropping the subtype while it is still a bank account.
	_, err = svc.UpdateMetadata(ctx, created.ID, account.UpdateMetadataInput{
		Name: "Mercantil Ahorros", Type: account.Bank, Institution: ptr("Mercantil"),
	})
	if !errors.Is(err, account.ErrInvalidInput) {
		t.Errorf("clearing a bank account's subtype = %v, want ErrInvalidInput", err)
	}

	// Becoming cash while keeping one.
	_, err = svc.UpdateMetadata(ctx, created.ID, account.UpdateMetadataInput{
		Name: "Efectivo", Type: account.Cash, Subtype: checking(),
	})
	if !errors.Is(err, account.ErrInvalidInput) {
		t.Errorf("cash with a subtype = %v, want ErrInvalidInput", err)
	}

	// Becoming cash and dropping it at the same time is the legitimate move.
	reclassified, err := svc.UpdateMetadata(ctx, created.ID, account.UpdateMetadataInput{
		Name: "Efectivo", Type: account.Cash,
	})
	if err != nil {
		t.Fatalf("a legitimate reclassification was rejected: %v", err)
	}
	if reclassified.Subtype != nil {
		t.Errorf("Subtype = %v after becoming cash, want nil", reclassified.Subtype)
	}

	// And it survives a reload rather than only living in the returned value.
	fetched, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.Subtype != nil {
		t.Errorf("Subtype after reload = %v, want nil", fetched.Subtype)
	}
}

// SubtypesFor is what a form uses to decide whether to render the control at
// all, so it must agree with the validation rather than merely resemble it.
func TestSubtypesForMatchesValidation(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	for _, accountType := range account.Types() {
		allowed := account.SubtypesFor(accountType)

		if accountType == account.Bank {
			if len(allowed) != 2 {
				t.Errorf("SubtypesFor(%s) returned %v, want both subtypes", accountType, allowed)
			}
		} else if len(allowed) != 0 {
			t.Errorf("SubtypesFor(%s) returned %v, want none", accountType, allowed)
		}

		// Every subtype it offers must be accepted, and creating without one
		// must succeed exactly when it offers none.
		for _, s := range allowed {
			if _, err := svc.Create(t.Context(), account.CreateInput{
				Name: "Test", Type: accountType, Subtype: subtype(s), CurrencyCode: "USD",
			}); err != nil {
				t.Errorf("SubtypesFor(%s) offers %q but Create rejected it: %v", accountType, s, err)
			}
		}

		_, err := svc.Create(t.Context(), account.CreateInput{
			Name: "Test", Type: accountType, CurrencyCode: "USD",
		})
		if len(allowed) == 0 && err != nil {
			t.Errorf("%s offers no subtype but Create demanded one: %v", accountType, err)
		}
		if len(allowed) > 0 && err == nil {
			t.Errorf("%s offers subtypes but Create accepted an account without one", accountType)
		}
	}
}
