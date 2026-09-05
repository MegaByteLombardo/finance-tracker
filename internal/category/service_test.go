package category_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

func typePtr(t category.Type) *category.Type { return &t }

// fixture returns a category service and the database behind it, so tests can
// assert on rows directly when the point is what did or did not get written.
func fixture(t *testing.T) (*category.Service, *sql.DB) {
	t.Helper()

	db := dbtest.New(t)
	return category.NewService(db), db
}

// Nothing is seeded: the user creates the handful of categories they use.
func TestNoCategoriesAreSeeded(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	categories, err := svc.List(t.Context(), category.ListInput{IncludeInactive: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(categories) != 0 {
		t.Errorf("a fresh database has %d categories, want 0", len(categories))
	}
}

func TestCreateGetList(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Error("the new category has no id")
	}
	if !created.IsActive {
		t.Error("a newly created category is not active")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("timestamps were not set on create")
	}

	fetched, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched != created {
		t.Errorf("Get returned %+v, want %+v", fetched, created)
	}

	categories, err := svc.List(ctx, category.ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(categories) != 1 {
		t.Errorf("List returned %d categories, want 1", len(categories))
	}
}

func TestCreateAcceptsEveryType(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	for _, categoryType := range category.Types() {
		created, err := svc.Create(t.Context(), category.CreateInput{
			Name: "Category " + string(categoryType), Type: categoryType,
		})
		if err != nil {
			t.Errorf("Create with type %q: %v", categoryType, err)
			continue
		}
		if created.Type != categoryType {
			t.Errorf("Type = %q, want %q", created.Type, categoryType)
		}
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := map[string]category.CreateInput{
		"blank name":    {Name: "   ", Type: category.Expense},
		"empty name":    {Name: "", Type: category.Expense},
		"overlong name": {Name: strings.Repeat("x", 81), Type: category.Expense},
		"empty type":    {Name: "Comida", Type: ""},
		"unknown type":  {Name: "Comida", Type: "TRANSFER"},
		// OPENING_BALANCE is a transaction type, not a category type. An
		// opening balance is deliberately uncategorised.
		"opening balance is not a category type": {Name: "Saldo inicial", Type: "OPENING_BALANCE"},
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, _ := fixture(t)
			if _, err := svc.Create(t.Context(), in); !errors.Is(err, category.ErrInvalidInput) {
				t.Fatalf("Create(%+v) = %v; want ErrInvalidInput", in, err)
			}
		})
	}
}

func TestCreateNormalisesInput(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	created, err := svc.Create(t.Context(), category.CreateInput{
		Name: "  Comida  ", Type: "  expense  ",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "Comida" {
		t.Errorf("Name = %q, want %q", created.Name, "Comida")
	}
	if created.Type != category.Expense {
		t.Errorf("Type = %q, want %q", created.Type, category.Expense)
	}
}

// A duplicate is a data-entry mistake: nothing downstream could tell two
// categories with the same name apart.
func TestCreateRejectsDuplicateNamePerType(t *testing.T) {
	t.Parallel()

	svc, db := fixture(t)
	ctx := t.Context()

	if _, err := svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, duplicate := range []string{"Comida", "comida", "COMIDA", "  Comida  "} {
		_, err := svc.Create(ctx, category.CreateInput{Name: duplicate, Type: category.Expense})
		if !errors.Is(err, category.ErrConflict) {
			t.Errorf("Create(%q) = %v, want ErrConflict", duplicate, err)
		}
	}

	// The rejected attempts left nothing behind.
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM categories").Scan(&count); err != nil {
		t.Fatalf("counting categories: %v", err)
	}
	if count != 1 {
		t.Errorf("%d category rows exist after four rejected creates, want 1", count)
	}
}

// "Intereses" is both earned and paid, so the type is part of the key.
func TestTheSameNameMayExistUnderBothTypes(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	if _, err := svc.Create(ctx, category.CreateInput{Name: "Intereses", Type: category.Income}); err != nil {
		t.Fatalf("Create income: %v", err)
	}
	if _, err := svc.Create(ctx, category.CreateInput{Name: "Intereses", Type: category.Expense}); err != nil {
		t.Errorf("the same name under the other type was rejected: %v", err)
	}
}

func TestListFiltersByTypeAndActivity(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	salary, err := svc.Create(ctx, category.CreateInput{Name: "Salario", Type: category.Income})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(ctx, category.CreateInput{Name: "Alquiler", Type: category.Expense}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.SetActive(ctx, salary.ID, false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	tests := map[string]struct {
		in    category.ListInput
		want  int
		names []string
	}{
		"active, both types": {category.ListInput{}, 2, []string{"Alquiler", "Comida"}},
		"everything":         {category.ListInput{IncludeInactive: true}, 3, nil},
		"active expenses":    {category.ListInput{Type: typePtr(category.Expense)}, 2, nil},
		"active income":      {category.ListInput{Type: typePtr(category.Income)}, 0, nil},
		"all income":         {category.ListInput{Type: typePtr(category.Income), IncludeInactive: true}, 1, nil},
		"type is normalised": {category.ListInput{Type: typePtr("  expense  ")}, 2, nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := svc.List(ctx, tc.in)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("List(%+v) returned %d categories, want %d", tc.in, len(got), tc.want)
			}
			for i, name := range tc.names {
				if got[i].Name != name {
					t.Errorf("category %d = %q, want %q", i, got[i].Name, name)
				}
			}
		})
	}
}

// Ordering is part of the contract: the presentation layer groups by type in
// one pass rather than sorting.
func TestListOrdersByTypeThenName(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	for _, c := range []category.CreateInput{
		{Name: "Zapatos", Type: category.Expense},
		{Name: "Salario", Type: category.Income},
		{Name: "Alquiler", Type: category.Expense},
		{Name: "Intereses", Type: category.Income},
	} {
		if _, err := svc.Create(ctx, c); err != nil {
			t.Fatalf("Create %q: %v", c.Name, err)
		}
	}

	got, err := svc.List(ctx, category.ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// EXPENSE sorts before INCOME as text, and names alphabetically within.
	want := []string{"Alquiler", "Zapatos", "Intereses", "Salario"}
	if len(got) != len(want) {
		t.Fatalf("List returned %d categories, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("category %d = %q, want %q", i, got[i].Name, want[i])
		}
	}
}

func TestListRejectsUnknownType(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	_, err := svc.List(t.Context(), category.ListInput{Type: typePtr("TRANSFER")})
	if !errors.Is(err, category.ErrInvalidInput) {
		t.Errorf("List with an unknown type = %v, want ErrInvalidInput", err)
	}
}

func TestGetUnknownCategory(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	if _, err := svc.Get(t.Context(), 4242); !errors.Is(err, category.ErrNotFound) {
		t.Errorf("Get of an unknown category = %v, want ErrNotFound", err)
	}
}

// The name changes; the type does not. There is no signature through which the
// type could be sent, so this asserts the value survives an update untouched.
func TestUpdateMetadataLeavesTypeUntouched(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := svc.UpdateMetadata(ctx, created.ID, category.UpdateMetadataInput{Name: "  Alimentación  "})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	if updated.Name != "Alimentación" {
		t.Errorf("Name = %q, want %q", updated.Name, "Alimentación")
	}
	if updated.Type != created.Type {
		t.Errorf("Type changed from %q to %q", created.Type, updated.Type)
	}
	if updated.ID != created.ID {
		t.Errorf("ID changed from %d to %d", created.ID, updated.ID)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt changed from %v to %v", created.CreatedAt, updated.CreatedAt)
	}
}

// Renaming onto an existing name is the same mistake as creating a duplicate.
func TestUpdateMetadataRejectsADuplicateName(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	if _, err := svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	other, err := svc.Create(ctx, category.CreateInput{Name: "Alquiler", Type: category.Expense})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.UpdateMetadata(ctx, other.ID, category.UpdateMetadataInput{Name: "comida"})
	if !errors.Is(err, category.ErrConflict) {
		t.Errorf("renaming onto an existing name = %v, want ErrConflict", err)
	}

	// Renaming a category to what it already is must not collide with itself.
	if _, err := svc.UpdateMetadata(ctx, other.ID, category.UpdateMetadataInput{Name: "Alquiler"}); err != nil {
		t.Errorf("renaming a category to its own name failed: %v", err)
	}
}

func TestUpdateMetadataUnknownCategory(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	_, err := svc.UpdateMetadata(t.Context(), 4242, category.UpdateMetadataInput{Name: "Nope"})
	if !errors.Is(err, category.ErrNotFound) {
		t.Errorf("UpdateMetadata of an unknown category = %v, want ErrNotFound", err)
	}
}

func TestSetActiveArchivesAndRestores(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	archived, err := svc.SetActive(ctx, created.ID, false)
	if err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	if archived.IsActive {
		t.Error("the category is still active after being archived")
	}

	// It still exists: archiving must never destroy the row.
	if _, err := svc.Get(ctx, created.ID); err != nil {
		t.Errorf("an archived category is no longer readable: %v", err)
	}

	active, err := svc.List(ctx, category.ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("List of active categories returned %d, want 0", len(active))
	}

	restored, err := svc.SetActive(ctx, created.ID, true)
	if err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	if !restored.IsActive {
		t.Error("the category was not restored")
	}
}

// An archived category still blocks its name: un-archiving it would otherwise
// collide with whatever took the name in the meantime.
func TestAnArchivedCategoryStillHoldsItsName(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)
	ctx := t.Context()

	created, err := svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.SetActive(ctx, created.ID, false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	_, err = svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense})
	if !errors.Is(err, category.ErrConflict) {
		t.Errorf("creating a category whose name is held by an archived one = %v, want ErrConflict", err)
	}
}

func TestSetActiveUnknownCategory(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	if _, err := svc.SetActive(t.Context(), 4242, false); !errors.Is(err, category.ErrNotFound) {
		t.Errorf("SetActive on an unknown category = %v, want ErrNotFound", err)
	}
}

// A cancelled context must stop the operation rather than let it run, including
// the transactional paths.
func TestOperationsRespectContextCancellation(t *testing.T) {
	t.Parallel()

	svc, _ := fixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := svc.List(ctx, category.ListInput{}); err == nil {
		t.Error("List succeeded with a cancelled context")
	}
	if _, err := svc.Get(ctx, 1); err == nil {
		t.Error("Get succeeded with a cancelled context")
	}
	if _, err := svc.Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense}); err == nil {
		t.Error("Create succeeded with a cancelled context")
	}
	if _, err := svc.UpdateMetadata(ctx, 1, category.UpdateMetadataInput{Name: "Comida"}); err == nil {
		t.Error("UpdateMetadata succeeded with a cancelled context")
	}
}
