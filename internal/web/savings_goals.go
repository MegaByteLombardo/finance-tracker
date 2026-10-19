package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// M7.3 screens and actions: goals, their lifecycle, moving money between them.

const (
	goalsPath       = savingsPath + "/goals"
	mobileGoalsPath = mobileSavingsPath + "/metas"
)

// handleSavingsGoals serves GET /savings/goals and GET /mobile/ahorro/metas.
func handleSavingsGoals(logger *slog.Logger, renderer *Renderer, svcs *Services, mobile bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		renderSavingsGoals(w, r, logger, renderer, svcs, mobile, http.StatusOK,
			SavingsGoalsView{Form: SavingsGoalInput{Type: "ONE_TIME"}, FormErrors: map[string]string{}},
			savingsAlert(r.URL.Query()))
	}
}

func renderSavingsGoals(w http.ResponseWriter, r *http.Request, logger *slog.Logger, renderer *Renderer,
	svcs *Services, mobile bool, status int, view SavingsGoalsView, alert *FlashAlert,
) {
	view.ShowClosed = r.URL.Query().Get("closed") == "1"
	view.Return = goalsPath
	if mobile {
		view.Return = mobileGoalsPath
		view.Navigation = MobileNavView{ActiveTab: "inicio"}
	}
	goals, err := svcs.Savings.ListGoals(r.Context(), view.ShowClosed)
	if err != nil {
		logger.Error("listing savings goals", slog.Any("error", err))
		view.Error = "No se pudieron cargar las metas."
	} else {
		view.Available = true
		view.Goals = goals
	}
	view.Alert = alert
	renderSavingsWithStatus(w, r, logger, renderer, mobile, status, "savings/goals.html", "mobile/savings_goals.html",
		PageData{Title: "Metas de ahorro", ActiveNav: NavSavings, Alert: alert, Data: view})
}

// handleCreateGoal serves POST /savings/goals.
func handleCreateGoal(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, goalsPath)
		in := goalInputFromForm(r)
		id, err := svcs.Savings.CreateGoal(r.Context(), in)
		if err != nil {
			logger.Warn("creating a savings goal", slog.Any("error", err))
			view := SavingsGoalsView{Form: in, FormErrors: map[string]string{"general": goalErrorMessage(err)}}
			renderSavingsGoals(w, r, logger, renderer, svcs, isMobileReturn(back), http.StatusUnprocessableEntity, view, nil)
			return
		}
		target := fmt.Sprintf("%s/%d", goalsPath, id)
		if isMobileReturn(back) {
			target = fmt.Sprintf("%s/%d", mobileGoalsPath, id)
		}
		redirectTo(w, r, withQuery(target, "msg", "goal-created"))
	}
}

// handleSavingsGoal serves GET /savings/goals/{id} and its mobile twin.
func handleSavingsGoal(logger *slog.Logger, renderer *Renderer, svcs *Services, mobile bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		goal, err := svcs.Savings.GetGoal(r.Context(), id)
		if errors.Is(err, ErrSavingsGoalNotFound) {
			http.NotFound(w, r)
			return
		}
		view := SavingsGoalView{Goal: goal, Return: fmt.Sprintf("%s/%d", goalsPath, id)}
		if mobile {
			view.Return = fmt.Sprintf("%s/%d", mobileGoalsPath, id)
			view.Navigation = MobileNavView{ActiveTab: "inicio"}
		}
		if err == nil {
			view.Fund, err = svcs.Savings.GetFund(r.Context())
		}
		var others []SavingsGoal
		if err == nil {
			others, err = svcs.Savings.ListGoals(r.Context(), false)
		}
		for _, key := range []*string{&view.ContributeKey, &view.WithdrawKey, &view.MoveKey, &view.CloseKey} {
			if err == nil {
				*key, err = newRequestKey()
			}
		}
		if err != nil {
			logger.Error("loading a savings goal", slog.Any("error", err))
			http.Error(w, "No se pudo cargar la meta.", http.StatusInternalServerError)
			return
		}
		for _, g := range others {
			if g.ID != id && g.AcceptsMoney {
				view.OtherGoals = append(view.OtherGoals, g)
			}
		}
		view.Alert = savingsAlert(r.URL.Query())
		renderSavings(w, r, logger, renderer, mobile, "savings/goal.html", "mobile/savings_goal.html",
			PageData{Title: goal.Name, ActiveNav: NavSavings, Alert: view.Alert, Data: view})
	}
}

