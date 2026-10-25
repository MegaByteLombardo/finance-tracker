-- +goose Up

-- M7.1: savings suggestions -- what the policy says should be saved from one
-- income, expressed in USD.
--
-- A suggestion is a TARGET, not money. It reserves nothing, moves no balance and
-- does not appear in the net worth. Four things are kept apart throughout M7 and
-- must not be confused: the income recorded (transactions), the saving suggested
-- (this table), the money actually reserved (M7.2) and the money assigned to a
-- goal (M7.3).
--
-- ONE income, MANY revisions, AT MOST ONE live. An income can be corrected after
-- its suggestion was made -- its amount, date, account or category change, or it
-- stops being an income at all -- and the suggestion must follow without
-- rewriting history. So a correction never UPDATEs the figures of a suggestion:
-- it closes the live revision (SUPERSEDED or INVALIDATED) and, if the income is
-- still eligible, opens the next one. The partial unique index below is what
-- makes "at most one live revision per income" a fact the database enforces,
-- and it is also what makes generating a suggestion idempotent (TD-079).
--
-- The valuation is FROZEN (TD-078). M5 and M6 derive every converted figure on
-- every read and store none (TD-052). A suggestion is different in kind: it is
-- the record of what the application told the user to save, on the rate in force
-- on the income's date, and a later correction of that rate must not silently
-- change a target the user may already have acted on. The USD amounts and the
-- observations used are therefore stored, and a corrected rate is surfaced and
-- applied only by an explicit revaluation.
--
-- Nothing is added to transactions. No transaction carries a rate, an exchange
-- rate id or a savings figure, here or anywhere.

