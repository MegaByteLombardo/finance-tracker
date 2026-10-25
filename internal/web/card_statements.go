package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// M8.2 screens: paying a card, its statements and their reconciliation.
//
// What a card owes, what each payment settled and how far the ledger is from
// the bank are derived by the backend on every read (TD-092); these handlers
// only carry them to the templates. The payment form proposes amounts and
// never submits by itself. Reconciliation informs: the only things it writes
// are the user's assertions that the bank billed a movement in a statement.

// PaymentSource is an account a card can be paid from: an active asset.
type PaymentSource struct {
	ID           int64
	Name         string
	Institution  *string
	CurrencyCode string
	MinorUnit    int
}

// PaymentSuggestion is an amount the payment form offers to fill in.
type PaymentSuggestion struct {
	Label  string
	Hint   string
	Amount string
}

// CardPaymentForm is /cards/{id}/payments/new.
type CardPaymentForm struct {
	RequestKey string
	Standing   CardStandingView

	Sources         []PaymentSource
	SourceAccountID int64
	Amount          string
	SourceAmount    string
	Description     string
	EffectiveDate   string

	Suggestions []PaymentSuggestion
	FormErrors
}

// Card is the card being paid.
func (f CardPaymentForm) Card() CreditCardView { return f.Standing.Card }

// selected returns the chosen source, and false when none is.
func (f CardPaymentForm) selected() (PaymentSource, bool) {
	for _, s := range f.Sources {
		if s.ID == f.SourceAccountID {
			return s, true
		}
	}
	return PaymentSource{}, false
}

// SourceCurrency is the chosen source's currency, or "".
func (f CardPaymentForm) SourceCurrency() string {
	s, _ := f.selected()
	return s.CurrencyCode
}

// NeedsSourceAmount reports that the chosen source is in another currency:
// the payment is an exchange and both amounts are entered (TD-055). The shape
// is rendered here, and card_payment.js keeps it in step with the select.
func (f CardPaymentForm) NeedsSourceAmount() bool {
	s, ok := f.selected()
	return ok && s.CurrencyCode != f.Standing.Card.CurrencyCode
}

// paymentSuggestions lists the amounts the backend derived, in the order of
// the line a payment is placed on. A zero is not offered.
func paymentSuggestions(s CardStandingView) []PaymentSuggestion {
	var out []PaymentSuggestion
	if s.PastDueMinor > 0 {
		out = append(out, PaymentSuggestion{Label: "Vencido", Hint: "Pone al día los mínimos atrasados", Amount: s.PastDueFormatted})
	}
	if s.MinimumKnown && s.MinimumPendingMinor > 0 {
		out = append(out, PaymentSuggestion{Label: "Mínimo pendiente", Hint: "Del estado de cuenta vigente", Amount: s.MinimumPendingFormatted})
	}
	if s.BilledPendingMinor > 0 {
		out = append(out, PaymentSuggestion{Label: "Facturado pendiente", Hint: "Cancela el estado de cuenta vigente", Amount: s.BilledPendingFormatted})
	}
	if s.Card.DebtMinor > 0 {
		out = append(out, PaymentSuggestion{Label: "Deuda total", Hint: "Incluye los consumos posteriores al corte", Amount: s.Card.DebtFormatted})
	}
	return out
}

// standingFromPath loads the card a /cards/{id}/... route names with its
// obligations, answering 404 for an id that is not a card.
func standingFromPath(w http.ResponseWriter, r *http.Request, logger *slog.Logger, svcs *Services) (CardStandingView, bool) {
	card, ok := cardFromPath(w, r, logger, svcs)
	if !ok {
		return CardStandingView{}, false
	}
	standing, err := svcs.Cards.Standing(r.Context(), card.AccountID)
	if err != nil {
		logger.Error("deriving a card's obligations", slog.Any("error", err))
		http.Error(w, "Failed to load the card", http.StatusInternalServerError)
		return CardStandingView{}, false
	}
	return standing, true
}

