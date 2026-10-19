package web

import (
	"errors"
	"time"
)

// M7.1: the savings policy and its suggestions, as the presentation layer sees
// them. Every amount arrives formatted by the adapter; every "can the user do
// this" arrives decided. A template neither adds two figures nor compares a
// status (docs/m7-contract.md).

// SavingsCurrency is the currency every savings figure is denominated in. It is
// not the valuation base, which the user may change.
const SavingsCurrency = "USD"

var (
	// ErrSavingsRetroactivePolicy: a version after the first would start in the
	// past or before the version in force.
	ErrSavingsRetroactivePolicy = errors.New("a later savings policy version cannot start in the past")
	// ErrSavingsPolicyVersionInUse: the version starting on that date already
	// governs suggestions.
	ErrSavingsPolicyVersionInUse = errors.New("that savings policy version already governs suggestions")
	// ErrSavingsInvalidState: the suggestion's status does not allow the action.
	ErrSavingsInvalidState = errors.New("the savings suggestion does not allow this")
	// ErrSavingsRateUnavailable: no rate for the income's date yet.
	ErrSavingsRateUnavailable = errors.New("no exchange rate is available for the income's date")
	// ErrSavingsNotYetEffective: the income is dated in the future.
	ErrSavingsNotYetEffective = errors.New("the income's date has not arrived yet")
	// ErrSavingsRateUnchanged: a revaluation found the same rate.
	ErrSavingsRateUnchanged = errors.New("the exchange rate of the income's date has not changed")
	// ErrSavingsCategoryNotIncome: eligibility was set on an expense category.
	ErrSavingsCategoryNotIncome = errors.New("only an income category can be eligible for savings")
	// ErrSavingsIncomeNotFound: the transaction is missing or not an income.
	ErrSavingsIncomeNotFound = errors.New("income not found")
)

// SavingsPolicyVersion is one savings rate and the day it starts.
type SavingsPolicyVersion struct {
	ID int64

	// RateFormatted is the percentage with two decimals: "20.00".
	RateFormatted string

	EffectiveFrom   time.Time
	SuggestionCount int
	CreatedAt       time.Time

	// InForce and Scheduled are decided by the backend against today.
	InForce   bool
	Scheduled bool
}

// SavingsPolicy is the history of the savings rate.
type SavingsPolicy struct {
	// Versions are newest first.
	Versions  []SavingsPolicyVersion
	Current   *SavingsPolicyVersion
	Scheduled *SavingsPolicyVersion

	// Start is the first version's start. Nil when no policy was ever set.
	Start *time.Time
}

// IsSet reports whether a policy was ever configured.
func (p SavingsPolicy) IsSet() bool { return p.Start != nil }

// SetSavingsPolicyInput is a new rate from a date on.
type SetSavingsPolicyInput struct {
	// Rate is a percentage such as "20" or "12.5".
	Rate          string
	EffectiveFrom string
}

// SavingsCategoryEligibility is one income category and whether its income is
// saved from. Explicit is false when the decision is the default.
type SavingsCategoryEligibility struct {
	CategoryID int64
	Name       string
	IsActive   bool
	Eligible   bool
	Explicit   bool
}

// SavingsRateLeg is one reference observation a valuation used, as it was then.
type SavingsRateLeg struct {
	ExchangeRateID int64
	// Pair reads "USD/VES": 1 USD = Rate VES.
	Pair          string
	Rate          string
	EffectiveDate time.Time
	SourceLabel   string
	Inverted      bool
}

