package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// handleCategoriesList serves GET /categories
func handleCategoriesList(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		incomeType := CategoryTypeIncome
		incomeCats, err := svcs.Categories.List(ctx, ListCategoriesInput{
			Type:            &incomeType,
			IncludeInactive: true,
		})
		if err != nil {
			logger.Error("listing income categories", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		expenseType := CategoryTypeExpense
		expenseCats, err := svcs.Categories.List(ctx, ListCategoriesInput{
			Type:            &expenseType,
			IncludeInactive: true,
		})
		if err != nil {
			logger.Error("listing expense categories", slog.Any("error", err))
			http.Error(w, "Failed to load categories", http.StatusInternalServerError)
			return
		}

		pageData := PageData{
			Title:     "Categories",
			ActiveNav: NavCategories,
			Data: CategoryGroupView{
				IncomeCategories:  incomeCats,
				ExpenseCategories: expenseCats,
			},
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "categories/index.html", pageData); err != nil {
			logger.Error("rendering categories list", slog.Any("error", err))
		}
	}
}

// handleNewCategoryForm serves GET /categories/new
func handleNewCategoryForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		typeParam := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("type")))
		catType := CategoryTypeExpense
		if typeParam == string(CategoryTypeIncome) {
			catType = CategoryTypeIncome
		}

		form := CategoryForm{
			Type:   catType,
			Errors: make(map[string]string),
		}

		pageData := PageData{
			Title:     "Create Category",
			ActiveNav: NavCategories,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "categories/new.html", pageData); err != nil {
			logger.Error("rendering new category form", slog.Any("error", err))
		}
	}
}

// handleCreateCategory serves POST /categories
func handleCreateCategory(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		catTypeStr := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
		catType := CategoryTypeExpense
		if catTypeStr == string(CategoryTypeIncome) {
			catType = CategoryTypeIncome
		}

		form := CategoryForm{
			Name:   name,
			Type:   catType,
			Errors: make(map[string]string),
		}

		if name == "" {
			form.Errors["name"] = "Category name is required."
		} else if len(name) > 80 {
			form.Errors["name"] = "Category name must not exceed 80 characters."
		}

		if len(form.Errors) > 0 {
			pageData := PageData{
				Title:     "Create Category",
				ActiveNav: NavCategories,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "categories/new.html", pageData)
			return
		}

		_, err := svcs.Categories.Create(ctx, CreateCategoryInput{
			Name: name,
			Type: catType,
		})
		if err != nil {
			logger.Warn("creating category failed", slog.Any("error", err))
			status := http.StatusUnprocessableEntity
			switch {
			case errors.Is(err, ErrConflict):
				status = http.StatusConflict
				form.Errors["name"] = "A category with this name already exists for this type."
			case errors.Is(err, ErrInvalidInput):
				form.Errors["name"] = "Invalid category name."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "Failed to create category. Please try again."
			}

			pageData := PageData{
				Title:     "Create Category",
				ActiveNav: NavCategories,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "categories/new.html", pageData)
			return
		}

		// Success: PRG redirect
		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/categories")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/categories", http.StatusSeeOther)
	}
}

// handleToggleCategory serves POST /categories/{id}/toggle
func handleToggleCategory(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid category ID", http.StatusBadRequest)
			return
		}

		cat, err := svcs.Categories.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.Error(w, "Category not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to retrieve category", http.StatusInternalServerError)
			return
		}

		newStatus := !cat.IsActive
		if _, err := svcs.Categories.SetActive(ctx, id, newStatus); err != nil {
			logger.Error("toggling category status", slog.Any("error", err))
			http.Error(w, "Failed to update category status", http.StatusInternalServerError)
			return
		}

		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/categories")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/categories", http.StatusSeeOther)
	}
}

// handleEditCategoryForm serves GET /categories/{id}/edit
func handleEditCategoryForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		cat, err := svcs.Categories.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to get category for edit", slog.Any("error", err))
			http.Error(w, "Failed to load category", http.StatusInternalServerError)
			return
		}

		form := EditCategoryForm{
			ID:     cat.ID,
			Name:   cat.Name,
			Type:   cat.Type,
			Errors: make(map[string]string),
		}

		pageData := PageData{
			Title:     "Edit Category",
			ActiveNav: NavCategories,
			Data:      form,
		}

		if err := renderer.RenderPage(w, r, http.StatusOK, "categories/edit.html", pageData); err != nil {
			logger.Error("rendering edit category form", slog.Any("error", err))
		}
	}
}

// handleUpdateCategory serves POST /categories/{id}
func handleUpdateCategory(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		existing, err := svcs.Categories.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("failed to get category for update", slog.Any("error", err))
			http.Error(w, "Failed to load category", http.StatusInternalServerError)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		form := EditCategoryForm{
			ID:     id,
			Name:   name,
			Type:   existing.Type,
			Errors: make(map[string]string),
		}

		if name == "" {
			form.Errors["name"] = "Category name is required."
		} else if len(name) > 80 {
			form.Errors["name"] = "Category name must not exceed 80 characters."
		}

		if len(form.Errors) > 0 {
			pageData := PageData{
				Title:     "Edit Category",
				ActiveNav: NavCategories,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, http.StatusUnprocessableEntity, "categories/edit.html", pageData)
			return
		}

		_, err = svcs.Categories.UpdateMetadata(ctx, id, UpdateCategoryMetadataInput{
			Name: name,
		})
		if err != nil {
			logger.Warn("failed to update category metadata", slog.Any("error", err))
			status := http.StatusUnprocessableEntity
			switch {
			case errors.Is(err, ErrNotFound):
				http.NotFound(w, r)
				return
			case errors.Is(err, ErrConflict):
				status = http.StatusConflict
				form.Errors["name"] = "A category with this name already exists for this type."
			case errors.Is(err, ErrInvalidInput):
				form.Errors["name"] = "Invalid category name."
			default:
				status = http.StatusInternalServerError
				form.Errors["general"] = "Failed to update category. Please try again."
			}

			pageData := PageData{
				Title:     "Edit Category",
				ActiveNav: NavCategories,
				Data:      form,
			}
			_ = renderer.RenderPage(w, r, status, "categories/edit.html", pageData)
			return
		}

		if IsHXRequest(r) {
			w.Header().Set("HX-Redirect", "/categories")
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Redirect(w, r, "/categories", http.StatusSeeOther)
	}
}
