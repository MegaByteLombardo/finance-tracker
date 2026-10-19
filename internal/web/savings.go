package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// M7.1 screens: the savings overview with its suggestions, the policy, the sync
// preview and one income's savings history. Desktop and mobile render the same
// data; every figure and every allowed action comes from the savings service.
// A failure to read degrades to a message on the page, never a 500 (the
// provider and the rates are never a reason a screen will not open).

// SavingsView is the overview page.
type SavingsView struct {
	Available bool
	Error     string

	Overview    SavingsOverview
	Suggestions []SavingsSuggestion
	// Goals are the open goals: the distribution of the fund among them.
	Goals []SavingsGoal
	// History is the fund month by month (M7.4).
	History []SavingsFundMonth

	// Return is where the actions on this page come back to.
	Return string

	// Alert repeats PageData.Alert for the mobile layout, which renders only
	// the page's own data.
	Alert *FlashAlert

	Navigation MobileNavView
}

// Help explains a savings term.
func (SavingsView) Help(key string) RecapHelp { return SavingsHelp(key) }

// SavingsPolicyView is the policy page.
type SavingsPolicyView struct {
	Policy        SavingsPolicy
	Categories    []SavingsCategoryEligibility
	Form          SavingsPolicyForm
	UnsyncedCount int
	Return        string
	Alert         *FlashAlert
	Navigation    MobileNavView
}

// Help explains a savings term.
func (SavingsPolicyView) Help(key string) RecapHelp { return SavingsHelp(key) }

// SavingsPolicyForm is the new-rate form's state.
type SavingsPolicyForm struct {
	Rate          string
	EffectiveFrom string
	Errors        map[string]string
}

// SavingsSyncView is the sync preview.
type SavingsSyncView struct {
	Plan       SavingsSyncPlan
	Return     string
	Alert      *FlashAlert
	Navigation MobileNavView
}

// Help explains a savings term.
func (SavingsSyncView) Help(key string) RecapHelp { return SavingsHelp(key) }

// SavingsIncomeView is one income's savings history.
type SavingsIncomeView struct {
	Detail SavingsIncomeDetail

	// Live is the income's live revision, when it has one; Fund and
	// ConfirmKey back the confirmation form (M7.2).
	Live       *SavingsSuggestion
	Fund       SavingsFund
	ConfirmKey string
	// Distribution is the suggested split of what remains among the active
	// goals (M7.3); empty when no goal takes a share.
	Distribution []SavingsDistributionLine

	Return     string
	Alert      *FlashAlert
	Navigation MobileNavView
}

// Help explains a savings term.
func (SavingsIncomeView) Help(key string) RecapHelp { return SavingsHelp(key) }

const (
	savingsPath       = "/savings"
	mobileSavingsPath = "/mobile/ahorro"
)

// handleSavings serves GET /savings and GET /mobile/ahorro.
func handleSavings(logger *slog.Logger, renderer *Renderer, svcs *Services, mobile bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view := SavingsView{Return: savingsPath}
		if mobile {
			view.Return = mobileSavingsPath
			view.Navigation = MobileNavView{ActiveTab: "inicio"}
		}
		overview, err := svcs.Savings.Overview(r.Context())
		if err == nil {
			view.Suggestions, err = svcs.Savings.ListSuggestions(r.Context())
		}
		if err == nil {
			view.Goals, err = svcs.Savings.ListGoals(r.Context(), false)
		}
		if err == nil {
			view.History, err = svcs.Savings.FundHistory(r.Context())
		}
		if err != nil {
			logger.Error("loading the savings overview", slog.Any("error", err))
			view.Error = "No se pudo cargar el ahorro."
		} else {
			view.Available = true
			view.Overview = overview
		}
		view.Alert = savingsAlert(r.URL.Query())
		renderSavings(w, r, logger, renderer, mobile, "savings/index.html", "mobile/savings.html",
			PageData{Title: "Ahorro", ActiveNav: NavSavings, Alert: view.Alert, Data: view})
	}
}

