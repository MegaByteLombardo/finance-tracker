package web

import (
	"context"
	"sync"
)

// FakeCards is the M8.1 stand-in for the card service. Handler tests set what it
// returns and read back what it was asked; the card rules themselves are
// tested against real SQLite in internal/creditcard and internal/application.
type FakeCards struct {
	mu sync.Mutex

	Cards       map[int64]CreditCardView
	Activity    map[int64][]CardMovement
	ChargeKinds map[int64]string

	// Err, when set, is returned by every write.
	Err error

	// What the handlers asked for. A request key already seen returns the
	// same result without recording again, as the backend does.
	Opened    []OpenCreditCardInput
	Terms     map[int64]CreditCardTermsInput
	Purchases []RecordExpenseInput
	Charges   []string // kinds, in order
	Refunds   []RecordRefundInput
	Updated   []UpdateRefundInput
	keys      map[string]int64
	nextID    int64
}

// NewFakeCards returns an empty stand-in.
func NewFakeCards() *FakeCards {
	return &FakeCards{
		Cards: make(map[int64]CreditCardView), Activity: make(map[int64][]CardMovement),
		ChargeKinds: make(map[int64]string), Terms: make(map[int64]CreditCardTermsInput),
		keys: make(map[string]int64), nextID: 1000,
	}
}

func (f *FakeCards) List(context.Context, bool) ([]CreditCardView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]CreditCardView, 0, len(f.Cards))
	for _, c := range f.Cards {
		out = append(out, c)
	}
	return out, nil
}

func (f *FakeCards) Get(_ context.Context, id int64) (CreditCardView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.Cards[id]
	if !ok {
		return CreditCardView{}, ErrNotACreditCard
	}
	return c, nil
}

func (f *FakeCards) replay(key string) (int64, bool) {
	if key == "" {
		return 0, false
	}
	id, ok := f.keys[key]
	return id, ok
}

func (f *FakeCards) record(key string) int64 {
	f.nextID++
	if key != "" {
		f.keys[key] = f.nextID
	}
	return f.nextID
}

func (f *FakeCards) Open(_ context.Context, in OpenCreditCardInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return 0, f.Err
	}
	if id, ok := f.replay(in.RequestKey); ok {
		return id, nil
	}
	f.Opened = append(f.Opened, in)
	id := f.record(in.RequestKey)
	f.Cards[id] = CreditCardView{AccountID: id, Name: in.Name, CurrencyCode: in.CurrencyCode,
		StatementDay: in.Terms.StatementDay, IsActive: true, Denomination: "NOMINAL"}
	return id, nil
}

func (f *FakeCards) UpdateTerms(_ context.Context, id int64, in CreditCardTermsInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Terms[id] = in
	return nil
}

func (f *FakeCards) Movements(_ context.Context, id int64, _ int) ([]CardMovement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Activity[id], nil
}

func (f *FakeCards) RecordPurchase(_ context.Context, key string, in RecordExpenseInput) (TransactionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return TransactionView{}, f.Err
	}
	if id, ok := f.replay(key); ok {
		return TransactionView{ID: id}, nil
	}
	f.Purchases = append(f.Purchases, in)
	return TransactionView{ID: f.record(key), Type: TransactionTypeExpense}, nil
}

func (f *FakeCards) RecordCharge(_ context.Context, key, kind string, in RecordExpenseInput) (TransactionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return TransactionView{}, f.Err
	}
	if id, ok := f.replay(key); ok {
		return TransactionView{ID: id}, nil
	}
	f.Charges = append(f.Charges, kind)
	id := f.record(key)
	f.ChargeKinds[id] = kind
	return TransactionView{ID: id, Type: TransactionTypeExpense}, nil
}

func (f *FakeCards) UpdateCharge(_ context.Context, id int64, kind string, _ UpdateTransactionInput) (TransactionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return TransactionView{}, f.Err
	}
	f.ChargeKinds[id] = kind
	return TransactionView{ID: id, Type: TransactionTypeExpense}, nil
}

func (f *FakeCards) ChargeKind(_ context.Context, id int64) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kind, ok := f.ChargeKinds[id]
	return kind, ok, nil
}

func (f *FakeCards) RecordRefund(_ context.Context, key string, in RecordRefundInput) (TransactionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return TransactionView{}, f.Err
	}
	if id, ok := f.replay(key); ok {
		return TransactionView{ID: id}, nil
	}
	f.Refunds = append(f.Refunds, in)
	return TransactionView{ID: f.record(key), Type: TransactionTypeRefund}, nil
}

func (f *FakeCards) UpdateRefund(_ context.Context, id int64, in UpdateRefundInput) (TransactionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return TransactionView{}, f.Err
	}
	f.Updated = append(f.Updated, in)
	return TransactionView{ID: id, Type: TransactionTypeRefund}, nil
}
