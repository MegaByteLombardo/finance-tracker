package web

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The ticker tape under the header (TD-096).
//
// It is a READOUT of figures other screens already compute: the month in
// progress from the recap (against the same days of the previous month, TD-072),
// the savings fund and the latest movements. It computes nothing, compares no two
// amounts and stores nothing. Which groups are shown, in what order, in what
// colour and at what speed is the viewer's choice and lives in their browser's
// localStorage; the server always renders every group, in catalogue order.

// TickerTone is how a figure reads, decided here from what the backend already
// decided -- a change's sign, a category's direction, a transaction's type -- and
// never from comparing two amounts.
type TickerTone string

const (
	TickerToneNeutral TickerTone = "neutral"
	TickerToneGood    TickerTone = "good"
	TickerToneBad     TickerTone = "bad"
)

// TickerArrow is which way a figure moved.
type TickerArrow string

const (
	TickerArrowNone TickerArrow = ""
	TickerArrowUp   TickerArrow = "up"
	TickerArrowDown TickerArrow = "down"
)

// TickerItem is one entry on the tape. Every string is already formatted.
type TickerItem struct {
	Code  string
	Value string
	Unit  string
	Delta string
	Arrow TickerArrow
	// Tone colours the arrow and the delta: how the CHANGE reads.
	Tone TickerTone
	// ValueTone colours the figure itself, when its sign alone says how it
	// reads: an expense, an income, a negative cash flow. Neutral otherwise.
	ValueTone TickerTone
	Note      string
	Href      string
}

// TickerGroup is what the viewer toggles: one catalogue entry and the items it
// produced. FX, CASH, CAT and TX produce several.
type TickerGroup struct {
	Key   string
	Items []TickerItem
}

// TickerCatalogueEntry names a group for the settings screen.
type TickerCatalogueEntry struct {
	Key         string
	Label       string
	Description string
}

// TickerCatalogue is every group, in the default order. The browser's stored
// order refers to these keys; a key it does not know is ignored, and a group it
// has never seen is shown at the end.
func TickerCatalogue() []TickerCatalogueEntry {
	return []TickerCatalogueEntry{
		{"NWTH", "Patrimonio", "Patrimonio consolidado de hoy y su cambio en el mes."},
		{"DEBT", "Deuda", "Lo que se debe en tarjetas de crédito, hoy."},
		{"CFLOW", "Flujo de caja", "Ingresos menos gastos del mes en curso."},
		{"SRATE", "Tasa de ahorro", "Proporción del ingreso del mes que no se gastó."},
		{"BURN", "Gasto diario", "Gasto promedio por día del mes en curso."},
		{"FX", "Tasas de referencia", "Las tasas que valoran hoy el patrimonio."},
		{"CASH", "Saldo por divisa", "Lo que hay en cada moneda, sin convertir."},
		{"FUND", "Fondo de ahorro", "Dinero reservado en el fondo de ahorro (USD)."},
		{"CAT", "Categorías", "Las tres categorías de gasto que más se movieron en el mes."},
		{"TX", "Movimientos recientes", "Las últimas ocho transacciones."},
	}
}

// TickerView is the tape's content. Empty means there is nothing to show at all.
type TickerView struct {
	Groups []TickerGroup
}

// Empty reports a tape with no item.
func (v TickerView) Empty() bool {
	for _, g := range v.Groups {
		if len(g.Items) > 0 {
			return false
		}
	}
	return true
}

// TickerSettingsView is the settings screen. Everything on it is applied by the
// browser; the server only lists what exists.
type TickerSettingsView struct {
	Catalogue []TickerCatalogueEntry
}

const tickerRecentTransactions = 8
const tickerCategories = 3

// handleTicker serves the tape's content (GET /ticker), loaded by htmx once the
// page has rendered. A source that fails leaves its groups out and is logged;
// the tape never answers with an error, because a screen must never break for
// it.
func handleTicker(logger *slog.Logger, renderer *Renderer, svcs *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view := loadTicker(r, logger, svcs)
		if err := renderer.RenderPartial(w, r, http.StatusOK, "components/ticker_tape.html", "ticker_tape", view); err != nil {
			logger.Error("rendering the ticker", slog.Any("error", err))
		}
	}
}

