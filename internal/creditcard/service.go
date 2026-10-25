package creditcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/paymentmethod"
)

// Service holds the card use cases that stay inside this domain: reading a
// card's standing and correcting its terms. Opening a card, and recording what
// happens on one, write to the ledger as well and live in internal/application.
type Service struct {
	db  *sql.DB
	now func() time.Time
}

// NewService builds the service over a pool.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now}
}

// View is a card as a screen needs it: the account, its terms, its currency's
// scale and its standing today.
type View struct {
	Account   account.Account
	Card      Card
	MinorUnit int
	Position  Position
}

// today is the as-of date of the standing: today on the Venezuelan calendar
// (TD-051, TD-063).
func (s *Service) today() time.Time { return database.TodayAt(s.now()) }

// Get returns one card with its standing, or ErrNotFound when the account is
// not a card.
func (s *Service) Get(ctx context.Context, accountID int64) (View, error) {
	return viewTx(ctx, s.db, accountID, s.today())
}

// List returns every card with its standing, ordered by name. Archived cards
// are included only when asked for.
func (s *Service) List(ctx context.Context, includeInactive bool) ([]View, error) {
	cards, err := NewRepository(s.db).List(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := account.NewRepository(s.db).List(ctx, includeInactive)
	if err != nil {
		return nil, fmt.Errorf("listing card accounts: %w", err)
	}
	balances, err := ledger.NewRepository(s.db).ListBalances(ctx, includeInactive, s.today())
	if err != nil {
		return nil, err
	}
	byAccount := make(map[int64]Card, len(cards))
	for _, c := range cards {
		byAccount[c.AccountID] = c
	}
	balanceOf := make(map[int64]ledger.AccountBalance, len(balances))
	for _, b := range balances {
		balanceOf[b.AccountID] = b
	}

	views := make([]View, 0, len(cards))
	for _, a := range accounts {
		card, ok := byAccount[a.ID]
		if !ok {
			continue
		}
		b := balanceOf[a.ID]
		views = append(views, View{
			Account:   a,
			Card:      card,
			MinorUnit: b.MinorUnit,
			Position:  position(b.BalanceMinor, b.FutureMovementCount, card),
		})
	}
	return views, nil
}

func viewTx(ctx context.Context, exec database.DBTX, accountID int64, today time.Time) (View, error) {
	acct, err := RequireCardTx(ctx, exec, accountID)
	if err != nil {
		return View{}, err
	}
	card, err := NewRepository(exec).Get(ctx, accountID)
	if err != nil {
		return View{}, err
	}
	balance, err := ledger.NewRepository(exec).GetBalance(ctx, accountID, today)
	if err != nil {
		return View{}, err
	}
	return View{
		Account:   acct,
		Card:      card,
		MinorUnit: balance.MinorUnit,
		Position:  position(balance.BalanceMinor, balance.FutureMovementCount, card),
	}, nil
}

// RequireCardTx loads an account and refuses it unless it is a credit card.
//
// Every card use case starts here, so "this account is a card" is decided in one
// place: on the account's type, never on whether terms happen to exist.
func RequireCardTx(ctx context.Context, exec database.DBTX, accountID int64) (account.Account, error) {
	acct, err := account.NewRepository(exec).Get(ctx, accountID)
	switch {
	case errors.Is(err, account.ErrNotFound):
		return account.Account{}, fmt.Errorf("account %d: %w", accountID, ErrAccountNotFound)
	case err != nil:
		return account.Account{}, fmt.Errorf("looking up account %d: %w", accountID, err)
	}
	if acct.Type != account.CreditCard {
		return account.Account{}, fmt.Errorf("%w: %q is %s", ErrNotACreditCard, acct.Name, acct.Type)
	}
	return acct, nil
}

// CreateTx records the terms of a card account that exists, through exec. It
// exists for internal/application, which opens the account and its terms in
// one transaction.
func CreateTx(ctx context.Context, exec database.DBTX, accountID int64, in TermsInput) (Card, error) {
	card, err := resolveTerms(ctx, exec, accountID, in)
	if err != nil {
		return Card{}, err
	}
	now := database.Now()
	card.CreatedAt, card.UpdatedAt = now, now
	if err := NewRepository(exec).Insert(ctx, card); err != nil {
		return Card{}, err
	}
	return card, nil
}

// UpdateTerms replaces a card's terms. The account's own fields -- name,
// institution, status -- are edited as an account's are.
func (s *Service) UpdateTerms(ctx context.Context, accountID int64, in TermsInput) (_ Card, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Card{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the terms of card %d: %w", accountID, rerr))
		}
	}()

	repo := NewRepository(tx)
	current, err := repo.Get(ctx, accountID)
	if err != nil {
		return Card{}, err
	}
	card, err := resolveTerms(ctx, tx, accountID, in)
	if err != nil {
		return Card{}, err
	}
	card.CreatedAt, card.UpdatedAt = current.CreatedAt, database.Now()
	if err := repo.Update(ctx, card); err != nil {
		return Card{}, err
	}
	if err := tx.Commit(); err != nil {
		return Card{}, fmt.Errorf("committing the terms of card %d: %w", accountID, err)
	}
	return card, nil
}

