// Package ledger owns the movement of money: what happened, and what it did to
// an account.
//
// It holds two concepts rather than one table:
//
//   - a transaction is the financial EVENT — its type, category, channel,
//     description and effective date;
//   - an account movement is the IMPACT of that event on one account, a signed
//     amount in that account's currency.
//
// They are separate because a transfer between two of the user's own accounts
// is one event with two impacts. That is a later milestone, but a flat
// transactions(account_id, amount) table would have to be migrated away from
// when it arrives, and migrating a ledger is exactly the thing worth avoiding.
// In M2 every transaction has exactly one movement.
//
// This package is the only place monetary amounts exist. Balances are never
// stored: they are SUM(amount_minor) over the movements, computed when read.
package ledger

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// Errors this package returns. Callers distinguish them with errors.Is.
//
// The account and category errors are declared here, not re-exported from those
// packages, so that a handler dealing with transactions never has to import
// account or category to interpret what the ledger returned. That this package
// consults them is an implementation detail and must not leak.
var (
	// ErrNotFound means no transaction exists with the given id.
	ErrNotFound = errors.New("transaction not found")
	// ErrInvalidInput means the input failed validation: a malformed date, an
	// unknown payment method, a description that is present but empty.
	ErrInvalidInput = errors.New("invalid transaction")
	// ErrInvalidAmount means the amount could not be read exactly: unparseable,
	// more decimals than the currency has, out of range, or signed where the
	// use case does not allow it.
	//
	// It is separate from ErrInvalidInput so that a form can highlight the
	// amount field specifically. A handler that treats all validation alike
	// should check for both.
	ErrInvalidAmount = errors.New("invalid amount")
	// ErrAccountNotFound means the account the movement would land in does not
	// exist.
	ErrAccountNotFound = errors.New("transaction account not found")
	// ErrAccountInactive means the account exists but has been archived, so no
	// new movement may be recorded against it.
	ErrAccountInactive = errors.New("transaction account is inactive")
	// ErrSameAccount means a transfer or an exchange named the same account as
	// its source and its destination.
	//
	// Such a transfer moves nothing: its two movements would cancel out in the
	// same balance. An exchange is worse than a no-op — it would claim money
	// changed currency without leaving the account that holds only one. Both are
	// refused rather than accepted, because a user who submitted one meant to
	// move money somewhere.
	ErrSameAccount = errors.New("the source and the destination must be two different accounts")
	// ErrCurrencyMismatch means a TRANSFER's two accounts are denominated in
	// different currencies.
	//
	// A transfer moves one sum of money, so it has one amount and one currency;
	// two currencies would need two amounts, and that operation is an
	// FX_EXCHANGE, recorded through RecordExchange. TRANSFER was not widened to
	// accept it: the two are different financial events, and one of them leaves
	// the total held in its currency unchanged while the other does not.
	ErrCurrencyMismatch = errors.New("a transfer needs both accounts in the same currency")
	// ErrSameCurrency means an FX_EXCHANGE's two accounts are denominated in the
	// SAME currency.
	//
	// It is ErrCurrencyMismatch's mirror image, and it exists for the same
	// reason: an exchange between two accounts in one currency is not an
	// exchange. Moving money between two accounts that share a currency is a
	// TRANSFER, recorded through RecordTransfer, and accepting it here would let
	// two movements of unequal magnitude sum to something other than zero inside
	// a single currency — money created or destroyed, silently.
	ErrSameCurrency = errors.New("an exchange needs the two accounts in different currencies")
	// ErrInconsistentExchange means a stored FX_EXCHANGE is not the shape this
	// package guarantees: two movements, opposite signs, two different accounts,
	// two different currencies.
	//
	// Like ErrInconsistentTransfer it is an integrity failure rather than a
	// validation failure, and it is reported rather than papered over. Note what
	// is NOT among the rules: the two magnitudes are unrelated. 8,500,000
	// céntimos and 10,000 cents are the same exchange, and comparing them would
	// be comparing two different kinds of thing.
	ErrInconsistentExchange = errors.New("stored exchange is inconsistent")
	// ErrInconsistentTransfer means a stored TRANSFER is not the shape this
	// package guarantees: two movements, opposite signs, equal magnitude, two
	// different accounts.
	//
	// It is an integrity failure rather than a validation failure. No input can
	// produce it — every write goes through one transaction that establishes the
	// shape — so seeing it means the database was modified by something else.
	// It is returned rather than papered over: presenting half a transfer as if
	// it were whole is worse than refusing to present it.
	ErrInconsistentTransfer = errors.New("stored transfer is inconsistent")
	// ErrCategoryNotFound means the requested category does not exist.
	ErrCategoryNotFound = errors.New("transaction category not found")
	// ErrCategoryInactive means the category exists but has been archived, so
	// it cannot classify a new transaction.
	ErrCategoryInactive = errors.New("transaction category is inactive")
	// ErrCategoryTypeMismatch means an expense was filed under an income
	// category, or the reverse.
	ErrCategoryTypeMismatch = errors.New("category type does not match the transaction type")
	// ErrPaymentMethodNotFound means the requested payment method does not
	// exist.
	ErrPaymentMethodNotFound = errors.New("transaction payment method not found")
	// ErrPaymentMethodInactive means the payment method exists but has been
	// archived, so it cannot be used for new or edited transactions.
	ErrPaymentMethodInactive = errors.New("transaction payment method is inactive")
	// ErrTypeChangeNotAllowed means an edit tried to convert a transaction to or
	// from OPENING_BALANCE.
	//
	// Income and expenses convert freely between each other -- entering one as
	// the other is a plausible mistake. An opening balance is different in kind:
	// it states where an account started rather than recording an operation, and
	// turning one into income would put it in every "what did I earn" figure.
	ErrTypeChangeNotAllowed = errors.New("this transaction type cannot be changed")
	// ErrCurrencyChangeNotConfirmed means an edit would move a transaction to an
	// account denominated in a different currency without saying so explicitly.
	//
	// It is the one error here that means "ask the user and try again" rather
	// than "the input was wrong". See UpdateTransactionInput.AllowCurrencyChange.
	ErrCurrencyChangeNotConfirmed = errors.New("moving this transaction changes its currency")
)