CREATE TABLE savings_suggestions (
    id                    INTEGER NOT NULL PRIMARY KEY,

    -- The income this suggestion is about. RESTRICT: transactions are never
    -- deleted, and a suggestion without its income would be unexplainable.
    income_transaction_id INTEGER NOT NULL
                                  REFERENCES transactions(id)
                                  ON DELETE RESTRICT
                                  ON UPDATE RESTRICT,

    -- 1 for the first suggestion about an income, then 2, 3... for each
    -- correction. Unique per income (index below).
    revision              INTEGER NOT NULL
                                  CHECK (revision >= 1),

    -- The version in force on income_effective_date. Its rate_bp is the
    -- percentage applied, and is never changed once referenced.
    policy_version_id     INTEGER NOT NULL
                                  REFERENCES savings_policy_versions(id)
                                  ON DELETE RESTRICT
                                  ON UPDATE RESTRICT,

    -- What the income WAS when this revision was made: the fingerprint against
    -- which a later edit is detected, and the explanation a person reads. These
    -- are deliberately copies -- the income may change, this record must not.
    income_account_id     INTEGER NOT NULL
                                  REFERENCES accounts(id)
                                  ON DELETE RESTRICT
                                  ON UPDATE RESTRICT,

    income_category_id    INTEGER NOT NULL
                                  REFERENCES categories(id)
                                  ON DELETE RESTRICT
                                  ON UPDATE RESTRICT,

    -- Positive integer minor units of the income's own currency.
    income_amount_minor   INTEGER NOT NULL
                                  CHECK (income_amount_minor > 0),

    income_currency_code  TEXT    NOT NULL
                                  REFERENCES currencies(code)
                                  ON DELETE RESTRICT
                                  ON UPDATE RESTRICT,

    income_effective_date TEXT    NOT NULL
                                  CHECK (income_effective_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                         AND date(income_effective_date) IS income_effective_date),

    -- The valuation, in USD cents. Both NULL while the revision has no rate
    -- (PENDING_VALUATION); both set once it has. income_usd_minor is the income
    -- converted and rounded once; suggested_usd_minor is the exact converted
    -- income times rate_bp, rounded once -- NOT derived from the rounded
    -- income_usd_minor, so the two are each a single rounding of an exact value.
    income_usd_minor      INTEGER CHECK (income_usd_minor IS NULL OR income_usd_minor >= 0),
    suggested_usd_minor   INTEGER CHECK (suggested_usd_minor IS NULL OR suggested_usd_minor >= 0),

    -- How the rate was arrived at: fx.Method. IDENTITY for a USD income, which
    -- uses no observation and has no legs.
    fx_method             TEXT    CHECK (fx_method IS NULL
                                         OR fx_method IN ('IDENTITY', 'DIRECT', 'INVERTED', 'CROSS')),

    -- When the valuation was made -- an instant, not a financial date. The
    -- financial date of the suggestion is income_effective_date, and the rate
    -- applied is the one in force ON that date, whenever valued_at is.
    valued_at             TEXT    CHECK (valued_at IS NULL
                                         OR valued_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- PENDING_VALUATION  live, waiting for a rate (or for its date to arrive)
    -- OPEN               live, valued, the user has not dismissed it
    -- DISMISSED          live, the user declined the (remaining) suggestion
    -- SUPERSEDED         closed: replaced by the next revision
    -- INVALIDATED        closed: the income stopped being eligible
    --
    -- PENDING_CONFIRMATION, PARTIALLY_CONFIRMED and CONFIRMED are NOT stored.
    -- They are what OPEN looks like against the money actually reserved, which
    -- arrives in M7.2 and is derived on read -- a stored copy of "confirmed"
    -- could disagree with the reservations it summarises.
    status                TEXT    NOT NULL
                                  CHECK (status IN ('PENDING_VALUATION', 'OPEN', 'DISMISSED',
                                                    'SUPERSEDED', 'INVALIDATED')),

    -- Why a revision is in its status: required when the application closed it
    -- or the user dismissed it, so history explains itself.
    status_reason         TEXT    CHECK (status_reason IS NULL
                                         OR length(trim(status_reason)) BETWEEN 1 AND 255),

    created_at            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                                  CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                                  CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- A valuation is all-or-nothing. Every clause is IS NULL / IS NOT NULL, so
    -- the expression is TRUE or FALSE and never a silently passing NULL (TD-025).
    CHECK (
        (income_usd_minor IS NULL     AND suggested_usd_minor IS NULL
            AND fx_method IS NULL     AND valued_at IS NULL)
        OR
        (income_usd_minor IS NOT NULL AND suggested_usd_minor IS NOT NULL
            AND fx_method IS NOT NULL AND valued_at IS NOT NULL)
    ),

    -- What each status requires. A closed revision keeps whatever valuation it
    -- had -- it may have been closed before it was ever valued.
    CHECK (
        (status = 'PENDING_VALUATION' AND suggested_usd_minor IS NULL)
        OR
        (status = 'OPEN'              AND suggested_usd_minor IS NOT NULL)
        OR
        (status = 'DISMISSED'         AND suggested_usd_minor IS NOT NULL AND status_reason IS NOT NULL)
        OR
        (status IN ('SUPERSEDED', 'INVALIDATED') AND status_reason IS NOT NULL)
    )
) STRICT;

-- One revision number per income. Its leading column also indexes the foreign
-- key to transactions, and serves "every revision of this income".
CREATE UNIQUE INDEX idx_savings_suggestions_revision
    ON savings_suggestions(income_transaction_id, revision);

-- At most ONE live revision per income. A partial index: closed revisions are
-- history and may be as many as there were corrections. This is the database's
-- half of the guarantee that an income is never suggested twice; Go checks for
-- the live revision first, in the same transaction, and never classifies the
-- constraint error (CLAUDE.md).
CREATE UNIQUE INDEX idx_savings_suggestions_live
    ON savings_suggestions(income_transaction_id)
    WHERE status IN ('PENDING_VALUATION', 'OPEN', 'DISMISSED');

-- Foreign-key child columns, indexed for the RESTRICT checks as everywhere else.
CREATE INDEX idx_savings_suggestions_policy_version_id
    ON savings_suggestions(policy_version_id);
CREATE INDEX idx_savings_suggestions_income_account_id
    ON savings_suggestions(income_account_id);
CREATE INDEX idx_savings_suggestions_income_category_id
    ON savings_suggestions(income_category_id);
CREATE INDEX idx_savings_suggestions_income_currency_code
    ON savings_suggestions(income_currency_code);

-- The observations a suggestion's valuation used, copied (TD-078).
--
-- exchange_rates rows are mutable on purpose: the BCV provider updates the value
-- of a Fecha Valor it re-publishes, a manual rate can be corrected or archived.
-- A reference to the row alone would therefore not say what the rate WAS when
-- the suggestion was made. The copy does, and the reference says where it came
-- from -- which is also how a later correction is detected: the copy and the
-- row disagree.
--
-- One leg for DIRECT and INVERTED, two for CROSS, none for IDENTITY -- exactly
-- fx.Resolution.Legs. The combined factor is NOT stored: it is a function of the
-- legs, and a cross rate is a fraction that is never materialised as a decimal
-- (TD-046).
CREATE TABLE savings_suggestion_rate_legs (
    suggestion_id       INTEGER NOT NULL
                                REFERENCES savings_suggestions(id)
                                ON DELETE RESTRICT
                                ON UPDATE RESTRICT,

    leg                 INTEGER NOT NULL
                                CHECK (leg IN (1, 2)),

    exchange_rate_id    INTEGER NOT NULL
                                REFERENCES exchange_rates(id)
                                ON DELETE RESTRICT
                                ON UPDATE RESTRICT,

    -- The observation as published: 1 base = rate quote.
    base_currency_code  TEXT    NOT NULL
                                REFERENCES currencies(code)
                                ON DELETE RESTRICT
                                ON UPDATE RESTRICT,

    quote_currency_code TEXT    NOT NULL
                                REFERENCES currencies(code)
                                ON DELETE RESTRICT
                                ON UPDATE RESTRICT,

    -- Same canonical decimal TEXT and the same CHECK as exchange_rates.rate,
    -- positivity included without casting to REAL.
    rate                TEXT    NOT NULL
                                CHECK (length(rate) BETWEEN 1 AND 48
                                       AND rate GLOB '[0-9]*'
                                       AND NOT rate GLOB '*[^0-9.]*'
                                       AND length(rate) - length(replace(rate, '.', '')) <= 1
                                       AND ltrim(replace(rate, '.', ''), '0') <> ''),

    effective_date      TEXT    NOT NULL
                                CHECK (effective_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                       AND date(effective_date) IS effective_date),

    source              TEXT    NOT NULL
                                CHECK (source IN ('MANUAL', 'BCV_OFFICIAL')),

    -- Whether the observation was inverted to point from the income's currency
    -- towards USD.
    inverted            INTEGER NOT NULL
                                CHECK (inverted IN (0, 1)),

    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                                CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    PRIMARY KEY (suggestion_id, leg),

    CHECK (base_currency_code <> quote_currency_code)
) STRICT;

-- suggestion_id is the primary key's leading column. The others are foreign-key
-- children like any other.
CREATE INDEX idx_savings_suggestion_rate_legs_exchange_rate_id
    ON savings_suggestion_rate_legs(exchange_rate_id);
CREATE INDEX idx_savings_suggestion_rate_legs_base_currency_code
    ON savings_suggestion_rate_legs(base_currency_code);
CREATE INDEX idx_savings_suggestion_rate_legs_quote_currency_code
    ON savings_suggestion_rate_legs(quote_currency_code);

-- +goose Down

-- Reversible: purely additive. Undoing it loses every suggestion and its
-- history, which is why Down is never run automatically.

DROP INDEX idx_savings_suggestion_rate_legs_quote_currency_code;
DROP INDEX idx_savings_suggestion_rate_legs_base_currency_code;
DROP INDEX idx_savings_suggestion_rate_legs_exchange_rate_id;
DROP TABLE savings_suggestion_rate_legs;
DROP INDEX idx_savings_suggestions_income_currency_code;
DROP INDEX idx_savings_suggestions_income_category_id;
DROP INDEX idx_savings_suggestions_income_account_id;
DROP INDEX idx_savings_suggestions_policy_version_id;
DROP INDEX idx_savings_suggestions_live;
DROP INDEX idx_savings_suggestions_revision;
DROP TABLE savings_suggestions;
