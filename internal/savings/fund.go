package savings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// M7.2: the savings fund. Money set aside is marked against the USD account
// that holds it; the fund is a sum over entries, never an account (TD-082).

var (
	// ErrInvalidRequestKey means the form's idempotency token is missing or
	// malformed.
	ErrInvalidRequestKey = errors.New("invalid request key")

	// ErrAccountNotFound means no account exists with that id.
	ErrAccountNotFound = errors.New("account not found")

	// ErrAccountInactive means the account is archived: it cannot back new
	// savings, though what it already backs can be released.
	ErrAccountInactive = errors.New("account is archived")

	// ErrNotUSDAccount means the account is not in USD, the savings currency.
	ErrNotUSDAccount = errors.New("savings can only be backed by a USD account")
	// ErrLiabilityAccount means the account is a liability -- a credit card --
	// whose balance is money owed, so it cannot back reserved money even when
	// it holds a credit (TD-094). A reservation backed by a debt would be money
	// set aside that does not exist.
	ErrLiabilityAccount = errors.New("savings can only be backed by an asset account")

	// ErrInsufficientFree means the account's free balance -- its balance today
	// minus what is already reserved on it -- does not cover the reservation.
	ErrInsufficientFree = errors.New("the account's free balance does not cover this")

	// ErrInsufficientReserved means less is reserved there than the release
	// asks for.
	ErrInsufficientReserved = errors.New("less is reserved there than requested")

	// ErrExceedsSuggestion means a confirmation would bring what is confirmed
	// for the income above its live suggestion, and the user did not record an
	// exception (TD-083).
	ErrExceedsSuggestion = errors.New("this would confirm more than the suggestion")
)

// OperationKind is what a savings operation did.
type OperationKind string

const (
	// KindReserve sets money aside.
	KindReserve OperationKind = "RESERVE"
	// KindRelease gives reserved money back to the free balance, with a reason.
	KindRelease OperationKind = "RELEASE"
	// KindReallocate moves reserved money between goals of one account (M7.3).
	KindReallocate OperationKind = "REALLOCATE"
	// KindReback moves reserved money between accounts with a transfer (M7.4).
	KindReback OperationKind = "REBACK"
	// KindSpend consumes reserved money with a linked expense (M7.4).
	KindSpend OperationKind = "SPEND"
)

// Entry is one signed impact of an operation on an account (and a goal).
type Entry struct {
	ID          int64
	AccountID   int64
	AccountName string
	GoalID      *int64
	GoalName    string
	AmountMinor int64
}

// Operation is one thing the user did with savings. It is never edited.
type Operation struct {
	ID         int64
	Kind       OperationKind
	RequestKey string

	// SuggestionID and IncomeTransactionID are set for a RESERVE that
	// confirms a suggestion.
	SuggestionID        *int64
	IncomeTransactionID *int64

	// TransactionID is the ledger transaction the operation answers to.
	TransactionID *int64

	Reason      *string
	IsException bool

	// EffectiveDate is the business day the operation happened: for a
	// confirmation, the confirmation date.
	EffectiveDate time.Time
	CreatedAt     time.Time

	Entries []Entry
	// TotalMinor is the signed sum of the entries.
	TotalMinor int64
}

// AccountAvailability is one USD account seen from the fund.
type AccountAvailability struct {
	AccountID   int64
	AccountName string
	IsActive    bool

	// BalanceMinor is the ledger's balance today (TD-063).
	BalanceMinor int64
	// ReservedMinor is the sum of the entries on the account.
	ReservedMinor int64
	// FreeMinor is BalanceMinor - ReservedMinor. It is negative when the
	// account holds less than is reserved on it.
	FreeMinor int64
	// OverallocatedMinor is how much of the reservation the balance does not
	// back: max(0, reserved - max(0, balance)). Never hidden, never fixed
	// automatically.
	OverallocatedMinor int64
}

// Fund is the savings fund today, per account and in total, in USD cents.
type Fund struct {
	Accounts []AccountAvailability

	BalanceMinor  int64
	ReservedMinor int64
	// FreeMinor sums only positive free balances: what could still be set
	// aside, account by account.
	FreeMinor          int64
	OverallocatedMinor int64

	// AllocatedMinor is reserved money assigned to a goal; UnallocatedMinor the
	// rest. Until M7.3 every reservation is unallocated.
	AllocatedMinor   int64
	UnallocatedMinor int64
}