// resolveTerms validates terms against the card they describe: its account
// must be a card, the limit is read at its currency's scale, and the default
// payment method must exist and be active.
func resolveTerms(ctx context.Context, exec database.DBTX, accountID int64, in TermsInput) (Card, error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return Card{}, err
	}
	acct, err := RequireCardTx(ctx, exec, accountID)
	if err != nil {
		return Card{}, err
	}

	card := Card{
		AccountID:              accountID,
		ProductName:            in.ProductName,
		StatementDay:           in.StatementDay,
		DueDaysAfterStatement:  in.DueDaysAfterStatement,
		PaymentProcessingDays:  in.PaymentProcessingDays,
		Denomination:           in.Denomination,
		DenominationConfirmed:  in.DenominationConfirmed,
		DefaultPaymentMethodID: in.DefaultPaymentMethodID,
		RefundPolicy:           in.RefundPolicy,
	}

	if in.CreditLimit != nil {
		cur, err := currency.NewRepository(exec).Get(ctx, acct.CurrencyCode)
		if err != nil {
			return Card{}, fmt.Errorf("looking up currency %q of card %d: %w", acct.CurrencyCode, accountID, err)
		}
		limit, err := money.Parse(*in.CreditLimit, cur.MinorUnit)
		if err != nil {
			return Card{}, fmt.Errorf("%w: the credit limit %q is not an amount in %s", ErrInvalidInput, *in.CreditLimit, cur.Code)
		}
		if limit <= 0 {
			return Card{}, fmt.Errorf("%w: a credit limit must be greater than zero; leave it empty when unknown", ErrInvalidInput)
		}
		card.CreditLimitMinor = &limit
	}

	if in.DefaultPaymentMethodID != nil {
		method, err := paymentmethod.NewRepository(exec).Get(ctx, *in.DefaultPaymentMethodID)
		switch {
		case errors.Is(err, paymentmethod.ErrNotFound):
			return Card{}, fmt.Errorf("payment method %d: %w", *in.DefaultPaymentMethodID, ErrPaymentMethodNotFound)
		case err != nil:
			return Card{}, fmt.Errorf("looking up payment method %d: %w", *in.DefaultPaymentMethodID, err)
		}
		if !method.IsActive {
			return Card{}, fmt.Errorf("payment method %q: %w", method.Name, ErrPaymentMethodInactive)
		}
	}
	return card, nil
}

// MarkChargeTx records, through exec, that the issuer charged a transaction as
// a kind. The transaction must be an EXPENSE on a liability: a charge is
// spending on the card, never income and never on a bank account.
func MarkChargeTx(ctx context.Context, exec database.DBTX, transactionID int64, kind ChargeKind) error {
	if err := checkChargeTx(ctx, exec, transactionID, kind); err != nil {
		return err
	}
	return NewRepository(exec).InsertCharge(ctx, transactionID, kind, database.Now())
}

// UpdateChargeTx corrects what a charge was charged as, after its transaction
// was corrected, and checks that it is still a charge's shape.
func UpdateChargeTx(ctx context.Context, exec database.DBTX, transactionID int64, kind ChargeKind) error {
	if _, isCharge, err := NewRepository(exec).ChargeKindOf(ctx, transactionID); err != nil {
		return err
	} else if !isCharge {
		return fmt.Errorf("%w: transaction %d", ErrNotACharge, transactionID)
	}
	if err := checkChargeTx(ctx, exec, transactionID, kind); err != nil {
		return err
	}
	return NewRepository(exec).UpdateChargeKind(ctx, transactionID, kind)
}

// IsChargeTx reports whether a transaction is a card charge.
func IsChargeTx(ctx context.Context, exec database.DBTX, transactionID int64) (bool, error) {
	_, isCharge, err := NewRepository(exec).ChargeKindOf(ctx, transactionID)
	return isCharge, err
}

// ChargeKindOf returns what a charge was charged as, and false when the
// transaction is not one.
func (s *Service) ChargeKindOf(ctx context.Context, transactionID int64) (ChargeKind, bool, error) {
	return NewRepository(s.db).ChargeKindOf(ctx, transactionID)
}

// ChargesOfAccount returns the kind of every charge on a card, by transaction.
func (s *Service) ChargesOfAccount(ctx context.Context, accountID int64) (map[int64]ChargeKind, error) {
	return NewRepository(s.db).ChargesOfAccount(ctx, accountID)
}

func checkChargeTx(ctx context.Context, exec database.DBTX, transactionID int64, kind ChargeKind) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: charge kind %q must be one of %v", ErrInvalidInput, kind, ChargeKinds())
	}
	repo := ledger.NewRepository(exec)
	tx, err := repo.GetTransaction(ctx, transactionID)
	if err != nil {
		return err
	}
	if tx.Type != ledger.Expense {
		return fmt.Errorf("%w: transaction %d is %s, and a charge is an expense", ErrNotACharge, transactionID, tx.Type)
	}
	movement, err := repo.GetMovementFor(ctx, transactionID)
	if err != nil {
		return err
	}
	acct, err := account.NewRepository(exec).Get(ctx, movement.AccountID)
	if err != nil {
		return fmt.Errorf("looking up account %d: %w", movement.AccountID, err)
	}
	if acct.Type.Nature() != account.Liability {
		return fmt.Errorf("%w: %q is not a credit account", ErrNotACharge, acct.Name)
	}
	return nil
}
