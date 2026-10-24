package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// M8.1: refunds. A REFUND is money given back for an expense, on any account:
// on a card it lowers the debt, on a bank account it raises the balance. It is
// recorded and corrected only here (TD-045); the generic transaction form
// redirects a refund's edit to these routes and refuses to record one.

// RefundForm records or corrects a refund.
type RefundForm struct {
	Action        string
	RequestKey    string
	TransactionID int64
	Return        string

	// Account is the account the money comes back to. A new refund chooses it
	// first, so the expenses it can refund are listed for that account only.
	Account   *Account
	Accounts  []Account
	AccountID int64

	CategoryID            int64
	PaymentMethodID       int64
	Amount                string
	Description           string
	EffectiveDate         string
	OriginalTransactionID int64

	// Originals are the expenses of the account a refund may name.
	Originals  []RefundOriginal
	Categories []Category
	Methods    []PaymentMethod
	FormErrors
}

// RefundOriginal is an expense a refund may name, labelled for a select.
type RefundOriginal struct {
	ID    int64
	Label string
}

// IsEdit reports the correction of an existing refund.
func (f RefundForm) IsEdit() bool { return f.TransactionID != 0 }

// refundReturn reads where a refund form comes back to: one of this
// application's own pages, never another host.
func refundReturn(raw string, fallback string) string {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") && !strings.Contains(raw, "\\") {
		return raw
	}
	return fallback
}

// loadRefundOptions fills the lists a refund form offers for its account.
func loadRefundOptions(r *http.Request, svcs *Services, form *RefundForm) error {
	ctx := r.Context()
	expense := CategoryTypeExpense
	var err error
	if form.Categories, err = svcs.Categories.List(ctx, ListCategoriesInput{Type: &expense}); err != nil {
		return err
	}
	if form.Methods, err = svcs.Methods.List(ctx, ListPaymentMethodsInput{}); err != nil {
		return err
	}
	if form.Accounts, err = svcs.Accounts.List(ctx, false); err != nil {
		return err
	}
	if form.AccountID == 0 {
		return nil
	}
	acct, err := svcs.Accounts.Get(ctx, form.AccountID)
	if err != nil {
		return err
	}
	form.Account = &acct

	movements, err := svcs.Cards.Movements(ctx, form.AccountID, 200)
	if err != nil {
		return err
	}
	for _, m := range movements {
		tx := m.Transaction
		if tx.Type != TransactionTypeExpense {
			continue
		}
		label := tx.EffectiveDate.Format("2006-01-02") + " · " + m.KindLabel
		if tx.Description != nil {
			label += " · " + *tx.Description
		} else if tx.CategoryName != nil {
			label += " · " + *tx.CategoryName
		}
		label += " · " + formatAmountString(strings.TrimPrefix(tx.AmountFormatted, "-")) + " " + tx.CurrencyCode
		form.Originals = append(form.Originals, RefundOriginal{ID: tx.ID, Label: label})
	}
	return nil
}

func refundFromForm(r *http.Request) RefundForm {
	account, _ := formID(r, "account_id")
	category, _ := formID(r, "category_id")
	method, _ := formID(r, "payment_method_id")
	original, _ := formID(r, "original_transaction_id")
	return RefundForm{
		RequestKey:            r.FormValue("request_key"),
		AccountID:             account,
		CategoryID:            category,
		PaymentMethodID:       method,
		Amount:                strings.TrimSpace(r.FormValue("amount")),
		Description:           strings.TrimSpace(r.FormValue("description")),
		EffectiveDate:         strings.TrimSpace(r.FormValue("effective_date")),
		OriginalTransactionID: original,
		Return:                refundReturn(r.FormValue("return_to"), "/transactions"),
		FormErrors:            FormErrors{},
	}
}

func (f RefundForm) validate() {
	if f.AccountID == 0 {
		f.FormErrors["account_id"] = "Elige la cuenta donde se devolvió el dinero."
	}
	if f.CategoryID == 0 {
		f.FormErrors["category_id"] = "Elige la categoría de egresos que se devuelve."
	}
	if f.PaymentMethodID == 0 {
		f.FormErrors["payment_method_id"] = "Elige un método de pago."
	}
	if f.Amount == "" {
		f.FormErrors["amount"] = "El monto es obligatorio."
	}
	if f.EffectiveDate == "" {
		f.FormErrors["effective_date"] = "La fecha es obligatoria."
	}
}

func (f RefundForm) original() *int64 {
	if f.OriginalTransactionID == 0 {
		return nil
	}
	id := f.OriginalTransactionID
	return &id
}

