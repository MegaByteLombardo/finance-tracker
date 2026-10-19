package adapter

import (
	"context"

	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// M7.2: the fund.

func (a *savingsAdapter) GetFund(ctx context.Context) (web.SavingsFund, error) {
	f, err := a.svc.GetFund(ctx)
	if err != nil {
		return web.SavingsFund{}, TranslateSavingsError(err)
	}
	return toWebSavingsFund(f), nil
}

func (a *savingsAdapter) Reserve(ctx context.Context, in web.SavingsReserveInput) (web.SavingsOperation, error) {
	lines := []savings.ReserveLine{{AccountID: in.AccountID, GoalID: in.GoalID, Amount: in.Amount}}
	if len(in.Lines) > 0 {
		lines = make([]savings.ReserveLine, 0, len(in.Lines))
		for _, l := range in.Lines {
			lines = append(lines, savings.ReserveLine{AccountID: in.AccountID, GoalID: l.GoalID, Amount: l.Amount})
		}
	}
	op, err := a.svc.Reserve(ctx, savings.ReserveInput{
		RequestKey:          in.RequestKey,
		IncomeTransactionID: in.IncomeTransactionID,
		Lines:               lines,
		Exception:           in.Exception,
		Reason:              in.Reason,
	})
	if err != nil {
		return web.SavingsOperation{}, TranslateSavingsError(err)
	}
	return toWebSavingsOperation(op), nil
}

func (a *savingsAdapter) Release(ctx context.Context, in web.SavingsReleaseInput) (web.SavingsOperation, error) {
	op, err := a.svc.Release(ctx, savings.ReleaseInput{
		RequestKey: in.RequestKey,
		AccountID:  in.AccountID,
		GoalID:     in.GoalID,
		Amount:     in.Amount,
		Reason:     in.Reason,
	})
	if err != nil {
		return web.SavingsOperation{}, TranslateSavingsError(err)
	}
	return toWebSavingsOperation(op), nil
}

func (a *savingsAdapter) ListOperations(ctx context.Context, limit int) ([]web.SavingsOperation, error) {
	ops, err := a.svc.ListOperations(ctx, limit)
	if err != nil {
		return nil, TranslateSavingsError(err)
	}
	out := make([]web.SavingsOperation, 0, len(ops))
	for _, op := range ops {
		out = append(out, toWebSavingsOperation(op))
	}
	return out, nil
}

func toWebSavingsFund(f savings.Fund) web.SavingsFund {
	out := web.SavingsFund{
		Accounts:               make([]web.SavingsAccountAvailability, 0, len(f.Accounts)),
		BalanceFormatted:       usd(f.BalanceMinor),
		ReservedFormatted:      usd(f.ReservedMinor),
		FreeFormatted:          usd(f.FreeMinor),
		OverallocatedFormatted: usd(f.OverallocatedMinor),
		AllocatedFormatted:     usd(f.AllocatedMinor),
		UnallocatedFormatted:   usd(f.UnallocatedMinor),
		Overallocated:          f.OverallocatedMinor > 0,
		CanRelease:             f.ReservedMinor > 0,
	}
	for _, a := range f.Accounts {
		out.Accounts = append(out.Accounts, web.SavingsAccountAvailability{
			AccountID:              a.AccountID,
			AccountName:            a.AccountName,
			IsActive:               a.IsActive,
			BalanceFormatted:       usd(a.BalanceMinor),
			ReservedFormatted:      usd(a.ReservedMinor),
			FreeFormatted:          usd(a.FreeMinor),
			OverallocatedFormatted: usd(a.OverallocatedMinor),
			HasReservation:         a.ReservedMinor != 0,
			HasFree:                a.IsActive && a.FreeMinor > 0,
			Overallocated:          a.OverallocatedMinor > 0,
		})
		if a.IsActive && a.FreeMinor > 0 {
			out.CanReserve = true
		}
	}
	return out
}

func toWebSavingsOperation(op savings.Operation) web.SavingsOperation {
	out := web.SavingsOperation{
		ID:                  op.ID,
		Kind:                string(op.Kind),
		KindLabel:           operationKindLabel(op.Kind),
		IncomeTransactionID: op.IncomeTransactionID,
		TransactionID:       op.TransactionID,
		Reason:              op.Reason,
		IsException:         op.IsException,
		EffectiveDate:       op.EffectiveDate,
		CreatedAt:           op.CreatedAt,
		TotalFormatted:      usd(op.TotalMinor),
		IsOutflow:           op.TotalMinor < 0,
	}
	for _, e := range op.Entries {
		out.Entries = append(out.Entries, web.SavingsEntry{
			AccountName:     e.AccountName,
			GoalName:        e.GoalName,
			AmountFormatted: usd(e.AmountMinor),
		})
	}
	return out
}

func operationKindLabel(k savings.OperationKind) string {
	switch k {
	case savings.KindReserve:
		return "Reserva"
	case savings.KindRelease:
		return "Liberación"
	case savings.KindReallocate:
		return "Reasignación"
	case savings.KindReback:
		return "Cambio de cuenta"
	case savings.KindSpend:
		return "Gasto con ahorro"
	default:
		return string(k)
	}
}

func confirmationLabel(c savings.Confirmation) string {
	switch c {
	case savings.ConfirmationPending:
		return "Pendiente de confirmar"
	case savings.ConfirmationPartial:
		return "Confirmada en parte"
	case savings.ConfirmationFull:
		return "Confirmada"
	default:
		return ""
	}
}

// usd formats USD cents.
func usd(minor int64) string { return money.Format(minor, usdMinorUnit) }