// handleUpdateGoal serves POST /savings/goals/{id}.
func handleUpdateGoal(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, fmt.Sprintf("%s/%d", goalsPath, id))
		if err := svcs.Savings.UpdateGoal(r.Context(), id, goalInputFromForm(r)); err != nil {
			logger.Warn("updating a savings goal", slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", goalErrorKey(err)))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", "goal-saved"))
	}
}

// handleGoalAction serves POST /savings/goals/{id}/{pause,resume,complete,cancel}.
func handleGoalAction(logger *slog.Logger, svcs *Services, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, fmt.Sprintf("%s/%d", goalsPath, id))
		closing := SavingsCloseGoalInput{RequestKey: r.FormValue("request_key"), Note: r.FormValue("note")}
		var (
			err  error
			done string
		)
		switch action {
		case "pause":
			err, done = svcs.Savings.PauseGoal(r.Context(), id), "goal-paused"
		case "resume":
			err, done = svcs.Savings.ResumeGoal(r.Context(), id), "goal-resumed"
		case "complete":
			err, done = svcs.Savings.CompleteGoal(r.Context(), id, closing), "goal-completed"
		case "cancel":
			err, done = svcs.Savings.CancelGoal(r.Context(), id, closing), "goal-cancelled"
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			logger.Warn("acting on a savings goal", slog.String("action", action), slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", goalErrorKey(err)))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", done))
	}
}

// handleReallocate serves POST /savings/reallocate. An empty goal field is the
// account's unallocated part.
func handleReallocate(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, goalsPath)
		accountID, ok := formID(r, "account_id")
		if !ok {
			redirectTo(w, r, withQuery(back, "err", "account"))
			return
		}
		in := SavingsReallocateInput{
			RequestKey: r.FormValue("request_key"),
			AccountID:  accountID,
			Amount:     strings.TrimSpace(r.FormValue("amount")),
			Reason:     r.FormValue("reason"),
		}
		if id, ok := formID(r, "from_goal_id"); ok {
			in.FromGoalID = &id
		}
		if id, ok := formID(r, "to_goal_id"); ok {
			in.ToGoalID = &id
		}
		if _, err := svcs.Savings.Reallocate(r.Context(), in); err != nil {
			logger.Warn("reallocating savings", slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", goalErrorKey(err)))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", "reallocated"))
	}
}

func goalInputFromForm(r *http.Request) SavingsGoalInput {
	return SavingsGoalInput{
		Name:         strings.TrimSpace(r.FormValue("name")),
		Type:         strings.TrimSpace(r.FormValue("type")),
		Target:       strings.TrimSpace(r.FormValue("target")),
		TargetDate:   strings.TrimSpace(r.FormValue("target_date")),
		Distribution: strings.TrimSpace(r.FormValue("distribution")),
	}
}

// reserveLinesFromForm reads a distributed confirmation: parallel line_goal and
// line_amount fields, an empty goal being the unallocated part. Lines left
// empty or at zero are skipped -- the user removed them.
func reserveLinesFromForm(r *http.Request) []SavingsReserveLine {
	goals, amounts := r.Form["line_goal"], r.Form["line_amount"]
	lines := make([]SavingsReserveLine, 0, len(amounts))
	for i, raw := range amounts {
		amount := strings.TrimSpace(raw)
		if strings.Trim(amount, "0.") == "" {
			continue
		}
		line := SavingsReserveLine{Amount: amount}
		if i < len(goals) {
			if id, ok := parsePositiveID(goals[i]); ok {
				line.GoalID = &id
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func goalErrorKey(err error) string {
	switch {
	case errors.Is(err, ErrSavingsGoalNotAccepting):
		return "goal-not-accepting"
	case errors.Is(err, ErrSavingsDistributionExceeded):
		return "distribution-exceeded"
	case errors.Is(err, ErrSavingsInsufficientReserved):
		return "insufficient-reserved"
	case errors.Is(err, ErrSavingsInvalidState):
		return "goal-invalid-state"
	case errors.Is(err, ErrSavingsInvalidRequestKey):
		return "request-key"
	case errors.Is(err, ErrInvalidInput):
		return "goal-invalid"
	default:
		return fundErrorKey(err)
	}
}

func goalErrorMessage(err error) string {
	switch {
	case errors.Is(err, ErrSavingsDistributionExceeded):
		return "Los porcentajes de las metas no pueden sumar más de 100%."
	case errors.Is(err, ErrInvalidInput):
		return "Revisa el nombre, el objetivo en USD, la fecha (solo para metas puntuales) y el porcentaje."
	default:
		return "No se pudo crear la meta."
	}
}

func parsePositiveID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
