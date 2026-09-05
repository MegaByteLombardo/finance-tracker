package category

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// Repository translates between Category and SQL. It holds no business rules
// and performs no validation.
type Repository struct {
	db database.DBTX
}

// NewRepository builds a repository over a pool or an open transaction.
// Passing a *sql.Tx is what lets Service.Create check for a duplicate name and
// insert the row atomically.
func NewRepository(db database.DBTX) *Repository {
	return &Repository{db: db}
}

// columns is the read projection, listed explicitly so that adding a column to
// the table cannot silently change what Scan expects.
const columns = `id, name, category_type, is_active, created_at, updated_at`

// Get returns one category, or ErrNotFound.
func (r *Repository) Get(ctx context.Context, id int64) (Category, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM categories WHERE id = ?`, id)

	c, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, fmt.Errorf("category %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Category{}, fmt.Errorf("reading category %d: %w", id, err)
	}
	return c, nil
}

// List returns categories ordered by type, then name, then id, so that a
// grouped list needs one pass and no sorting above this layer.
func (r *Repository) List(ctx context.Context, in ListInput) (_ []Category, err error) {
	query := `SELECT ` + columns + ` FROM categories`

	var (
		conditions []string
		args       []any
	)
	if in.Type != nil {
		conditions = append(conditions, `category_type = ?`)
		args = append(args, string(*in.Type))
	}
	if !in.IncludeInactive {
		conditions = append(conditions, `is_active = 1`)
	}
	for i, condition := range conditions {
		if i == 0 {
			query += ` WHERE ` + condition
			continue
		}
		query += ` AND ` + condition
	}
	query += ` ORDER BY category_type, name, id`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing categories: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing category rows: %w", cerr))
		}
	}()

	categories := make([]Category, 0)
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reading category row: %w", err)
		}
		categories = append(categories, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating categories: %w", err)
	}
	return categories, nil
}

// FindByName looks a category up the way the unique index sees it: by type, and
// by name case-insensitively.
//
// It exists so that a duplicate is determined rather than inferred from a
// driver error. Excluding an id lets an update check for a clash with rows
// other than itself; pass 0 to exclude nothing.
func (r *Repository) FindByName(ctx context.Context, categoryType Type, name string, excludeID int64) (Category, error) {
	// COLLATE NOCASE here matches the collation of idx_categories_type_name, so
	// this query answers exactly the question that index enforces.
	row := r.db.QueryRowContext(ctx,
		`SELECT `+columns+`
		   FROM categories
		  WHERE category_type = ? AND name = ? COLLATE NOCASE AND id <> ?`,
		string(categoryType), name, excludeID)

	c, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, fmt.Errorf("category %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Category{}, fmt.Errorf("looking up category %q: %w", name, err)
	}
	return c, nil
}

// Insert stores a new category and returns it with its assigned id.
func (r *Repository) Insert(ctx context.Context, c Category) (Category, error) {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO categories (name, category_type, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		c.Name, string(c.Type), c.IsActive,
		database.FormatTime(c.CreatedAt), database.FormatTime(c.UpdatedAt))
	if err != nil {
		return Category{}, fmt.Errorf("inserting category %q: %w", c.Name, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Category{}, fmt.Errorf("reading the id of the new category %q: %w", c.Name, err)
	}
	c.ID = id
	return c, nil
}

// UpdateMetadata changes only the name.
//
// category_type is absent from this statement on purpose: it is the meaning of
// every transaction filed under the category, and changing it would rewrite
// them all.
func (r *Repository) UpdateMetadata(ctx context.Context, id int64, name string, updatedAt time.Time) error {
	return r.exec(ctx, id,
		`UPDATE categories SET name = ?, updated_at = ? WHERE id = ?`,
		name, database.FormatTime(updatedAt), id)
}

// SetActive archives a category, or brings it back.
func (r *Repository) SetActive(ctx context.Context, id int64, isActive bool, updatedAt time.Time) error {
	return r.exec(ctx, id,
		`UPDATE categories SET is_active = ?, updated_at = ? WHERE id = ?`,
		isActive, database.FormatTime(updatedAt), id)
}

// exec runs a statement that must affect exactly one category, turning "no rows
// matched" into ErrNotFound.
func (r *Repository) exec(ctx context.Context, id int64, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating category %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for category %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("category %d: %w", id, ErrNotFound)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (Category, error) {
	var (
		c                    Category
		categoryType         string
		createdAt, updatedAt string
	)
	if err := s.Scan(&c.ID, &c.Name, &categoryType, &c.IsActive, &createdAt, &updatedAt); err != nil {
		return Category{}, err
	}

	c.Type = Type(categoryType)

	var err error
	if c.CreatedAt, err = database.ParseTime(createdAt); err != nil {
		return Category{}, fmt.Errorf("category %d created_at: %w", c.ID, err)
	}
	if c.UpdatedAt, err = database.ParseTime(updatedAt); err != nil {
		return Category{}, fmt.Errorf("category %d updated_at: %w", c.ID, err)
	}
	return c, nil
}