// SavingsSuggestion is one revision of what the policy says to save from an
// income.
type SavingsSuggestion struct {
	ID                  int64
	IncomeTransactionID int64
	Revision            int

	RateFormatted       string
	PolicyEffectiveFrom time.Time

	IncomeAccountName     string
	IncomeCategoryName    string
	IncomeDescription     *string
	IncomeAmountFormatted string
	IncomeCurrencyCode    string
	IncomeEffectiveDate   time.Time

	// Valued is false while the revision waits for a rate; the USD figures are
	// then empty and must not be shown as zero.
	Valued                bool
	IncomeUSDFormatted    string
	SuggestedUSDFormatted string
	MethodLabel           string
	ValuedAt              *time.Time
	Legs                  []SavingsRateLeg

	// ShowUSDEquivalent: the income is in another currency and has been
	// valued, so its USD figure is worth showing beside it.
	ShowUSDEquivalent bool

	Status       string
	StatusLabel  string
	StatusReason *string
	// BadgeClass styles the status; chosen by the backend, not compared in a
	// template.
	BadgeClass string
	CreatedAt  time.Time

	// PendingLabel explains why a pending revision has no amount.
	PendingLabel string
	// RateChanged: the rate in force on the income's date is no longer the one
	// this valuation used.
	RateChanged bool

	// What reservations confirmed (M7.2). ConfirmedFormatted is this
	// revision's; the Income* figures are the income's, across revisions, on
	// the live one.
	ConfirmedFormatted       string
	IncomeConfirmedFormatted string
	RemainingFormatted       string
	OverConfirmedFormatted   string
	// RemainingAmount is RemainingFormatted without separators, to prefill a
	// form field.
	RemainingAmount   string
	ConfirmationLabel string
	HasConfirmed      bool
	OverConfirmed     bool

	// What the user may do with this revision, decided by the backend.
	Live       bool
	CanDismiss bool
	CanValue   bool
	CanRevalue bool
	CanConfirm bool
}

// SavingsCurrencyAmount is an amount in its own currency, formatted.
type SavingsCurrencyAmount struct {
	CurrencyCode    string
	AmountFormatted string
}

// SavingsOverview is the policy's figures for the current month.
type SavingsOverview struct {
	PeriodFrom time.Time
	PeriodTo   time.Time

	Policy SavingsPolicy

	// USD, formatted. A suggestion belongs to its income's effective date.
	EligibleIncomeFormatted string
	SuggestedFormatted      string
	OpenFormatted           string
	DismissedFormatted      string

	ConfirmedFormatted     string
	OverConfirmedFormatted string
	OverConfirmed          bool
	// ProgressFormatted is confirmed over suggested, as a percentage with two
	// decimals; empty when nothing was suggested. It is NOT the fund's growth.
	ProgressFormatted string

	// The fund's movement in the period, by the operations' dates.
	GrossContributionsFormatted string
	WithdrawalsFormatted        string
	// NetChangeFormatted is signed.
	NetChangeFormatted string

	Fund SavingsFund

	OpenCount             int
	DismissedCount        int
	PendingValuationCount int
	PendingByCurrency     []SavingsCurrencyAmount
	RateChangedCount      int
	UnsyncedCount         int

	// IssueCount is the unacknowledged reconciliation issues; IssueTotal all.
	IssueCount int
	IssueTotal int
}

// SavingsIncomeOverride is an explicit exception for one income.
type SavingsIncomeOverride struct {
	Eligible  bool
	Reason    string
	UpdatedAt time.Time
}

// SavingsIncomeDetail is one income seen from the savings side.
type SavingsIncomeDetail struct {
	TransactionID   int64
	Description     *string
	Date            time.Time
	AccountName     string
	CategoryName    string
	CurrencyCode    string
	AmountFormatted string

	Eligible bool
	Reason   string
	Override *SavingsIncomeOverride

	// Revisions are oldest first.
	Revisions []SavingsSuggestion
}

// SavingsSyncItem is one income the sync would change.
type SavingsSyncItem struct {
	Action          string
	ActionLabel     string
	TransactionID   int64
	Date            time.Time
	Description     *string
	AccountName     string
	CategoryName    string
	CurrencyCode    string
	AmountFormatted string
	// RateFormatted is the policy rate that would apply; empty when the income
	// is being invalidated.
	RateFormatted string
	Reason        string
}