// loadPaymentSources lists the active asset accounts a card can be paid from.
func loadPaymentSources(r *http.Request, svcs *Services) ([]PaymentSource, error) {
	accounts, err := svcs.Accounts.List(r.Context(), false)
	if err != nil {
		return nil, err
	}
	currencies, err := svcs.Currencies.List(r.Context(), true)
	if err != nil {
		return nil, err
	}
	units := make(map[string]int, len(currencies))
	for _, c := range currencies {
		units[c.Code] = c.MinorUnit
	}
	out := make([]PaymentSource, 0, len(accounts))
	for _, a := range accounts {
		if !a.IsActive || a.Type.IsLiability() {
			continue
		}
		out = append(out, PaymentSource{
			ID: a.ID, Name: a.Name, Institution: a.Institution, CurrencyCode: a.CurrencyCode, MinorUnit: units[a.CurrencyCode],
		})
	}
	return out, nil
}

// handleNewCardPayment serves GET /cards/{id}/payments/new.
func handleNewCardPayment(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		standing, ok := standingFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		key, err := newRequestKey()
		if err != nil {
			logger.Error("generating a request key", slog.Any("error", err))
			http.Error(w, "Failed to prepare the form", http.StatusInternalServerError)
			return
		}
		form := CardPaymentForm{
			RequestKey: key, Standing: standing, EffectiveDate: TodayDate(),
			Suggestions: paymentSuggestions(standing), FormErrors: FormErrors{},
		}
		if id, ok := parsePositiveID(r.URL.Query().Get("source_account_id")); ok {
			form.SourceAccountID = id
		}
		if form.Sources, err = loadPaymentSources(r, svcs); err != nil {
			logger.Error("loading the payment form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, http.StatusOK, "cards/payment.html",
			PageData{Title: "Pagar " + standing.Card.Name, ActiveNav: NavCards, Data: form})
	}
}

// handleRecordCardPayment serves POST /cards/{id}/payments.
func handleRecordCardPayment(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		standing, ok := standingFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		source, _ := formID(r, "source_account_id")
		form := CardPaymentForm{
			RequestKey: r.FormValue("request_key"), Standing: standing,
			SourceAccountID: source,
			Amount:          strings.TrimSpace(r.FormValue("amount")),
			SourceAmount:    strings.TrimSpace(r.FormValue("source_amount")),
			Description:     strings.TrimSpace(r.FormValue("description")),
			EffectiveDate:   strings.TrimSpace(r.FormValue("effective_date")),
			Suggestions:     paymentSuggestions(standing),
			FormErrors:      FormErrors{},
		}
		var err error
		if form.Sources, err = loadPaymentSources(r, svcs); err != nil {
			logger.Error("loading the payment form", slog.Any("error", err))
			http.Error(w, "Failed to load the form", http.StatusInternalServerError)
			return
		}
		if _, ok := form.selected(); !ok {
			form.FormErrors["source_account_id"] = "Elige la cuenta desde la que pagas."
		}
		if form.Amount == "" {
			form.FormErrors["amount"] = "Indica cuánto recibe la tarjeta."
		}
		if form.NeedsSourceAmount() && form.SourceAmount == "" {
			form.FormErrors["source_amount"] = "La cuenta está en " + form.SourceCurrency() + ": indica cuánto salió de ella."
		}
		if form.EffectiveDate == "" {
			form.FormErrors["effective_date"] = "La fecha es obligatoria: la del pago, no la de hoy."
		}
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			in := RecordCardPaymentInput{
				RequestKey: form.RequestKey, CardAccountID: standing.Card.AccountID, SourceAccountID: form.SourceAccountID,
				Amount: form.Amount, Description: OptionalString(form.Description), EffectiveDate: form.EffectiveDate,
				Savings: expensePlanFromForm(r),
			}
			if form.NeedsSourceAmount() {
				in.SourceAmount = form.SourceAmount
			}
			_, err := svcs.Cards.RecordPayment(r.Context(), in)
			if err == nil {
				redirectTo(w, r, cardsPath+"/"+strconv.FormatInt(standing.Card.AccountID, 10))
				return
			}
			logger.Warn("recording a card payment", slog.Any("error", err))
			status = paymentErrorMessage(err, form.FormErrors)
		}
		render(w, r, logger, renderer, status, "cards/payment.html",
			PageData{Title: "Pagar " + standing.Card.Name, ActiveNav: NavCards, Data: form})
	}
}