// handleSavingsPolicy serves GET /savings/policy and GET /mobile/ahorro/politica.
func handleSavingsPolicy(logger *slog.Logger, renderer *Renderer, svcs *Services, mobile bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		renderSavingsPolicy(w, r, logger, renderer, svcs, mobile, http.StatusOK,
			SavingsPolicyForm{EffectiveFrom: todayForForms(), Errors: map[string]string{}},
			savingsAlert(r.URL.Query()))
	}
}

func renderSavingsPolicy(w http.ResponseWriter, r *http.Request, logger *slog.Logger, renderer *Renderer,
	svcs *Services, mobile bool, status int, form SavingsPolicyForm, alert *FlashAlert,
) {
	ctx := r.Context()
	view := SavingsPolicyView{Form: form, Return: savingsPath + "/policy"}
	if mobile {
		view.Return = mobileSavingsPath + "/politica"
		view.Navigation = MobileNavView{ActiveTab: "inicio"}
	}
	policy, err := svcs.Savings.GetPolicy(ctx)
	if err == nil {
		view.Categories, err = svcs.Savings.ListCategoryEligibility(ctx)
	}
	var plan SavingsSyncPlan
	if err == nil {
		plan, err = svcs.Savings.PlanSync(ctx)
	}
	if err != nil {
		logger.Error("loading the savings policy", slog.Any("error", err))
		alert = &FlashAlert{Type: "danger", Message: "No se pudo cargar la política de ahorro."}
	}
	view.Policy = policy
	view.UnsyncedCount = len(plan.Items)
	view.Alert = alert
	if view.Form.Rate == "" && policy.Current != nil {
		view.Form.Rate = policy.Current.RateFormatted
	}
	renderSavingsWithStatus(w, r, logger, renderer, mobile, status, "savings/policy.html", "mobile/savings_policy.html",
		PageData{Title: "Política de ahorro", ActiveNav: NavSavings, Alert: alert, Data: view})
}

// handleSetSavingsPolicy serves POST /savings/policy.
func handleSetSavingsPolicy(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, savingsPath+"/policy")
		form := SavingsPolicyForm{
			Rate:          strings.TrimSpace(r.FormValue("rate")),
			EffectiveFrom: strings.TrimSpace(r.FormValue("effective_from")),
			Errors:        map[string]string{},
		}
		err := svcs.Savings.SetPolicy(r.Context(), SetSavingsPolicyInput{Rate: form.Rate, EffectiveFrom: form.EffectiveFrom})
		if err != nil {
			logger.Warn("setting the savings policy", slog.Any("error", err))
			switch {
			case errors.Is(err, ErrInvalidInput):
				form.Errors["rate"] = "Indica un porcentaje entre 0 y 100, con hasta dos decimales, y una fecha válida."
			case errors.Is(err, ErrSavingsRetroactivePolicy):
				form.Errors["effective_from"] = "Solo la primera versión puede empezar en el pasado. " +
					"Un cambio de tasa empieza hoy o más adelante, para no alterar sugerencias ya hechas."
			case errors.Is(err, ErrSavingsPolicyVersionInUse):
				form.Errors["effective_from"] = "La versión que empieza ese día ya se aplicó a ingresos; " +
					"elige una fecha posterior."
			default:
				form.Errors["general"] = "No se pudo guardar la política de ahorro."
			}
			renderSavingsPolicy(w, r, logger, renderer, svcs, isMobileReturn(back),
				http.StatusUnprocessableEntity, form, nil)
			return
		}
		redirectTo(w, r, withQuery(back, "msg", "policy-saved"))
	}
}

// handleSetCategoryEligibility serves POST /savings/categories/{id}/eligibility.
func handleSetCategoryEligibility(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, savingsPath+"/policy")
		eligible := r.FormValue("eligible") == "1"
		if err := svcs.Savings.SetCategoryEligibility(r.Context(), id, eligible); err != nil {
			logger.Warn("setting a category's savings eligibility", slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", "eligibility"))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", "eligibility-saved"))
	}
}

