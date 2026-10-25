package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// M7.4: the savings side of the movement forms, and reconciliation.

// handleSavingsImpact serves GET /savings/impact, the partial the expense,
// transfer and card payment forms load beside their amount, desktop and mobile.
// It reads the form's own fields: an expense names account_id, a transfer
// source_account_id, and a card payment (type=CARD_PAYMENT, D-12) its
// source_account_id with the amount that left it -- source_amount when the
// payment is an exchange.
// Anything else -- an income, an exchange, an account outside the fund -- gets
// an empty container: nothing is shown where nothing applies.
func handleSavingsImpact(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		view := SavingsImpactView{}
		var accountID int64
		amount := strings.TrimSpace(q.Get("amount"))
		switch txType := strings.ToUpper(strings.TrimSpace(q.Get("type"))); {
		case txType == string(TransactionTypeExpense):
			view.Mode, accountID = "expense", parseQueryID(q.Get("account_id"))
		case txType == "CARD_PAYMENT":
			view.Mode, accountID = "payment", parseQueryID(q.Get("source_account_id"))
			if sourceAmount := strings.TrimSpace(q.Get("source_amount")); sourceAmount != "" {
				amount = sourceAmount
			}
		case (txType == "" || txType == string(TransactionTypeTransfer)) && q.Has("source_account_id") && !q.Has("source_amount"):
			view.Mode, accountID = "transfer", parseQueryID(q.Get("source_account_id"))
		}
		if accountID > 0 {
			impact, err := svcs.Savings.AccountImpact(r.Context(), accountID, amount)
			if err != nil {
				// The form must stay usable: the savings hint is a convenience.
				logger.Warn("computing a savings impact", slog.Any("error", err))
			} else {
				view.Impact = impact
			}
		}
		if view.Impact.Applies && view.Impact.HasReservations {
			key, err := newRequestKey()
			if err != nil {
				logger.Error("generating a request key", slog.Any("error", err))
				view.Impact = SavingsAccountImpact{}
			}
			view.Key = key
		}
		if err := renderer.RenderPartial(w, r, http.StatusOK, "savings/impact.html", "savings_impact", view); err != nil {
			logger.Error("rendering the savings impact", slog.Any("error", err))
		}
	}
}

// expensePlanFromForm reads the savings part of an expense form, or nil when
// the user asked for nothing. use_goal / use_amount are parallel; an empty goal
// is the unallocated part. savings_purpose says whether the expense was what
// the money was for ("goal": SPEND) or the money covers a shortfall ("cover":
// RELEASE, with savings_reason).
func expensePlanFromForm(r *http.Request) *SavingsExpensePlan {
	uses := usesFromForm(r, "use_goal", "use_amount")
	if len(uses) == 0 {
		return nil
	}
	plan := &SavingsExpensePlan{RequestKey: r.FormValue("savings_key"), Reason: r.FormValue("savings_reason")}
	if r.FormValue("savings_purpose") == "goal" {
		plan.Spend = uses
	} else {
		plan.Release = uses
	}
	return plan
}

// transferPlanFromForm reads the reserved money a transfer moves with it.
func transferPlanFromForm(r *http.Request) *SavingsTransferPlan {
	uses := usesFromForm(r, "move_goal", "move_amount")
	if len(uses) == 0 {
		return nil
	}
	return &SavingsTransferPlan{RequestKey: r.FormValue("savings_key"), Move: uses}
}

func usesFromForm(r *http.Request, goalField, amountField string) []SavingsUse {
	goals, amounts := r.Form[goalField], r.Form[amountField]
	uses := make([]SavingsUse, 0, len(amounts))
	for i, raw := range amounts {
		amount := strings.TrimSpace(raw)
		if strings.Trim(amount, "0.") == "" {
			continue
		}
		use := SavingsUse{Amount: amount}
		if i < len(goals) {
			if id, ok := parsePositiveID(goals[i]); ok {
				use.GoalID = &id
			}
		}
		uses = append(uses, use)
	}
	return uses
}

// savingsMovementError is the form message for a savings plan the backend
// refused, or "" when the error is not about savings.
func savingsMovementError(err error) string {
	switch {
	case errors.Is(err, ErrSavingsInsufficientReserved):
		return "Pediste usar más ahorro del que hay reservado en esa meta o en esa cuenta."
	case errors.Is(err, ErrSavingsNotLinkable):
		return "Ese ahorro no puede usarse en este movimiento: los ahorros en USD solo cubren gastos o transferencias de cuentas en USD."
	case errors.Is(err, ErrSavingsInvalidRequestKey):
		return "El formulario caducó. Vuelve a cargarlo."
	case errors.Is(err, ErrSavingsReasonRequired):
		return "Para liberar ahorro por un gasto extraordinario indica el motivo."
	case errors.Is(err, ErrSavingsExceedsMovement):
		return "El ahorro usado no puede superar el importe del movimiento."
	default:
		return ""
	}
}

// handleSavingsIssues serves GET /savings/issues and its mobile twin.
func handleSavingsIssues(logger *slog.Logger, renderer *Renderer, svcs *Services, mobile bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view := SavingsIssuesView{Return: savingsPath + "/issues"}
		if mobile {
			view.Return = mobileSavingsPath + "/incidencias"
			view.Navigation = MobileNavView{ActiveTab: "inicio"}
		}
		issues, err := svcs.Savings.ListIssues(r.Context())
		if err != nil {
			logger.Error("listing savings issues", slog.Any("error", err))
			view.Error = "No se pudieron calcular las incidencias."
		} else {
			view.Available, view.Issues = true, issues
		}
		view.Alert = savingsAlert(r.URL.Query())
		renderSavings(w, r, logger, renderer, mobile, "savings/issues.html", "mobile/savings_issues.html",
			PageData{Title: "Reconciliación", ActiveNav: NavSavings, Alert: view.Alert, Data: view})
	}
}

// handleAcknowledgeIssue serves POST /savings/issues/acknowledge.
func handleAcknowledgeIssue(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, savingsPath+"/issues")
		if err := svcs.Savings.AcknowledgeIssue(r.Context(), r.FormValue("key"), r.FormValue("note")); err != nil {
			logger.Warn("acknowledging a savings issue", slog.Any("error", err))
			key := "invalid-state"
			if errors.Is(err, ErrInvalidInput) {
				key = "reason-required"
			}
			redirectTo(w, r, withQuery(back, "err", key))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", "acknowledged"))
	}
}

func parseQueryID(raw string) int64 {
	id, ok := parsePositiveID(raw)
	if !ok {
		return 0
	}
	return id
}
