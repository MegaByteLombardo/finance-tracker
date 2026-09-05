-- +goose Up

-- A currency is identified by its ISO 4217 code, which is stable, globally
-- unique and already the value every other table will reference. A surrogate
-- integer key would add a join to read something the foreign key column
-- already contains.
CREATE TABLE currencies (
    code       TEXT    NOT NULL PRIMARY KEY
                       -- Exactly three uppercase letters. GLOB is
                       -- case-sensitive, so this also enforces the canonical
                       -- ISO 4217 casing.
                       CHECK (code GLOB '[A-Z][A-Z][A-Z]'),

    name       TEXT    NOT NULL
                       CHECK (length(trim(name)) BETWEEN 1 AND 60),

    -- Presentation metadata only, never an identifier: '$' is shared by USD,
    -- CAD, AUD and others. Nullable because not every currency has a
    -- distinctive symbol in common use; the UI falls back to the code. The
    -- CHECK keeps '' out, so "no symbol" has exactly one representation.
    symbol     TEXT
                       CHECK (symbol IS NULL OR length(trim(symbol)) BETWEEN 1 AND 8),

    -- Number of decimal places in the currency's minor unit. The range is the
    -- real span of ISO 4217 exponents: 0 (JPY, CLP), 2 (most), 3 (KWD, BHD),
    -- 4 (CLF, UYW). Immutable once set -- changing it would silently
    -- reinterpret every amount ever stored in this currency.
    minor_unit INTEGER NOT NULL
                       CHECK (minor_unit BETWEEN 0 AND 4),

    -- SQLite has no boolean type; INTEGER constrained to 0/1 is the correct
    -- spelling under STRICT. Retires a currency from new use without
    -- destroying history.
    is_active  INTEGER NOT NULL DEFAULT 1
                       CHECK (is_active IN (0, 1)),

    -- RFC 3339 UTC with fixed-width milliseconds, so lexicographic order is
    -- chronological order. The GLOB is what guarantees that invariant holds
    -- even for a hand-written INSERT. Go supplies these values explicitly; the
    -- DEFAULT is a safety net.
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                       CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                       CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- +goose Down

DROP TABLE currencies;
