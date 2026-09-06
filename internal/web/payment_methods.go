package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

func handlePaymentMethodsList(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		methods, err := svcs.Methods.List(ctx, ListPaymentMethodsInput{IncludeInactive: true})
		if err != nil {
			logger.Error("failed to list payment methods", slog.Any("error", err))
			http.Error(w, "Failed to load payment methods", http.StatusInternalServerError)
			return
		}

		pageData := PageData{
			Title:     "Payment Methods",
			ActiveNav: NavPaymentMethods,
			Data: PaymentMethodsView{
				Methods: methods,
			},
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "payment_methods/index.html", pageData); err != nil {
			logger.Error("failed to render payment methods list", slog.Any("error", err))
		}
	}
}

func handleNewPaymentMethodForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		form := PaymentMethodForm{
			Errors: make(map[string]string),
		}

		pageData := PageData{
			Title:     "New Payment Method",
			ActiveNav: NavPaymentMethods,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "payment_methods/new.html", pageData); err != nil {
			logger.Error("failed to render new payment method form", slog.Any("error", err))
		}
	}
}

func handleCreatePaymentMethod(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		form := PaymentMethodForm{
			Name:   name,
			Errors: make(map[string]string),
		}

		if name == "" {
			form.Errors["name"] = "Name cannot be empty."
		} else if len([]rune(name)) > 60 {
			form.Errors["name"] = "Name must not exceed 60 characters."
		}

		if len(form.Errors) > 0 {
			pageData := PageData{
				Title:     "New Payment Method",
				ActiveNav: NavPaymentMethods,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "payment_methods/new.html", pageData)
			return
		}

		_, err := svcs.Methods.Create(ctx, CreatePaymentMethodInput{Name: name})
		if err != nil {
			logger.Warn("failed to create payment method", slog.Any("error", err))
			status := http.StatusUnprocessableEntity
			switch {
			case errors.Is(err, ErrConflict):
				status = http.StatusConflict
				form.Errors["name"] = "A payment method with this name already exists."
			case errors.Is(err, ErrInvalidInput):
				form.Errors["name"] = "Invalid payment method name."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "Failed to create payment method. Please try again."
			}

			pageData := PageData{
				Title:     "New Payment Method",
				ActiveNav: NavPaymentMethods,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "payment_methods/new.html", pageData)
			return
		}

		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/payment-methods")
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Redirect(w, r, "/payment-methods", http.StatusSeeOther)
	}
}

func handleEditPaymentMethodForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		m, err := svcs.Methods.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to get payment method for edit", slog.Any("error", err))
			http.Error(w, "Failed to load payment method", http.StatusInternalServerError)
			return
		}

		form := PaymentMethodForm{
			ID:     m.ID,
			Name:   m.Name,
			Errors: make(map[string]string),
		}

		pageData := PageData{
			Title:     "Edit Payment Method",
			ActiveNav: NavPaymentMethods,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "payment_methods/edit.html", pageData); err != nil {
			logger.Error("failed to render edit payment method form", slog.Any("error", err))
		}
	}
}

func handleUpdatePaymentMethod(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		form := PaymentMethodForm{
			ID:     id,
			Name:   name,
			Errors: make(map[string]string),
		}

		if name == "" {
			form.Errors["name"] = "Name cannot be empty."
		} else if len([]rune(name)) > 60 {
			form.Errors["name"] = "Name must not exceed 60 characters."
		}

		if len(form.Errors) > 0 {
			pageData := PageData{
				Title:     "Edit Payment Method",
				ActiveNav: NavPaymentMethods,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "payment_methods/edit.html", pageData)
			return
		}

		_, err = svcs.Methods.UpdateMetadata(ctx, id, UpdatePaymentMethodMetadataInput{Name: name})
		if err != nil {
			logger.Warn("failed to update payment method", slog.Any("error", err))
			status := http.StatusUnprocessableEntity
			switch {
			case errors.Is(err, ErrNotFound):
				http.NotFound(w, r)
				return
			case errors.Is(err, ErrConflict):
				status = http.StatusConflict
				form.Errors["name"] = "A payment method with this name already exists."
			case errors.Is(err, ErrInvalidInput):
				form.Errors["name"] = "Invalid payment method name."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "Failed to update payment method. Please try again."
			}

			pageData := PageData{
				Title:     "Edit Payment Method",
				ActiveNav: NavPaymentMethods,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "payment_methods/edit.html", pageData)
			return
		}

		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/payment-methods")
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Redirect(w, r, "/payment-methods", http.StatusSeeOther)
	}
}

func handleTogglePaymentMethod(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		m, err := svcs.Methods.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to get payment method for toggle", slog.Any("error", err))
			http.Error(w, "Failed to load payment method", http.StatusInternalServerError)
			return
		}

		_, err = svcs.Methods.SetActive(ctx, id, !m.IsActive)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to toggle payment method active status", slog.Any("error", err))
			http.Error(w, "Failed to toggle status", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/payment-methods", http.StatusSeeOther)
	}
}
