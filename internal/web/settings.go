package web

import (
	"log/slog"
	"net/http"
)

// handleBackupsList serves GET /settings/data
func handleBackupsList(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		backups, err := svcs.Backups.List(ctx)
		if err != nil {
			logger.Error("failed to list backups", slog.Any("error", err))
			http.Error(w, "Failed to load backups", http.StatusInternalServerError)
			return
		}

		pageData := PageData{
			Title:     "Data & Backups",
			ActiveNav: NavSettings,
			Data: BackupsView{
				Backups: backups,
			},
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "settings/data.html", pageData); err != nil {
			logger.Error("failed to render backups page", slog.Any("error", err))
		}
	}
}

// handleCreateBackup serves POST /settings/data/backup and POST /settings/data
func handleCreateBackup(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		_, err := svcs.Backups.CreateManual(ctx)
		if err != nil {
			logger.Error("failed to create manual backup", slog.Any("error", err))
			http.Error(w, "Failed to create backup", http.StatusInternalServerError)
			return
		}

		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/settings/data")
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Redirect(w, r, "/settings/data", http.StatusSeeOther)
	}
}
