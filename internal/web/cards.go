package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// M8.1 screens: the cards, opening one, its terms, and what is recorded on it
// -- purchases, charges and refunds. A payment is a TRANSFER or an FX_EXCHANGE
// into the card (TD-091), recorded on those forms; M8.2 adds the payment
// screen with its suggested amounts.
//
// Every form carries a request key rendered here (TD-095): a double submission
// returns what the first one recorded. A refused submission re-renders with
// 422 and the SAME key, so correcting it and submitting again still records
// once.

const cardsPath = "/cards"

// FormErrors are a form's messages by field; "general" is the form's own.
type FormErrors map[string]string

// HasError reports whether a field has a message.
func (e FormErrors) HasError(field string) bool { _, ok := e[field]; return ok }

// Error returns a field's message.
func (e FormErrors) Error(field string) string { return e[field] }

// CardsView is the /cards page.
type CardsView struct {
	Cards []CreditCardView
}

// CardDetailView is /cards/{id}.
type CardDetailView struct {
	Card      CreditCardView
	Movements []CardMovement
	Error     string
}

// CardOpenForm is /cards/new.
type CardOpenForm struct {
	RequestKey   string
	Name         string
	Institution  string
	CurrencyCode string
	Terms        CardTermsFields

	OpeningDebt     string
	OpeningDebtDate string

	Currencies []Currency
	Methods    []PaymentMethod
	FormErrors
}

// CardTermsFields are the terms as a form holds them: strings, as typed.
type CardTermsFields struct {
	ProductName            string
	CreditLimit            string
	StatementDay           string
	DueDays                string
	ProcessingDays         string
	Denomination           string
	DenominationConfirmed  bool
	DefaultPaymentMethodID int64
}

// CardTermsForm is /cards/{id}/edit.
type CardTermsForm struct {
	Card    CreditCardView
	Terms   CardTermsFields
	Methods []PaymentMethod
	FormErrors
}

// CardEntryForm records or corrects a purchase or a charge on a card.
type CardEntryForm struct {
	// Mode is "purchase", "charge" or "charge-edit"; the template shows the
	// kind selector for the two charge modes only.
	Mode          string
	Action        string
	RequestKey    string
	TransactionID int64

	Card            CreditCardView
	Kind            string
	CategoryID      int64
	PaymentMethodID int64
	Amount          string
	Description     string
	EffectiveDate   string

	Categories []Category
	Methods    []PaymentMethod
	Kinds      []string
	FormErrors
}

// IsCharge reports a charge form.
func (f CardEntryForm) IsCharge() bool { return f.Mode == "charge" || f.Mode == "charge-edit" }

func cardTermsFieldsFrom(c CreditCardView) CardTermsFields {
	f := CardTermsFields{
		CreditLimit:           c.CreditLimitFormatted,
		StatementDay:          strconv.Itoa(c.StatementDay),
		ProcessingDays:        strconv.Itoa(c.PaymentProcessingDays),
		Denomination:          c.Denomination,
		DenominationConfirmed: c.DenominationConfirmed,
	}
	if c.ProductName != nil {
		f.ProductName = *c.ProductName
	}
	if c.DueDaysAfterStatement != nil {
		f.DueDays = strconv.Itoa(*c.DueDaysAfterStatement)
	}
	if c.DefaultPaymentMethodID != nil {
		f.DefaultPaymentMethodID = *c.DefaultPaymentMethodID
	}
	return f
}

func cardTermsFieldsFromForm(r *http.Request) CardTermsFields {
	method, _ := formID(r, "default_payment_method_id")
	return CardTermsFields{
		ProductName:            strings.TrimSpace(r.FormValue("product_name")),
		CreditLimit:            strings.TrimSpace(r.FormValue("credit_limit")),
		StatementDay:           strings.TrimSpace(r.FormValue("statement_day")),
		DueDays:                strings.TrimSpace(r.FormValue("due_days")),
		ProcessingDays:         strings.TrimSpace(r.FormValue("processing_days")),
		Denomination:           strings.TrimSpace(r.FormValue("denomination")),
		DenominationConfirmed:  r.FormValue("denomination_confirmed") == "1",
		DefaultPaymentMethodID: method,
	}
}