// handleSavingsSync serves GET /savings/sync and GET /mobile/ahorro/sincronizar.
func handleSavingsSync(logger *slog.Logger, renderer *Renderer, svcs *Services, mobile bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view := SavingsSyncView{Return: savingsPath}
		if mobile {
			view.Return = mobileSavingsPath
			view.Navigation = MobileNavView{ActiveTab: "inicio"}
		}
		var alert *FlashAlert
		plan, err := svcs.Savings.PlanSync(r.Context())
		if err != nil {
			logger.Error("previewing the savings sync", slog.Any("error", err))
			alert = &FlashAlert{Type: "danger", Message: "No se pudo calcular la vista previa."}
		}
		view.Plan = plan
		view.Alert = alert
		renderSavings(w, r, logger, renderer, mobile, "savings/sync.html", "mobile/savings_sync.html",
			PageData{Title: "Sincronizar ahorro", ActiveNav: NavSavings, Alert: alert, Data: view})
	}
}

// handleApplySavingsSync serves POST /savings/sync. It applies exactly the
// incomes the preview listed, which arrive as the form's transaction_id values.
func handleApplySavingsSync(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, savingsPath)
		ids := make([]int64, 0, len(r.Form["transaction_id"]))
		for _, raw := range r.Form["transaction_id"] {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "Invalid transaction id", http.StatusBadRequest)
				return
			}
			ids = append(ids, id)
		}
		result, err := svcs.Savings.ApplySync(r.Context(), ids)
		if err != nil {
			logger.Error("applying the savings sync", slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", "sync"))
			return
		}
		redirectTo(w, r, back+"?msg=synced&created="+strconv.Itoa(result.Created)+
			"&revised="+strconv.Itoa(result.Revised)+"&invalidated="+strconv.Itoa(result.Invalidated))
	}
}

// handleSavingsIncome serves GET /savings/incomes/{id} and its mobile twin.
func handleSavingsIncome(logger *slog.Logger, renderer *Renderer, svcs *Services, mobile bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		detail, err := svcs.Savings.IncomeDetail(r.Context(), id)
		if errors.Is(err, ErrSavingsIncomeNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			logger.Error("loading an income's savings history", slog.Any("error", err))
			http.Error(w, "No se pudo cargar el ingreso.", http.StatusInternalServerError)
			return
		}
		view := SavingsIncomeView{Detail: detail, Return: fmt.Sprintf("%s/incomes/%d", savingsPath, id)}
		for i := range detail.Revisions {
			if detail.Revisions[i].Live {
				view.Live = &detail.Revisions[i]
			}
		}
		if view.Live != nil && view.Live.CanConfirm {
			if view.ConfirmKey, err = newRequestKey(); err == nil {
				view.Fund, err = svcs.Savings.GetFund(r.Context())
			}
			if err == nil {
				view.Distribution, err = svcs.Savings.SuggestDistribution(r.Context(), view.Live.RemainingAmount)
			}
			if !hasGoalLine(view.Distribution) {
				view.Distribution = nil
			}
			if err != nil {
				logger.Error("loading the fund for a confirmation", slog.Any("error", err))
				http.Error(w, "No se pudo cargar el ingreso.", http.StatusInternalServerError)
				return
			}
		}
		if mobile {
			view.Return = fmt.Sprintf("%s/ingresos/%d", mobileSavingsPath, id)
			view.Navigation = MobileNavView{ActiveTab: "inicio"}
		}
		view.Alert = savingsAlert(r.URL.Query())
		renderSavings(w, r, logger, renderer, mobile, "savings/income.html", "mobile/savings_income.html",
			PageData{Title: "Ahorro de un ingreso", ActiveNav: NavSavings, Alert: view.Alert, Data: view})
	}
}

// handleSetIncomeOverride serves POST /savings/incomes/{id}/override.
func handleSetIncomeOverride(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, fmt.Sprintf("%s/incomes/%d", savingsPath, id))
		err := svcs.Savings.SetIncomeOverride(r.Context(), id, r.FormValue("eligible") == "1", r.FormValue("reason"))
		if err != nil {
			logger.Warn("setting an income's savings override", slog.Any("error", err))
			key := "override"
			if errors.Is(err, ErrInvalidInput) {
				key = "reason-required"
			}
			redirectTo(w, r, withQuery(back, "err", key))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", "override-saved"))
	}
}

