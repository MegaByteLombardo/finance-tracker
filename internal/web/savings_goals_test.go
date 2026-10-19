package web

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func sampleGoals() []SavingsGoal {
	date := savingsDay("2027-01-31")
	return []SavingsGoal{
		{ID: 1, Name: "Emergencias", Type: "PERMANENT", TypeLabel: "Permanente", TargetFormatted: "1000.00",
			OriginalTargetFormatted: "1000.00", TargetAmount: "1000.00", DistributionFormatted: "50.00", HasShare: true,
			Status: "ACTIVE", StatusLabel: "Activa", FundingLabel: "En progreso", FundingBadge: "badge-info",
			ReservedFormatted: "700.00", RemainingFormatted: "300.00", ProgressFormatted: "70.00", ProgressWidth: 70,
			AcceptsMoney: true, HasMoney: true, CanEdit: true, CanPause: true, CanCancel: true,
			Accounts:        []SavingsGoalAccount{{AccountID: 1, AccountName: "Mercantil USD", ReservedFormatted: "700.00"}},
			TargetRevisions: []SavingsGoalTarget{{TargetFormatted: "1000.00"}}},
		{ID: 2, Name: "Viaje", Type: "ONE_TIME", TypeLabel: "Puntual", IsOneTime: true, TargetFormatted: "1000.00",
			OriginalTargetFormatted: "800.00", TargetChanged: true, TargetAmount: "1000.00", TargetDate: &date,
			Status: "ACTIVE", StatusLabel: "Activa", FundingLabel: "Sin fondos", FundingBadge: "badge-neutral",
			ReservedFormatted: "0.00", RemainingFormatted: "1000.00", ProgressFormatted: "0.00",
			SuggestedContributionFormatted: "333.33",
			AcceptsMoney:                   true, CanEdit: true, CanPause: true, CanComplete: true, CanCancel: true},
	}
}