// termsInput reads the typed terms, recording a message per field that is not
// a number. Ranges are the backend's to judge.
func (f CardTermsFields) termsInput(errs FormErrors) CreditCardTermsInput {
	in := CreditCardTermsInput{
		ProductName:           OptionalString(f.ProductName),
		CreditLimit:           OptionalString(f.CreditLimit),
		Denomination:          f.Denomination,
		DenominationConfirmed: f.DenominationConfirmed,
	}
	if day, err := strconv.Atoi(f.StatementDay); err != nil {
		errs["statement_day"] = "Indica el día de corte (1 a 31)."
	} else {
		in.StatementDay = day
	}
	if f.DueDays != "" {
		if days, err := strconv.Atoi(f.DueDays); err != nil {
			errs["due_days"] = "Indica los días hasta el vencimiento, o déjalo vacío si no los conoces."
		} else {
			in.DueDaysAfterStatement = &days
		}
	}
	if f.ProcessingDays != "" {
		if days, err := strconv.Atoi(f.ProcessingDays); err != nil {
			errs["processing_days"] = "Indica los días hábiles de procesamiento (0 a 10)."
		} else {
			in.PaymentProcessingDays = days
		}
	}
	if f.DefaultPaymentMethodID > 0 {
		id := f.DefaultPaymentMethodID
		in.DefaultPaymentMethodID = &id
	}
	return in
}

// cardErrorMessage puts a backend refusal on the field it concerns.
func cardErrorMessage(err error, errs FormErrors) int {
	switch {
	case errors.Is(err, ErrInvalidRequestKey):
		errs["general"] = "El formulario caducó. Recarga la página y vuelve a enviarlo."
	case errors.Is(err, ErrInvalidAmount):
		errs["amount"] = "El monto no es válido para la moneda de la tarjeta."
	case errors.Is(err, ErrCurrencyNotFound), errors.Is(err, ErrCurrencyInactive):
		errs["currency_code"] = "La moneda elegida no está disponible."
	case errors.Is(err, ErrPaymentMethodNotFound), errors.Is(err, ErrPaymentMethodInactive):
		errs["payment_method_id"] = "El método de pago no está disponible."
	case errors.Is(err, ErrCategoryNotFound), errors.Is(err, ErrCategoryInactive), errors.Is(err, ErrCategoryTypeMismatch):
		errs["category_id"] = "Elige una categoría de egresos activa."
	case errors.Is(err, ErrAccountInactive):
		errs["general"] = "La tarjeta está archivada: no admite movimientos nuevos."
	case errors.Is(err, ErrNotACreditCard):
		errs["general"] = "La cuenta no es una tarjeta de crédito."
	case errors.Is(err, ErrNotACharge):
		errs["general"] = "Un cargo es un egreso de una tarjeta y se mantiene así."
	case errors.Is(err, ErrRefundOriginal):
		errs["original_transaction_id"] = "Una devolución solo puede referirse a un egreso de la misma cuenta."
	case errors.Is(err, ErrRefundExceedsOriginal):
		errs["amount"] = "Las devoluciones de ese egreso superarían su monto."
	case errors.Is(err, ErrCurrencyChangeNotConfirmed):
		errs["account_id"] = "Esa cuenta está en otra moneda: el monto se leería en ella sin convertirse."
		return http.StatusConflict
	case errors.Is(err, ErrInvalidInput):
		errs["general"] = "Revisa los datos: " + err.Error()
	default:
		errs["general"] = "Ocurrió un error inesperado. Intenta de nuevo."
		return http.StatusInternalServerError
	}
	return http.StatusUnprocessableEntity
}

