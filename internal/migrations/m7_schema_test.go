package migrations_test

import (
	"database/sql"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

// These assert what the M7.1 tables refuse on their own, whatever Go does
// before them (00014, 00015).

const insertPolicyVersion = `INSERT INTO savings_policy_versions (rate_bp, effective_from) VALUES (?, ?)`

func TestSavingsPolicyVersionConstraints(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		rate     any
		from     any
		accepted bool
	}{
		"twenty percent":  {2000, "2026-10-01", true},
		"zero percent":    {0, "2026-10-01", true},
		"everything":      {10000, "2026-10-01", true},
		"negative":        {-1, "2026-10-01", false},
		"above a hundred": {10001, "2026-10-01", false},
		"a real":          {20.5, "2026-10-01", false},
		"null rate":       {nil, "2026-10-01", false},
		"impossible date": {2000, "2026-02-30", false},
		"badly shaped":    {2000, "2026-10-1", false},
		"null date":       {2000, nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := dbtest.New(t)
			_, err := db.ExecContext(t.Context(), insertPolicyVersion, tc.rate, tc.from)
			if tc.accepted && err != nil {
				t.Fatalf("refused a valid version: %v", err)
			}
			if !tc.accepted && err == nil {
				t.Fatal("accepted an invalid version")
			}
		})
	}
}

