package web

import (
	"log/slog"
	"net/http"
)

// RecapView is the recap page, desktop or mobile.
type RecapView struct {
	// Available is false when the recap could not be computed; Error says so.
	Available bool
	Error     string

	Recap Recap
	Nav   RecapNav

	// Navigation is the mobile bottom bar; the desktop page ignores it.
	Navigation MobileNavView
}

// handleRecap serves the weekly and monthly recap (GET /recap?period=&start=).
//
// Every figure comes from one call to the analytics service. The handler parses
// the query, builds the links between recaps and renders; it compares nothing.
// The selector and the arrows link back here with htmx selecting #recap out of
// the full page, so a deep link and a click render the same thing.
func handleRecap(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := loadRecap(r, logger, svcs, "/recap")
		pageData := PageData{Title: "Recap", ActiveNav: NavRecap, Data: data}
		if err := renderer.RenderPage(w, r, http.StatusOK, "recap/index.html", pageData); err != nil {
			logger.Error("rendering the recap", slog.Any("error", err))
		}
	}
}

// handleMobileRecap serves the same recap in the mobile layout
// (GET /mobile/recap?period=&start=).
func handleMobileRecap(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := loadRecap(r, logger, svcs, "/mobile/recap")
		data.Navigation = MobileNavView{ActiveTab: "inicio"}
		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/recap.html", PageData{Title: "Recap", Data: data}); err != nil {
			logger.Error("rendering the mobile recap", slog.Any("error", err))
		}
	}
}

// loadRecap computes the recap a request asks for. A failure to value degrades
// to a message on the page, never a 500: the provider and the rates are never a
// reason a screen will not open.
func loadRecap(r *http.Request, logger *slog.Logger, svcs *Services, path string) RecapView {
	req := ParseRecapRequest(r.URL.Query())
	recap, err := svcs.Analytics.Recap(r.Context(), req)
	if err != nil {
		logger.Error("computing the recap", slog.Any("error", err))
		return RecapView{Error: "No se pudo calcular el recap."}
	}
	return RecapView{Available: true, Recap: recap, Nav: newRecapNav(path, recap)}
}
