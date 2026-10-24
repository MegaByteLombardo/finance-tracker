package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// M8.1: credit cards. Each workflow below writes to the ledger and to the card
// domain -- or to the ledger and its request log -- and commits both or neither.
//
// Every one of them takes a request key the form rendered (TD-095). The first
// submission records what it produced under that key; a second one finds it and
// returns the same result, writing nothing. The lookup happens inside the
// transaction that would write, which holds the write lock from BEGIN (TD-075).

// OpenCreditCardInput is the input to Service.OpenCreditCard.
type OpenCreditCardInput struct {
	RequestKey string

	// The account: a name, an institution and a currency, exactly as any other
	// account has them. Its type is CREDIT_CARD by construction.
	Name         string
	Institution  *string
	CurrencyCode string

	// Terms are the issuer's.
	Terms creditcard.TermsInput

	// OpeningDebt is what was owed on the card before this application knew
	// about it, as a POSITIVE decimal string ("1250.00"), recorded as a
	// negative opening balance. nil, blank or zero means nothing was owed.
	OpeningDebt     *string
	OpeningDebtDate string
}

// OpenCreditCardResult is what OpenCreditCard produced.
type OpenCreditCardResult struct {
	Account account.Account
	Card    creditcard.Card

	// OpeningBalance is nil when no previous debt was recorded.
	OpeningBalance *ledger.TransactionView
}

// OpenCreditCard opens a card -- the account, its terms and any previous debt --
// in one transaction, or nothing.
func (s *Service) OpenCreditCard(ctx context.Context, in OpenCreditCardInput) (OpenCreditCardResult, error) {
	var result OpenCreditCardResult
	err := s.inTx(ctx, "opening a credit card", func(exec database.DBTX) error {
		request, replay, err := s.replay(ctx, exec, in.RequestKey)
		if err != nil {
			return err
		}
		if replay {
			if request.AccountID == nil {
				return fmt.Errorf("%w: it was used for something other than opening a card", ledger.ErrInvalidRequestKey)
			}
			return s.readOpenedCard(ctx, exec, request, &result)
		}

		debt, err := openingDebt(in.OpeningDebt)
		if err != nil {
			return err
		}
		if result.Account, err = account.CreateTx(ctx, exec, account.CreateInput{
			Name:         in.Name,
			Type:         account.CreditCard,
			CurrencyCode: in.CurrencyCode,
			Institution:  in.Institution,
		}); err != nil {
			return err
		}
		if result.Card, err = creditcard.CreateTx(ctx, exec, result.Account.ID, in.Terms); err != nil {
			return err
		}
		if debt != "" {
			if result.OpeningBalance, err = ledger.RecordOpeningBalanceTx(ctx, exec, ledger.RecordOpeningBalanceInput{
				AccountID:     result.Account.ID,
				Amount:        debt,
				EffectiveDate: in.OpeningDebtDate,
			}); err != nil {
				return err
			}
		}

		record := ledger.Request{Key: in.RequestKey, AccountID: &result.Account.ID}
		if result.OpeningBalance != nil {
			record.TransactionID = &result.OpeningBalance.ID
		}
		return ledger.RecordRequestTx(ctx, exec, record)
	})
	if err != nil {
		return OpenCreditCardResult{}, err
	}
	return result, nil
}

func (s *Service) readOpenedCard(ctx context.Context, exec database.DBTX, request ledger.Request, result *OpenCreditCardResult) error {
	var err error
	if result.Account, err = account.NewRepository(exec).Get(ctx, *request.AccountID); err != nil {
		return err
	}
	if result.Card, err = creditcard.NewRepository(exec).Get(ctx, *request.AccountID); err != nil {
		return err
	}
	if request.TransactionID != nil {
		view, err := ledger.NewRepository(exec).GetView(ctx, *request.TransactionID)
		if err != nil {
			return err
		}
		result.OpeningBalance = &view
	}
	return nil
}

// openingDebt turns the debt a user typed into the opening balance the ledger
// records: "1250.00" owed is a balance of "-1250.00".
//
// The sign belongs to the operation, as everywhere in the ledger. A signed
// value is refused rather than reinterpreted: a user who typed "-1250" may have
// meant a credit balance, and guessing would record the opposite of the truth.
// The amount itself is read by the ledger, at the card currency's scale.
func openingDebt(raw *string) (string, error) {
	amount := openingAmount(raw)
	if amount == "" {
		return "", nil
	}
	if strings.HasPrefix(amount, "-") || strings.HasPrefix(amount, "+") {
		return "", fmt.Errorf("%w: the previous debt is entered as a positive amount", ledger.ErrInvalidAmount)
	}
	return "-" + amount, nil
}