// MovementSide names which end of a two-sided transaction something refers to.
//
// Two types have two ends: a TRANSFER and an FX_EXCHANGE. The idea is the same
// for both — money left one account and arrived in another — so there is one
// type for it rather than one per transaction type.
type MovementSide string

const (
	// SourceSide is the account money leaves.
	SourceSide MovementSide = "source"
	// DestinationSide is the account money arrives in.
	DestinationSide MovementSide = "destination"
)

// TransferSide is the name M4 froze for MovementSide, kept because
// docs/m4-contract.md is part of the contract. It is the SAME type, not a
// parallel one, so nothing has to convert between them.
type TransferSide = MovementSide

// AccountSideError says which of a two-sided transaction's accounts was
// rejected.
//
// A transfer form, and an exchange form, each have two account fields, and
// "account not found" alone cannot tell either which one to mark. Rather than
// split ErrAccountNotFound and ErrAccountInactive into four sentinels, this
// wraps them: errors.Is still matches the sentinel, and errors.As recovers the
// side.
//
//	var accountErr *ledger.AccountSideError
//	switch {
//	case errors.As(err, &accountErr) && accountErr.Side == ledger.SourceSide:
//	    // mark the "from" field
//	}
type AccountSideError struct {
	// Side is which account was rejected.
	Side MovementSide
	// AccountID is the id that was given for it.
	AccountID int64
	// Err is ErrAccountNotFound or ErrAccountInactive.
	Err error
}

func (e *AccountSideError) Error() string {
	return fmt.Sprintf("%s account %d: %s", e.Side, e.AccountID, e.Err)
}

func (e *AccountSideError) Unwrap() error { return e.Err }

// TransferAccountError is the name M4 froze for AccountSideError, kept for the
// same reason TransferSide is. It is an alias, so errors.As with either
// spelling matches the same value.
type TransferAccountError = AccountSideError

// TransactionType is what kind of financial event a transaction records.
type TransactionType string