// paymentErrorMessage puts a refused payment's message on its field.
func paymentErrorMessage(err error, errs FormErrors) int {
	if msg := savingsMovementError(err); msg != "" {
		errs["savings"] = msg
		return http.StatusUnprocessableEntity
	}
	var side *TransferAccountError
	switch {
	case errors.As(err, &side) && side.Side == TransferSideSource:
		errs["source_account_id"] = "La cuenta de origen no existe o está archivada."
	case errors.Is(err, ErrSavingsLiabilityAccount):
		errs["savings"] = "El ahorro solo se respalda con cuentas de activo."
	case errors.Is(err, ErrInvalidAmount):
		errs["amount"] = "Revisa los montos: positivos y con los decimales de cada moneda."
	case errors.Is(err, ErrInvalidInput):
		errs["general"] = "Revisa los datos: una tarjeta se paga desde una cuenta de activo, distinta de la tarjeta."
	default:
		return cardErrorMessage(err, errs)
	}
	return http.StatusUnprocessableEntity
}

// --- statements -------------------------------------------------------------

// CardStatementsView is /cards/{id}/statements.
type CardStatementsView struct {
	Standing CardStandingView
	Alert    *FlashAlert
}

// CardStatementForm records or corrects a statement.
type CardStatementForm struct {
	Card        CreditCardView
	StatementID int64
	Action      string
	Fields      CardStatementInput
	FormErrors
}

// MinorUnit is the scale of the card's currency, for the amount fields.
func (f CardStatementForm) MinorUnit() int { return f.Card.MinorUnit }

// handleCardStatements serves GET /cards/{id}/statements.
func handleCardStatements(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		standing, ok := standingFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		view := CardStatementsView{Standing: standing, Alert: cardAlert(r.URL.Query())}
		render(w, r, logger, renderer, http.StatusOK, "cards/statements.html",
			PageData{Title: "Estados de cuenta de " + standing.Card.Name, ActiveNav: NavCards, Data: view})
	}
}

// handleNewCardStatement serves GET /cards/{id}/statements/new. The dates come
// from the query when a computed cycle is registered from the list, otherwise
// from the backend's proposal.
func handleNewCardStatement(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		start, closing, err := svcs.Cards.NextStatementDates(r.Context(), card.AccountID)
		if err != nil {
			logger.Error("proposing statement dates", slog.Any("error", err))
			http.Error(w, "Failed to prepare the form", http.StatusInternalServerError)
			return
		}
		q := r.URL.Query()
		if v := strings.TrimSpace(q.Get("period_start")); v != "" {
			start = v
		}
		if v := strings.TrimSpace(q.Get("closing_date")); v != "" {
			closing = v
		}
		form := CardStatementForm{
			Card: card, Action: statementsPath(card.AccountID),
			Fields: CardStatementInput{PeriodStart: start, ClosingDate: closing}, FormErrors: FormErrors{},
		}
		render(w, r, logger, renderer, http.StatusOK, "cards/statement_form.html",
			PageData{Title: "Registrar estado de cuenta", ActiveNav: NavCards, Data: form})
	}
}

func statementsPath(cardID int64) string {
	return cardsPath + "/" + strconv.FormatInt(cardID, 10) + "/statements"
}

func statementPath(cardID, statementID int64) string {
	return statementsPath(cardID) + "/" + strconv.FormatInt(statementID, 10)
}

func statementFieldsFromForm(r *http.Request) CardStatementInput {
	v := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }
	return CardStatementInput{
		PeriodStart: v("period_start"), ClosingDate: v("closing_date"), DueDate: v("due_date"),
		ClosingOwed: v("closing_owed"), MinimumPayment: v("minimum_payment"), PreviousOwed: v("previous_owed"),
		Payments: v("payments"), Debits: v("debits"), PastDue: v("past_due"),
	}
}

