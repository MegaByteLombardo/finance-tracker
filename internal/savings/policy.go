package savings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// GetPolicy returns the history of the savings rate.
func (s *Service) GetPolicy(ctx context.Context) (Policy, error) {
	versions, err := NewRepository(s.db).ListPolicyVersions(ctx)
	if err != nil {
		return Policy{}, err
	}
	return policyFrom(versions, s.today()), nil
}

// policyFrom builds the Policy view of versions given oldest first.
func policyFrom(versions []PolicyVersion, today time.Time) Policy {
	p := Policy{Versions: make([]PolicyVersion, 0, len(versions))}
	for i := len(versions) - 1; i >= 0; i-- {
		p.Versions = append(p.Versions, versions[i])
	}
	if len(versions) == 0 {
		return p
	}
	start := versions[0].EffectiveFrom
	p.Start = &start
	for i := range versions {
		v := versions[i]
		if !v.EffectiveFrom.After(today) {
			p.Current = &v
		} else if p.Scheduled == nil {
			p.Scheduled = &v
		}
	}
	return p
}

// SetPolicy records a savings rate from a date on.
//
// The rules on WHEN a version may start are what keep history fixed (TD-077):
//
//   - The FIRST version may start on any date, including in the past. Saving it
//     generates nothing by itself: the incomes it would reach are shown by the
//     sync preview and suggested from only when the user confirms it.
//   - Every later version starts today or later, and never before the version
//     already in force. A rate therefore never changes for an income that a
//     previous version governed.
//   - A version no suggestion has used yet may be corrected in place: setting a
//     rate for the same start date rewrites it. If the policy has never produced
//     a suggestion at all, its single version may have its start moved earlier.
//     Neither loses anything, because nothing depended on what is rewritten.
func (s *Service) SetPolicy(ctx context.Context, in SetPolicyInput) error {
	rateBP, err := parseRate(in.Rate)
	if err != nil {
		return err
	}
	from, err := database.ParseDate(strings.TrimSpace(in.EffectiveFrom))
	if err != nil {
		return fmt.Errorf("%w: the start date must be YYYY-MM-DD", ErrInvalidInput)
	}

	return s.inTx(ctx, "setting the savings policy", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		versions, err := repo.ListPolicyVersions(ctx)
		if err != nil {
			return err
		}
		now := database.Now()

		if len(versions) == 0 {
			return repo.InsertPolicyVersion(ctx, rateBP, from, now)
		}

		// A policy that has never produced a suggestion can have its start
		// moved earlier: nothing depends on it yet. A LATER date is not a move
		// but a second version, scheduled, and falls through to the rules below.
		if len(versions) == 1 && from.Before(versions[0].EffectiveFrom) {
			used, err := repo.CountSuggestions(ctx)
			if err != nil {
				return err
			}
			if used == 0 {
				return repo.CorrectPolicyVersion(ctx, versions[0].ID, rateBP, from, now)
			}
		}

		for _, v := range versions {
			if v.EffectiveFrom.Equal(from) {
				if v.SuggestionCount > 0 {
					return fmt.Errorf("%w: the version starting on %s governs %d suggestions; choose a later start date",
						ErrPolicyVersionInUse, database.FormatDate(from), v.SuggestionCount)
				}
				return repo.CorrectPolicyVersion(ctx, v.ID, rateBP, from, now)
			}
		}

		latest := versions[len(versions)-1]
		today := s.today()
		if from.Before(today) {
			return fmt.Errorf("%w: only the first version may start before today (%s)",
				ErrRetroactivePolicy, database.FormatDate(today))
		}
		if from.Before(latest.EffectiveFrom) {
			return fmt.Errorf("%w: the latest version starts on %s",
				ErrRetroactivePolicy, database.FormatDate(latest.EffectiveFrom))
		}
		return repo.InsertPolicyVersion(ctx, rateBP, from, now)
	})
}

// parseRate reads a percentage with at most two decimals as basis points. A
// percentage with two decimals IS a count of basis points, so money.Parse at
// scale 2 reads it exactly with no float: "12.5" is 1250.
func parseRate(raw string) (int64, error) {
	bp, err := money.Parse(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%")), 2)
	if err != nil {
		return 0, fmt.Errorf("%w: the rate must be a percentage such as 20 or 12.5, with at most two decimals",
			ErrInvalidInput)
	}
	if bp < 0 || bp > money.BasisPointsPerUnit {
		return 0, fmt.Errorf("%w: the rate must be between 0 and 100", ErrInvalidInput)
	}
	return bp, nil
}

// ListCategoryEligibility returns every INCOME category, archived ones
// included, with whether its income is saved from.
func (s *Service) ListCategoryEligibility(ctx context.Context) ([]CategoryEligibility, error) {
	incomeType := category.Income
	categories, err := category.NewRepository(s.db).List(ctx, category.ListInput{Type: &incomeType, IncludeInactive: true})
	if err != nil {
		return nil, fmt.Errorf("listing income categories: %w", err)
	}
	rules, err := NewRepository(s.db).CategoryRules(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]CategoryEligibility, 0, len(categories))
	for _, c := range categories {
		eligible, explicit := rules[c.ID]
		if !explicit {
			eligible = true
		}
		out = append(out, CategoryEligibility{
			CategoryID: c.ID,
			Name:       c.Name,
			IsActive:   c.IsActive,
			Eligible:   eligible,
			Explicit:   explicit,
		})
	}
	return out, nil
}

// SetCategoryEligibility includes or excludes an INCOME category.
//
// It changes no suggestion by itself. The incomes the change affects appear in
// the sync preview, and are re-suggested or invalidated only when the user
// applies it -- the same explicit, previewed step as a backfill (TD-080).
func (s *Service) SetCategoryEligibility(ctx context.Context, categoryID int64, eligible bool) error {
	return s.inTx(ctx, "setting a category's savings eligibility", func(exec database.DBTX) error {
		c, err := category.NewRepository(exec).Get(ctx, categoryID)
		switch {
		case errors.Is(err, category.ErrNotFound):
			return fmt.Errorf("category %d: %w", categoryID, ErrCategoryNotFound)
		case err != nil:
			return fmt.Errorf("looking up category %d: %w", categoryID, err)
		}
		if c.Type != category.Income {
			return fmt.Errorf("%w: %q is an %s category", ErrCategoryNotIncome, c.Name, c.Type)
		}
		return NewRepository(exec).SetCategoryRule(ctx, categoryID, eligible, database.Now())
	})
}

// SetIncomeOverride records an explicit exception for one income and brings its
// suggestion in line, in the same transaction: overriding an income is an act
// on that income, like correcting it.
func (s *Service) SetIncomeOverride(ctx context.Context, transactionID int64, eligible bool, reason string) error {
	reason, err := normalizeReason(reason)
	if err != nil {
		return err
	}
	return s.inTx(ctx, "setting an income's savings override", func(exec database.DBTX) error {
		if _, err := getIncome(ctx, exec, transactionID); err != nil {
			return err
		}
		if err := NewRepository(exec).SetOverride(ctx, transactionID, eligible, reason, database.Now()); err != nil {
			return err
		}
		_, err := s.SyncIncomeTx(ctx, exec, transactionID)
		return err
	})
}