// handleCards serves GET /cards.
func handleCards(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cards, err := svcs.Cards.List(r.Context(), true)
		if err != nil {
			logger.Error("listing cards", slog.Any("error", err))
			http.Error(w, "Failed to load cards", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, http.StatusOK, "cards/index.html",
			PageData{Title: "Tarjetas", ActiveNav: NavCards, Data: CardsView{Cards: cards}})
	}
}

// handleNewCardForm serves GET /cards/new.
func handleNewCardForm(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := newRequestKey()
		if err != nil {
			logger.Error("generating a request key", slog.Any("error", err))
			http.Error(w, "Failed to prepare the form", http.StatusInternalServerError)
			return
		}
		form := CardOpenForm{
			RequestKey:      key,
			CurrencyCode:    "VES",
			OpeningDebtDate: TodayDate(),
			Terms:           CardTermsFields{StatementDay: "25", ProcessingDays: "0", Denomination: "NOMINAL"},
			FormErrors:      FormErrors{},
		}
		if !loadCardOpenOptions(w, r, logger, svcs, &form) {
			return
		}
		render(w, r, logger, renderer, http.StatusOK, "cards/new.html",
			PageData{Title: "Nueva tarjeta", ActiveNav: NavCards, Data: form})
	}
}

func loadCardOpenOptions(w http.ResponseWriter, r *http.Request, logger *slog.Logger, svcs *Services, form *CardOpenForm) bool {
	var err error
	if form.Currencies, err = svcs.Currencies.List(r.Context(), false); err == nil {
		form.Methods, err = svcs.Methods.List(r.Context(), ListPaymentMethodsInput{})
	}
	if err != nil {
		logger.Error("loading the card form", slog.Any("error", err))
		http.Error(w, "Failed to load the form", http.StatusInternalServerError)
		return false
	}
	return true
}

// handleOpenCard serves POST /cards.
func handleOpenCard(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		form := CardOpenForm{
			RequestKey:      r.FormValue("request_key"),
			Name:            strings.TrimSpace(r.FormValue("name")),
			Institution:     strings.TrimSpace(r.FormValue("institution")),
			CurrencyCode:    strings.TrimSpace(r.FormValue("currency_code")),
			Terms:           cardTermsFieldsFromForm(r),
			OpeningDebt:     strings.TrimSpace(r.FormValue("opening_debt")),
			OpeningDebtDate: strings.TrimSpace(r.FormValue("opening_debt_date")),
			FormErrors:      FormErrors{},
		}
		if !loadCardOpenOptions(w, r, logger, svcs, &form) {
			return
		}
		if form.Name == "" {
			form.FormErrors["name"] = "El nombre es obligatorio."
		}
		if form.CurrencyCode == "" {
			form.FormErrors["currency_code"] = "Elige la moneda de la tarjeta."
		}
		terms := form.Terms.termsInput(form.FormErrors)
		if form.OpeningDebt != "" && form.OpeningDebtDate == "" {
			form.FormErrors["opening_debt_date"] = "Indica la fecha de la deuda previa."
		}
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			id, err := svcs.Cards.Open(r.Context(), OpenCreditCardInput{
				RequestKey:      form.RequestKey,
				Name:            form.Name,
				Institution:     OptionalString(form.Institution),
				CurrencyCode:    form.CurrencyCode,
				Terms:           terms,
				OpeningDebt:     OptionalString(form.OpeningDebt),
				OpeningDebtDate: form.OpeningDebtDate,
			})
			if err == nil {
				redirectTo(w, r, cardsPath+"/"+strconv.FormatInt(id, 10))
				return
			}
			logger.Warn("opening a card", slog.Any("error", err))
			status = cardErrorMessage(err, form.FormErrors)
			if errors.Is(err, ErrInvalidAmount) {
				delete(form.FormErrors, "amount")
				form.FormErrors["opening_debt"] = "La deuda previa es un monto positivo en la moneda de la tarjeta (por ejemplo 1250.00)."
			}
		}
		render(w, r, logger, renderer, status, "cards/new.html",
			PageData{Title: "Nueva tarjeta", ActiveNav: NavCards, Data: form})
	}
}