// Two versions starting on one day would leave the choice to row order.
func TestSavingsPolicyVersionIsUniquePerStartDate(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	if _, err := db.ExecContext(t.Context(), insertPolicyVersion, 2000, "2026-10-01"); err != nil {
		t.Fatalf("first version: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), insertPolicyVersion, 2500, "2026-10-01"); err == nil {
		t.Fatal("accepted a second version starting on the same day")
	}
}

func TestSavingsEligibilityRuleConstraints(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()

	category := seedCategory(t, db, "Salario", "INCOME")
	income := seedIncome(t, db, category)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO savings_category_rules (category_id, is_eligible) VALUES (?, 0)`, category); err != nil {
		t.Fatalf("excluding a category: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO savings_category_rules (category_id, is_eligible) VALUES (?, 0)`, 9999); err == nil {
		t.Error("accepted a rule for a category that does not exist")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO savings_category_rules (category_id, is_eligible) VALUES (?, 2)`, category); err == nil {
		t.Error("accepted a non-boolean eligibility")
	}

	for name, reason := range map[string]any{"missing": nil, "empty": "", "blank": "   "} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO savings_income_overrides (transaction_id, is_eligible, reason) VALUES (?, 0, ?)`,
			income, reason); err == nil {
			t.Errorf("accepted an override with a %s reason", name)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO savings_income_overrides (transaction_id, is_eligible, reason) VALUES (?, 0, ?)`,
		income, "Es un reembolso"); err != nil {
		t.Errorf("refused a valid override: %v", err)
	}
}

// suggestionFixture is an income and a policy version a suggestion can name.
type suggestionFixture struct {
	db       *sql.DB
	income   int64
	account  int64
	category int64
	policy   int64
}

func newSuggestionFixture(t *testing.T) suggestionFixture {
	t.Helper()
	db := dbtest.New(t)
	category := seedCategory(t, db, "Salario", "INCOME")
	account := seedAccount(t, db, "Mercantil USD")
	income := seedIncome(t, db, category)
	result, err := db.ExecContext(t.Context(), insertPolicyVersion, 2000, "2026-10-01")
	if err != nil {
		t.Fatalf("seeding a policy version: %v", err)
	}
	policy, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading the policy id: %v", err)
	}
	return suggestionFixture{db: db, income: income, account: account, category: category, policy: policy}
}

const insertSuggestion = `
	INSERT INTO savings_suggestions
	    (income_transaction_id, revision, policy_version_id, income_account_id, income_category_id,
	     income_amount_minor, income_currency_code, income_effective_date,
	     income_usd_minor, suggested_usd_minor, fx_method, valued_at, status, status_reason)
	VALUES (?, ?, ?, ?, ?, 80000, 'USD', '2026-10-05', ?, ?, ?, ?, ?, ?)`

const valuedAt = "2026-10-05T12:00:00.000Z"

func (f suggestionFixture) insert(t *testing.T, revision int, usd, suggested, method, valued, status, reason any) error {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), insertSuggestion,
		f.income, revision, f.policy, f.account, f.category, usd, suggested, method, valued, status, reason)
	return err
}

func TestSavingsSuggestionShapePerStatus(t *testing.T) {
	t.Parallel()

	type row struct {
		usd, suggested, method, valued, status, reason any
	}
	valued := func(status, reason any) row { return row{80000, 16000, "IDENTITY", valuedAt, status, reason} }
	unvalued := func(status, reason any) row { return row{nil, nil, nil, nil, status, reason} }

	cases := map[string]struct {
		row      row
		accepted bool
	}{
		"open, valued":                 {valued("OPEN", nil), true},
		"open, unvalued":               {unvalued("OPEN", nil), false},
		"pending, unvalued":            {unvalued("PENDING_VALUATION", nil), true},
		"pending, valued":              {valued("PENDING_VALUATION", nil), false},
		"dismissed with a reason":      {valued("DISMISSED", "No este mes"), true},
		"dismissed without a reason":   {valued("DISMISSED", nil), false},
		"superseded, valued":           {valued("SUPERSEDED", "Ingreso modificado"), true},
		"superseded, never valued":     {unvalued("SUPERSEDED", "Ingreso modificado"), true},
		"superseded without a reason":  {valued("SUPERSEDED", nil), false},
		"invalidated without a reason": {unvalued("INVALIDATED", nil), false},
		"half a valuation":             {row{80000, nil, "IDENTITY", valuedAt, "OPEN", nil}, false},
		"valuation without a time":     {row{80000, 16000, "IDENTITY", nil, "OPEN", nil}, false},
		"unknown method":               {row{80000, 16000, "GUESSED", valuedAt, "OPEN", nil}, false},
		"negative suggestion":          {row{80000, -1, "IDENTITY", valuedAt, "OPEN", nil}, false},
		"stored confirmation status":   {valued("CONFIRMED", nil), false},
		"real amount":                  {row{80000, 160.5, "IDENTITY", valuedAt, "OPEN", nil}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSuggestionFixture(t)
			r := tc.row
			err := f.insert(t, 1, r.usd, r.suggested, r.method, r.valued, r.status, r.reason)
			if tc.accepted && err != nil {
				t.Fatalf("refused a valid suggestion: %v", err)
			}
			if !tc.accepted && err == nil {
				t.Fatal("accepted an invalid suggestion")
			}
		})
	}
}

// At most one LIVE revision per income, any number of closed ones, and one
// number per revision. This is the database's half of "an income is never
// suggested twice" (TD-079).
func TestSavingsSuggestionHasOneLiveRevisionPerIncome(t *testing.T) {
	t.Parallel()
	f := newSuggestionFixture(t)

	if err := f.insert(t, 1, 80000, 16000, "IDENTITY", valuedAt, "OPEN", nil); err != nil {
		t.Fatalf("first revision: %v", err)
	}
	if err := f.insert(t, 2, nil, nil, nil, nil, "PENDING_VALUATION", nil); err == nil {
		t.Fatal("accepted a second live revision for one income")
	}
	if err := f.insert(t, 1, 80000, 16000, "IDENTITY", valuedAt, "SUPERSEDED", "x"); err == nil {
		t.Fatal("accepted a repeated revision number")
	}

	if _, err := f.db.ExecContext(t.Context(),
		`UPDATE savings_suggestions SET status = 'SUPERSEDED', status_reason = 'Ingreso modificado'`); err != nil {
		t.Fatalf("closing the first revision: %v", err)
	}
	if err := f.insert(t, 2, 80000, 16000, "IDENTITY", valuedAt, "OPEN", nil); err != nil {
		t.Fatalf("refused the next revision once the first was closed: %v", err)
	}
	if err := f.insert(t, 3, 80000, 16000, "IDENTITY", valuedAt, "INVALIDATED", "No elegible"); err != nil {
		t.Fatalf("refused a closed revision beside a live one: %v", err)
	}
}

// The suggestion's income cannot disappear from under it.
func TestIncomeWithASuggestionCannotBeDeleted(t *testing.T) {
	t.Parallel()
	f := newSuggestionFixture(t)
	if err := f.insert(t, 1, 80000, 16000, "IDENTITY", valuedAt, "OPEN", nil); err != nil {
		t.Fatalf("suggestion: %v", err)
	}
	if _, err := f.db.ExecContext(t.Context(), `DELETE FROM transactions WHERE id = ?`, f.income); err == nil {
		t.Fatal("deleted an income that has a suggestion")
	}
}

func TestSavingsSuggestionRateLegConstraints(t *testing.T) {
	t.Parallel()

	const insertLeg = `
		INSERT INTO savings_suggestion_rate_legs
		    (suggestion_id, leg, exchange_rate_id, base_currency_code, quote_currency_code,
		     rate, effective_date, source, inverted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	cases := map[string]struct {
		leg      any
		base     any
		rate     any
		source   any
		inverted any
		accepted bool
	}{
		"published precision": {1, "USD", "842.20670000", "BCV_OFFICIAL", 1, true},
		"second leg":          {2, "USD", "842.2067", "MANUAL", 0, true},
		"third leg":           {3, "USD", "842.2067", "MANUAL", 0, false},
		"zero rate":           {1, "USD", "0.000", "MANUAL", 0, false},
		"signed rate":         {1, "USD", "-842", "MANUAL", 0, false},
		"comma":               {1, "USD", "842,2067", "MANUAL", 0, false},
		"same currency":       {1, "VES", "842.2067", "MANUAL", 0, false},
		"unknown source":      {1, "USD", "842.2067", "BCV_MIRROR", 0, false},
		"non-boolean":         {1, "USD", "842.2067", "MANUAL", 2, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSuggestionFixture(t)
			result, err := f.db.ExecContext(t.Context(), insertSuggestion,
				f.income, 1, f.policy, f.account, f.category, 9499, 1900, "INVERTED", valuedAt, "OPEN", nil)
			if err != nil {
				t.Fatalf("suggestion: %v", err)
			}
			suggestion, err := result.LastInsertId()
			if err != nil {
				t.Fatalf("suggestion id: %v", err)
			}
			rate, err := f.db.ExecContext(t.Context(), insertRate,
				"USD", "VES", "2026-10-05", "842.20670000", "BCV_OFFICIAL", 1, fetchedNow)
			if err != nil {
				t.Fatalf("rate: %v", err)
			}
			rateID, err := rate.LastInsertId()
			if err != nil {
				t.Fatalf("rate id: %v", err)
			}

			_, err = f.db.ExecContext(t.Context(), insertLeg,
				suggestion, tc.leg, rateID, tc.base, "VES", tc.rate, "2026-10-05", tc.source, tc.inverted)
			if tc.accepted && err != nil {
				t.Fatalf("refused a valid leg: %v", err)
			}
			if !tc.accepted && err == nil {
				t.Fatal("accepted an invalid leg")
			}
		})
	}
}

// seedIncome returns the id of an INCOME transaction in the given category.
func seedIncome(t *testing.T, db *sql.DB, category int64) int64 {
	t.Helper()
	result, err := db.ExecContext(t.Context(), insertTransaction,
		"INCOME", category, methodBankTransfer, nil, "2026-10-05")
	if err != nil {
		t.Fatalf("seeding an income: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading the income id: %v", err)
	}
	return id
}

const insertOperation = `
	INSERT INTO savings_operations (kind, request_key, suggestion_id, transaction_id, reason, is_exception, effective_date)
	VALUES (?, ?, ?, ?, ?, ?, '2026-10-15')`

// The shape of each operation kind (00016).
func TestSavingsOperationShapePerKind(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		kind, key      any
		suggestion, tx bool
		reason         any
		exception      any
		accepted       bool
	}{
		"manual reserve":               {"RESERVE", "k-0000000000000001", false, false, nil, 0, true},
		"reserve of a suggestion":      {"RESERVE", "k-0000000000000002", true, false, nil, 0, true},
		"exception with a reason":      {"RESERVE", "k-0000000000000003", true, false, "Bono", 1, true},
		"exception without a reason":   {"RESERVE", "k-0000000000000004", true, false, nil, 1, false},
		"exception without suggestion": {"RESERVE", "k-0000000000000005", false, false, "x", 1, false},
		"release with a reason":        {"RELEASE", "k-0000000000000006", false, false, "Emergencia", 0, true},
		"release without a reason":     {"RELEASE", "k-0000000000000007", false, false, nil, 0, false},
		"release naming a suggestion":  {"RELEASE", "k-0000000000000008", true, false, "x", 0, false},
		"spend with its expense":       {"SPEND", "k-0000000000000009", false, true, nil, 0, true},
		"spend without an expense":     {"SPEND", "k-0000000000000010", false, false, nil, 0, false},
		"unknown kind":                 {"WITHDRAW", "k-0000000000000011", false, false, nil, 0, false},
		"short key":                    {"RESERVE", "short", false, false, nil, 0, false},
		"key with spaces":              {"RESERVE", "k 000000000000000012", false, false, nil, 0, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSuggestionFixture(t)
			if err := f.insert(t, 1, 80000, 16000, "IDENTITY", valuedAt, "OPEN", nil); err != nil {
				t.Fatalf("suggestion: %v", err)
			}
			var suggestion, tx any
			if tc.suggestion {
				suggestion = 1
			}
			if tc.tx {
				tx = f.income
			}
			_, err := f.db.ExecContext(t.Context(), insertOperation, tc.kind, tc.key, suggestion, tx, tc.reason, tc.exception)
			if tc.accepted && err != nil {
				t.Fatalf("refused a valid operation: %v", err)
			}
			if !tc.accepted && err == nil {
				t.Fatal("accepted an invalid operation")
			}
		})
	}
}