// handleSuggestionAction serves POST /savings/incomes/{id}/{value,revalue,dismiss}.
func handleSuggestionAction(logger *slog.Logger, svcs *Services, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, savingsPath)
		var (
			err  error
			done string
		)
		switch action {
		case "value":
			err, done = svcs.Savings.ValueSuggestion(r.Context(), id), "valued"
		case "revalue":
			err, done = svcs.Savings.RevalueSuggestion(r.Context(), id), "revalued"
		case "dismiss":
			err, done = svcs.Savings.DismissSuggestion(r.Context(), id, r.FormValue("reason")), "dismissed"
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			logger.Warn("acting on a savings suggestion", slog.String("action", action), slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", suggestionErrorKey(err)))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", done))
	}
}

// handleValuePending serves POST /savings/value-pending.
func handleValuePending(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, savingsPath)
		n, err := svcs.Savings.ValuePending(r.Context())
		if err != nil {
			logger.Error("valuing pending suggestions", slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", "value-pending"))
			return
		}
		redirectTo(w, r, back+"?msg=valued-pending&n="+strconv.Itoa(n))
	}
}

func suggestionErrorKey(err error) string {
	switch {
	case errors.Is(err, ErrSavingsRateUnavailable):
		return "rate-unavailable"
	case errors.Is(err, ErrSavingsNotYetEffective):
		return "not-yet"
	case errors.Is(err, ErrSavingsRateUnchanged):
		return "rate-unchanged"
	case errors.Is(err, ErrSavingsInvalidState), errors.Is(err, ErrNotFound):
		return "invalid-state"
	default:
		return "action"
	}
}

// savingsAlert turns the ?msg= or ?err= a savings action redirected with into
// the page's alert. The text is chosen here from a fixed list: the query never
// supplies words that reach the page.
func savingsAlert(q url.Values) *FlashAlert {
	if key := q.Get("err"); key != "" {
		messages := map[string]string{
			"rate-unavailable":      "Todavía no hay una tasa vigente en la fecha de ese ingreso. Regístrala en Tasas de cambio y vuelve a valorar.",
			"not-yet":               "La fecha de ese ingreso aún no llega; se podrá valorar ese día.",
			"rate-unchanged":        "La tasa de esa fecha no ha cambiado: no hay nada que revalorar.",
			"invalid-state":         "Esa sugerencia ya no admite esta acción. La lista se actualizó.",
			"reason-required":       "Indica el motivo de la excepción.",
			"eligibility":           "No se pudo cambiar la elegibilidad de la categoría.",
			"insufficient-free":     "La cuenta no tiene suficientes USD libres: su saldo menos lo ya reservado no cubre ese importe.",
			"insufficient-reserved": "No hay tanto reservado ahí.",
			"exceeds-suggestion":    "Eso supera lo que queda de la sugerencia. Si de verdad quieres apartar más, marca la excepción e indica el motivo.",
			"not-usd":               "El ahorro solo se respalda con cuentas en USD.",
			"account-inactive":      "Esa cuenta está archivada: no puede respaldar ahorro nuevo.",
			"account":               "Elige una cuenta en USD.",
			"request-key":           "El formulario caducó. Vuelve a intentarlo desde la página actualizada.",
			"amount-or-reason":      "Revisa el importe (en USD, con hasta dos decimales) y el motivo, si hace falta.",
			"goal-not-accepting":    "Esa meta está pausada o cerrada: no recibe dinero.",
			"distribution-exceeded": "Los porcentajes de las metas no pueden sumar más de 100%.",
			"goal-invalid-state":    "Esa meta ya no admite esta acción.",
			"goal-invalid":          "Revisa los datos: nombre, objetivo en USD, fecha y porcentaje, o el motivo si hace falta.",
		}
		message, ok := messages[key]
		if !ok {
			message = "No se pudo completar la acción."
		}
		return &FlashAlert{Type: "warning", Message: message}
	}
	switch q.Get("msg") {
	case "policy-saved":
		return &FlashAlert{Type: "success", Message: "Política de ahorro guardada."}
	case "eligibility-saved":
		return &FlashAlert{Type: "success",
			Message: "Elegibilidad guardada. Revisa «Sincronizar» para aplicarla a los ingresos existentes."}
	case "override-saved":
		return &FlashAlert{Type: "success", Message: "Excepción guardada y aplicada a este ingreso."}
	case "goal-created":
		return &FlashAlert{Type: "success", Message: "Meta creada."}
	case "goal-saved":
		return &FlashAlert{Type: "success", Message: "Meta guardada. Si cambiaste el objetivo, el original queda en su historial."}
	case "goal-paused":
		return &FlashAlert{Type: "success", Message: "Meta pausada: conserva lo que tiene y no recibe más."}
	case "goal-resumed":
		return &FlashAlert{Type: "success", Message: "Meta reactivada."}
	case "goal-completed":
		return &FlashAlert{Type: "success", Message: "Meta completada. Lo que aún tenía quedó reservado sin asignar."}
	case "goal-cancelled":
		return &FlashAlert{Type: "success", Message: "Meta cancelada. Su dinero quedó reservado sin asignar."}
	case "reallocated":
		return &FlashAlert{Type: "success", Message: "Dinero movido. El total reservado no cambia."}
	case "acknowledged":
		return &FlashAlert{Type: "success", Message: "Incidencia reconocida. Sigue visible, y los saldos no cambian, hasta que la resuelvas."}
	case "reserved":
		return &FlashAlert{Type: "success", Message: "Ahorro reservado. El dinero sigue en su cuenta; solo quedó marcado."}
	case "released":
		return &FlashAlert{Type: "success", Message: "Reserva liberada: ese dinero vuelve a estar libre."}
	case "valued":
		return &FlashAlert{Type: "success", Message: "Sugerencia valorada."}
	case "revalued":
		return &FlashAlert{Type: "success", Message: "Sugerencia revalorada con la tasa corregida."}
	case "dismissed":
		return &FlashAlert{Type: "success", Message: "Sugerencia descartada."}
	case "valued-pending":
		return &FlashAlert{Type: "success",
			Message: fmt.Sprintf("Sugerencias valoradas: %d.", atoiOrZero(q.Get("n")))}
	case "synced":
		return &FlashAlert{Type: "success", Message: fmt.Sprintf(
			"Sincronizado: %d creadas, %d actualizadas, %d anuladas.",
			atoiOrZero(q.Get("created")), atoiOrZero(q.Get("revised")), atoiOrZero(q.Get("invalidated")))}
	default:
		return nil
	}
}