func TestTheGoalsPageListsGoalsAndCreatesOne(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.Goals = sampleGoals()
	router := savingsRouter(t, f)

	for _, path := range []string{"/savings/goals", "/mobile/ahorro/metas"} {
		page := html.UnescapeString(body(t, get(t, router, path)))
		for _, want := range []string{"Emergencias", "Viaje", "700.00", "En progreso", "333.33", "width: 70%", `action="/savings/goals"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
	}

	rec := post(t, router, "/savings/goals", url.Values{
		"name": {" Laptop "}, "type": {"ONE_TIME"}, "target": {"1200"}, "target_date": {"2027-06-01"}, "distribution": {"20"},
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/savings/goals/101?msg=goal-created" {
		t.Fatalf("%d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := f.CreatedGoals[0]; got.Name != "Laptop" || got.Type != "ONE_TIME" || got.Target != "1200" || got.Distribution != "20" {
		t.Errorf("CreateGoal got %+v", got)
	}

	f.ActionErr = ErrSavingsDistributionExceeded
	rec = post(t, router, "/savings/goals", url.Values{"name": {"X"}, "type": {"PERMANENT"}, "target": {"1"}, "distribution": {"90"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(body(t, rec)), "no pueden sumar más de 100%") {
		t.Errorf("a share past 100%% answered %d", rec.Code)
	}
}

func TestTheGoalPageOffersItsActions(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.Goals = sampleGoals()
	f.FundResult = sampleFund()
	router := savingsRouter(t, f)

	page := html.UnescapeString(body(t, get(t, router, "/savings/goals/1")))
	for _, want := range []string{"Emergencias", "Mercantil USD", `name="goal_id" value="1"`, `action="/savings/release"`,
		`action="/savings/reallocate"`, `<option value="2">Viaje</option>`, "/savings/goals/1/pause", "/savings/goals/1/cancel"} {
		if !strings.Contains(page, want) {
			t.Errorf("the permanent goal page does not show %q", want)
		}
	}
	if strings.Contains(page, "/savings/goals/1/complete") {
		t.Error("a permanent goal offers completion")
	}
	if n := len(requestKeyField.FindAllString(page, -1)); n < 4 {
		t.Errorf("the goal page carries %d request keys, want one per form", n)
	}

	page = html.UnescapeString(body(t, get(t, router, "/savings/goals/2")))
	for _, want := range []string{"/savings/goals/2/complete", "Presupuesto original: <strong>800.00 USD", "333.33"} {
		if !strings.Contains(page, want) {
			t.Errorf("the one-time goal page does not show %q", want)
		}
	}
	if rec := get(t, router, "/mobile/ahorro/metas/2"); rec.Code != http.StatusOK {
		t.Errorf("mobile goal page: %d", rec.Code)
	}
	if rec := get(t, router, "/savings/goals/99"); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown goal answered %d", rec.Code)
	}
}

func TestGoalActionsAndReallocationPostWhatTheFormSays(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.Goals = sampleGoals()
	router := savingsRouter(t, f)
	const k = "0123456789abcdef0123456789abcdef"

	for path, want := range map[string]string{
		"/savings/goals/1/pause":    "/savings/goals/1?msg=goal-paused",
		"/savings/goals/1/resume":   "/savings/goals/1?msg=goal-resumed",
		"/savings/goals/2/complete": "/savings/goals/2?msg=goal-completed",
		"/savings/goals/2/cancel":   "/savings/goals/2?msg=goal-cancelled",
	} {
		rec := post(t, router, path, url.Values{"request_key": {k}, "note": {"Hecho"}})
		if rec.Header().Get("Location") != want {
			t.Errorf("%s redirected to %q", path, rec.Header().Get("Location"))
		}
	}
	if len(f.GoalActions) != 4 {
		t.Errorf("actions = %v", f.GoalActions)
	}

	rec := post(t, router, "/savings/goals/2", url.Values{"name": {"Viaje"}, "target": {"1200"}, "target_date": {"2027-02-01"}})
	if rec.Header().Get("Location") != "/savings/goals/2?msg=goal-saved" || f.UpdatedGoals[2].Target != "1200" {
		t.Errorf("update: %q %+v", rec.Header().Get("Location"), f.UpdatedGoals)
	}

	rec = post(t, router, "/savings/reallocate", url.Values{"request_key": {k}, "account_id": {"1"}, "from_goal_id": {"1"},
		"to_goal_id": {""}, "amount": {"50"}, "return": {"/savings/goals/1"}})
	if rec.Header().Get("Location") != "/savings/goals/1?msg=reallocated" {
		t.Errorf("reallocate redirected to %q", rec.Header().Get("Location"))
	}
	got := f.Reallocations[0]
	if got.FromGoalID == nil || *got.FromGoalID != 1 || got.ToGoalID != nil || got.Amount != "50" {
		t.Errorf("Reallocate got %+v (an empty destination is the unallocated part)", got)
	}

	f.ActionErr = ErrSavingsGoalNotAccepting
	rec = post(t, router, "/savings/reallocate", url.Values{"request_key": {k}, "account_id": {"1"}, "to_goal_id": {"2"}, "amount": {"1"}})
	if rec.Header().Get("Location") != "/savings/goals?err=goal-not-accepting" {
		t.Errorf("a paused destination redirected to %q", rec.Header().Get("Location"))
	}
}

// A confirmation with goals shows the suggested split, and the lines posted
// become the reservation's lines -- empty or zero lines dropped.
func TestAConfirmationIsDistributedAmongGoals(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.FundResult = sampleFund()
	one, two := int64(1), int64(2)
	f.Distribution = []SavingsDistributionLine{
		{GoalID: &one, GoalName: "Emergencias", Amount: "80.00"},
		{GoalID: &two, GoalName: "Viaje", Amount: "80.00", Capped: true},
		{Amount: "0.00"},
	}
	f.Details[11] = SavingsIncomeDetail{TransactionID: 11, Eligible: true, Revisions: []SavingsSuggestion{{
		IncomeTransactionID: 11, Status: "OPEN", Live: true, Valued: true, CanConfirm: true,
		SuggestedUSDFormatted: "160.00", RemainingAmount: "160.00", RemainingFormatted: "160.00",
	}}}
	router := savingsRouter(t, f)

	for _, path := range []string{"/savings/incomes/11", "/mobile/ahorro/ingresos/11"} {
		page := html.UnescapeString(body(t, get(t, router, path)))
		for _, want := range []string{`name="line_goal" value="1"`, `name="line_goal" value="2"`, `name="line_goal" value=""`, `value="80.00"`, "Sin asignar"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
		if strings.Contains(page, `id="confirm_amount"`) || strings.Contains(page, `id="m_confirm_amount"`) {
			t.Errorf("%s shows the single amount beside the distribution", path)
		}
	}

	rec := post(t, router, "/savings/reserve", url.Values{
		"request_key": {"0123456789abcdef0123456789abcdef"}, "income_id": {"11"}, "account_id": {"1"},
		"line_goal": {"1", "2", ""}, "line_amount": {"100.00", "0", " 60 "},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("%d", rec.Code)
	}
	lines := f.Reserved[0].Lines
	if len(lines) != 2 || lines[0].GoalID == nil || *lines[0].GoalID != 1 || lines[0].Amount != "100.00" ||
		lines[1].GoalID != nil || lines[1].Amount != "60" {
		t.Errorf("lines = %+v", lines)
	}
}
