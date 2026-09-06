package paymentmethod_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/paymentmethod"
)

func fixture(t *testing.T) (*paymentmethod.Service, *sql.DB) {
	t.Helper()

	db := dbtest.New(t)
	return paymentmethod.NewService(db), db
}

// The four M2 methods must exist after migrating, under the names the interface
// already showed, so that upgrading changes nothing a user can see.
func TestTheFourMethodsFromM2Survive(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	methods, err := svc.List(t.Context(), paymentmethod.ListInput{IncludeInactive: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(methods) != 4 {
		t.Fatalf("found %d payment methods, want the 4 migrated from M2", len(methods))
	}

	// Ordered by name.
	want := []string{"Bank transfer", "Cash", "Other", "Point of sale"}
	for i, name := range want {
		if methods[i].Name != name {
			t.Errorf("method %d is %q, want %q", i, methods[i].Name, name)
		}
		if !methods[i].IsActive {
			t.Errorf("%q is not active", methods[i].Name)
		}
	}
}

func TestCreateGetList(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, paymentmethod.CreateInput{Name: "  Pago móvil  "})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Error("the new method has no id")
	}
	if created.Name != "Pago móvil" {
		t.Errorf("Name = %q, want it trimmed to %q", created.Name, "Pago móvil")
	}
	if !created.IsActive {
		t.Error("a newly created method is not active")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("timestamps were not set")
	}

	fetched, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched != created {
		t.Errorf("Get returned %+v, want %+v", fetched, created)
	}

	methods, err := svc.List(ctx, paymentmethod.ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(methods) != 5 {
		t.Errorf("List returned %d methods, want 5", len(methods))
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	for label, name := range map[string]string{
		"blank":    "   ",
		"empty":    "",
		"overlong": strings.Repeat("x", 61),
	} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			_, err := svc.Create(t.Context(), paymentmethod.CreateInput{Name: name})
			if !errors.Is(err, paymentmethod.ErrInvalidInput) {
				t.Errorf("Create(%q) = %v, want ErrInvalidInput", name, err)
			}
		})
	}
}

