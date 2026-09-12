-- +goose Up

-- Reference exchange rates, as OBSERVATIONS of what a source published.
--
-- This table exists to answer one question: what was one currency worth in
-- another on a given day? It is REFERENCE / VALUATION FX and nothing else.
--
--   Reference FX   != Transaction FX
--
-- A transaction rate is the rate a specific operation actually happened at -- a
-- card's rate on a purchase, a merchant's rate, the rate a cross-currency
-- transfer settled at. None of that exists in this application, nothing here
-- implies it, and `transactions` gains no exchange_rate_id in this milestone.
-- Mixing the two would make it impossible to say later which one a row meant.
--
-- ORIENTATION, fixed for the entire project:
--
--   1 <base_currency_code> = <rate> <quote_currency_code>
--
--   base = USD, quote = VES, rate = '842.20670000'  means  1 USD = 842.2067 VES
--
-- The columns are named base/quote rather than from/to precisely because
-- from/to invites a reader to assume a direction of conversion that the row does
-- not state. Conversion direction is decided by the caller, and the inverse is
-- computed exactly rather than stored, so that the two directions of one fact
-- can never drift apart.
--
-- Careful: "base_currency_code" here is one half of a PAIR. It is NOT the
-- application's base currency of valuation, which lives in app_settings and is
-- called the valuation base everywhere in the Go code.

