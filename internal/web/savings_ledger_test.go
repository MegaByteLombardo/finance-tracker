package web

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func sampleImpact() SavingsAccountImpact {
	travel := int64(2)
	return SavingsAccountImpact{
		Applies: true, AccountName: "Mercantil USD", BalanceFormatted: "1000.00", ReservedFormatted: "800.00",
		FreeFormatted: "200.00", ShortfallFormatted: "100.00", HasShortfall: true, HasReservations: true,
		Slices: []SavingsSlice{{GoalID: &travel, GoalName: "Viaje", ReservedFormatted: "500.00"}, {ReservedFormatted: "300.00"}},
	}
}

// The impact partial shows what an expense does to the account's reservations,
// and nothing at all where nothing applies.
func TestTheSavingsImpactPartial(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.Impact = sampleImpact()
	router := savingsRouter(t, f)

	page := html.UnescapeString(body(t, get(t, router, "/savings/impact?type=EXPENSE&account_id=1&amount=300")))
	for _, want := range []string{"supera lo libre en <strong>100.00 USD", `name="use_goal" value="2"`, `name="use_goal" value=""`,
		`name="savings_purpose" value="cover" checked`, `name="savings_reason"`, "Viaje", "Sin asignar", "<details class=\"savings-impact-plan\" open"} {
		if !strings.Contains(page, want) {
			t.Errorf("the expense impact does not show %q", want)
		}
	}
	if !requestKeyField.MatchString(strings.ReplaceAll(page, "savings_key", "request_key")) {
		t.Error("the impact carries no idempotency key")
	}
	if f.ImpactCalls[0] != "1:300" {
		t.Errorf("AccountImpact got %v", f.ImpactCalls)
	}

	page = html.UnescapeString(body(t, get(t, router, "/savings/impact?source_account_id=1&destination_account_id=3&amount=900")))
	if !strings.Contains(page, `name="move_goal" value="2"`) || strings.Contains(page, "use_amount") {
		t.Error("the transfer impact does not offer to move the backing")
	}

	for _, q := range []string{"type=INCOME&account_id=1&amount=5", "type=FX_EXCHANGE&source_account_id=1&source_amount=5", "type=EXPENSE"} {
		if strings.Contains(body(t, get(t, router, "/savings/impact?"+q)), "savings-impact") {
			t.Errorf("%s rendered a savings impact", q)
		}
	}
	f.Impact = SavingsAccountImpact{}
	if strings.Contains(body(t, get(t, router, "/savings/impact?type=EXPENSE&account_id=5&amount=5")), "savings-impact") {
		t.Error("an account outside the fund rendered a savings impact")
	}
}