// handleTickerSettings serves the screen that chooses what the tape shows
// (GET /settings/ticker).
func handleTickerSettings(logger *slog.Logger, renderer *Renderer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pageData := PageData{
			Title:     "Ticker",
			ActiveNav: NavSettings,
			Data:      TickerSettingsView{Catalogue: TickerCatalogue()},
		}
		if err := renderer.RenderPage(w, r, http.StatusOK, "settings/ticker.html", pageData); err != nil {
			logger.Error("rendering the ticker settings", slog.Any("error", err))
		}
	}
}

func loadTicker(r *http.Request, logger *slog.Logger, svcs *Services) TickerView {
	ctx := r.Context()
	groups := map[string][]TickerItem{}

	if svcs.Analytics != nil {
		today := time.Now().In(economicLocation)
		recap, err := svcs.Analytics.Recap(ctx, RecapRequest{
			Granularity: RecapMonth,
			Anchor:      time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC),
			HasAnchor:   true,
		})
		if err != nil {
			logger.Warn("loading the ticker's month", slog.Any("error", err))
		} else {
			tickerRecapGroups(groups, recap)
		}
	}

	if svcs.Savings != nil {
		if o, err := svcs.Savings.Overview(ctx); err != nil {
			logger.Warn("loading the ticker's savings", slog.Any("error", err))
		} else if o.Policy.IsSet() || o.Fund.CanRelease {
			groups["FUND"] = []TickerItem{{
				Code: "FUND", Value: formatAmountString(o.Fund.ReservedFormatted), Unit: "USD",
				Tone: TickerToneNeutral, Href: "/savings/fund",
			}}
		}
	}

	if txs, err := svcs.Ledger.ListTransactions(ctx, ListTransactionsInput{Limit: tickerRecentTransactions}); err != nil {
		logger.Warn("loading the ticker's transactions", slog.Any("error", err))
	} else {
		for _, tx := range txs {
			groups["TX"] = append(groups["TX"], tickerTransaction(tx))
		}
	}

	var view TickerView
	for _, entry := range TickerCatalogue() {
		if items := groups[entry.Key]; len(items) > 0 {
			view.Groups = append(view.Groups, TickerGroup{Key: entry.Key, Items: items})
		}
	}
	return view
}