// Account returns one account's availability.
func (f Fund) Account(id int64) (AccountAvailability, bool) {
	for _, a := range f.Accounts {
		if a.AccountID == id {
			return a, true
		}
	}
	return AccountAvailability{}, false
}

// ReserveLine is money to set aside on one account.
type ReserveLine struct {
	AccountID int64
	GoalID    *int64
	// Amount is a positive USD decimal string.
	Amount string
}

// ReserveInput is the input to Service.Reserve.
type ReserveInput struct {
	// RequestKey is the form's idempotency token.
	RequestKey string

	// IncomeTransactionID, when set, confirms that income's live suggestion.
	IncomeTransactionID *int64

	Lines []ReserveLine

	// Exception records that the user means to confirm MORE than what remains
	// of the suggestion; Reason is then required.
	Exception bool
	Reason    string
}

// ReleaseInput is the input to Service.Release.
type ReleaseInput struct {
	RequestKey string
	AccountID  int64
	GoalID     *int64
	Amount     string
	Reason     string
}

// FundDelta is the signed sum of one kind of operation on one day.
type FundDelta struct {
	Date        time.Time
	Kind        OperationKind
	AmountMinor int64
}

// GetFund returns the fund today.
func (s *Service) GetFund(ctx context.Context) (Fund, error) {
	var fund Fund
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		var err error
		fund, err = s.fundTx(ctx, exec)
		return err
	})
	return fund, err
}

// fundTx computes the fund through exec, from the ledger's balances today and
// the reserved sums: two queries, whatever the number of accounts.
//
// Only USD ASSETS back the fund (TD-094). A USD credit card is left out of the
// balance, of what is free and of the per-account table, even when it holds a
// credit: that credit is the issuer's debt to the user, not money in hand.
func (s *Service) fundTx(ctx context.Context, exec database.DBTX) (Fund, error) {
	balances, err := ledger.NewRepository(exec).ListBalances(ctx, true, s.today())
	if err != nil {
		return Fund{}, err
	}
	repo := NewRepository(exec)
	reserved, err := repo.ReservedByAccount(ctx)
	if err != nil {
		return Fund{}, err
	}
	allocated, err := repo.AllocatedTotal(ctx)
	if err != nil {
		return Fund{}, err
	}

	fund := Fund{Accounts: make([]AccountAvailability, 0)}
	for _, b := range balances {
		if b.CurrencyCode != FundCurrency || b.AccountType.Nature() != account.Asset {
			continue
		}
		a := AccountAvailability{
			AccountID:     b.AccountID,
			AccountName:   b.AccountName,
			IsActive:      b.IsActive,
			BalanceMinor:  b.BalanceMinor,
			ReservedMinor: reserved[b.AccountID],
		}
		a.FreeMinor = a.BalanceMinor - a.ReservedMinor
		backed := max(a.BalanceMinor, 0)
		a.OverallocatedMinor = max(a.ReservedMinor-backed, 0)

		fund.Accounts = append(fund.Accounts, a)
		fund.BalanceMinor += a.BalanceMinor
		fund.ReservedMinor += a.ReservedMinor
		fund.FreeMinor += max(a.FreeMinor, 0)
		fund.OverallocatedMinor += a.OverallocatedMinor
	}
	fund.AllocatedMinor = allocated
	fund.UnallocatedMinor = fund.ReservedMinor - allocated
	return fund, nil
}