// cardFromPath loads the card a /cards/{id}/... route names, answering 404 for
// an id that is not a card.
func cardFromPath(w http.ResponseWriter, r *http.Request, logger *slog.Logger, svcs *Services) (CreditCardView, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return CreditCardView{}, false
	}
	card, err := svcs.Cards.Get(r.Context(), id)
	switch {
	case errors.Is(err, ErrNotACreditCard), errors.Is(err, ErrNotFound), errors.Is(err, ErrAccountNotFound):
		http.NotFound(w, r)
		return CreditCardView{}, false
	case err != nil:
		logger.Error("loading a card", slog.Any("error", err))
		http.Error(w, "Failed to load the card", http.StatusInternalServerError)
		return CreditCardView{}, false
	}
	return card, true
}

// handleCard serves GET /cards/{id}.
func handleCard(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		view := CardDetailView{Card: card}
		movements, err := svcs.Cards.Movements(r.Context(), card.AccountID, 100)
		if err != nil {
			logger.Error("listing card movements", slog.Any("error", err))
			view.Error = "No se pudieron cargar los movimientos."
		}
		view.Movements = movements
		render(w, r, logger, renderer, http.StatusOK, "cards/show.html",
			PageData{Title: card.Name, ActiveNav: NavCards, Data: view})
	}
}