// tickerRecapGroups reads the month in progress. Closing is the net worth at
// the close of today, with its breakdown and the rates that valued it.
func tickerRecapGroups(groups map[string][]TickerItem, recap Recap) {
	worth := recap.Closing
	base := worth.BaseCurrencyCode
	if len(worth.Accounts) == 0 {
		// No account, no figure worth a tape: an empty ledger's zeros say
		// nothing.
		return
	}

	nwth := TickerItem{
		Code: "NWTH", Value: formatAmountString(worth.TotalFormatted), Unit: base, Href: "/",
	}
	nwth.Delta, nwth.Arrow, nwth.Tone = tickerChange(recap.Change, false)
	if !worth.Complete {
		nwth.Note = "PARCIAL"
	}
	groups["NWTH"] = []TickerItem{nwth}

	for _, a := range worth.Accounts {
		if a.IsLiability() {
			debt := TickerItem{
				Code: "DEBT", Value: formatAmountString(worth.LiabilitiesFormatted), Unit: base,
				Tone: TickerToneNeutral, Href: "/cards",
			}
			if !worth.Complete {
				debt.Note = "PARCIAL"
			}
			groups["DEBT"] = []TickerItem{debt}
			break
		}
	}

	cf := recap.CashFlow
	cflow := TickerItem{Code: "CFLOW", Value: tickerSigned(cf.NetFormatted, cf.NetMinor), Unit: recap.BaseCurrencyCode, Href: "/recap"}
	cflow.Delta, cflow.Arrow, cflow.Tone = tickerChange(recap.NetCashFlowChange, false)
	cflow.ValueTone = tickerSignTone(cf.NetMinor)
	if !cf.Complete {
		cflow.Note = "PARCIAL"
	}
	groups["CFLOW"] = []TickerItem{cflow}

	srate := TickerItem{Code: "SRATE", Value: "—", Href: "/recap"}
	if cf.SavingsRate.Defined {
		srate.Value = cf.SavingsRate.Formatted + "%"
	}
	if c := recap.SavingsRateChange; c.Defined {
		srate.Delta = tickerSignedRatio(c) + " pts"
		srate.Arrow, srate.Tone = tickerRatioDirection(c, false)
	}
	groups["SRATE"] = []TickerItem{srate}

	burn := TickerItem{
		Code: "BURN", Value: formatAmountString(recap.ExpenseDailyAverageFormatted), Unit: recap.BaseCurrencyCode + "/día", Href: "/recap",
	}
	if burn.Value == "" {
		burn.Value, burn.Unit = "—", ""
	}
	if c := recap.ExpenseDailyAverageChange; c.Defined && c.Percent.Defined {
		burn.Delta = tickerSignedRatio(c.Percent) + "%"
		burn.Arrow, burn.Tone = tickerRatioDirection(c.Percent, true)
	}
	groups["BURN"] = []TickerItem{burn}

	seen := map[string]bool{}
	for _, comp := range worth.Components {
		if comp.Resolution == nil {
			continue
		}
		for _, leg := range comp.Resolution.Legs {
			if seen[leg.Pair] {
				continue
			}
			seen[leg.Pair] = true
			groups["FX"] = append(groups["FX"], TickerItem{
				Code: leg.Pair, Value: formatAmountString(leg.RateShort), Note: strings.ToUpper(leg.SourceLabel),
				Tone: TickerToneNeutral, Href: "/settings/exchange-rates",
			})
		}
	}
	for _, comp := range worth.Components {
		if comp.BalanceMinor == 0 {
			continue
		}
		groups["CASH"] = append(groups["CASH"], TickerItem{
			Code: "CASH." + comp.CurrencyCode, Value: formatAmountString(comp.BalanceFormatted),
			Tone: TickerToneNeutral, Href: "/accounts",
		})
	}

	// The recap lists categories by the size of their movement, largest first.
	for _, c := range recap.ExpenseCategories {
		if len(groups["CAT"]) == tickerCategories {
			break
		}
		item := TickerItem{
			Code: strings.ToUpper(c.CategoryName), Value: formatAmountString(c.CurrentFormatted),
			Unit: recap.BaseCurrencyCode, Href: "/recap",
		}
		switch c.Direction {
		case DirectionUp:
			item.Arrow, item.Tone = TickerArrowUp, TickerToneBad
		case DirectionDown:
			item.Arrow, item.Tone = TickerArrowDown, TickerToneGood
		case DirectionNew:
			item.Arrow, item.Tone, item.Note = TickerArrowUp, TickerToneBad, "NUEVA"
		case DirectionGone:
			item.Arrow, item.Tone = TickerArrowDown, TickerToneGood
		case DirectionSame, DirectionUnknown:
			item.Tone = TickerToneNeutral
		}
		if c.Change.Defined && c.Change.Percent.Defined && c.Direction != DirectionNew {
			item.Delta = tickerSignedRatio(c.Change.Percent) + "%"
		}
		groups["CAT"] = append(groups["CAT"], item)
	}
}

// tickerChange reads a change the backend computed. A rise is good unless
// riseIsBad.
func tickerChange(c Change, riseIsBad bool) (string, TickerArrow, TickerTone) {
	if !c.Defined {
		return "", TickerArrowNone, TickerToneNeutral
	}
	delta := tickerSigned(c.AmountFormatted, c.AmountMinor)
	if c.Percent.Defined {
		delta += " (" + tickerSignedRatio(c.Percent) + "%)"
	}
	switch {
	case c.IsPositive():
		return delta, TickerArrowUp, tickerTone(!riseIsBad)
	case c.IsNegative():
		return delta, TickerArrowDown, tickerTone(riseIsBad)
	default:
		return delta, TickerArrowNone, TickerToneNeutral
	}
}

