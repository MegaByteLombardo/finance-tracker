package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// M7.2 screens and actions: the fund page, reserving (with or without a
// suggestion) and releasing.

// newRequestKey returns a fresh idempotency token for a form. A double click or
// a browser retry submits the same token, and the backend replays the
// operation instead of performing it twice.
func newRequestKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// handleSavingsFund serves GET /savings/fund and GET /mobile/ahorro/fondo.
func handleSavingsFund(logger *slog.Logger, renderer *Renderer, svcs *Services, mobile bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view := SavingsFundView{Return: savingsPath + "/fund"}
		if mobile {
			view.Return = mobileSavingsPath + "/fondo"
			view.Navigation = MobileNavView{ActiveTab: "inicio"}
		}
		var err error
		if view.ReserveKey, err = newRequestKey(); err == nil {
			view.ReleaseKey, err = newRequestKey()
		}
		if err == nil {
			view.Fund, err = svcs.Savings.GetFund(r.Context())
		}
		if err == nil {
			view.Goals, err = svcs.Savings.ListGoals(r.Context(), false)
		}
		if err == nil {
			view.Operations, err = svcs.Savings.ListOperations(r.Context(), 50)
		}
		if err != nil {
			logger.Error("loading the savings fund", slog.Any("error", err))
			view.Error = "No se pudo cargar el fondo de ahorro."
		} else {
			view.Available = true
		}
		view.Alert = savingsAlert(r.URL.Query())
		renderSavings(w, r, logger, renderer, mobile, "savings/fund.html", "mobile/savings_fund.html",
			PageData{Title: "Fondo de ahorro", ActiveNav: NavSavings, Alert: view.Alert, Data: view})
	}
}

// handleSavingsReserve serves POST /savings/reserve. With income_id it confirms
// that income's suggestion; without it, it is a manual contribution.
func handleSavingsReserve(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, savingsPath+"/fund")
		accountID, ok := formID(r, "account_id")
		if !ok {
			redirectTo(w, r, withQuery(back, "err", "account"))
			return
		}
		in := SavingsReserveInput{
			RequestKey: r.FormValue("request_key"),
			AccountID:  accountID,
			Amount:     strings.TrimSpace(r.FormValue("amount")),
			Exception:  r.FormValue("exception") == "1",
			Reason:     r.FormValue("reason"),
		}
		if raw := r.FormValue("income_id"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "Invalid income id", http.StatusBadRequest)
				return
			}
			in.IncomeTransactionID = &id
		}
		if goalID, ok := formID(r, "goal_id"); ok {
			in.GoalID = &goalID
		}
		in.Lines = reserveLinesFromForm(r)
		if _, err := svcs.Savings.Reserve(r.Context(), in); err != nil {
			logger.Warn("reserving savings", slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", fundErrorKey(err)))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", "reserved"))
	}
}

// handleSavingsRelease serves POST /savings/release.
func handleSavingsRelease(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := savingsReturn(r, savingsPath+"/fund")
		accountID, ok := formID(r, "account_id")
		if !ok {
			redirectTo(w, r, withQuery(back, "err", "account"))
			return
		}
		in := SavingsReleaseInput{
			RequestKey: r.FormValue("request_key"),
			AccountID:  accountID,
			Amount:     strings.TrimSpace(r.FormValue("amount")),
			Reason:     r.FormValue("reason"),
		}
		if goalID, ok := formID(r, "goal_id"); ok {
			in.GoalID = &goalID
		}
		if _, err := svcs.Savings.Release(r.Context(), in); err != nil {
			logger.Warn("releasing savings", slog.Any("error", err))
			redirectTo(w, r, withQuery(back, "err", fundErrorKey(err)))
			return
		}
		redirectTo(w, r, withQuery(back, "msg", "released"))
	}
}

// formID reads a positive id from a form field; an empty field is not an id.
func formID(r *http.Request, field string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue(field)), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func fundErrorKey(err error) string {
	switch {
	case errors.Is(err, ErrSavingsInsufficientFree):
		return "insufficient-free"
	case errors.Is(err, ErrSavingsInsufficientReserved):
		return "insufficient-reserved"
	case errors.Is(err, ErrSavingsExceedsSuggestion):
		return "exceeds-suggestion"
	case errors.Is(err, ErrSavingsNotUSDAccount):
		return "not-usd"
	case errors.Is(err, ErrAccountInactive):
		return "account-inactive"
	case errors.Is(err, ErrSavingsInvalidRequestKey):
		return "request-key"
	case errors.Is(err, ErrInvalidInput):
		return "amount-or-reason"
	case errors.Is(err, ErrSavingsInvalidState), errors.Is(err, ErrNotFound):
		return "invalid-state"
	default:
		return "action"
	}
}