func TestSavingsOperationKeyIsUniqueAndEntriesAreNonZero(t *testing.T) {
	t.Parallel()
	f := newSuggestionFixture(t)
	ctx := t.Context()
	result, err := f.db.ExecContext(ctx, insertOperation, "RESERVE", "k-aaaaaaaaaaaaaaaa", nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("operation: %v", err)
	}
	op, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("operation id: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, insertOperation, "RESERVE", "k-aaaaaaaaaaaaaaaa", nil, nil, nil, 0); err == nil {
		t.Error("accepted a repeated request key")
	}
	const insertEntry = `INSERT INTO savings_entries (operation_id, account_id, amount_minor) VALUES (?, ?, ?)`
	if _, err := f.db.ExecContext(ctx, insertEntry, op, f.account, 0); err == nil {
		t.Error("accepted a zero entry")
	}
	if _, err := f.db.ExecContext(ctx, insertEntry, op, f.account, 10.5); err == nil {
		t.Error("accepted a REAL entry")
	}
	if _, err := f.db.ExecContext(ctx, insertEntry, op, 9999, 100); err == nil {
		t.Error("accepted an entry for an account that does not exist")
	}
	if _, err := f.db.ExecContext(ctx, insertEntry, op, f.account, -100); err != nil {
		t.Errorf("refused a signed entry: %v", err)
	}
}

