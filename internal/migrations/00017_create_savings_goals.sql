-- +goose Up

-- M7.3: savings goals -- what reserved money is FOR.
--
-- A goal holds no money of its own either. Its balance is SUM(amount_minor) over
-- the savings entries that name it (00016), so there is exactly one source of
-- truth for how much a goal has, and its status never stands in for it
-- (TD-084). Reserved money that names no goal is the fund's unallocated part.

CREATE TABLE savings_goals (
    id               INTEGER NOT NULL PRIMARY KEY,

    name             TEXT    NOT NULL
                             CHECK (length(trim(name)) BETWEEN 1 AND 80),

    -- PERMANENT: an emergency fund, a maintenance reserve. Reaching the target
    --            makes it FUNDED, never COMPLETED; it can be drawn on and refilled.
    -- ONE_TIME:  a trip, a laptop. It can be completed when its purpose is met.
    -- Immutable: switching kind would reinterpret the goal's history.
    goal_type        TEXT    NOT NULL
                             CHECK (goal_type IN ('PERMANENT', 'ONE_TIME')),

    -- The CURRENT target, in USD cents. Every value it has had is in
    -- savings_goal_target_revisions; the first one is the original budget a
    -- completion is compared against.
    target_usd_minor INTEGER NOT NULL
                             CHECK (target_usd_minor > 0),

    -- Optional, and only for a one-time goal.
    target_date      TEXT    CHECK (target_date IS NULL
                                    OR (target_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                        AND date(target_date) IS target_date)),

    -- The goal's share of each confirmed saving, in basis points. The sum over
    -- goals that are not closed is at most 10000 (checked in Go: a CHECK sees
    -- one row). Less than 10000 leaves the rest unallocated.
    distribution_bp  INTEGER NOT NULL DEFAULT 0
                             CHECK (distribution_bp BETWEEN 0 AND 10000),

    -- The lifecycle, and only the lifecycle. Whether a goal is funded is derived
    -- from its entries against its target, never stored.
    status           TEXT    NOT NULL DEFAULT 'ACTIVE'
                             CHECK (status IN ('ACTIVE', 'PAUSED', 'COMPLETED', 'CANCELLED')),

    -- When and why a goal was closed. COMPLETED or CANCELLED has both; an open
    -- goal has neither.
    closed_at        TEXT    CHECK (closed_at IS NULL
                                    OR closed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    closing_note     TEXT    CHECK (closing_note IS NULL OR length(trim(closing_note)) BETWEEN 1 AND 255),

    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                             CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                             CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- Only a one-time goal is completed, and only a one-time goal has a date.
    CHECK (status <> 'COMPLETED' OR goal_type = 'ONE_TIME'),
    CHECK (target_date IS NULL OR goal_type = 'ONE_TIME'),

    -- Closed has its instant and its note; open has neither (TD-025).
    CHECK (
        (status IN ('COMPLETED', 'CANCELLED') AND closed_at IS NOT NULL AND closing_note IS NOT NULL)
        OR
        (status IN ('ACTIVE', 'PAUSED') AND closed_at IS NULL AND closing_note IS NULL)
    )
) STRICT;

-- Every target a goal has had. A change of target appends a row; the original
-- budget is never overwritten silently.
CREATE TABLE savings_goal_target_revisions (
    id               INTEGER NOT NULL PRIMARY KEY,

    goal_id          INTEGER NOT NULL
                             REFERENCES savings_goals(id)
                             ON DELETE RESTRICT
                             ON UPDATE RESTRICT,

    target_usd_minor INTEGER NOT NULL
                             CHECK (target_usd_minor > 0),

    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                             CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

CREATE INDEX idx_savings_goal_target_revisions_goal_id ON savings_goal_target_revisions(goal_id, id);

-- An entry may now name the goal its money is for. NULL is the fund's
-- unallocated part. ADD COLUMN with a REFERENCES clause is allowed with foreign
-- keys on because the column's default is NULL; no existing row changes
-- meaning -- every reservation made before goals existed stays unallocated.
ALTER TABLE savings_entries ADD COLUMN goal_id INTEGER
    REFERENCES savings_goals(id) ON DELETE RESTRICT ON UPDATE RESTRICT;

CREATE INDEX idx_savings_entries_goal_id ON savings_entries(goal_id);

-- +goose Down

-- Reversible: additive. DROP COLUMN works here because nothing indexes or
-- references goal_id except the index dropped first.

DROP INDEX idx_savings_entries_goal_id;
ALTER TABLE savings_entries DROP COLUMN goal_id;
DROP INDEX idx_savings_goal_target_revisions_goal_id;
DROP TABLE savings_goal_target_revisions;
DROP TABLE savings_goals;