// handleNewRefund serves GET /refunds/new?account_id=&original=&return_to=.
func handleNewRefund(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := newRequestKey()
		if err != nil {
			logger.Error("generating a request key", slog.Any("error", err))
			http.Error(w, "Failed to prepare the form", http.StatusInternalServerError)
			return
		}
		q := r.URL.Query()
		form := RefundForm{
			Action: "/refunds", RequestKey: key, EffectiveDate: TodayDate(),
			Return: refundReturn(q.Get("return_to"), "/transactions"), FormErrors: FormErrors{},
		}
		form.AccountID, _ = strconv.ParseInt(q.Get("account_id"), 10, 64)
		form.OriginalTransactionID, _ = strconv.ParseInt(q.Get("original"), 10, 64)
		if err := loadRefundOptions(r, svcs, &form); err != nil {
			if errors.Is(err, ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			logger.Error("loading the refund form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, http.StatusOK, "refunds/form.html",
			PageData{Title: "Registrar devolución", ActiveNav: NavTransactions, Data: form})
	}
}

// handleRecordRefund serves POST /refunds.
func handleRecordRefund(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		form := refundFromForm(r)
		form.Action = "/refunds"
		form.validate()
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			_, err := svcs.Cards.RecordRefund(r.Context(), form.RequestKey, RecordRefundInput{
				AccountID: form.AccountID, CategoryID: form.CategoryID, Amount: form.Amount,
				PaymentMethodID: form.PaymentMethodID, Description: OptionalString(form.Description),
				EffectiveDate: form.EffectiveDate, OriginalTransactionID: form.original(),
			})
			if err == nil {
				redirectTo(w, r, form.Return)
				return
			}
			logger.Warn("recording a refund", slog.Any("error", err))
			status = refundError(err, form.FormErrors)
		}
		if err := loadRefundOptions(r, svcs, &form); err != nil {
			logger.Error("loading the refund form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, status, "refunds/form.html",
			PageData{Title: "Registrar devolución", ActiveNav: NavTransactions, Data: form})
	}
}

func refundError(err error, errs FormErrors) int {
	switch {
	case errors.Is(err, ErrAccountNotFound), errors.Is(err, ErrAccountInactive):
		errs["account_id"] = "La cuenta no existe o está archivada."
		return http.StatusUnprocessableEntity
	default:
		return cardErrorMessage(err, errs)
	}
}

// refundFromPath loads the refund /refunds/{id} names, or 404 for anything
// that is not a refund.
func refundFromPath(w http.ResponseWriter, r *http.Request, logger *slog.Logger, svcs *Services) (TransactionView, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return TransactionView{}, false
	}
	tx, err := svcs.Ledger.GetTransaction(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return TransactionView{}, false
	}
	if err != nil {
		logger.Error("loading a refund", slog.Any("error", err))
		http.Error(w, "Failed to load the refund", http.StatusInternalServerError)
		return TransactionView{}, false
	}
	if tx.Type != TransactionTypeRefund {
		http.NotFound(w, r)
		return TransactionView{}, false
	}
	return tx, true
}

// handleEditRefund serves GET /refunds/{id}/edit.
func handleEditRefund(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tx, ok := refundFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		form := RefundForm{
			Action: "/refunds/" + strconv.FormatInt(tx.ID, 10), TransactionID: tx.ID,
			AccountID: tx.AccountID, Amount: tx.AmountFormatted,
			EffectiveDate: tx.EffectiveDate.Format("2006-01-02"),
			Return:        refundReturn(r.URL.Query().Get("return_to"), "/transactions"), FormErrors: FormErrors{},
		}
		if tx.CategoryID != nil {
			form.CategoryID = *tx.CategoryID
		}
		if tx.PaymentMethodID != nil {
			form.PaymentMethodID = *tx.PaymentMethodID
		}
		if tx.Description != nil {
			form.Description = *tx.Description
		}
		if tx.RefundOf != nil {
			form.OriginalTransactionID = *tx.RefundOf
		}
		if err := loadRefundOptions(r, svcs, &form); err != nil {
			logger.Error("loading the refund form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, http.StatusOK, "refunds/form.html",
			PageData{Title: "Corregir devolución", ActiveNav: NavTransactions, Data: form})
	}
}

// handleUpdateRefund serves POST /refunds/{id}.
func handleUpdateRefund(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tx, ok := refundFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		form := refundFromForm(r)
		form.TransactionID = tx.ID
		form.Action = "/refunds/" + strconv.FormatInt(tx.ID, 10)
		form.validate()
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			_, err := svcs.Cards.UpdateRefund(r.Context(), tx.ID, UpdateRefundInput{
				AccountID: form.AccountID, CategoryID: form.CategoryID, Amount: form.Amount,
				PaymentMethodID: form.PaymentMethodID, Description: OptionalString(form.Description),
				EffectiveDate: form.EffectiveDate, OriginalTransactionID: form.original(),
				AllowCurrencyChange: r.FormValue("allow_currency_change") == "1",
			})
			if err == nil {
				redirectTo(w, r, form.Return)
				return
			}
			logger.Warn("correcting a refund", slog.Any("error", err))
			status = refundError(err, form.FormErrors)
		}
		if err := loadRefundOptions(r, svcs, &form); err != nil {
			logger.Error("loading the refund form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, status, "refunds/form.html",
			PageData{Title: "Corregir devolución", ActiveNav: NavTransactions, Data: form})
	}
}