// Reserve sets money aside on one or more USD accounts, optionally confirming
// an income's suggestion -- in full, in part, or above it as a recorded
// exception (TD-083).
//
// It is idempotent: a request key that already produced an operation returns
// that operation and writes nothing. Everything it decides on -- the free
// balance of each account, what remains of the suggestion -- is read inside
// the transaction that writes, which BEGIN IMMEDIATE (TD-075) keeps to itself.
// A reservation never exceeds an account's free balance: unlike an expense, it
// is not a real movement, so refusing it hides nothing.
func (s *Service) Reserve(ctx context.Context, in ReserveInput) (Operation, error) {
	if err := validateRequestKey(in.RequestKey); err != nil {
		return Operation{}, err
	}
	if len(in.Lines) == 0 {
		return Operation{}, fmt.Errorf("%w: nothing to reserve", ErrInvalidInput)
	}
	var reason *string
	if in.Exception || strings.TrimSpace(in.Reason) != "" {
		r, err := normalizeReason(in.Reason)
		if err != nil {
			return Operation{}, err
		}
		reason = &r
	}
	if in.Exception && in.IncomeTransactionID == nil {
		return Operation{}, fmt.Errorf("%w: an exception belongs to a suggestion", ErrInvalidInput)
	}

	var op Operation
	err := s.inTx(ctx, "reserving savings", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		if id, replay, err := repo.OperationIDByKey(ctx, in.RequestKey); err != nil {
			return err
		} else if replay {
			op, err = repo.GetOperation(ctx, id)
			return err
		}

		fund, err := s.fundTx(ctx, exec)
		if err != nil {
			return err
		}
		entries, total, err := s.reserveEntries(ctx, exec, fund, in.Lines)
		if err != nil {
			return err
		}

		var suggestionID *int64
		if in.IncomeTransactionID != nil {
			live, err := repo.LiveSuggestion(ctx, *in.IncomeTransactionID)
			if err != nil {
				return err
			}
			if live.Status != StatusOpen {
				return fmt.Errorf("%w: only an open suggestion can be confirmed; this one is %s",
					ErrInvalidState, live.Status)
			}
			confirmed, err := repo.ConfirmedByIncome(ctx)
			if err != nil {
				return err
			}
			remaining := max(*live.SuggestedUSDMinor-confirmed[*in.IncomeTransactionID], 0)
			if total > remaining && !in.Exception {
				return fmt.Errorf("%w: %s USD remain of the suggestion and %s were asked for",
					ErrExceedsSuggestion, money.Format(remaining, 2), money.Format(total, 2))
			}
			suggestionID = &live.ID
		}

		id, err := repo.InsertOperation(ctx, newOperation{
			kind:          KindReserve,
			requestKey:    in.RequestKey,
			suggestionID:  suggestionID,
			reason:        reason,
			isException:   in.Exception && suggestionID != nil,
			effectiveDate: s.today(),
			entries:       entries,
			now:           database.Now(),
		})
		if err != nil {
			return err
		}
		op, err = repo.GetOperation(ctx, id)
		return err
	})
	return op, err
}

// reserveEntries validates reservation lines against the fund and returns the
// entries to write and their total.
func (s *Service) reserveEntries(ctx context.Context, exec database.DBTX, fund Fund, lines []ReserveLine,
) ([]newEntry, int64, error) {
	perAccount := make(map[int64]int64)
	perGoal := make(map[int64]int64)
	entries := make([]newEntry, 0, len(lines))
	var total int64
	for _, line := range lines {
		amount, err := parseUSD(line.Amount)
		if err != nil {
			return nil, 0, err
		}
		if amount <= 0 {
			return nil, 0, fmt.Errorf("%w: an amount to reserve must be greater than zero", ErrInvalidInput)
		}
		if err := checkFundAccount(ctx, exec, line.AccountID, true); err != nil {
			return nil, 0, err
		}
		if line.GoalID != nil {
			if err := s.checkGoalAcceptsMoney(ctx, exec, *line.GoalID); err != nil {
				return nil, 0, err
			}
			perGoal[*line.GoalID] += amount
		}
		perAccount[line.AccountID] += amount
		total += amount
		entries = append(entries, newEntry{accountID: line.AccountID, goalID: line.GoalID, amountMinor: amount})
	}
	for accountID, amount := range perAccount {
		a, _ := fund.Account(accountID)
		if amount > a.FreeMinor {
			return nil, 0, fmt.Errorf("%w: %s holds %s USD free and %s were asked for",
				ErrInsufficientFree, a.AccountName, money.Format(max(a.FreeMinor, 0), 2), money.Format(amount, 2))
		}
	}
	return entries, total, nil
}