func atoiOrZero(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// savingsReturn reads where an action should come back to. Only a savings page
// of this application is accepted, so the field cannot redirect anywhere else.
func savingsReturn(r *http.Request, fallback string) string {
	back := r.FormValue("return")
	if strings.Contains(back, "//") || strings.ContainsAny(back, "?#\\") {
		return fallback
	}
	if back == savingsPath || strings.HasPrefix(back, savingsPath+"/") ||
		back == mobileSavingsPath || strings.HasPrefix(back, mobileSavingsPath+"/") {
		return back
	}
	return fallback
}

func isMobileReturn(path string) bool { return strings.HasPrefix(path, mobileSavingsPath) }

func withQuery(path, key, value string) string {
	return path + "?" + url.Values{key: {value}}.Encode()
}

func renderSavings(w http.ResponseWriter, r *http.Request, logger *slog.Logger, renderer *Renderer, mobile bool,
	desktopTemplate, mobileTemplate string, data PageData,
) {
	renderSavingsWithStatus(w, r, logger, renderer, mobile, http.StatusOK, desktopTemplate, mobileTemplate, data)
}

func renderSavingsWithStatus(w http.ResponseWriter, r *http.Request, logger *slog.Logger, renderer *Renderer,
	mobile bool, status int, desktopTemplate, mobileTemplate string, data PageData,
) {
	var err error
	if mobile {
		err = renderer.RenderMobilePage(w, r, status, mobileTemplate, data)
	} else {
		err = renderer.RenderPage(w, r, status, desktopTemplate, data)
	}
	if err != nil {
		logger.Error("rendering a savings page", slog.Any("error", err))
	}
}

// hasGoalLine reports whether a distribution names at least one goal.
func hasGoalLine(lines []SavingsDistributionLine) bool {
	for _, l := range lines {
		if l.GoalID != nil {
			return true
		}
	}
	return false
}