// Uniqueness is exact, not case-insensitive. That is a deliberate divergence
// from category, and it is worth pinning down so it cannot drift silently.
func TestNamesAreUniqueExactly(t *testing.T) {
	t.Parallel()

	svc, db := fixture(t)
	ctx := t.Context()

	if _, err := svc.Create(ctx, paymentmethod.CreateInput{Name: "Zelle"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The same name, including after trimming, is a conflict.
	for _, duplicate := range []string{"Zelle", "  Zelle  "} {
		_, err := svc.Create(ctx, paymentmethod.CreateInput{Name: duplicate})
		if !errors.Is(err, paymentmethod.ErrConflict) {
			t.Errorf("Create(%q) = %v, want ErrConflict", duplicate, err)
		}
	}

	// A different case is a different method here. Documented, not accidental:
	// NOCASE would fold ASCII only, and half-working case folding is worse than
	// none.
	if _, err := svc.Create(ctx, paymentmethod.CreateInput{Name: "zelle"}); err != nil {
		t.Errorf("Create(%q) = %v; uniqueness is exact, so this is allowed", "zelle", err)
	}

	// The rejected attempts left nothing behind.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM payment_methods WHERE name LIKE 'zelle'").Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 2 {
		t.Errorf("%d rows named some case of zelle, want 2", count)
	}
}

func TestUpdateMetadataRenames(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, paymentmethod.CreateInput{Name: "Pago movil"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := svc.UpdateMetadata(ctx, created.ID, paymentmethod.UpdateMetadataInput{
		Name: "  Pago móvil  ",
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if updated.Name != "Pago móvil" {
		t.Errorf("Name = %q, want %q", updated.Name, "Pago móvil")
	}
	if updated.ID != created.ID {
		t.Errorf("ID changed from %d to %d", created.ID, updated.ID)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt changed from %v to %v", created.CreatedAt, updated.CreatedAt)
	}

	// Renaming onto a name already in use is the same mistake as creating a
	// duplicate.
	_, err = svc.UpdateMetadata(ctx, created.ID, paymentmethod.UpdateMetadataInput{Name: "Cash"})
	if !errors.Is(err, paymentmethod.ErrConflict) {
		t.Errorf("renaming onto an existing name = %v, want ErrConflict", err)
	}

	// Renaming a method to what it already is must not collide with itself.
	if _, err := svc.UpdateMetadata(ctx, created.ID, paymentmethod.UpdateMetadataInput{
		Name: "Pago móvil",
	}); err != nil {
		t.Errorf("renaming a method to its own name failed: %v", err)
	}
}

func TestSetActiveArchivesAndRestores(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, paymentmethod.CreateInput{Name: "Zelle"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	archived, err := svc.SetActive(ctx, created.ID, false)
	if err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	if archived.IsActive {
		t.Error("the method is still active after being archived")
	}

	// It still exists: archiving must never destroy the row.
	if _, err := svc.Get(ctx, created.ID); err != nil {
		t.Errorf("an archived method is no longer readable: %v", err)
	}

	active, err := svc.List(ctx, paymentmethod.ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range active {
		if m.ID == created.ID {
			t.Error("an archived method appeared in the default listing")
		}
	}

	all, err := svc.List(ctx, paymentmethod.ListInput{IncludeInactive: true})
	if err != nil {
		t.Fatalf("List(IncludeInactive): %v", err)
	}
	if len(all) != len(active)+1 {
		t.Errorf("IncludeInactive returned %d, want one more than %d", len(all), len(active))
	}

	restored, err := svc.SetActive(ctx, created.ID, true)
	if err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	if !restored.IsActive {
		t.Error("the method was not restored")
	}
}

// An archived method still holds its name, so un-archiving it cannot collide
// with something created in the meantime.
func TestAnArchivedMethodStillHoldsItsName(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, paymentmethod.CreateInput{Name: "Zelle"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.SetActive(ctx, created.ID, false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	_, err = svc.Create(ctx, paymentmethod.CreateInput{Name: "Zelle"})
	if !errors.Is(err, paymentmethod.ErrConflict) {
		t.Errorf("creating a method whose name is held by an archived one = %v, want ErrConflict", err)
	}
}

// There is no delete, and the database enforces it: a method in use cannot be
// removed even by hand.
func TestAMethodInUseCannotBeDeleted(t *testing.T) {
	t.Parallel()

	svc, db := fixture(t)
	ctx := t.Context()

	seed := []string{
		`INSERT INTO accounts (id,name,account_type,account_subtype,currency_code,is_active,created_at,updated_at)
		 VALUES (1,'Mercantil USD','BANK','CHECKING','USD',1,'2026-09-01T10:00:00.000Z','2026-09-01T10:00:00.000Z')`,
		`INSERT INTO categories (id,name,category_type,is_active,created_at,updated_at)
		 VALUES (1,'Comida','EXPENSE',1,'2026-09-01T10:00:01.000Z','2026-09-01T10:00:01.000Z')`,
		`INSERT INTO transactions (id,transaction_type,category_id,payment_method_id,description,effective_date,created_at,updated_at)
		 VALUES (10,'EXPENSE',1,2,NULL,'2026-09-04','2026-09-04T11:00:00.000Z','2026-09-04T11:00:00.000Z')`,
	}
	for _, statement := range seed {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM payment_methods WHERE id = 2"); err == nil {
		t.Error("a payment method referenced by a transaction was deleted")
	}

	// Archiving it instead works, and the transaction keeps displaying it.
	if _, err := svc.SetActive(ctx, 2, false); err != nil {
		t.Fatalf("archiving a method in use: %v", err)
	}

	var name string
	err := db.QueryRowContext(ctx, `
		SELECT pm.name FROM transactions t
		  JOIN payment_methods pm ON pm.id = t.payment_method_id
		 WHERE t.id = 10`).Scan(&name)
	if err != nil {
		t.Fatalf("a historical transaction became unreadable with an archived method: %v", err)
	}
	if name != "Point of sale" {
		t.Errorf("the transaction shows %q, want %q", name, "Point of sale")
	}
}

func TestGetUnknownMethod(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	if _, err := svc.Get(t.Context(), 4242); !errors.Is(err, paymentmethod.ErrNotFound) {
		t.Errorf("Get of an unknown method = %v, want ErrNotFound", err)
	}
	_, err := svc.UpdateMetadata(t.Context(), 4242, paymentmethod.UpdateMetadataInput{Name: "Nope"})
	if !errors.Is(err, paymentmethod.ErrNotFound) {
		t.Errorf("UpdateMetadata of an unknown method = %v, want ErrNotFound", err)
	}
	if _, err := svc.SetActive(t.Context(), 4242, false); !errors.Is(err, paymentmethod.ErrNotFound) {
		t.Errorf("SetActive on an unknown method = %v, want ErrNotFound", err)
	}
}

func TestOperationsRespectContextCancellation(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := svc.List(ctx, paymentmethod.ListInput{}); err == nil {
		t.Error("List succeeded with a cancelled context")
	}
	if _, err := svc.Get(ctx, 1); err == nil {
		t.Error("Get succeeded with a cancelled context")
	}
	if _, err := svc.Create(ctx, paymentmethod.CreateInput{Name: "Zelle"}); err == nil {
		t.Error("Create succeeded with a cancelled context")
	}
	if _, err := svc.UpdateMetadata(ctx, 1, paymentmethod.UpdateMetadataInput{Name: "Zelle"}); err == nil {
		t.Error("UpdateMetadata succeeded with a cancelled context")
	}
}
