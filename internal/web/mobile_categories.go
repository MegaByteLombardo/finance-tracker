package web

import (
	"log/slog"
	"net/http"
)

// MobileCategoriesPageView holds data for rendering the mobile categories screen.
type MobileCategoriesPageView struct {
	ExpenseCategories []MobileCategoryItemView
	IncomeCategories  []MobileCategoryItemView
	Navigation        MobileNavView
}

// MobileCategoryItemView represents a category in the mobile view.
type MobileCategoryItemView struct {
	ID        int64
	Name      string
	Icon      string // CSS class name suffix: "cart", "coffee", etc.
	IconEmoji string
	IsActive  bool
}

// DefaultMobileCategoriesMockData returns default mock data for mobile categories.
func DefaultMobileCategoriesMockData() MobileCategoriesPageView {
	return MobileCategoriesPageView{
		ExpenseCategories: []MobileCategoryItemView{
			{ID: 1, Name: "Alimentación & Supermercado", Icon: "cart", IconEmoji: "🛒", IsActive: true},
			{ID: 2, Name: "Restaurantes & Café", Icon: "coffee", IconEmoji: "☕", IsActive: true},
			{ID: 3, Name: "Transporte & Gasolina", Icon: "car", IconEmoji: "🚗", IsActive: true},
			{ID: 4, Name: "Salud & Farmacia", Icon: "pill", IconEmoji: "💊", IsActive: true},
			{ID: 5, Name: "Entretenimiento & Streaming", Icon: "entertainment", IconEmoji: "🎬", IsActive: true},
		},
		IncomeCategories: []MobileCategoryItemView{
			{ID: 6, Name: "Nómina y Sueldos", Icon: "briefcase", IconEmoji: "💼", IsActive: true},
			{ID: 7, Name: "Servicios Profesionales", Icon: "briefcase", IconEmoji: "💻", IsActive: true},
			{ID: 8, Name: "Rendimientos e Inversiones", Icon: "briefcase", IconEmoji: "📈", IsActive: true},
		},
		Navigation: MobileNavView{
			ActiveTab: "inicio",
		},
	}
}

// handleMobileCategories serves the mobile categories management view.
func handleMobileCategories(logger *slog.Logger, renderer *Renderer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := DefaultMobileCategoriesMockData()
		pageData := PageData{
			Title: "Categorías",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/categories.html", pageData); err != nil {
			logger.Error("rendering mobile categories", slog.Any("error", err))
		}
	}
}
