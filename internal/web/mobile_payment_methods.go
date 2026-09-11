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

// handleMobilePaymentMethods serves the mobile payment methods management view.
func handleMobilePaymentMethods(logger *slog.Logger, renderer *Renderer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := DefaultMobilePaymentMethodsMockData()
		pageData := PageData{
			Title: "Métodos de pago",
			Data:  data,
		}

		if err := renderer.RenderMobilePage(w, r, http.StatusOK, "mobile/payment_methods.html", pageData); err != nil {
			logger.Error("rendering mobile payment methods", slog.Any("error", err))
		}
	}
}
