package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
)

// handleSetAccountActive serves POST /accounts/{id}/archive and
// POST /accounts/{id}/restore.
//
// Each route names the state it SETS rather than flipping the current one, so a
// double submission -- a reload, a second tap -- archives an archived account
// again, which is a no-op, instead of quietly restoring it.
//
// Archiving is never refused because the account still holds money. Archiving
// is visibility, not a financial event: the money keeps counting in the net
// worth, today and in history (TD-064), and the screens say so. Refusing would
// make the user move money they did not mean to move just to tidy a list.
func handleSetAccountActive(logger *slog.Logger, svcs *Services, active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if _, err := svcs.Accounts.SetActive(ctx, id, active); err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("changing an account's status",
				slog.Int64("account_id", id), slog.Bool("active", active), slog.Any("error", err))
			http.Error(w, "Failed to update the account", http.StatusInternalServerError)
			return
		}

		redirectURL := returnTo(r, "/accounts")
		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", redirectURL)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
	}
}

// fillAccountStatus adds what the edit page needs to offer archiving or
// restoring: whether the account is active, and its current balance, so the page
// can say that an archived account's money still counts.
func fillAccountStatus(ctx context.Context, svcs *Services, form *EditAccountForm, isActive bool) error {
	form.IsActive = isActive
	balance, err := svcs.Ledger.GetAccountBalance(ctx, form.ID)
	if err != nil {
		return err
	}
	form.BalanceMinor = balance.BalanceMinor
	form.BalanceFormatted = balance.BalanceFormatted
	return nil
}