func (f CardStatementForm) validate() {
	if f.Fields.PeriodStart == "" {
		f.FormErrors["period_start"] = "Indica el inicio del período."
	}
	if f.Fields.ClosingDate == "" {
		f.FormErrors["closing_date"] = "Indica la fecha de corte."
	}
	if f.Fields.ClosingOwed == "" {
		f.FormErrors["closing_owed"] = "Indica el saldo al corte que dice el estado de cuenta."
	}
}

// statementErrorMessage puts a refused statement's message on its field.
func statementErrorMessage(err error, errs FormErrors) int {
	switch {
	case errors.Is(err, ErrStatementExists):
		errs["closing_date"] = "Ya registraste otro estado de cuenta con este corte. Corrígelo en lugar de crear uno nuevo."
		return http.StatusConflict
	case errors.Is(err, ErrStatementOverlap):
		errs["period_start"] = "El período se solapa con otro estado de cuenta de la tarjeta: un día solo lo factura un estado."
	case errors.Is(err, ErrAssertionAfterClosing):
		errs["closing_date"] = "Hay movimientos asignados a este estado con fecha posterior a ese corte. Quita esas asignaciones primero."
	case errors.Is(err, ErrInvalidStatement):
		errs["general"] = "Revisa el estado de cuenta: fechas AAAA-MM-DD, inicio no posterior al corte, corte no posterior a hoy, " +
			"vencimiento posterior al corte y montos con los decimales de la moneda (mínimo, pagos, consumos y vencido no negativos)."
	default:
		return cardErrorMessage(err, errs)
	}
	return http.StatusUnprocessableEntity
}

// handleRecordCardStatement serves POST /cards/{id}/statements.
func handleRecordCardStatement(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		form := CardStatementForm{
			Card: card, Action: statementsPath(card.AccountID), Fields: statementFieldsFromForm(r), FormErrors: FormErrors{},
		}
		form.validate()
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			id, err := svcs.Cards.RecordStatement(r.Context(), card.AccountID, form.Fields)
			if err == nil {
				redirectTo(w, r, statementPath(card.AccountID, id)+"/reconcile")
				return
			}
			logger.Warn("recording a statement", slog.Any("error", err))
			status = statementErrorMessage(err, form.FormErrors)
		}
		render(w, r, logger, renderer, status, "cards/statement_form.html",
			PageData{Title: "Registrar estado de cuenta", ActiveNav: NavCards, Data: form})
	}
}

// statementIDFromPath reads {sid}, answering 404 when it is not an id.
func statementIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := parsePositiveID(r.PathValue("sid"))
	if !ok {
		http.NotFound(w, r)
	}
	return id, ok
}

// handleEditCardStatement serves GET /cards/{id}/statements/{sid}/edit.
func handleEditCardStatement(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		sid, ok := statementIDFromPath(w, r)
		if !ok {
			return
		}
		fields, err := svcs.Cards.Statement(r.Context(), card.AccountID, sid)
		switch {
		case errors.Is(err, ErrStatementNotFound):
			http.NotFound(w, r)
			return
		case err != nil:
			logger.Error("loading a statement", slog.Any("error", err))
			http.Error(w, "Failed to load the statement", http.StatusInternalServerError)
			return
		}
		form := CardStatementForm{
			Card: card, StatementID: sid, Action: statementPath(card.AccountID, sid), Fields: fields, FormErrors: FormErrors{},
		}
		render(w, r, logger, renderer, http.StatusOK, "cards/statement_form.html",
			PageData{Title: "Corregir estado de cuenta", ActiveNav: NavCards, Data: form})
	}
}

