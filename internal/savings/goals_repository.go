package savings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// --- goals (M7.3) -------------------------------------------------------------

const goalSelect = `
	SELECT g.id, g.name, g.goal_type, g.target_usd_minor,
	       (SELECT r.target_usd_minor FROM savings_goal_target_revisions r
	         WHERE r.goal_id = g.id ORDER BY r.id LIMIT 1),
	       g.target_date, g.distribution_bp, g.status, g.closed_at, g.closing_note,
	       g.created_at, g.updated_at
	  FROM savings_goals g`

// newGoal is what InsertGoal writes.
type newGoal struct {
	name           string
	goalType       GoalType
	targetMinor    int64
	targetDate     *time.Time
	distributionBP int64
	now            time.Time
}

// InsertGoal writes a goal and its first target revision.
func (r *Repository) InsertGoal(ctx context.Context, in newGoal) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_goals
		    (name, goal_type, target_usd_minor, target_date, distribution_bp, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'ACTIVE', ?, ?)`,
		in.name, string(in.goalType), in.targetMinor, formatOptionalDate(in.targetDate), in.distributionBP,
		database.FormatTime(in.now), database.FormatTime(in.now))
	if err != nil {
		return 0, fmt.Errorf("inserting a savings goal: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading the id of the new goal: %w", err)
	}
	return id, r.AddTargetRevision(ctx, id, in.targetMinor, in.now)
}

// UpdateGoal rewrites a goal's editable fields.
func (r *Repository) UpdateGoal(ctx context.Context, id int64, name string, targetMinor int64,
	targetDate *time.Time, distributionBP int64, now time.Time,
) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE savings_goals
		   SET name = ?, target_usd_minor = ?, target_date = ?, distribution_bp = ?, updated_at = ?
		 WHERE id = ? AND status IN ('ACTIVE', 'PAUSED')`,
		name, targetMinor, formatOptionalDate(targetDate), distributionBP, database.FormatTime(now), id)
	if err != nil {
		return fmt.Errorf("updating savings goal %d: %w", id, err)
	}
	return expectOne(result, fmt.Errorf("savings goal %d is closed: %w", id, ErrInvalidState))
}

// AddTargetRevision appends a target to a goal's history.
func (r *Repository) AddTargetRevision(ctx context.Context, goalID, targetMinor int64, now time.Time) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO savings_goal_target_revisions (goal_id, target_usd_minor, created_at) VALUES (?, ?, ?)`,
		goalID, targetMinor, database.FormatTime(now)); err != nil {
		return fmt.Errorf("recording a target of goal %d: %w", goalID, err)
	}
	return nil
}

// SetGoalStatus moves a goal from one of the given statuses to another. A
// closing status records its instant and note; an open one clears nothing,
// because an open goal never had them.
func (r *Repository) SetGoalStatus(ctx context.Context, id int64, from []GoalStatus, to GoalStatus,
	note *string, now time.Time,
) error {
	var closedAt any
	if to.Closed() {
		closedAt = database.FormatTime(now)
	}
	args := []any{string(to), closedAt, note, database.FormatTime(now), id}
	placeholders := ""
	for i, f := range from {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
		args = append(args, string(f))
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE savings_goals
		   SET status = ?, closed_at = ?, closing_note = ?, updated_at = ?
		 WHERE id = ? AND status IN (`+placeholders+`)`, args...)
	if err != nil {
		return fmt.Errorf("setting savings goal %d to %s: %w", id, to, err)
	}
	return expectOne(result, fmt.Errorf("savings goal %d cannot become %s from its status: %w", id, to, ErrInvalidState))
}

// GetGoal returns one goal, without its derived figures.
func (r *Repository) GetGoal(ctx context.Context, id int64) (Goal, error) {
	goals, err := r.listGoals(ctx, goalSelect+` WHERE g.id = ?`, id)
	if err != nil {
		return Goal{}, err
	}
	if len(goals) == 0 {
		return Goal{}, fmt.Errorf("savings goal %d: %w", id, ErrGoalNotFound)
	}
	return goals[0], nil
}