CREATE TABLE exchange_rates (
    id INTEGER NOT NULL PRIMARY KEY,

    base_currency_code  TEXT NOT NULL
                             REFERENCES currencies(code)
                             ON DELETE RESTRICT
                             ON UPDATE RESTRICT,

    quote_currency_code TEXT NOT NULL
                             REFERENCES currencies(code)
                             ON DELETE RESTRICT
                             ON UPDATE RESTRICT,

    -- The ECONOMIC date of the observation: the BCV's "Fecha Valor", or the day
    -- a manually entered rate applies to.
    --
    -- It is emphatically NOT the day the rate was fetched -- see fetched_at. The
    -- BCV publishes on a Friday a rate whose Fecha Valor is the following
    -- Tuesday, and keeps one Fecha Valor across a weekend, so the two dates
    -- disagree in both directions and neither can stand in for the other.
    --
    -- Same convention and same two CHECK clauses as transactions.effective_date:
    -- the GLOB fixes the shape and rejects '2026-9-5', and date(x) IS x rejects
    -- a well-formed date that does not exist, such as '2026-02-31'.
    effective_date TEXT NOT NULL
                        CHECK (effective_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                               AND date(effective_date) IS effective_date),

    -- The rate as a canonical decimal string, exactly as published.
    --
    -- TEXT, not REAL -- REAL is binary floating point and cannot hold
    -- 842.20670000 exactly. TEXT rather than an integer mantissa plus a scale
    -- column because the two would have to be kept consistent by hand, because
    -- the same value would have several spellings, and because the owner of this
    -- database opens it with the sqlite3 CLI: '842.20670000' reads where
    -- 84220670000 paired with 8 does not. The same reasoning that made
    -- timestamps TEXT (TD-016).
    --
    -- The scale is significant and preserved: the trailing zeros of
    -- '842.20670000' record the precision the source published at.
    --
    -- Nothing in this application ever ORDERS or RANGE-FILTERS by rate, so the
    -- fact that TEXT does not sort numerically costs nothing. The lookup is by
    -- pair and date, which are both exact.
    --
    -- What these CHECKs honestly guarantee, and no more: the value is made only
    -- of digits and at most one decimal point, starts with a digit (so it
    -- carries no sign and no leading point), and is not zero. Strict positivity
    -- is expressed WITHOUT casting to REAL -- stripping the point and then the
    -- leading zeros leaves an empty string exactly when the value is zero.
    -- Full canonical-form validation lives in Go, in internal/money.
    rate TEXT NOT NULL
              CHECK (length(rate) BETWEEN 1 AND 48
                     AND rate GLOB '[0-9]*'
                     AND NOT rate GLOB '*[^0-9.]*'
                     AND length(rate) - length(replace(rate, '.', '')) <= 1
                     AND ltrim(replace(rate, '.', ''), '0') <> ''),

    -- Where the observation came from. A whitelist rather than a boolean
    -- is_manual, because provenance is the point: two rows can state different
    -- values for the same day and the application has to know which one to
    -- prefer and be able to tell the user which one it used.
    --
    -- No BCV_MIRROR. A third-party mirror was evaluated for M5 and left out --
    -- the historical one no longer resolves, and the reachable one publishes
    -- four decimals where the BCV publishes eight, under a date field that is
    -- not the Fecha Valor. A value is added to this list when a source is
    -- actually implemented, not in anticipation.
    source TEXT NOT NULL
                CHECK (source IN ('MANUAL', 'BCV_OFFICIAL')),

    -- Archived rather than deleted, the same mechanism every other table in this
    -- schema uses. Archiving a MANUAL observation is how a correction is undone:
    -- valuation then falls back to whatever the official source published for
    -- that day. An archived row is invisible to valuation and is never removed.
    is_active INTEGER NOT NULL DEFAULT 1
                      CHECK (is_active IN (0, 1)),

    -- When this application SAW the value. A provider row always has one; a
    -- manually typed rate never does, because nothing was fetched.
    --
    -- Both branches are IS NULL / IS NOT NULL, so the table CHECK below can only
    -- be TRUE or FALSE and cannot silently pass (TD-025).
    fetched_at TEXT
                    CHECK (fetched_at IS NULL
                           OR fetched_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                    CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                    CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- A currency is worth exactly one of itself. Storing USD/USD = 1 would be a
    -- row that says nothing, that could be edited into saying something false,
    -- and that the lookup would then have to prefer over arithmetic identity.
    CHECK (base_currency_code <> quote_currency_code),

    -- A fetched timestamp belongs to a fetch. Written with IS NULL / IS NOT NULL
    -- on both sides so it is always TRUE or FALSE (TD-025).
    CHECK (
        (source =  'MANUAL' AND fetched_at IS NULL)
        OR
        (source <> 'MANUAL' AND fetched_at IS NOT NULL)
    )
) STRICT;

-- The natural identity of an observation: one source, one pair, one economic
-- day. A second run of the scraper on the same day updates this row rather than
-- adding another, and a corrected publication for the same Fecha Valor replaces
-- the value it corrects. A manual rate and an official rate for the same day are
-- two different observations and both survive, which is what makes precedence a
-- decision rather than a destructive write.
CREATE UNIQUE INDEX idx_exchange_rates_observation
    ON exchange_rates(base_currency_code, quote_currency_code, effective_date, source);

-- The critical path of every valuation:
--
--   WHERE base = ? AND quote = ? AND effective_date <= ?
--   ORDER BY effective_date DESC
--   LIMIT 1
--
-- SQLite walks this index backwards to answer that without a sort. Source
-- precedence is resolved among the rows of the single winning date, which is a
-- handful of rows at most, so it is not part of the key.
CREATE INDEX idx_exchange_rates_lookup
    ON exchange_rates(base_currency_code, quote_currency_code, effective_date DESC);

-- SQLite does not index the child columns of a foreign key. base_currency_code
-- is the leading column of both indexes above and is therefore already covered;
-- quote_currency_code is not covered by anything, and without this every
-- RESTRICT check against currencies would scan this table in full, as would any
-- join that resolves the quote side.
CREATE INDEX idx_exchange_rates_quote_currency_code
    ON exchange_rates(quote_currency_code);

-- +goose Down

-- Reversible, unlike 00008 to 00010.
--
-- Those three rewrote the ledger and could not be undone without inventing a
-- lossy mapping for data that had no representation in the older schema. This
-- migration only ADDS a table: nothing that existed before it is reinterpreted,
-- moved or dropped, so undoing it restores exactly the schema it found. What is
-- lost is the recorded rates themselves, which is why Down is still never run
-- automatically.

DROP INDEX idx_exchange_rates_quote_currency_code;
DROP INDEX idx_exchange_rates_lookup;
DROP INDEX idx_exchange_rates_observation;
DROP TABLE exchange_rates;
