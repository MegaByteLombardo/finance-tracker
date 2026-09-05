package currency_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

func newService(t *testing.T) *currency.Service {
	t.Helper()
	return currency.NewService(dbtest.New(t))
}

func TestCreateGetList(t *testing.T) {
	t.Parallel()

	svc := newService(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, currency.CreateInput{
		Code: "GBP", Name: "Pound Sterling", Symbol: ptr("£"), MinorUnit: 2,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created.IsActive {
		t.Error("a newly created currency is not active")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("timestamps were not set on create")
	}

	fetched, err := svc.Get(ctx, "GBP")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.Code != created.Code || fetched.Name != created.Name || fetched.MinorUnit != created.MinorUnit {
		t.Errorf("Get returned %+v, want %+v", fetched, created)
	}
	// Timestamps must survive the round trip exactly, which is what the
	// millisecond truncation in database.Now is for.
	if !fetched.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt round trip: got %v, want %v", fetched.CreatedAt, created.CreatedAt)
	}

	// The seed plus the one just created.
	all, err := svc.List(ctx, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("List returned %d currencies, want 4", len(all))
	}
	// Ordered by code: EUR, GBP, USD, VES.
	for i := 1; i < len(all); i++ {
		if all[i-1].Code > all[i].Code {
			t.Errorf("List is not ordered by code: %q before %q", all[i-1].Code, all[i].Code)
		}
	}
}

func TestCreateRejectsDuplicateCode(t *testing.T) {
	t.Parallel()

	svc := newService(t)

	// USD comes from the seed.
	_, err := svc.Create(t.Context(), currency.CreateInput{
		Code: "USD", Name: "Another Dollar", Symbol: ptr("$"), MinorUnit: 2,
	})
	if !errors.Is(err, currency.ErrConflict) {
		t.Fatalf("Create with a taken code = %v, want ErrConflict", err)
	}

	// The existing row must be untouched.
	existing, err := svc.Get(t.Context(), "USD")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if existing.Name != "US Dollar" {
		t.Errorf("the failed create modified the existing currency: name = %q", existing.Name)
	}
}

func TestGetUnknownCode(t *testing.T) {
	t.Parallel()

	svc := newService(t)

	if _, err := svc.Get(t.Context(), "ZZZ"); !errors.Is(err, currency.ErrNotFound) {
		t.Errorf("Get of an unknown currency = %v, want ErrNotFound", err)
	}
}

// The editable fields change; the immutable ones do not.
//
// Note what is *not* asserted: that UpdatedAt strictly increased. Timestamps
// have millisecond precision, so two legitimate operations can share one, and a
// strict comparison would fail for reasons that are not bugs.
func TestUpdateMetadata(t *testing.T) {
	t.Parallel()

	svc := newService(t)
	ctx := t.Context()

	before, err := svc.Get(ctx, "USD")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	updated, err := svc.UpdateMetadata(ctx, "usd", currency.UpdateMetadataInput{
		Name: "United States Dollar", Symbol: ptr("US$"),
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	if updated.Name != "United States Dollar" {
		t.Errorf("Name = %q, want %q", updated.Name, "United States Dollar")
	}
	if updated.Symbol == nil || *updated.Symbol != "US$" {
		t.Errorf("Symbol = %v, want %q", updated.Symbol, "US$")
	}

	// Immutable fields.
	if updated.Code != before.Code {
		t.Errorf("Code changed from %q to %q", before.Code, updated.Code)
	}
	if updated.MinorUnit != before.MinorUnit {
		t.Errorf("MinorUnit changed from %d to %d", before.MinorUnit, updated.MinorUnit)
	}
	if !updated.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("CreatedAt changed from %v to %v", before.CreatedAt, updated.CreatedAt)
	}
	if updated.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero after an update")
	}
}

// The symbol can be removed by passing nil, and that must persist as NULL.
func TestUpdateMetadataCanClearTheSymbol(t *testing.T) {
	t.Parallel()

	svc := newService(t)

	updated, err := svc.UpdateMetadata(t.Context(), "USD", currency.UpdateMetadataInput{
		Name: "US Dollar", Symbol: nil,
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if updated.Symbol != nil {
		t.Errorf("Symbol = %q, want nil", *updated.Symbol)
	}
}

func TestUpdateMetadataUnknownCode(t *testing.T) {
	t.Parallel()

	svc := newService(t)

	_, err := svc.UpdateMetadata(t.Context(), "ZZZ", currency.UpdateMetadataInput{Name: "Nope"})
	if !errors.Is(err, currency.ErrNotFound) {
		t.Errorf("UpdateMetadata of an unknown currency = %v, want ErrNotFound", err)
	}
}

func TestSetActiveArchivesAndRestores(t *testing.T) {
	t.Parallel()

	svc := newService(t)
	ctx := t.Context()

	archived, err := svc.SetActive(ctx, "EUR", false)
	if err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	if archived.IsActive {
		t.Error("the currency is still active after being archived")
	}

	// It still exists: archiving must never destroy reference data.
	if _, err := svc.Get(ctx, "EUR"); err != nil {
		t.Errorf("an archived currency is no longer readable: %v", err)
	}

	active, err := svc.List(ctx, false)
	if err != nil {
		t.Fatalf("List(false): %v", err)
	}
	for _, c := range active {
		if c.Code == "EUR" {
			t.Error("List(includeInactive=false) returned an archived currency")
		}
	}

	all, err := svc.List(ctx, true)
	if err != nil {
		t.Fatalf("List(true): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List(includeInactive=true) returned %d, want 3", len(all))
	}

	restored, err := svc.SetActive(ctx, "EUR", true)
	if err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	if !restored.IsActive {
		t.Error("the currency was not restored")
	}
}

func TestSetActiveUnknownCode(t *testing.T) {
	t.Parallel()

	svc := newService(t)

	if _, err := svc.SetActive(t.Context(), "ZZZ", false); !errors.Is(err, currency.ErrNotFound) {
		t.Errorf("SetActive on an unknown currency = %v, want ErrNotFound", err)
	}
}

// A cancelled context must stop the operation rather than let it run.
func TestOperationsRespectContextCancellation(t *testing.T) {
	t.Parallel()

	svc := newService(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := svc.Get(ctx, "USD"); err == nil {
		t.Error("Get succeeded with a cancelled context")
	}
	if _, err := svc.List(ctx, true); err == nil {
		t.Error("List succeeded with a cancelled context")
	}
	if _, err := svc.Create(ctx, currency.CreateInput{
		Code: "GBP", Name: "Pound Sterling", Symbol: ptr("£"), MinorUnit: 2,
	}); err == nil {
		t.Error("Create succeeded with a cancelled context")
	}
}