// SavingsSyncPlan is the preview of a sync. Nothing is written until applied.
type SavingsSyncPlan struct {
	Items []SavingsSyncItem
}

// SavingsSyncResult counts what a sync did.
type SavingsSyncResult struct {
	Created     int
	Revised     int
	Invalidated int
}

// SavingsHelp explains one savings term. It carries no figure.
func SavingsHelp(key string) RecapHelp {
	h := RecapHelp{ID: "savings-help-" + key}
	switch key {
	case "policy-rate":
		h.Title = "Tasa de ahorro objetivo"
		h.Text = "El porcentaje de cada ingreso elegible que te propones ahorrar. No es la tasa de ahorro " +
			"del recap, que mide lo que de verdad quedó entre ingresos y gastos. Cambiarla nunca altera las " +
			"sugerencias de ingresos fechados antes del cambio."
	case "eligible-income":
		h.Title = "Ingresos elegibles"
		h.Text = "Los ingresos del mes de los que se calcula el ahorro, convertidos a USD con la tasa vigente en " +
			"la fecha de cada uno. No incluyen transferencias, compras de divisas ni saldos iniciales, ni las " +
			"categorías o ingresos que excluiste."
	case "suggested":
		h.Title = "Ahorro sugerido"
		h.Text = "Lo que la política dice que deberías apartar de cada ingreso. Es un objetivo, no dinero " +
			"reservado: no mueve ningún saldo ni cambia tu patrimonio."
	case "open":
		h.Title = "Pendiente de confirmar"
		h.Text = "Lo que falta por confirmar de las sugerencias que no descartaste. Confirmar es reservar " +
			"dólares de una cuenta en USD contra la sugerencia, todo o una parte."
	case "pending-valuation":
		h.Title = "Sin valorar"
		h.Text = "Ingresos en otra moneda para los que todavía no hay tasa en su fecha, o cuya fecha aún no " +
			"llegó. Su ahorro no se estima ni se cuenta como cero: se valora cuando la tasa exista."
	case "rate-changed":
		h.Title = "Tasa corregida"
		h.Text = "La tasa vigente en la fecha del ingreso ya no es la que se usó al sugerir (una corrección del " +
			"BCV o una tasa manual). La sugerencia no cambia sola; puedes revalorarla."
	case "confirmed":
		h.Title = "Ahorro confirmado"
		h.Text = "Lo que de verdad apartaste contra las sugerencias de los ingresos del mes, cuando sea que lo " +
			"hayas hecho. El porcentaje compara lo confirmado con lo sugerido; no es el crecimiento del fondo."
	case "fund":
		h.Title = "Fondo de ahorro"
		h.Text = "Dólares que marcaste como ahorro dentro de tus cuentas en USD. No es una cuenta nueva: el " +
			"dinero sigue donde está y tu patrimonio no cambia. Cada reserva está respaldada por el saldo de una cuenta."
	case "free":
		h.Title = "USD libres"
		h.Text = "Saldo de tus cuentas en USD menos lo reservado en cada una. Es lo que todavía puedes reservar."
	case "overallocated":
		h.Title = "Sobreasignado"
		h.Text = "Lo reservado que el saldo de su cuenta ya no respalda, por un gasto o una transferencia " +
			"posterior. No se corrige solo: libera o mueve reservas cuando lo decidas."
	case "fund-movement":
		h.Title = "Movimiento del fondo"
		h.Text = "Aportes brutos (todo lo reservado en el mes, con o sin sugerencia), retiros (lo liberado o " +
			"gastado) y su diferencia, por la fecha de cada operación. No es rentabilidad ni crecimiento del patrimonio."
	case "sync":
		h.Title = "Sincronizar"
		h.Text = "Ingresos cuya sugerencia falta, está desactualizada o ya no corresponde: por ejemplo, tras " +
			"fijar un inicio retroactivo o excluir una categoría. Nada cambia hasta que lo confirmas."
	default:
		return RecapHelp{}
	}
	return h
}