// handleEditCardTerms serves GET /cards/{id}/edit.
func handleEditCardTerms(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		form := CardTermsForm{Card: card, Terms: cardTermsFieldsFrom(card), FormErrors: FormErrors{}}
		methods, err := svcs.Methods.List(r.Context(), ListPaymentMethodsInput{})
		if err != nil {
			logger.Error("listing payment methods", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		form.Methods = methods
		render(w, r, logger, renderer, http.StatusOK, "cards/edit.html",
			PageData{Title: "Condiciones de " + card.Name, ActiveNav: NavCards, Data: form})
	}
}

// handleUpdateCardTerms serves POST /cards/{id}/edit.
func handleUpdateCardTerms(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		form := CardTermsForm{Card: card, Terms: cardTermsFieldsFromForm(r), FormErrors: FormErrors{}}
		terms := form.Terms.termsInput(form.FormErrors)
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			err := svcs.Cards.UpdateTerms(r.Context(), card.AccountID, terms)
			if err == nil {
				redirectTo(w, r, cardsPath+"/"+strconv.FormatInt(card.AccountID, 10))
				return
			}
			logger.Warn("updating card terms", slog.Any("error", err))
			status = cardErrorMessage(err, form.FormErrors)
		}
		methods, err := svcs.Methods.List(r.Context(), ListPaymentMethodsInput{})
		if err != nil {
			logger.Error("listing payment methods", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		form.Methods = methods
		render(w, r, logger, renderer, status, "cards/edit.html",
			PageData{Title: "Condiciones de " + card.Name, ActiveNav: NavCards, Data: form})
	}
}

// loadEntryOptions fills the lists a purchase or charge form offers: active
// EXPENSE categories and active payment methods.
func loadEntryOptions(r *http.Request, svcs *Services, form *CardEntryForm) error {
	expense := CategoryTypeExpense
	var err error
	if form.Categories, err = svcs.Categories.List(r.Context(), ListCategoriesInput{Type: &expense}); err != nil {
		return err
	}
	form.Methods, err = svcs.Methods.List(r.Context(), ListPaymentMethodsInput{})
	return err
}

// handleNewCardEntry serves GET /cards/{id}/purchases/new and
// GET /cards/{id}/charges/new.
func handleNewCardEntry(logger *slog.Logger, renderer *Renderer, svcs *Services, mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		key, err := newRequestKey()
		if err != nil {
			logger.Error("generating a request key", slog.Any("error", err))
			http.Error(w, "Failed to prepare the form", http.StatusInternalServerError)
			return
		}
		form := CardEntryForm{
			Mode: mode, Action: entryAction(card.AccountID, mode, 0), RequestKey: key, Card: card,
			EffectiveDate: TodayDate(), Kinds: ChargeKinds(), FormErrors: FormErrors{},
		}
		if mode == "charge" {
			form.Kind = "INTEREST"
		}
		if card.DefaultPaymentMethodID != nil {
			form.PaymentMethodID = *card.DefaultPaymentMethodID
		}
		if err := loadEntryOptions(r, svcs, &form); err != nil {
			logger.Error("loading the card entry form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, http.StatusOK, "cards/entry.html",
			PageData{Title: entryTitle(mode), ActiveNav: NavCards, Data: form})
	}
}

func entryAction(cardID int64, mode string, transactionID int64) string {
	base := cardsPath + "/" + strconv.FormatInt(cardID, 10)
	switch mode {
	case "purchase":
		return base + "/purchases"
	case "charge":
		return base + "/charges"
	default:
		return base + "/charges/" + strconv.FormatInt(transactionID, 10)
	}
}

func entryTitle(mode string) string {
	switch mode {
	case "purchase":
		return "Registrar compra"
	case "charge":
		return "Registrar cargo del banco"
	default:
		return "Corregir cargo"
	}
}

func entryFromForm(r *http.Request, card CreditCardView, mode string) CardEntryForm {
	category, _ := formID(r, "category_id")
	method, _ := formID(r, "payment_method_id")
	return CardEntryForm{
		Mode: mode, RequestKey: r.FormValue("request_key"), Card: card,
		Kind:            strings.TrimSpace(r.FormValue("kind")),
		CategoryID:      category,
		PaymentMethodID: method,
		Amount:          strings.TrimSpace(r.FormValue("amount")),
		Description:     strings.TrimSpace(r.FormValue("description")),
		EffectiveDate:   strings.TrimSpace(r.FormValue("effective_date")),
		Kinds:           ChargeKinds(),
		FormErrors:      FormErrors{},
	}
}

func (f CardEntryForm) validate() {
	if f.CategoryID == 0 {
		f.FormErrors["category_id"] = "Elige una categoría de egresos."
	}
	if f.PaymentMethodID == 0 {
		f.FormErrors["payment_method_id"] = "Elige un método de pago."
	}
	if f.Amount == "" {
		f.FormErrors["amount"] = "El monto es obligatorio."
	}
	if f.EffectiveDate == "" {
		f.FormErrors["effective_date"] = "La fecha es obligatoria: la de la operación, no la de hoy."
	}
}

// handleRecordCardEntry serves POST /cards/{id}/purchases and
// POST /cards/{id}/charges.
func handleRecordCardEntry(logger *slog.Logger, renderer *Renderer, svcs *Services, mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		form := entryFromForm(r, card, mode)
		form.Action = entryAction(card.AccountID, mode, 0)
		form.validate()
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			in := RecordExpenseInput{
				AccountID: card.AccountID, CategoryID: form.CategoryID, Amount: form.Amount,
				PaymentMethodID: form.PaymentMethodID, Description: OptionalString(form.Description),
				EffectiveDate: form.EffectiveDate,
			}
			var err error
			if mode == "charge" {
				_, err = svcs.Cards.RecordCharge(r.Context(), form.RequestKey, form.Kind, in)
			} else {
				_, err = svcs.Cards.RecordPurchase(r.Context(), form.RequestKey, in)
			}
			if err == nil {
				redirectTo(w, r, cardsPath+"/"+strconv.FormatInt(card.AccountID, 10))
				return
			}
			logger.Warn("recording on a card", slog.String("mode", mode), slog.Any("error", err))
			status = cardErrorMessage(err, form.FormErrors)
		}
		if err := loadEntryOptions(r, svcs, &form); err != nil {
			logger.Error("loading the card entry form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, status, "cards/entry.html",
			PageData{Title: entryTitle(mode), ActiveNav: NavCards, Data: form})
	}
}