// Release gives reserved money on one account back to its free balance. A
// reason is required: releasing savings is a decision worth reading back.
func (s *Service) Release(ctx context.Context, in ReleaseInput) (Operation, error) {
	if err := validateRequestKey(in.RequestKey); err != nil {
		return Operation{}, err
	}
	reason, err := normalizeReason(in.Reason)
	if err != nil {
		return Operation{}, err
	}
	amount, err := parseUSD(in.Amount)
	if err != nil {
		return Operation{}, err
	}
	if amount <= 0 {
		return Operation{}, fmt.Errorf("%w: an amount to release must be greater than zero", ErrInvalidInput)
	}

	var op Operation
	err = s.inTx(ctx, "releasing savings", func(exec database.DBTX) error {
		repo := NewRepository(exec)
		if id, replay, err := repo.OperationIDByKey(ctx, in.RequestKey); err != nil {
			return err
		} else if replay {
			op, err = repo.GetOperation(ctx, id)
			return err
		}
		// An archived account may still release what it backs.
		if err := checkFundAccount(ctx, exec, in.AccountID, false); err != nil {
			return err
		}
		available, err := s.reservedAt(ctx, exec, in.AccountID, in.GoalID)
		if err != nil {
			return err
		}
		if amount > available {
			return fmt.Errorf("%w: %s USD are reserved there and %s were asked for",
				ErrInsufficientReserved, money.Format(available, 2), money.Format(amount, 2))
		}
		id, err := repo.InsertOperation(ctx, newOperation{
			kind:          KindRelease,
			requestKey:    in.RequestKey,
			reason:        &reason,
			effectiveDate: s.today(),
			entries:       []newEntry{{accountID: in.AccountID, goalID: in.GoalID, amountMinor: -amount}},
			now:           database.Now(),
		})
		if err != nil {
			return err
		}
		op, err = repo.GetOperation(ctx, id)
		return err
	})
	return op, err
}

// ListOperations returns the most recent savings operations, newest first.
func (s *Service) ListOperations(ctx context.Context, limit int) ([]Operation, error) {
	return NewRepository(s.db).ListOperations(ctx, "", limit)
}

// checkFundAccount verifies an account can take part in the fund: it exists,
// it is an asset, it is in USD and, when backing new money, it is active.
func checkFundAccount(ctx context.Context, exec database.DBTX, accountID int64, mustBeActive bool) error {
	a, err := account.NewRepository(exec).Get(ctx, accountID)
	switch {
	case errors.Is(err, account.ErrNotFound):
		return fmt.Errorf("account %d: %w", accountID, ErrAccountNotFound)
	case err != nil:
		return fmt.Errorf("looking up account %d: %w", accountID, err)
	}
	if a.Type.Nature() != account.Asset {
		return fmt.Errorf("%w: %q is a %s", ErrLiabilityAccount, a.Name, a.Type)
	}
	if a.CurrencyCode != FundCurrency {
		return fmt.Errorf("%w: %q is in %s", ErrNotUSDAccount, a.Name, a.CurrencyCode)
	}
	if mustBeActive && !a.IsActive {
		return fmt.Errorf("account %q: %w", a.Name, ErrAccountInactive)
	}
	return nil
}

// parseUSD reads a USD amount at its two decimals.
func parseUSD(raw string) (int64, error) {
	amount, err := money.Parse(strings.TrimSpace(raw), 2)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a USD amount with at most two decimals", ErrInvalidInput, raw)
	}
	return amount, nil
}

func validateRequestKey(key string) error {
	if len(key) < 16 || len(key) > 64 {
		return ErrInvalidRequestKey
	}
	for _, r := range key {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return ErrInvalidRequestKey
		}
	}
	return nil
}

// applyConfirmation fills in what is confirmed for an income against its
// revision -- derived from the reservations, never stored (TD-079).
func applyConfirmation(s *Suggestion, confirmedByIncome, confirmedBySuggestion map[int64]int64) {
	s.ConfirmedUSDMinor = confirmedBySuggestion[s.ID]
	if !s.Status.Live() {
		return
	}
	s.IncomeConfirmedUSDMinor = confirmedByIncome[s.IncomeTransactionID]
	if s.SuggestedUSDMinor == nil {
		return
	}
	suggested := *s.SuggestedUSDMinor
	s.RemainingUSDMinor = max(suggested-s.IncomeConfirmedUSDMinor, 0)
	s.OverConfirmedUSDMinor = max(s.IncomeConfirmedUSDMinor-suggested, 0)
	switch {
	case s.IncomeConfirmedUSDMinor == 0 && suggested > 0:
		s.Confirmation = ConfirmationPending
	case s.IncomeConfirmedUSDMinor < suggested:
		s.Confirmation = ConfirmationPartial
	default:
		s.Confirmation = ConfirmationFull
	}
}

func (s *Service) reservedAt(ctx context.Context, exec database.DBTX, accountID int64, goalID *int64) (int64, error) {
	return NewRepository(exec).ReservedAt(ctx, accountID, goalID)
}
