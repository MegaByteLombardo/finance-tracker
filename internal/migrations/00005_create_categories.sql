-- +goose Up

-- Categories group income and expenses so that spending can be compared.
--
-- No rows are seeded. A default set of twenty categories would be twenty rows
-- guessed on someone's behalf, most of which they would archive; the user
-- creates the handful they actually use. Nor is there a category for opening
-- balances: an opening balance is not income and not an expense, it is where
-- the account started, and the transactions table refuses a category on one.
CREATE TABLE categories (
    id            INTEGER NOT NULL PRIMARY KEY,

    name          TEXT    NOT NULL
                          CHECK (length(trim(name)) BETWEEN 1 AND 80),

    -- Immutable. Flipping a category from INCOME to EXPENSE would reinterpret
    -- every transaction already filed under it, so no use case accepts it and
    -- no UPDATE statement names it -- the same treatment as currencies.code and
    -- accounts.currency_code, and recorded alongside them in data-model.md.
    category_type TEXT    NOT NULL
                          CHECK (category_type IN ('INCOME', 'EXPENSE')),

    -- Archived, never deleted: transactions reference categories permanently,
    -- and the foreign key from transactions is ON DELETE RESTRICT to enforce
    -- that rather than trust it.
    is_active     INTEGER NOT NULL DEFAULT 1
                          CHECK (is_active IN (0, 1)),

    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                          CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                          CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- Two categories called "Comida" under EXPENSE are a data-entry mistake, not a
-- distinction: nothing downstream could tell the user which is which. The same
-- name across the two types is legitimate, though -- "Intereses" is earned and
-- paid -- so the type is part of the key.
--
-- COLLATE NOCASE stops "Comida" and "comida" being separate rows. SQLite's
-- NOCASE folds ASCII only, so "Café" and "CAFÉ" would both be accepted; that is
-- a known limitation of the collation, recorded rather than papered over.
--
-- This index exists for correctness, not for speed. It is also the only index
-- on this table: a personal finance database holds tens of categories, where a
-- scan is cheaper than an index lookup, so an index on (category_type,
-- is_active) was considered for the form dropdowns and rejected as one that
-- would never earn its maintenance.
CREATE UNIQUE INDEX idx_categories_type_name ON categories(category_type, name COLLATE NOCASE);

-- +goose Down

DROP INDEX idx_categories_type_name;
DROP TABLE categories;