// The expense form passes the savings plan the user filled in, and nothing when
// they filled in nothing.
func TestTheExpenseFormPassesItsSavingsPlan(t *testing.T) {
	t.Parallel()
	fs := NewFakeServices()
	if err := fs.SeedSampleData(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fs.ExpensePlans = nil // the seed records expenses of its own
	router := testRouter(t, fs)
	accounts, _ := fs.ListAccounts(t.Context(), false)
	cats, _ := fs.ListCategories(t.Context(), ListCategoriesInput{})
	var expenseCat int64
	for _, c := range cats {
		if c.Type == CategoryTypeExpense {
			expenseCat = c.ID
		}
	}
	base := url.Values{
		"type": {"EXPENSE"}, "account_id": {itoa64(accounts[0].ID)}, "category_id": {itoa64(expenseCat)},
		"amount": {"300"}, "payment_method_id": {"1"}, "effective_date": {"2026-10-10"},
	}

	post(t, router, "/transactions", base)
	withPlan := cloneValues(base)
	withPlan["savings_key"] = []string{"0123456789abcdef0123456789abcdef"}
	withPlan["savings_purpose"] = []string{"cover"}
	withPlan["savings_reason"] = []string{"Reparación"}
	withPlan["use_goal"] = []string{"2", ""}
	withPlan["use_amount"] = []string{"", "100"}
	rec := post(t, router, "/transactions", withPlan)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	withGoal := cloneValues(withPlan)
	withGoal["savings_purpose"] = []string{"goal"}
	withGoal["use_amount"] = []string{"300", ""}
	post(t, router, "/transactions", withGoal)

	if len(fs.ExpensePlans) != 3 || fs.ExpensePlans[0] != nil {
		t.Fatalf("plans = %+v", fs.ExpensePlans)
	}
	cover := fs.ExpensePlans[1]
	if cover.RequestKey == "" || cover.Reason != "Reparación" || len(cover.Release) != 1 || cover.Release[0].GoalID != nil ||
		cover.Release[0].Amount != "100" || len(cover.Spend) != 0 {
		t.Errorf("cover plan = %+v", cover)
	}
	spend := fs.ExpensePlans[2]
	if len(spend.Spend) != 1 || spend.Spend[0].GoalID == nil || *spend.Spend[0].GoalID != 2 || len(spend.Release) != 0 {
		t.Errorf("goal plan = %+v", spend)
	}
}

func TestTheIssuesPageAndAcknowledgement(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	income := int64(11)
	f.Issues = []SavingsIssue{
		{Key: "overallocated:account:1:10000", Kind: "OVERALLOCATED", KindLabel: "Sobreasignación", AccountName: "Mercantil USD",
			AmountFormatted: "100.00", Detail: "Mercantil USD tiene 700.00 USD y hay 800.00 USD reservados en ella."},
		{Key: "overconfirmed:income:11:6000", Kind: "OVER_CONFIRMED", KindLabel: "Confirmado de más", IncomeTransactionID: &income,
			AmountFormatted: "60.00", Detail: "x", Acknowledged: true, AckNote: "Lo dejo así"},
	}
	router := savingsRouter(t, f)
	for _, path := range []string{"/savings/issues", "/mobile/ahorro/incidencias"} {
		page := html.UnescapeString(body(t, get(t, router, path)))
		for _, want := range []string{"Sobreasignación", "700.00 USD", "Confirmado de más", "reconocida", "Lo dejo así",
			`name="key" value="overallocated:account:1:10000"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
		if strings.Contains(page, `value="overconfirmed:income:11:6000"`) {
			t.Errorf("%s offers to acknowledge an acknowledged issue", path)
		}
	}
	rec := post(t, router, "/savings/issues/acknowledge", url.Values{"key": {"overallocated:account:1:10000"}, "note": {"Luego"}})
	if rec.Header().Get("Location") != "/savings/issues?msg=acknowledged" || f.Acknowledged["overallocated:account:1:10000"] != "Luego" {
		t.Errorf("acknowledge: %q %v", rec.Header().Get("Location"), f.Acknowledged)
	}
}

// The dashboards show the savings block when savings are in use, and nothing
// otherwise.
func TestTheDashboardsShowTheSavingsBlock(t *testing.T) {
	t.Parallel()
	f := NewFakeSavings()
	router := savingsRouter(t, f)
	if strings.Contains(body(t, get(t, router, "/")), "dash-savings") {
		t.Error("the dashboard shows a savings block with nothing in use")
	}

	f = sampleSavings()
	f.OverviewResult.Fund = sampleFund()
	f.OverviewResult.IssueCount = 2
	router = savingsRouter(t, f)
	page := html.UnescapeString(body(t, get(t, router, "/")))
	for _, want := range []string{"dash-savings", "Fondo reservado", "950.00", "Sobreasignado: 50.00", "2 incidencias", "Confirmado este mes"} {
		if !strings.Contains(page, want) {
			t.Errorf("the dashboard does not show %q", want)
		}
	}
	mobile := html.UnescapeString(body(t, get(t, router, "/mobile")))
	if !strings.Contains(mobile, "Fondo reservado") || !strings.Contains(mobile, "950.00 USD") {
		t.Error("the mobile home does not show the savings summary")
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}