const insertGoal = `
	INSERT INTO savings_goals (name, goal_type, target_usd_minor, target_date, distribution_bp, status, closed_at, closing_note)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

const closedAt = "2026-10-15T12:00:00.000Z"

// The shape of a goal (00017).
func TestSavingsGoalConstraints(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		name, kind, target, date, bp, status, closed, note any
		accepted                                           bool
	}{
		"permanent":                    {"Emergencias", "PERMANENT", 100000, nil, 5000, "ACTIVE", nil, nil, true},
		"one-time with a date":         {"Viaje", "ONE_TIME", 80000, "2027-03-01", 5000, "ACTIVE", nil, nil, true},
		"permanent with a date":        {"Emergencias", "PERMANENT", 100000, "2027-03-01", 0, "ACTIVE", nil, nil, false},
		"completed one-time":           {"Viaje", "ONE_TIME", 80000, nil, 0, "COMPLETED", closedAt, "Hecho", true},
		"completed permanent":          {"Emergencias", "PERMANENT", 100000, nil, 0, "COMPLETED", closedAt, "x", false},
		"cancelled without a note":     {"Viaje", "ONE_TIME", 80000, nil, 0, "CANCELLED", closedAt, nil, false},
		"active with a closing":        {"Viaje", "ONE_TIME", 80000, nil, 0, "ACTIVE", closedAt, "x", false},
		"paused":                       {"Viaje", "ONE_TIME", 80000, nil, 0, "PAUSED", nil, nil, true},
		"funded is not a stored state": {"Viaje", "ONE_TIME", 80000, nil, 0, "FUNDED", nil, nil, false},
		"zero target":                  {"Viaje", "ONE_TIME", 0, nil, 0, "ACTIVE", nil, nil, false},
		"real target":                  {"Viaje", "ONE_TIME", 800.5, nil, 0, "ACTIVE", nil, nil, false},
		"share above a hundred":        {"Viaje", "ONE_TIME", 80000, nil, 10001, "ACTIVE", nil, nil, false},
		"blank name":                   {" ", "ONE_TIME", 80000, nil, 0, "ACTIVE", nil, nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := dbtest.New(t)
			_, err := db.ExecContext(t.Context(), insertGoal, tc.name, tc.kind, tc.target, tc.date, tc.bp, tc.status, tc.closed, tc.note)
			if tc.accepted && err != nil {
				t.Fatalf("refused a valid goal: %v", err)
			}
			if !tc.accepted && err == nil {
				t.Fatal("accepted an invalid goal")
			}
		})
	}
}

// An entry may name a goal that exists, or none.
func TestSavingsEntryGoalIsAForeignKey(t *testing.T) {
	t.Parallel()
	f := newSuggestionFixture(t)
	ctx := t.Context()
	result, err := f.db.ExecContext(ctx, insertOperation, "RESERVE", "k-bbbbbbbbbbbbbbbb", nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("operation: %v", err)
	}
	op, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("operation id: %v", err)
	}
	const insertEntry = `INSERT INTO savings_entries (operation_id, account_id, amount_minor, goal_id) VALUES (?, ?, 100, ?)`
	if _, err := f.db.ExecContext(ctx, insertEntry, op, f.account, nil); err != nil {
		t.Errorf("an unallocated entry was refused: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, insertEntry, op, f.account, 9999); err == nil {
		t.Error("an entry naming a goal that does not exist was accepted")
	}
	goal, err := f.db.ExecContext(ctx, insertGoal, "Viaje", "ONE_TIME", 80000, nil, 0, "ACTIVE", nil, nil)
	if err != nil {
		t.Fatalf("goal: %v", err)
	}
	goalID, err := goal.LastInsertId()
	if err != nil {
		t.Fatalf("goal id: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, insertEntry, op, f.account, goalID); err != nil {
		t.Errorf("an entry naming a goal was refused: %v", err)
	}
}
