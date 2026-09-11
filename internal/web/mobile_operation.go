package web

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// MobileOperationFormView holds initial state for rendering the mobile new operation form.
type MobileOperationFormView struct {
	SelectedType string // "EXPENSE", "INCOME", "TRANSFER"
	CurrentDate  string
}

// handleMobileNewOperation serves the mobile new operation form.
func handleMobileNewOperation(logger *slog.Logger, renderer *Renderer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opType := strings.ToUpper(r.URL.Query().Get("type"))
		if opType != "INCOME" && opType != "TRANSFER" {
			opType = "EXPENSE"
		}

		viewData := MobileOperationFormView{
			SelectedType: opType,
			CurrentDate:  time.Now().Format("2006-01-02"),
		}

		pageData := PageData{
			Title: "Nueva Operación",
			Data:  viewData,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/new_operation.html", pageData); err != nil {
			logger.Error("rendering mobile new operation form", slog.Any("error", err))
		}
	}
}