const (
	// Income is money arriving from outside. Its movement is positive.
	Income TransactionType = "INCOME"
	// Expense is money leaving. Its movement is negative.
	Expense TransactionType = "EXPENSE"
	// OpeningBalance is where an account started before this application knew
	// about it. It is not income: recording a starting balance as income would
	// make it appear in every "what did I earn" figure forever.
	OpeningBalance TransactionType = "OPENING_BALANCE"
	// Transfer is money moving between two of the user's own accounts. It has
	// exactly two movements: one negative in the source account, one positive in
	// the destination, of equal magnitude and in the same currency.
	//
	// It is neither income nor an expense, so it carries no category, and it is
	// not an operation with the outside world, so it carries no payment method.
	// It does not change the total held in its currency — that invariant is the
	// point of the type.
	//
	// It is NOT the payment method named "Bank transfer". That method records
	// the channel an external operation used; this type records that money moved
	// between two accounts we own.
	Transfer TransactionType = "TRANSFER"
	// FXExchange is money actually exchanged between two of the user's own
	// accounts denominated in DIFFERENT currencies: bolívares left one account
	// and dollars arrived in another. It has exactly two movements, one negative
	// in the source and one positive in the destination, and their magnitudes
	// are UNRELATED — they are quantities of two different currencies.
	//
	// It is not a transfer. A transfer's two movements cancel out, so the total
	// held in its currency is unchanged; an exchange changes the totals held in
	// both of its currencies, which is the entire point of it. Nothing about
	// TRANSFER was widened to accept two currencies.
	//
	// It is not income and not an expense: nothing was earned and nothing was
	// consumed, so it carries no category, and it is not an operation whose
	// channel is interesting, so it carries no payment method.
	//
	// There is no BUY and no SELL. Which currency was bought is stated by which
	// account was the destination; a screen may call it "Compra de USD", but
	// that is a label inferred from the pair, not a type stored in the ledger.
	//
	// The rate the exchange happened at is NOT stored. It is the source amount
	// divided by the destination amount, derived exactly on every read, and a
	// stored third copy could only ever disagree with the two amounts. It is
	// also unrelated to the reference rates in exchange_rates, which say what a
	// source published, not what this operation cost.
	FXExchange TransactionType = "FX_EXCHANGE"
)

// TransactionTypes lists every valid type, in the order a UI would present
// them.
//
// Transfer and FXExchange are last because each was appended rather than
// inserted: a caller rendering this list must not have its existing order
// change underneath it. Knowing a value does not make it offerable — a transfer
// is created and edited only through RecordTransfer and UpdateTransfer, and an
// exchange only through RecordExchange and UpdateExchange, never through the
// income-and-expense use cases.
func TransactionTypes() []TransactionType {
	return []TransactionType{Income, Expense, OpeningBalance, Transfer, FXExchange}
}

// Valid reports whether t is a known transaction type.
func (t TransactionType) Valid() bool {
	for _, known := range TransactionTypes() {
		if t == known {
			return true
		}
	}
	return false
}

// Payment methods are rows in their own table, not values of a type here.
//
// M2 held four of them in a Go constant and a CHECK constraint. Real use
// produced "Pago móvil", "Zelle" and "Débito", and adding one meant a migration,
// so they became data the user administers -- see internal/paymentmethod.
//
// A transaction therefore carries a payment_method_id. The name is resolved for
// display when a listing is read, which means renaming a method changes how past
// transactions are shown; that is intended, and the one hazard it creates is
// recorded in data-model.md.
//
// A payment method still describes the CHANNEL of an operation with the outside
// world. It is not a transfer between the user's own accounts, which remains a
// later milestone.

const (
	maxDescriptionLength = 255

	// defaultListLimit is what a "recent activity" screen shows. maxListLimit
	// bounds what a caller may ask for: this is a listing, not an export, and
	// an unbounded query over a growing ledger is a page that gets slower every
	// month without anyone noticing.
	defaultListLimit = 50
	maxListLimit     = 500
)

