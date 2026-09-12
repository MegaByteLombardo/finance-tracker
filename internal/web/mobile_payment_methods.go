package web

import (
	"log/slog"
	"net/http"
)

// MobilePaymentMethodsPageView holds data for rendering the mobile payment methods screen.
type MobilePaymentMethodsPageView struct {
	Methods    []MobilePaymentMethodItemView
	Navigation MobileNavView
}

// MobilePaymentMethodItemView represents a payment method in the mobile view.
type MobilePaymentMethodItemView struct {
	ID       int64
	Name     string
	IsActive bool
}

// handleMobilePaymentMethods serves the mobile payment methods management view with real database data.
func handleMobilePaymentMethods(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		methods, err := svcs.Methods.List(ctx, ListPaymentMethodsInput{IncludeInactive: false})
		if err != nil {
			logger.Error("listing payment methods for mobile view", slog.Any("error", err))
			http.Error(w, "Failed to load payment methods", http.StatusInternalServerError)
			return
		}

		var items []MobilePaymentMethodItemView
		for _, m := range methods {
			items = append(items, MobilePaymentMethodItemView{
				ID:       m.ID,
				Name:     m.Name,
				IsActive: m.IsActive,
			})
		}

		data := MobilePaymentMethodsPageView{
			Methods: items,
			Navigation: MobileNavView{
				ActiveTab: "inicio",
			},
		}

		pageData := PageData{
			Title: "Métodos de pago",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/payment_methods.html", pageData); err != nil {
			logger.Error("rendering mobile payment methods", slog.Any("error", err))
		}
	}
}

// DefaultMobilePaymentMethodsMockData returns default mock data for payment methods.
func DefaultMobilePaymentMethodsMockData() MobilePaymentMethodsPageView {
	return MobilePaymentMethodsPageView{
		Methods: []MobilePaymentMethodItemView{
			{ID: 1, Name: "Pago Móvil", IsActive: true},
			{ID: 2, Name: "Transferencia bancaria", IsActive: true},
			{ID: 3, Name: "Tarjeta de débito / crédito", IsActive: true},
			{ID: 4, Name: "Efectivo", IsActive: true},
			{ID: 5, Name: "Zelle", IsActive: true},
		},
		Navigation: MobileNavView{
			ActiveTab: "inicio",
		},
	}
}
