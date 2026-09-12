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
	Type      CategoryType
}

// handleMobileCategories serves the mobile categories management view with real database data.
func handleMobileCategories(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		categories, err := svcs.Categories.List(ctx, ListCategoriesInput{IncludeInactive: false})
		if err != nil {
			logger.Error("listing categories for mobile view", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		var expenseCats []MobileCategoryItemView
		var incomeCats []MobileCategoryItemView

		for _, c := range categories {
			icon, emoji := inferCategoryIconAndEmoji(c.Name)
			item := MobileCategoryItemView{
				ID:        c.ID,
				Name:      c.Name,
				Icon:      icon,
				IconEmoji: emoji,
				IsActive:  c.IsActive,
				Type:      c.Type,
			}

			if c.Type == CategoryTypeIncome {
				incomeCats = append(incomeCats, item)
			} else {
				expenseCats = append(expenseCats, item)
			}
		}

		data := MobileCategoriesPageView{
			ExpenseCategories: expenseCats,
			IncomeCategories:  incomeCats,
			Navigation: MobileNavView{
				ActiveTab: "inicio",
			},
		}

		pageData := PageData{
			Title: "Categorías",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/categories.html", pageData); err != nil {
			logger.Error("rendering mobile categories", slog.Any("error", err))
		}
	}
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