// Transaction is a financial event as stored. It carries no amount and names no
// account; its movements do.
type Transaction struct {
	ID   int64
	Type TransactionType

	// CategoryID and PaymentMethodID are set for income and expenses, and
	// absent for an opening balance. The database enforces that pairing.
	CategoryID      *int64
	PaymentMethodID *int64

	Description *string

	// EffectiveDate is when the event happened financially, as a calendar date
	// at UTC midnight. It is not CreatedAt: a purchase made on the 3rd and
	// entered on the 5th is dated the 3rd.
	EffectiveDate time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Movement is the impact of a transaction on one account.
type Movement struct {
	ID            int64
	TransactionID int64
	AccountID     int64

	// AmountMinor is signed and never zero. Its currency is the account's, by
	// construction: there is no currency column here to disagree with it.
	AmountMinor int64

	CreatedAt time.Time
}

// TransactionView is one movement joined to its transaction, its account and
// its currency: everything a table row needs, in one query.
//
// It is movement-centric rather than transaction-centric. In M2 that is a
// distinction without a difference, because every transaction has exactly one
// movement. When transfers arrive, one transaction will produce two rows here,
// and that change belongs to the milestone that introduces them rather than
// happening quietly now.
type TransactionView struct {
	// The event.
	ID            int64
	Type          TransactionType
	EffectiveDate time.Time
	Description   *string

	// The payment method. Absent for an opening balance.
	//
	// The id is the identifier; the name is for display and can change when the
	// method is renamed. Do not treat the name as stable.
	PaymentMethodID   *int64
	PaymentMethodName *string

	// The category. Absent for an opening balance.
	CategoryID   *int64
	CategoryName *string
	CategoryType *category.Type

	// Transfer is set if and only if Type is Transfer, and carries the WHOLE
	// transfer: both movements, both accounts, and the amount moved.
	//
	// For a transfer, this is the only field that should be read to render it.
	// The movement-centric fields below describe just one of its two halves —
	// the side the money left — because this view is one row per movement by
	// construction. Reading AmountMinor for a transfer yields a negative number
	// that is correct for the source account and wrong as "the amount
	// transferred", and reading AccountID yields the source with no hint that a
	// destination exists. Neither is a field a caller should have to know the
	// anchoring rule to interpret.
	Transfer *TransferDetail

	// Exchange is set if and only if Type is FXExchange, and carries the WHOLE
	// exchange: both movements, both accounts, BOTH CURRENCIES, both amounts and
	// the rate the operation happened at.
	//
	// For an exchange, this is the only field that should be read to render it,
	// and the reason is stronger than it is for a transfer. The
	// movement-centric fields below describe one side only, and for an exchange
	// that means CurrencyCode, MinorUnit and AmountFormatted all describe the
	// currency the money LEFT. A caller that read AmountFormatted and
	// CurrencyCode would render "-85,000.00 VES" and have no way to discover
	// that 100.00 USD arrived.
	Exchange *ExchangeDetail

	// The account the money moved in.
	//
	// For a transfer and for an exchange this is the SOURCE account and
	// MovementID is its negative movement. Use Transfer or Exchange instead; see
	// above.
	MovementID     int64
	AccountID      int64
	AccountName    string
	AccountType    account.Type
	AccountSubtype *account.Subtype
	Institution    *string

	// The currency, which is the account's currency, always.
	CurrencyCode   string
	CurrencySymbol *string
	MinorUnit      int

	// The amount, signed: income positive, expenses negative.
	AmountMinor int64
	// AmountFormatted is AmountMinor rendered exactly at the currency's scale,
	// sign included. Display this rather than computing it.
	AmountFormatted string

	// CreatedAt is when the row was recorded, not when the event happened.
	CreatedAt time.Time
}

// TransferDetail is a whole transfer: where the money left, where it arrived,
// and how much moved.
//
// It is deliberately self-sufficient. Everything needed to render or to link to
// a transfer is here, so nothing above this package has to know that
// TransactionView anchors a transfer on its source movement, or that
// TransactionView.AmountMinor is negative for one. A caller that reads only this
// struct cannot get a transfer wrong.
//
// The currency is the one both accounts share — TransactionView.CurrencyCode and
// MinorUnit describe it, and there is exactly one because a cross-currency
// transfer cannot be recorded.
type TransferDetail struct {
	// The source: the account the money left, and its negative movement.
	SourceMovementID  int64
	SourceAccountID   int64
	SourceAccountName string

	// The destination: the account the money arrived in, and its positive
	// movement.
	DestinationMovementID  int64
	DestinationAccountID   int64
	DestinationAccountName string

	// AmountMinor is the amount that moved, always POSITIVE. The two movements
	// hold -AmountMinor and +AmountMinor, which is why their sum is zero and a
	// transfer neither creates nor destroys money.
	AmountMinor int64
	// AmountFormatted is AmountMinor rendered exactly at the currency's scale,
	// without a sign. Display this rather than computing it.
	AmountFormatted string
}

// ExecutionRate is the rate an FX_EXCHANGE ACTUALLY HAPPENED AT, derived from
// its two amounts.
//
// It is not stored anywhere and must never be. The two movements state it
// exactly — 84,220.67 VES for 100.00 USD is 842.2067 VES per USD — so a stored
// copy would be a third source of truth able to disagree with the two that
// caused it. It is recomputed on every read, from big.Rat, with no float
// anywhere.
//
// It is NOT a reference rate and has nothing to do with exchange_rates. Nothing
// here is published by anybody, nothing here participates in valuing a balance,
// and recording an exchange writes no row to that table. See
// docs/m5.5-contract.md.
//
// # Orientation
//
// The same one the whole project uses: 1 BaseCurrencyCode = Rate
// QuoteCurrencyCode. Which of the two currencies is the base is decided by one
// rule, applied in order:
//
//  1. If exactly one side is USD, USD is the base. "1 USD = 842.2067 VES" is
//     how the rate was quoted to the user, whichever direction they traded in.
//  2. Otherwise the base is whichever side makes the rate at least 1, so that a
//     pair is read in units of the cheaper currency: "1 EUR = 95.40 VES", never
//     "1 VES = 0.0104 EUR".
//  3. If both orientations give exactly 1, the destination is the base. The
//     tie-break exists so that the answer is always the same one.
//
// The base is therefore NOT always the destination. A buy and a sell of the same
// currency pair produce a rate a person can compare, which is the whole reason
// the choice is made here rather than in a template.
type ExecutionRate struct {
	// BaseCurrencyCode is the "1" side of the quote.
	BaseCurrencyCode string
	// QuoteCurrencyCode is the side the rate is expressed in.
	QuoteCurrencyCode string

	// Rate is the exact value, as a decimal carrying the smallest scale that
	// states it: "850" for a whole number, "842.2067" for the acceptance case.
	Rate money.Decimal
	// RateFormatted is Rate rendered as its canonical decimal string. Display
	// this rather than computing anything.
	RateFormatted string

	// Exact reports whether the rate is representable in decimal at all.
	//
	// It is false for a pair like 100 VES for 3 USD, whose rate is a third of a
	// hundred and has no finite decimal expansion. RateFormatted is then rounded
	// half to even at money.MaxScale places, and an interface that shows it must
	// say so — "≈" — rather than presenting an approximation as the fact.
	Exact bool
}

// ExchangeDetail is a whole FX_EXCHANGE: what left, what arrived, in which two
// currencies, and at what rate.
//
// It is deliberately self-sufficient, more so than TransferDetail has to be.
// A transfer has one currency, so TransactionView's own currency fields describe
// it; an exchange has two, and only one of them appears up there. Every field
// needed to render an exchange is here, so nothing above this package has to
// know that TransactionView anchors on the source movement or that its
// CurrencyCode names only one of the two currencies involved.
type ExchangeDetail struct {
	// The source: the account the money left, its negative movement, and the
	// currency it was denominated in.
	SourceMovementID     int64
	SourceAccountID      int64
	SourceAccountName    string
	SourceCurrencyCode   string
	SourceCurrencySymbol *string
	SourceMinorUnit      int

	// SourceAmountMinor is the amount DELIVERED, always POSITIVE, in the source
	// currency's minor units. The stored movement holds its negation.
	SourceAmountMinor int64
	// SourceAmountFormatted is it rendered at the source currency's scale,
	// unsigned. Display this rather than computing it.
	SourceAmountFormatted string

	// The destination: the account the money arrived in, its positive movement,
	// and the currency it was denominated in.
	DestinationMovementID     int64
	DestinationAccountID      int64
	DestinationAccountName    string
	DestinationCurrencyCode   string
	DestinationCurrencySymbol *string
	DestinationMinorUnit      int

	// DestinationAmountMinor is the amount RECEIVED, always positive, in the
	// destination currency's minor units. The stored movement holds it as it is.
	DestinationAmountMinor int64
	// DestinationAmountFormatted is it rendered at the destination currency's
	// scale, unsigned.
	DestinationAmountFormatted string

	// ExecutionRate is what the operation cost, derived from the two amounts
	// above. There is no field here for a reference rate, and there is nowhere
	// to put one.
	ExecutionRate ExecutionRate
}

// AccountBalance is one account with its balance derived from its movements.
type AccountBalance struct {
	AccountID      int64
	AccountName    string
	AccountType    account.Type
	AccountSubtype *account.Subtype
	Institution    *string
	IsActive       bool

	CurrencyCode   string
	CurrencySymbol *string
	MinorUnit      int

	// BalanceMinor is the sum of the account's movements dated on or before the
	// as-of date -- today, for the current balance (TD-063). An account with
	// none has a balance of zero.
	BalanceMinor     int64
	BalanceFormatted string

	// FutureMovementCount is how many of the account's movements are dated
	// AFTER the as-of date. They exist and are listed, but are not part of
	// BalanceMinor yet. It is a count, not a projected balance.
	FutureMovementCount int
}

// CurrencyTotal is the sum of every account balance in one currency, as of a
// date.
//
// Currencies are never converted into one another here. A consolidated figure
// needs exchange rates, and is internal/valuation's job.
type CurrencyTotal struct {
	CurrencyCode   string
	CurrencyName   string
	CurrencySymbol *string
	MinorUnit      int

	AccountCount   int
	TotalMinor     int64
	TotalFormatted string
}

// DailyDelta is the net effect of one day's movements on one account.
//
// It is the unit the historical net worth is rebuilt from (TD-065): an
// account's balance as of D is the sum of its deltas dated on or before D, the
// same rule every balance in this package follows.
type DailyDelta struct {
	AccountID int64

	// Date is the transactions' effective_date, at UTC midnight like every
	// other calendar date in this application.
	Date time.Time

	// AmountMinor is the signed sum of that day's movements, in the account's
	// own currency. It may be zero: a day can hold a movement and its reversal.
	AmountMinor int64

	// MovementCount is how many movements the delta sums.
	MovementCount int
}

// Flow is income or expense on one day, in one category and one currency,
// summed.
//
// Only INCOME and EXPENSE produce flows. A transfer, an exchange and an opening
// balance were neither earned nor spent (TD-068).
type Flow struct {
	Date time.Time

	// Type is Income or Expense, and nothing else.
	Type TransactionType

	CategoryID   int64
	CategoryName string

	CurrencyCode string
	MinorUnit    int

	// AmountMinor is the signed sum as stored: positive for income, negative
	// for an expense.
	AmountMinor int64

	// Count is how many transactions the flow sums.
	Count int
}

// ExpenseItem is one EXPENSE transaction, in its own currency.
//
// It is what a screen ranks "the largest expenses" from. Ranking needs every
// expense valued in one currency, which only the valuation snapshot can do, so
// the ledger returns them unranked and in their own currencies.
type ExpenseItem struct {
	TransactionID int64
	Date          time.Time
	Description   *string

	CategoryID   int64
	CategoryName string

	AccountID    int64
	AccountName  string
	CurrencyCode string
	MinorUnit    int

	// AmountMinor is how much went out: POSITIVE, the magnitude of the stored
	// negative movement.
	AmountMinor int64
}

// RecordIncomeInput is the input to Service.RecordIncome.
type RecordIncomeInput struct {
	AccountID int64

	// CategoryID must name an active INCOME category.
	CategoryID int64

	// Amount is a positive decimal string such as "1500.00". The sign is
	// applied by the use case, never taken from here.
	Amount string

	// PaymentMethodID must name an active payment method.
	PaymentMethodID int64

	// Description is optional. nil means none; an empty string is a caller
	// mistake and is rejected.
	Description *string

	// EffectiveDate is "YYYY-MM-DD" and is required.
	EffectiveDate string
}

// RecordExpenseInput is the input to Service.RecordExpense.
//
// Amount is positive here too. Sending "-25.99" is an error rather than a
// synonym: the sign belongs to the operation, which is what makes an expense
// that increases a balance impossible to express.
type RecordExpenseInput struct {
	AccountID       int64
	CategoryID      int64
	Amount          string
	PaymentMethodID int64
	Description     *string
	EffectiveDate   string
}

// RecordOpeningBalanceInput is the input to Service.RecordOpeningBalance.
//
// It has no category and no payment method — not as fields left empty, but
// absent from the struct, so an opening balance cannot be given either.
type RecordOpeningBalanceInput struct {
	AccountID int64

	// Amount may be negative, for an account that starts overdrawn, and may be
	// zero, which records nothing at all.
	Amount string

	EffectiveDate string
}

// RecordTransferInput is the input to Service.RecordTransfer.
//
// It has no category and no payment method — not as fields left empty, but
// absent from the struct, so a transfer cannot be given either. It has no type
// either: this input records a transfer and nothing else.
type RecordTransferInput struct {
	// SourceAccountID is the account the money leaves. It must exist, be
	// active, and differ from the destination.
	SourceAccountID int64

	// DestinationAccountID is the account the money arrives in. It must exist,
	// be active, and be denominated in the same currency as the source.
	DestinationAccountID int64

	// Amount is a positive decimal string such as "250.00", read at the shared
	// currency's scale. Sending "-250.00" is an error rather than a synonym for
	// the other direction: the direction is the pair of accounts, and the signs
	// are applied by the use case.
	Amount string

	// Description is optional. nil means none; an empty string is a caller
	// mistake and is rejected.
	Description *string

	// EffectiveDate is "YYYY-MM-DD" and is required.
	EffectiveDate string
}

// UpdateTransferInput is the input to Service.UpdateTransfer.
//
// Like the other edit inputs it is a REPLACEMENT, not a patch: every editable
// field is sent on every call and the service revalidates exactly what a
// creation would.
//
// There is no Type: a transfer cannot become anything else, and nothing else can
// become a transfer.
type UpdateTransferInput struct {
	SourceAccountID      int64
	DestinationAccountID int64
	Amount               string
	Description          *string
	EffectiveDate        string

	// AllowCurrencyChange must be set to move a transfer to a pair of accounts
	// denominated in a different currency from the one it currently uses.
	//
	// The amount is a decimal string read at the new pair's scale, so moving a
	// transfer from two USD accounts to two VES accounts does not convert it —
	// it reinterprets it as a different sum of money. M4 has no exchange rates,
	// so the reinterpretation must be deliberate: without this flag the service
	// returns ErrCurrencyChangeNotConfirmed and writes nothing. It is the same
	// rule UpdateTransaction applies, for the same reason.
	//
	// It does NOT permit a cross-currency transfer. The two accounts must still
	// share a currency as each other; this only concerns which currency that is.
	// A cross-currency operation is an FX_EXCHANGE and is recorded through
	// RecordExchange.
	AllowCurrencyChange bool
}

// RecordExchangeInput is the input to Service.RecordExchange.
//
// It has no category and no payment method — not as fields left empty, but
// absent from the struct — and no type: this input records an exchange and
// nothing else.
//
// It carries TWO amounts, and that is the shape of the operation rather than a
// convenience. The user knows what was debited and what was credited; those are
// the two facts a statement shows. It carries NO rate: supplying a rate and one
// amount would make the application compute the other, and the computed one
// would then disagree with the bank by a céntimo whenever the bank rounded
// differently. The rate is derived from the two amounts, not the other way
// round.
type RecordExchangeInput struct {
	// SourceAccountID is the account the money leaves. It must exist, be
	// active, and differ from the destination.
	SourceAccountID int64

	// SourceAmount is the amount DELIVERED, a positive decimal string read at
	// the source account's currency scale: "85000.00" for bolívares.
	//
	// It is positive because the direction is the pair of accounts. Sending
	// "-85000.00" is an error rather than a synonym for the other direction.
	SourceAmount string

	// DestinationAccountID is the account the money arrives in. It must exist,
	// be active, and be denominated in a DIFFERENT currency from the source —
	// two accounts in one currency are a TRANSFER, not an exchange.
	DestinationAccountID int64

	// DestinationAmount is the amount RECEIVED, a positive decimal string read
	// at the destination account's currency scale: "100.00" for dollars.
	//
	// It is not compared with SourceAmount. They are quantities of two different
	// currencies, so there is no magnitude relation between them to check.
	DestinationAmount string

	// Description is optional. nil means none; an empty string is a caller
	// mistake and is rejected.
	Description *string

	// EffectiveDate is "YYYY-MM-DD" and is required.
	EffectiveDate string
}

// UpdateExchangeInput is the input to Service.UpdateExchange.
//
// Like every other edit input it is a REPLACEMENT, not a patch: every editable
// field is sent on every call and the service revalidates exactly what a
// creation would.
//
// There is no Type: an exchange cannot become anything else, and nothing else
// can become an exchange.
//
// There is also no AllowCurrencyChange, and its absence is deliberate rather
// than an omission. That flag exists on the transfer and transaction edits
// because those carry ONE amount, so moving them to another currency re-reads
// the same digits at another scale and silently means a different sum of money.
// An exchange names both currencies explicitly, one amount for each, and each
// amount is read at its own account's scale: changing an account changes which
// currency that side is in, and the amount beside it says how much of it there
// was. There is no reinterpretation to confirm.
type UpdateExchangeInput struct {
	SourceAccountID      int64
	SourceAmount         string
	DestinationAccountID int64
	DestinationAmount    string
	Description          *string
	EffectiveDate        string
}

// UpdateTransactionInput is the input to Service.UpdateTransaction.
//
// It is a REPLACEMENT, not a patch: every editable field is sent on every call,
// and the service revalidates exactly the same invariants as a creation. A patch
// would need a way to say "leave this alone" for each field, and the difference
// between "unset" and "set to nothing" is precisely where partial-update APIs go
// wrong.
type UpdateTransactionInput struct {
	AccountID int64

	// Type may be Income or Expense. Converting to or from OpeningBalance is
	// ErrTypeChangeNotAllowed.
	//
	// Switching between the two recomputes the movement's sign from the new
	// type; the old sign is never carried over.
	Type TransactionType

	CategoryID      int64
	PaymentMethodID int64

	// Amount is a positive decimal string. The sign belongs to the type.
	Amount string

	Description   *string
	EffectiveDate string

	// AllowCurrencyChange must be set to move a transaction to an account
	// denominated in a different currency.
	//
	// The amount is a decimal string read at the destination account's scale, so
	// moving 100.00 from a USD account to a VES account does not convert it --
	// it reinterprets it as a different sum of money. M3 has no exchange rates
	// and performs no conversion, so the reinterpretation must be deliberate:
	// without this flag the service returns ErrCurrencyChangeNotConfirmed and
	// writes nothing.
	AllowCurrencyChange bool
}

// UpdateOpeningBalanceInput is the input to Service.UpdateOpeningBalance.
//
// The account cannot change and neither can the type. Category and payment
// method are not fields of this struct, so an opening balance cannot acquire
// either.
type UpdateOpeningBalanceInput struct {
	// Amount may be negative, for an account that started overdrawn. It may NOT
	// be zero: in M2 a zero opening balance means "record nothing", and M3 has
	// no delete, so setting an existing one to zero would leave a transaction
	// with no financial effect.
	Amount string

	EffectiveDate string
}

// ListTransactionsInput filters Service.ListTransactions.
type ListTransactionsInput struct {
	// Limit caps the result. Zero or less uses the default of 50; anything
	// above 500 is capped at 500.
	Limit int
}

func (in ListTransactionsInput) limit() int {
	switch {
	case in.Limit <= 0:
		return defaultListLimit
	case in.Limit > maxListLimit:
		return maxListLimit
	default:
		return in.Limit
	}
}

// normalizeDescription trims the value behind a pointer. nil stays nil; a value
// that trims to empty stays empty so validation can reject it rather than
// quietly turning it into an absent description.
func normalizeDescription(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	return &trimmed
}

// validateDescription rejects a present-but-empty description.
//
// An empty string is not silently treated as "absent": a caller who sends ""
// believed they were sending a description, and swallowing that would hide
// their bug. Absence is expressed with nil, explicitly — the same rule as
// account.Institution and currency.Symbol.
func validateDescription(description *string) error {
	if description == nil {
		return nil
	}
	if *description == "" {
		return fmt.Errorf("%w: description must not be blank; omit it entirely instead", ErrInvalidInput)
	}
	if len([]rune(*description)) > maxDescriptionLength {
		return fmt.Errorf("%w: description must be at most %d characters",
			ErrInvalidInput, maxDescriptionLength)
	}
	return nil
}

// validateEffectiveDate parses the calendar date a transaction happened on.
//
// The layout demands zero-padded components, so "2026-9-5" is refused, and
// time.Parse validates the calendar itself, so "2026-02-31" is refused too. The
// same two rules are enforced by a CHECK constraint on the column.
func validateEffectiveDate(s string) (time.Time, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("%w: an effective date is required, as YYYY-MM-DD", ErrInvalidInput)
	}

	date, err := database.ParseDate(trimmed)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: effective date %q is not a date in the form YYYY-MM-DD",
			ErrInvalidInput, trimmed)
	}
	return date, nil
}
