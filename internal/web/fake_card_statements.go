package web

import "context"

// The M8.2 half of FakeCards: statements, obligations and payments.

func (f *FakeCards) Standing(_ context.Context, id int64) (CardStandingView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.Cards[id]
	if !ok {
		return CardStandingView{}, ErrNotACreditCard
	}
	s := f.Standings[id]
	s.Card = c
	return s, nil
}

func (f *FakeCards) RecordPayment(_ context.Context, in RecordCardPaymentInput) (TransactionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return TransactionView{}, f.Err
	}
	if id, ok := f.replay(in.RequestKey); ok {
		return TransactionView{ID: id, AccountID: in.SourceAccountID}, nil
	}
	f.Payments = append(f.Payments, in)
	return TransactionView{ID: f.record(in.RequestKey), Type: TransactionTypeTransfer, AccountID: in.SourceAccountID}, nil
}

func (f *FakeCards) NextStatementDates(context.Context, int64) (string, string, error) {
	return "2026-09-26", "2026-10-25", nil
}

func (f *FakeCards) Statement(_ context.Context, _, statementID int64) (CardStatementInput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.StatementInputs[statementID]
	if !ok {
		return CardStatementInput{}, ErrStatementNotFound
	}
	return in, nil
}

func (f *FakeCards) RecordStatement(_ context.Context, _ int64, in CardStatementInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return 0, f.Err
	}
	id := f.record("")
	f.StatementInputs[id] = in
	return id, nil
}

func (f *FakeCards) UpdateStatement(_ context.Context, _, statementID int64, in CardStatementInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	if _, ok := f.StatementInputs[statementID]; !ok {
		return ErrStatementNotFound
	}
	f.StatementInputs[statementID] = in
	return nil
}

func (f *FakeCards) Reconcile(_ context.Context, id, statementID int64) (CardReconciliationView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.Reconciliations[statementID]
	if !ok {
		return CardReconciliationView{}, ErrStatementNotFound
	}
	r.Card = f.Cards[id]
	return r, nil
}

func (f *FakeCards) AssertMembership(_ context.Context, id, transactionID int64, statementID *int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Assertions = append(f.Assertions, FakeAssertion{CardID: id, TransactionID: transactionID, StatementID: statementID})
	return nil
}
