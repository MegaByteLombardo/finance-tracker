package web

import (
	"context"
	"math/big"
	"strings"
	"time"
)

// The FX_EXCHANGE half of the in-memory stand-in for the backend.
//
// It applies the same rules, in the same order, as internal/ledger's
// resolveExchange, so that a handler tested against this fake meets the same
// errors it will meet in production. Where the ledger reads accounts and
// currencies from SQLite, this reads them from maps; everything else is meant to
// be the same code twice, which is why the two are worth reading side by side.
//
// The arithmetic here is exact and uses math/big, exactly as the ledger's does.
// A fake that computed the execution rate with a float would pass every test in
// this package and prove nothing about the one behaviour M5.5 adds.

// RecordExchange records money exchanged between two accounts in two different
// currencies.
func (fs *FakeServices) RecordExchange(ctx context.Context, in RecordExchangeInput) (TransactionView, error) {
	if err := ctx.Err(); err != nil {
		return TransactionView{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	resolved, err := fs.resolveExchange(in.SourceAccountID, in.SourceAmount,
		in.DestinationAccountID, in.DestinationAmount, in.Description, in.EffectiveDate)
	if err != nil {
		return TransactionView{}, err
	}

	tx := TransactionView{
		ID:            fs.nextTxID,
		Type:          TransactionTypeFXExchange,
		EffectiveDate: resolved.effectiveDate,
		Description:   resolved.description,
		// An exchange has neither, ever.
		CategoryID:      nil,
		PaymentMethodID: nil,
		Exchange:        resolved.detail(fs.nextMovID, fs.nextMovID+1),

		// The movement-centric fields describe the SOURCE side, exactly as the
		// real read model's do: it anchors on the lowest-numbered movement, and
		// that is the one the money left from.
		MovementID:      fs.nextMovID,
		AccountID:       resolved.source.ID,
		AccountName:     resolved.source.Name,
		AccountType:     resolved.source.Type,
		AccountSubtype:  resolved.source.Subtype,
		Institution:     resolved.source.Institution,
		CurrencyCode:    resolved.sourceCurrency.Code,
		CurrencySymbol:  resolved.sourceCurrency.Symbol,
		MinorUnit:       resolved.sourceCurrency.MinorUnit,
		AmountMinor:     -resolved.sourceAmountMinor,
		AmountFormatted: formatAmount(-resolved.sourceAmountMinor, resolved.sourceCurrency.MinorUnit),
		CreatedAt:       time.Now().UTC(),
	}

	fs.nextTxID++
	fs.nextMovID += 2 // an exchange consumes two
	fs.transactions = append(fs.transactions, tx)

	return tx, nil
}

// UpdateExchange corrects an exchange, keeping both movement ids.
func (fs *FakeServices) UpdateExchange(ctx context.Context, id int64, in UpdateExchangeInput) (TransactionView, error) {
	if err := ctx.Err(); err != nil {
		return TransactionView{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	idx := -1
	for i, t := range fs.transactions {
		if t.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return TransactionView{}, ErrNotFound
	}
	existing := fs.transactions[idx]
	// The guard names what is ACCEPTED, per TD-045.
	if existing.Type != TransactionTypeFXExchange || existing.Exchange == nil {
		return TransactionView{}, ErrTypeChangeNotAllowed
	}

	resolved, err := fs.resolveExchange(in.SourceAccountID, in.SourceAmount,
		in.DestinationAccountID, in.DestinationAmount, in.Description, in.EffectiveDate)
	if err != nil {
		return TransactionView{}, err
	}

	updated := existing
	updated.EffectiveDate = resolved.effectiveDate
	updated.Description = resolved.description
	// The movement ids survive an edit: an edit corrects an event, it does not
	// replace it with a different one.
	updated.Exchange = resolved.detail(
		existing.Exchange.SourceMovementID, existing.Exchange.DestinationMovementID)

	updated.MovementID = existing.Exchange.SourceMovementID
	updated.AccountID = resolved.source.ID
	updated.AccountName = resolved.source.Name
	updated.AccountType = resolved.source.Type
	updated.AccountSubtype = resolved.source.Subtype
	updated.Institution = resolved.source.Institution
	updated.CurrencyCode = resolved.sourceCurrency.Code
	updated.CurrencySymbol = resolved.sourceCurrency.Symbol
	updated.MinorUnit = resolved.sourceCurrency.MinorUnit
	updated.AmountMinor = -resolved.sourceAmountMinor
	updated.AmountFormatted = formatAmount(-resolved.sourceAmountMinor, resolved.sourceCurrency.MinorUnit)

	fs.transactions[idx] = updated
	return updated, nil
}

type resolvedFakeExchange struct {
	source                 Account
	sourceCurrency         Currency
	sourceAmountMinor      int64
	destination            Account
	destinationCurrency    Currency
	destinationAmountMinor int64
	effectiveDate          time.Time
	description            *string
}

// detail builds the read model for two given movement ids.
func (r resolvedFakeExchange) detail(sourceMovementID, destinationMovementID int64) *ExchangeDetail {
	return &ExchangeDetail{
		SourceMovementID:      sourceMovementID,
		SourceAccountID:       r.source.ID,
		SourceAccountName:     r.source.Name,
		SourceCurrencyCode:    r.sourceCurrency.Code,
		SourceCurrencySymbol:  r.sourceCurrency.Symbol,
		SourceMinorUnit:       r.sourceCurrency.MinorUnit,
		SourceAmountMinor:     r.sourceAmountMinor,
		SourceAmountFormatted: formatAmount(r.sourceAmountMinor, r.sourceCurrency.MinorUnit),

		DestinationMovementID:      destinationMovementID,
		DestinationAccountID:       r.destination.ID,
		DestinationAccountName:     r.destination.Name,
		DestinationCurrencyCode:    r.destinationCurrency.Code,
		DestinationCurrencySymbol:  r.destinationCurrency.Symbol,
		DestinationMinorUnit:       r.destinationCurrency.MinorUnit,
		DestinationAmountMinor:     r.destinationAmountMinor,
		DestinationAmountFormatted: formatAmount(r.destinationAmountMinor, r.destinationCurrency.MinorUnit),

		ExecutionRate: fakeExecutionRate(
			r.sourceCurrency.Code, r.sourceCurrency.MinorUnit, r.sourceAmountMinor,
			r.destinationCurrency.Code, r.destinationCurrency.MinorUnit, r.destinationAmountMinor),
	}
}

// resolveExchange applies the same rules, in the same order, as the ledger's
// resolveExchange.
//
// The caller already holds the lock.
func (fs *FakeServices) resolveExchange(
	sourceID int64, sourceAmount string,
	destinationID int64, destinationAmount string,
	description *string, effectiveDate string,
) (resolvedFakeExchange, error) {
	var desc *string
	if description != nil {
		trimmed := strings.TrimSpace(*description)
		if trimmed == "" || len(trimmed) > 255 {
			return resolvedFakeExchange{}, ErrInvalidInput
		}
		desc = &trimmed
	}

	source, ok := fs.accounts[sourceID]
	if !ok {
		return resolvedFakeExchange{}, &AccountSideError{
			Side: MovementSideSource, AccountID: sourceID, Err: ErrAccountNotFound}
	}
	if !source.IsActive {
		return resolvedFakeExchange{}, &AccountSideError{
			Side: MovementSideSource, AccountID: sourceID, Err: ErrAccountInactive}
	}

	destination, ok := fs.accounts[destinationID]
	if !ok {
		return resolvedFakeExchange{}, &AccountSideError{
			Side: MovementSideDestination, AccountID: destinationID, Err: ErrAccountNotFound}
	}
	if !destination.IsActive {
		return resolvedFakeExchange{}, &AccountSideError{
			Side: MovementSideDestination, AccountID: destinationID, Err: ErrAccountInactive}
	}

	if source.ID == destination.ID {
		return resolvedFakeExchange{}, ErrSameAccount
	}
	// The mirror image of the transfer rule, and the one that keeps the two
	// operations from collapsing into one.
	if source.CurrencyCode == destination.CurrencyCode {
		return resolvedFakeExchange{}, ErrSameCurrency
	}

	sourceCurrency := fs.currencies[source.CurrencyCode]
	destinationCurrency := fs.currencies[destination.CurrencyCode]

	// Each amount is read at ITS OWN currency's scale. There is no shared one.
	sourceMinor, err := parseAmount(sourceAmount, sourceCurrency.MinorUnit)
	if err != nil || sourceMinor <= 0 {
		return resolvedFakeExchange{}, ErrInvalidAmount
	}
	destinationMinor, err := parseAmount(destinationAmount, destinationCurrency.MinorUnit)
	if err != nil || destinationMinor <= 0 {
		return resolvedFakeExchange{}, ErrInvalidAmount
	}

	// The two magnitudes are deliberately not compared with each other.

	date, err := time.Parse("2006-01-02", effectiveDate)
	if err != nil {
		return resolvedFakeExchange{}, ErrInvalidInput
	}

	return resolvedFakeExchange{
		source: source, sourceCurrency: sourceCurrency, sourceAmountMinor: sourceMinor,
		destination: destination, destinationCurrency: destinationCurrency,
		destinationAmountMinor: destinationMinor,
		effectiveDate:          date, description: desc,
	}, nil
}

// fakeExecutionRate mirrors internal/ledger's executionRate: exact rational
// arithmetic, the same orientation rule, and the smallest scale that states the
// value.
//
// It is duplicated rather than imported because this package must not depend on
// the domain — that is the whole point of the adapter — and because a fake that
// shared the implementation would stop being an independent statement of what
// the backend promises.
func fakeExecutionRate(
	sourceCode string, sourceMinorUnit int, sourceAmountMinor int64,
	destinationCode string, destinationMinorUnit int, destinationAmountMinor int64,
) ExecutionRate {
	sourceValue := new(big.Rat).SetFrac(
		big.NewInt(sourceAmountMinor), pow10Int(sourceMinorUnit))
	destinationValue := new(big.Rat).SetFrac(
		big.NewInt(destinationAmountMinor), pow10Int(destinationMinorUnit))

	if sourceValue.Sign() <= 0 || destinationValue.Sign() <= 0 {
		return ExecutionRate{}
	}

	perDestination := new(big.Rat).Quo(sourceValue, destinationValue)
	perSource := new(big.Rat).Quo(destinationValue, sourceValue)

	base, quote, chosen := destinationCode, sourceCode, perDestination
	switch {
	case sourceCode == "USD":
		base, quote, chosen = sourceCode, destinationCode, perSource
	case destinationCode == "USD":
		base, quote, chosen = destinationCode, sourceCode, perDestination
	case perDestination.Cmp(big.NewRat(1, 1)) < 0:
		base, quote, chosen = sourceCode, destinationCode, perSource
	}

	formatted, exact := ratToDecimalString(chosen, 12)
	return ExecutionRate{
		BaseCurrencyCode:  base,
		QuoteCurrencyCode: quote,
		RateFormatted:     formatted,
		Exact:             exact,
	}
}

// ratToDecimalString renders an exact rational at the smallest scale that states
// it, rounding half to even at maxScale when there is no such scale.
func ratToDecimalString(r *big.Rat, maxScale int) (string, bool) {
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	num.Mul(num, pow10Int(maxScale))

	quotient, remainder := new(big.Int).QuoRem(num, den, new(big.Int))
	exact := remainder.Sign() == 0
	scale := maxScale

	if exact {
		ten := big.NewInt(10)
		for scale > 0 {
			trimmed, rest := new(big.Int).QuoRem(quotient, ten, new(big.Int))
			if rest.Sign() != 0 {
				break
			}
			quotient, scale = trimmed, scale-1
		}
	} else {
		twice := new(big.Int).Abs(remainder)
		twice.Lsh(twice, 1)
		if cmp := twice.Cmp(den); cmp > 0 || (cmp == 0 && quotient.Bit(0) == 1) {
			quotient.Add(quotient, big.NewInt(int64(num.Sign())))
		}
	}

	digits := quotient.String()
	sign := ""
	if rest, found := strings.CutPrefix(digits, "-"); found {
		sign, digits = "-", rest
	}
	if scale == 0 {
		return sign + digits, exact
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	split := len(digits) - scale
	return sign + digits[:split] + "." + digits[split:], exact
}

func pow10Int(n int) *big.Int {
	if n <= 0 {
		return big.NewInt(1)
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