// RecordCardPurchase records a purchase with a card: an EXPENSE whose one
// movement lowers the card's balance (I3: no asset is touched). It is exactly
// what the ledger's RecordExpense records; what this adds is the check that the
// account IS a card, and the request key.
func (s *Service) RecordCardPurchase(ctx context.Context, requestKey string, in ledger.RecordExpenseInput) (ledger.TransactionView, error) {
	return s.recordOnce(ctx, "recording a card purchase", requestKey, func(exec database.DBTX) (ledger.TransactionView, error) {
		if _, err := creditcard.RequireCardTx(ctx, exec, in.AccountID); err != nil {
			return ledger.TransactionView{}, err
		}
		return ledger.RecordExpenseTx(ctx, exec, in)
	})
}

// RecordCardCharge records interest, a fee or a penalty the issuer charged: an
// EXPENSE on the card, marked with its kind in the same transaction.
func (s *Service) RecordCardCharge(
	ctx context.Context, requestKey string, kind creditcard.ChargeKind, in ledger.RecordExpenseInput,
) (ledger.TransactionView, error) {
	return s.recordOnce(ctx, "recording a card charge", requestKey, func(exec database.DBTX) (ledger.TransactionView, error) {
		if _, err := creditcard.RequireCardTx(ctx, exec, in.AccountID); err != nil {
			return ledger.TransactionView{}, err
		}
		view, err := ledger.RecordExpenseTx(ctx, exec, in)
		if err != nil {
			return ledger.TransactionView{}, err
		}
		return view, creditcard.MarkChargeTx(ctx, exec, view.ID, kind)
	})
}

// UpdateCardCharge corrects a charge: the expense, and what it was charged as,
// together. It is the only edit a charge accepts (TD-045): the guard names the
// one shape it takes -- a transaction that is a charge -- and the result must
// still be an expense on a card.
func (s *Service) UpdateCardCharge(
	ctx context.Context, id int64, kind creditcard.ChargeKind, in ledger.UpdateTransactionInput,
) (ledger.TransactionView, error) {
	var view ledger.TransactionView
	err := s.inTx(ctx, "correcting a card charge", func(exec database.DBTX) error {
		isCharge, err := creditcard.IsChargeTx(ctx, exec, id)
		if err != nil {
			return err
		}
		if !isCharge {
			return fmt.Errorf("%w: transaction %d", creditcard.ErrNotACharge, id)
		}
		// A charge stays an expense: the type is not the caller's to change.
		in.Type = ledger.Expense
		if view, err = ledger.UpdateTransactionTx(ctx, exec, id, in); err != nil {
			return err
		}
		return creditcard.UpdateChargeTx(ctx, exec, id, kind)
	})
	if err != nil {
		return ledger.TransactionView{}, err
	}
	return view, nil
}

// RecordRefund records money given back for an expense, on any account: a card
// (it lowers the debt) or a bank account (it raises the balance). It is never
// income and never eligible for savings (TD-090, TD-094).
func (s *Service) RecordRefund(ctx context.Context, requestKey string, in ledger.RecordRefundInput) (ledger.TransactionView, error) {
	return s.recordOnce(ctx, "recording a refund", requestKey, func(exec database.DBTX) (ledger.TransactionView, error) {
		return ledger.RecordRefundTx(ctx, exec, in)
	})
}

// UpdateRefund corrects a refund and its link to the expense it gives back.
func (s *Service) UpdateRefund(ctx context.Context, id int64, in ledger.UpdateRefundInput) (ledger.TransactionView, error) {
	var view ledger.TransactionView
	err := s.inTx(ctx, "correcting a refund", func(exec database.DBTX) error {
		var err error
		view, err = ledger.UpdateRefundTx(ctx, exec, id, in)
		return err
	})
	if err != nil {
		return ledger.TransactionView{}, err
	}
	return view, nil
}

// recordOnce runs a recording workflow at most once per request key, in one
// transaction with the key's record.
func (s *Service) recordOnce(
	ctx context.Context, what, requestKey string, record func(exec database.DBTX) (ledger.TransactionView, error),
) (ledger.TransactionView, error) {
	var view ledger.TransactionView
	err := s.inTx(ctx, what, func(exec database.DBTX) error {
		request, replay, err := s.replay(ctx, exec, requestKey)
		if err != nil {
			return err
		}
		if replay {
			if request.TransactionID == nil {
				return fmt.Errorf("%w: it was used for something other than recording a transaction", ledger.ErrInvalidRequestKey)
			}
			view, err = ledger.NewRepository(exec).GetView(ctx, *request.TransactionID)
			return err
		}
		if view, err = record(exec); err != nil {
			return err
		}
		return ledger.RecordRequestTx(ctx, exec, ledger.Request{Key: requestKey, TransactionID: &view.ID})
	})
	if err != nil {
		return ledger.TransactionView{}, err
	}
	return view, nil
}

// replay validates a request key and returns what it already produced, if
// anything.
func (s *Service) replay(ctx context.Context, exec database.DBTX, key string) (ledger.Request, bool, error) {
	if err := ledger.ValidateRequestKey(key); err != nil {
		return ledger.Request{}, false, err
	}
	return ledger.ReplayedRequestTx(ctx, exec, key)
}