// handleUpdateCardStatement serves POST /cards/{id}/statements/{sid}.
func handleUpdateCardStatement(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		sid, ok := statementIDFromPath(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		form := CardStatementForm{
			Card: card, StatementID: sid, Action: statementPath(card.AccountID, sid),
			Fields: statementFieldsFromForm(r), FormErrors: FormErrors{},
		}
		form.validate()
		status := http.StatusUnprocessableEntity
		if len(form.FormErrors) == 0 {
			err := svcs.Cards.UpdateStatement(r.Context(), card.AccountID, sid, form.Fields)
			if errors.Is(err, ErrStatementNotFound) {
				http.NotFound(w, r)
				return
			}
			if err == nil {
				redirectTo(w, r, statementPath(card.AccountID, sid)+"/reconcile")
				return
			}
			logger.Warn("correcting a statement", slog.Any("error", err))
			status = statementErrorMessage(err, form.FormErrors)
		}
		render(w, r, logger, renderer, status, "cards/statement_form.html",
			PageData{Title: "Corregir estado de cuenta", ActiveNav: NavCards, Data: form})
	}
}

// CardReconcilePage is /cards/{id}/statements/{sid}/reconcile.
type CardReconcilePage struct {
	Recon CardReconciliationView
	Alert *FlashAlert
}

// handleReconcileCardStatement serves GET /cards/{id}/statements/{sid}/reconcile.
func handleReconcileCardStatement(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		sid, ok := statementIDFromPath(w, r)
		if !ok {
			return
		}
		recon, err := svcs.Cards.Reconcile(r.Context(), card.AccountID, sid)
		switch {
		case errors.Is(err, ErrStatementNotFound):
			http.NotFound(w, r)
			return
		case err != nil:
			logger.Error("reconciling a statement", slog.Any("error", err))
			http.Error(w, "Failed to reconcile the statement", http.StatusInternalServerError)
			return
		}
		render(w, r, logger, renderer, http.StatusOK, "cards/reconcile.html",
			PageData{Title: "Conciliación", ActiveNav: NavCards, Data: CardReconcilePage{Recon: recon, Alert: cardAlert(r.URL.Query())}})
	}
}

// handleAssertMembership serves POST /cards/{id}/statements/{sid}/assertions:
// action=assign says the bank billed the movement in this statement,
// action=clear withdraws an assertion so the calendar decides again.
func handleAssertMembership(logger *slog.Logger, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		card, ok := cardFromPath(w, r, logger, svcs)
		if !ok {
			return
		}
		sid, ok := statementIDFromPath(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		back := statementPath(card.AccountID, sid) + "/reconcile"
		txID, ok := formID(r, "transaction_id")
		if !ok {
			redirectTo(w, r, withQuery(back, "err", "assertion-invalid"))
			return
		}
		var target *int64
		switch r.FormValue("action") {
		case "assign":
			target = &sid
		case "clear":
		default:
			redirectTo(w, r, withQuery(back, "err", "assertion-invalid"))
			return
		}
		if err := svcs.Cards.AssertMembership(r.Context(), card.AccountID, txID, target); err != nil {
			logger.Warn("asserting a statement membership", slog.Any("error", err))
			key := "assertion-invalid"
			if errors.Is(err, ErrAssertionAfterClosing) {
				key = "assertion-after-closing"
			}
			redirectTo(w, r, withQuery(back, "err", key))
			return
		}
		msg := "assigned"
		if target == nil {
			msg = "cleared"
		}
		redirectTo(w, r, withQuery(back, "msg", msg))
	}
}

// cardAlert turns the msg / err of a redirect into the page's flash.
func cardAlert(q url.Values) *FlashAlert {
	switch q.Get("msg") {
	case "assigned":
		return &FlashAlert{Type: "success", Message: "Movimiento asignado a este estado de cuenta. Las cifras se recalcularon."}
	case "cleared":
		return &FlashAlert{Type: "success", Message: "Asignación quitada: el movimiento vuelve a la regla del calendario."}
	}
	switch q.Get("err") {
	case "assertion-after-closing":
		return &FlashAlert{Type: "danger", Message: "Ese movimiento ocurrió después del corte: un estado de cuenta no factura lo que aún no pasó."}
	case "assertion-invalid":
		return &FlashAlert{Type: "danger", Message: "No se pudo asignar ese movimiento a este estado de cuenta."}
	}
	return nil
}