// ListGoals returns goals, open ones first, then by id.
func (r *Repository) ListGoals(ctx context.Context, includeClosed bool) ([]Goal, error) {
	query := goalSelect
	if !includeClosed {
		query += ` WHERE g.status IN ('ACTIVE', 'PAUSED')`
	}
	query += ` ORDER BY CASE WHEN g.status IN ('ACTIVE', 'PAUSED') THEN 0 ELSE 1 END, g.id`
	return r.listGoals(ctx, query)
}

// DistributionTotal sums the shares of the open goals, leaving one out.
func (r *Repository) DistributionTotal(ctx context.Context, excludeID int64) (int64, error) {
	var sum int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(distribution_bp), 0) FROM savings_goals
		 WHERE status IN ('ACTIVE', 'PAUSED') AND id <> ?`, excludeID).Scan(&sum); err != nil {
		return 0, fmt.Errorf("summing goal shares: %w", err)
	}
	return sum, nil
}

// TargetRevisions returns every target a goal has had, oldest first.
func (r *Repository) TargetRevisions(ctx context.Context, goalID int64) (_ []TargetRevision, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT target_usd_minor, created_at FROM savings_goal_target_revisions WHERE goal_id = ? ORDER BY id`, goalID)
	if err != nil {
		return nil, fmt.Errorf("reading the targets of goal %d: %w", goalID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing target rows: %w", cerr))
		}
	}()
	out := make([]TargetRevision, 0)
	for rows.Next() {
		var (
			t       TargetRevision
			created string
		)
		if err := rows.Scan(&t.TargetUSDMinor, &created); err != nil {
			return nil, fmt.Errorf("reading a target: %w", err)
		}
		if t.CreatedAt, err = database.ParseTime(created); err != nil {
			return nil, fmt.Errorf("target created_at: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReservedByGoal returns every goal's reserved balance.
func (r *Repository) ReservedByGoal(ctx context.Context) (map[int64]int64, error) {
	return r.sumBy(ctx, `SELECT goal_id, SUM(amount_minor) FROM savings_entries WHERE goal_id IS NOT NULL GROUP BY goal_id`)
}

// ReservedByGoalAccount returns one goal's reserved balance per account.
func (r *Repository) ReservedByGoalAccount(ctx context.Context, goalID int64) (map[int64]int64, error) {
	return r.sumBy(ctx, `SELECT account_id, SUM(amount_minor) FROM savings_entries WHERE goal_id = ? GROUP BY account_id`, goalID)
}

func (r *Repository) listGoals(ctx context.Context, query string, args ...any) (_ []Goal, err error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing savings goals: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing goal rows: %w", cerr))
		}
	}()
	out := make([]Goal, 0)
	for rows.Next() {
		var (
			g                    Goal
			goalType, status     string
			targetDate, closedAt sql.NullString
			note                 sql.NullString
			created, updated     string
		)
		if err := rows.Scan(&g.ID, &g.Name, &goalType, &g.TargetUSDMinor, &g.OriginalTargetUSDMinor,
			&targetDate, &g.DistributionBP, &status, &closedAt, &note, &created, &updated); err != nil {
			return nil, fmt.Errorf("reading a savings goal: %w", err)
		}
		g.Type = GoalType(goalType)
		g.Status = GoalStatus(status)
		if targetDate.Valid {
			d, err := database.ParseDate(targetDate.String)
			if err != nil {
				return nil, fmt.Errorf("goal %d target_date: %w", g.ID, err)
			}
			g.TargetDate = &d
		}
		if closedAt.Valid {
			at, err := database.ParseTime(closedAt.String)
			if err != nil {
				return nil, fmt.Errorf("goal %d closed_at: %w", g.ID, err)
			}
			g.ClosedAt = &at
		}
		if note.Valid {
			text := note.String
			g.ClosingNote = &text
		}
		if g.CreatedAt, err = database.ParseTime(created); err != nil {
			return nil, fmt.Errorf("goal %d created_at: %w", g.ID, err)
		}
		if g.UpdatedAt, err = database.ParseTime(updated); err != nil {
			return nil, fmt.Errorf("goal %d updated_at: %w", g.ID, err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func formatOptionalDate(d *time.Time) any {
	if d == nil {
		return nil
	}
	return database.FormatDate(*d)
}