func tickerRatioDirection(r Ratio, riseIsBad bool) (TickerArrow, TickerTone) {
	switch {
	case r.IsPositive():
		return TickerArrowUp, tickerTone(!riseIsBad)
	case r.IsNegative():
		return TickerArrowDown, tickerTone(riseIsBad)
	default:
		return TickerArrowNone, TickerToneNeutral
	}
}

// tickerSignTone reads a figure by its sign: below zero is bad, above is good.
func tickerSignTone(minor int64) TickerTone {
	switch {
	case minor < 0:
		return TickerToneBad
	case minor > 0:
		return TickerToneGood
	default:
		return TickerToneNeutral
	}
}

func tickerTone(good bool) TickerTone {
	if good {
		return TickerToneGood
	}
	return TickerToneBad
}

// tickerSigned prefixes a "+" to a positive formatted amount; a negative one
// already carries its "-".
func tickerSigned(formatted string, minor int64) string {
	amount := formatAmountString(formatted)
	if minor > 0 && !strings.HasPrefix(amount, "+") {
		return "+" + amount
	}
	return amount
}

func tickerSignedRatio(r Ratio) string {
	if r.IsPositive() {
		return "+" + r.Formatted
	}
	return r.Formatted
}

// tickerTransaction is one movement in the classic "TX: Uber -14.50 [TRANS]"
// shape. Each type it knows is named; anything else is shown neutrally, never
// mistaken for one of them (TD-045).
func tickerTransaction(tx TransactionView) TickerItem {
	item := TickerItem{
		Code: "TX",
		Href: "/transactions/" + strconv.FormatInt(tx.ID, 10) + "/edit",
		Tone: TickerToneNeutral,
	}
	label := ""
	if tx.Description != nil {
		label = strings.TrimSpace(*tx.Description)
	}
	if tx.CategoryName != nil {
		item.Note = strings.ToUpper(*tx.CategoryName)
	}

	switch tx.Type {
	case TransactionTypeIncome:
		item.Value, item.Unit, item.Tone = tickerSigned(tx.AmountFormatted, tx.AmountMinor), tx.CurrencyCode, TickerToneGood
	case TransactionTypeRefund:
		item.Value, item.Unit, item.Tone = tickerSigned(tx.AmountFormatted, tx.AmountMinor), tx.CurrencyCode, TickerToneGood
		if item.Note == "" {
			item.Note = "REEMBOLSO"
		}
	case TransactionTypeExpense:
		item.Value, item.Unit, item.Tone = formatAmountString(tx.AmountFormatted), tx.CurrencyCode, TickerToneBad
	case TransactionTypeOpeningBalance:
		item.Value, item.Unit, item.Note = formatAmountString(tx.AmountFormatted), tx.CurrencyCode, "SALDO INICIAL"
	case TransactionTypeTransfer:
		item.Href = "/transfers/" + strconv.FormatInt(tx.ID, 10) + "/edit"
		item.Note = "TRANSFERENCIA"
		if tx.Transfer != nil {
			item.Value, item.Unit = formatAmountString(tx.Transfer.AmountFormatted), tx.CurrencyCode
			if label == "" {
				label = tx.Transfer.SourceAccountName + " → " + tx.Transfer.DestinationAccountName
			}
		}
	case TransactionTypeFXExchange:
		item.Href = "/exchanges/" + strconv.FormatInt(tx.ID, 10) + "/edit"
		item.Note = "CAMBIO"
		if tx.Exchange != nil {
			item.Value = formatAmountString(tx.Exchange.SourceAmountFormatted) + " " + tx.Exchange.SourceCurrencyCode +
				" → " + formatAmountString(tx.Exchange.DestinationAmountFormatted)
			item.Unit = tx.Exchange.DestinationCurrencyCode
		}
	default:
		item.Value, item.Unit = formatAmountString(tx.AmountFormatted), tx.CurrencyCode
	}

	if label == "" {
		label = tx.AccountName
	}
	// A movement has no change to colour, so its own figure carries the tone.
	item.ValueTone = item.Tone
	item.Code = "TX " + label
	if item.Value == "" {
		item.Value = "—"
	}
	return item
}