// chargeFromPath loads the charge /cards/{id}/charges/{tid} names: a
// transaction of that card that IS a charge, or 404.
func chargeFromPath(w http.ResponseWriter, r *http.Request, logger *slog.Logger, svcs *Services, card CreditCardView) (TransactionView, string, bool) {
	id, err := strconv.ParseInt(r.PathValue("tid"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return TransactionView{}, "", false
	}
	tx, err := svcs.Ledger.GetTransaction(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return TransactionView{}, "", false
	}
	if err != nil {
		logger.Error("loading a charge", slog.Any("error", err))
		http.Error(w, "Failed to load the charge", http.StatusInternalServerError)
		return TransactionView{}, "", false
	}
	kind, isCharge, err := svcs.Cards.ChargeKind(r.Context(), id)
	if err != nil {
		logger.Error("reading a charge kind", slog.Any("error", err))
		http.Error(w, "Failed to load the charge", http.StatusInternalServerError)
		return TransactionView{}, "", false
	}
	if !isCharge || tx.AccountID != card.AccountID {
		http.NotFound(w, r)
		return TransactionView{}, "", false
	}
	return tx, kind, true
}

// handleEditCardCharge serves GET /cards/{id}/charges/{tid}/edit.
func handleEditCardCharge(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		tx, kind, ok := chargeFromPath(w, r, logger, svcs, card)
		if !ok {
			return
		}
		form := CardEntryForm{
			Mode: "charge-edit", Action: entryAction(card.AccountID, "charge-edit", tx.ID), TransactionID: tx.ID,
			Card: card, Kind: kind, Amount: strings.TrimPrefix(tx.AmountFormatted, "-"),
			EffectiveDate: tx.EffectiveDate.Format("2006-01-02"), Kinds: ChargeKinds(), FormErrors: FormErrors{},
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
		if err := loadEntryOptions(r, svcs, &form); err != nil {
			logger.Error("loading the charge form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, http.StatusOK, "cards/entry.html",
			PageData{Title: entryTitle("charge-edit"), ActiveNav: NavCards, Data: form})
	}
}

// handleUpdateCardCharge serves POST /cards/{id}/charges/{tid}.
func handleUpdateCardCharge(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		tx, _, ok := chargeFromPath(w, r, logger, svcs, card)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		form := entryFromForm(r, card, "charge-edit")
		form.TransactionID = tx.ID
		form.Action = entryAction(card.AccountID, "charge-edit", tx.ID)
		form.validate()
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			_, err := svcs.Cards.UpdateCharge(r.Context(), tx.ID, form.Kind, UpdateTransactionInput{
				AccountID: card.AccountID, Type: TransactionTypeExpense, CategoryID: form.CategoryID,
				PaymentMethodID: form.PaymentMethodID, Amount: form.Amount,
				Description: OptionalString(form.Description), EffectiveDate: form.EffectiveDate,
			})
			if err == nil {
				redirectTo(w, r, cardsPath+"/"+strconv.FormatInt(card.AccountID, 10))
				return
			}
			logger.Warn("correcting a charge", slog.Any("error", err))
			status = cardErrorMessage(err, form.FormErrors)
		}
		if err := loadEntryOptions(r, svcs, &form); err != nil {
			logger.Error("loading the charge form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, status, "cards/entry.html",
			PageData{Title: entryTitle("charge-edit"), ActiveNav: NavCards, Data: form})
	}
}

// render renders a desktop page, logging a failure the response can no longer
// report.
func render(w http.ResponseWriter, r *http.Request, logger *slog.Logger, renderer *Renderer, status int, page string, data PageData) {
	if err := renderer.RenderPage(w, r, status, page, data); err != nil {
		logger.Error("rendering "+page, slog.Any("error", err))
	}
}
