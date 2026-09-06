package adapter

import (
	"errors"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/backup"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/paymentmethod"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// translated reports the web sentinel while preserving the domain cause message.
type translated struct {
	sentinel error
	cause    error
}

func (e translated) Error() string {
	return e.cause.Error()
}

func (e translated) Unwrap() error {
	return e.cause
}

func (e translated) Is(target error) bool {
	return target == e.sentinel
}

func translateError(sentinel error, cause error) error {
	if cause == nil {
		return nil
	}
	return translated{
		sentinel: sentinel,
		cause:    cause,
	}
}

func TranslateAccountError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, account.ErrNotFound):
		return translateError(web.ErrNotFound, err)
	case errors.Is(err, account.ErrInvalidInput):
		return translateError(web.ErrInvalidInput, err)
	case errors.Is(err, account.ErrConflict):
		return translateError(web.ErrConflict, err)
	case errors.Is(err, account.ErrCurrencyNotFound):
		return translateError(web.ErrCurrencyNotFound, err)
	case errors.Is(err, account.ErrCurrencyInactive):
		return translateError(web.ErrCurrencyInactive, err)
	default:
		return err
	}
}

func TranslateCategoryError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, category.ErrNotFound):
		return translateError(web.ErrNotFound, err)
	case errors.Is(err, category.ErrInvalidInput):
		return translateError(web.ErrInvalidInput, err)
	case errors.Is(err, category.ErrConflict):
		return translateError(web.ErrConflict, err)
	default:
		return err
	}
}

func TranslateCurrencyError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, currency.ErrNotFound):
		return translateError(web.ErrNotFound, err)
	case errors.Is(err, currency.ErrInvalidInput):
		return translateError(web.ErrInvalidInput, err)
	case errors.Is(err, currency.ErrConflict):
		return translateError(web.ErrConflict, err)
	default:
		return err
	}
}

func TranslatePaymentMethodError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, paymentmethod.ErrNotFound):
		return translateError(web.ErrNotFound, err)
	case errors.Is(err, paymentmethod.ErrInvalidInput):
		return translateError(web.ErrInvalidInput, err)
	case errors.Is(err, paymentmethod.ErrConflict):
		return translateError(web.ErrConflict, err)
	default:
		return err
	}
}

func TranslateLedgerError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		return translateError(web.ErrNotFound, err)
	case errors.Is(err, ledger.ErrInvalidInput):
		return translateError(web.ErrInvalidInput, err)
	case errors.Is(err, ledger.ErrInvalidAmount):
		return translateError(web.ErrInvalidAmount, err)
	case errors.Is(err, ledger.ErrAccountNotFound):
		return translateError(web.ErrAccountNotFound, err)
	case errors.Is(err, ledger.ErrAccountInactive):
		return translateError(web.ErrAccountInactive, err)
	case errors.Is(err, ledger.ErrCategoryNotFound):
		return translateError(web.ErrCategoryNotFound, err)
	case errors.Is(err, ledger.ErrCategoryInactive):
		return translateError(web.ErrCategoryInactive, err)
	case errors.Is(err, ledger.ErrCategoryTypeMismatch):
		return translateError(web.ErrCategoryTypeMismatch, err)
	case errors.Is(err, ledger.ErrPaymentMethodNotFound):
		return translateError(web.ErrPaymentMethodNotFound, err)
	case errors.Is(err, ledger.ErrPaymentMethodInactive):
		return translateError(web.ErrPaymentMethodInactive, err)
	case errors.Is(err, ledger.ErrTypeChangeNotAllowed):
		return translateError(web.ErrTypeChangeNotAllowed, err)
	case errors.Is(err, ledger.ErrCurrencyChangeNotConfirmed):
		return translateError(web.ErrCurrencyChangeNotConfirmed, err)
	default:
		return err
	}
}

func TranslateBackupError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, backup.ErrNotFound):
		return translateError(web.ErrNotFound, err)
	case errors.Is(err, backup.ErrInvalidName), errors.Is(err, backup.ErrInvalidBackup):
		return translateError(web.ErrInvalidInput, err)
	default:
		return err
	}
}

func TranslateApplicationError(err error) error {
	if err == nil {
		return nil
	}
	// Application.OpenAccount can return errors from account or ledger domains.
	if accErr := TranslateAccountError(err); accErr != err {
		return accErr
	}
	if ledErr := TranslateLedgerError(err); ledErr != err {
		return ledErr
	}
	return err
}
