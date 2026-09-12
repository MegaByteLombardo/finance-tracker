package web

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeServices provides an in-memory, thread-safe implementation of all M2 services
// following the exact rules and invariants defined in docs/m2-contract.md.
type FakeServices struct {
	// Rates holds the M5 stand-ins: exchange rates, the valuation base and the
	// net worth. It is exported so a test can drive the provider's failure paths
	// and the incomplete-valuation path, which is most of what is interesting
	// about those screens.
	Rates *FakeRateStore

	mu           sync.RWMutex
	currencies   map[string]Currency
	accounts     map[int64]Account
	nextAcctID   int64
	categories   map[int64]Category
	nextCatID    int64
	methods      map[int64]PaymentMethod
	nextMethodID int64
	transactions []TransactionView
	nextTxID     int64
	nextMovID    int64
	backups      []Backup
}

func NewFakeServices() *FakeServices {
	usdSymbol := "$"
	eurSymbol := "€"
	vesSymbol := "Bs."

	now := time.Now().UTC()

	fs := &FakeServices{
		methods: map[int64]PaymentMethod{
			1: {ID: 1, Name: "Bank transfer", IsActive: true, CreatedAt: now, UpdatedAt: now},
			2: {ID: 2, Name: "Point of sale", IsActive: true, CreatedAt: now, UpdatedAt: now},
			3: {ID: 3, Name: "Cash", IsActive: true, CreatedAt: now, UpdatedAt: now},
			4: {ID: 4, Name: "Other", IsActive: true, CreatedAt: now, UpdatedAt: now},
		},
		nextMethodID: 5,
		currencies: map[string]Currency{
			"USD": {
				Code:      "USD",
				Name:      "United States Dollar",
				Symbol:    &usdSymbol,
				MinorUnit: 2,
				IsActive:  true,
				CreatedAt: now,
				UpdatedAt: now,
			},
			"EUR": {
				Code:      "EUR",
				Name:      "Euro",
				Symbol:    &eurSymbol,
				MinorUnit: 2,
				IsActive:  true,
				CreatedAt: now,
				UpdatedAt: now,
			},
			"VES": {
				Code:      "VES",
				Name:      "Bolívar Soberano",
				Symbol:    &vesSymbol,
				MinorUnit: 2,
				IsActive:  true,
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		accounts:     make(map[int64]Account),
		nextAcctID:   1,
		categories:   make(map[int64]Category),
		nextCatID:    1,
		transactions: make([]TransactionView, 0),
		nextTxID:     1,
		nextMovID:    1,
	}

	return fs
}

// Exact money helpers without float

func parseAmount(s string, minorUnit int) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrInvalidAmount
	}

	negative := false
	if strings.HasPrefix(s, "-") {
		negative = true
		s = s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}

	if s == "" {
		return 0, ErrInvalidAmount
	}

	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return 0, ErrInvalidAmount
	}

	intPart := parts[0]
	if intPart == "" {
		return 0, ErrInvalidAmount
	}
	for _, ch := range intPart {
		if ch < '0' || ch > '9' {
			return 0, ErrInvalidAmount
		}
	}

	var decPart string
	if len(parts) == 2 {
		decPart = parts[1]
		if decPart == "" {
			return 0, ErrInvalidAmount
		}
		for _, ch := range decPart {
			if ch < '0' || ch > '9' {
				return 0, ErrInvalidAmount
			}
		}
		if len(decPart) > minorUnit {
			return 0, ErrInvalidAmount
		}
	}

	// Pad decPart to minorUnit
	for len(decPart) < minorUnit {
		decPart += "0"
	}

	combined := intPart + decPart
	// Remove leading zeros but keep at least 1 digit
	combined = strings.TrimLeft(combined, "0")
	if combined == "" {
		combined = "0"
	}

	val, err := strconv.ParseInt(combined, 10, 64)
	if err != nil {
		return 0, ErrInvalidAmount
	}

	if negative && val != 0 {
		val = -val
	}

	return val, nil
}

func formatAmount(minor int64, minorUnit int) string {
	negative := minor < 0
	if negative {
		minor = -minor
	}

	s := strconv.FormatInt(minor, 10)
	if minorUnit == 0 {
		if negative {
			return "-" + s
		}
		return s
	}

	for len(s) <= minorUnit {
		s = "0" + s
	}

	splitIdx := len(s) - minorUnit
	intPart := s[:splitIdx]
	decPart := s[splitIdx:]

	res := intPart + "." + decPart
	if negative {
		res = "-" + res
	}
	return res
}

// CurrencyService implementation

func (fs *FakeServices) List(ctx context.Context, includeInactive bool) ([]Currency, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	var result []Currency
	for _, c := range fs.currencies {
		if includeInactive || c.IsActive {
			result = append(result, c)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Code < result[j].Code
	})
	return result, nil
}

func (fs *FakeServices) Get(ctx context.Context, code string) (Currency, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	c, ok := fs.currencies[strings.ToUpper(strings.TrimSpace(code))]
	if !ok {
		return Currency{}, ErrCurrencyNotFound
	}
	return c, nil
}

// AccountService implementation

func (fs *FakeServices) ListAccounts(ctx context.Context, includeInactive bool) ([]Account, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	var result []Account
	for _, a := range fs.accounts {
		if includeInactive || a.IsActive {
			result = append(result, a)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// AccountService interface method
func (fs *FakeServices) List_Accounts(ctx context.Context, includeInactive bool) ([]Account, error) {
	return fs.ListAccounts(ctx, includeInactive)
}

func (fs *FakeServices) GetAccount(ctx context.Context, id int64) (Account, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	a, ok := fs.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

func (fs *FakeServices) CreateAccount(ctx context.Context, in CreateAccountInput) (Account, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 120 {
		return Account{}, ErrInvalidInput
	}

	curr, ok := fs.currencies[in.CurrencyCode]
	if !ok {
		return Account{}, ErrCurrencyNotFound
	}
	if !curr.IsActive {
		return Account{}, ErrCurrencyInactive
	}

	// Validate subtype rule
	if in.Type == AccountTypeBank {
		if in.Subtype == nil {
			return Account{}, ErrInvalidInput
		}
		if *in.Subtype != AccountSubtypeChecking && *in.Subtype != AccountSubtypeSavings {
			return Account{}, ErrInvalidInput
		}
	} else {
		if in.Subtype != nil {
			return Account{}, ErrInvalidInput
		}
	}

	var inst *string
	if in.Institution != nil {
		it := strings.TrimSpace(*in.Institution)
		if it != "" {
			if len(it) > 120 {
				return Account{}, ErrInvalidInput
			}
			inst = &it
		}
	}

	now := time.Now().UTC()
	acct := Account{
		ID:           fs.nextAcctID,
		Name:         name,
		Type:         in.Type,
		Subtype:      in.Subtype,
		CurrencyCode: in.CurrencyCode,
		Institution:  inst,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	fs.nextAcctID++
	fs.accounts[acct.ID] = acct

	return acct, nil
}

func (fs *FakeServices) UpdateAccountMetadata(ctx context.Context, id int64, in UpdateAccountMetadataInput) (Account, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	acct, ok := fs.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}

	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 120 {
		return Account{}, ErrInvalidInput
	}

	if in.Type == AccountTypeBank {
		if in.Subtype == nil {
			return Account{}, ErrInvalidInput
		}
		if *in.Subtype != AccountSubtypeChecking && *in.Subtype != AccountSubtypeSavings {
			return Account{}, ErrInvalidInput
		}
	} else {
		if in.Subtype != nil {
			return Account{}, ErrInvalidInput
		}
	}

	var inst *string
	if in.Institution != nil {
		it := strings.TrimSpace(*in.Institution)
		if it != "" {
			if len(it) > 120 {
				return Account{}, ErrInvalidInput
			}
			inst = &it
		}
	}

	acct.Name = name
	acct.Type = in.Type
	acct.Subtype = in.Subtype
	acct.Institution = inst
	acct.UpdatedAt = time.Now().UTC()

	fs.accounts[id] = acct
	return acct, nil
}

func (fs *FakeServices) SetAccountActive(ctx context.Context, id int64, isActive bool) (Account, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	acct, ok := fs.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	acct.IsActive = isActive
	acct.UpdatedAt = time.Now().UTC()
	fs.accounts[id] = acct
	return acct, nil
}

// CategoryService implementation

func (fs *FakeServices) ListCategories(ctx context.Context, in ListCategoriesInput) ([]Category, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	var result []Category
	for _, c := range fs.categories {
		if in.Type != nil && c.Type != *in.Type {
			continue
		}
		if !in.IncludeInactive && !c.IsActive {
			continue
		}
		result = append(result, c)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		if strings.ToLower(result[i].Name) != strings.ToLower(result[j].Name) {
			return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
		}
		return result[i].ID < result[j].ID
	})

	return result, nil
}

func (fs *FakeServices) GetCategory(ctx context.Context, id int64) (Category, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	c, ok := fs.categories[id]
	if !ok {
		return Category{}, ErrNotFound
	}
	return c, nil
}

func (fs *FakeServices) CreateCategory(ctx context.Context, in CreateCategoryInput) (Category, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		return Category{}, ErrInvalidInput
	}
	if in.Type != CategoryTypeIncome && in.Type != CategoryTypeExpense {
		return Category{}, ErrInvalidInput
	}

	// Check case-insensitive uniqueness per type
	for _, c := range fs.categories {
		if c.Type == in.Type && strings.EqualFold(c.Name, name) {
			return Category{}, ErrConflict
		}
	}

	now := time.Now().UTC()
	cat := Category{
		ID:        fs.nextCatID,
		Name:      name,
		Type:      in.Type,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	fs.nextCatID++
	fs.categories[cat.ID] = cat

	return cat, nil
}

func (fs *FakeServices) UpdateCategoryMetadata(ctx context.Context, id int64, in UpdateCategoryMetadataInput) (Category, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	cat, ok := fs.categories[id]
	if !ok {
		return Category{}, ErrNotFound
	}

	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		return Category{}, ErrInvalidInput
	}

	for _, c := range fs.categories {
		if c.ID != id && c.Type == cat.Type && strings.EqualFold(c.Name, name) {
			return Category{}, ErrConflict
		}
	}

	cat.Name = name
	cat.UpdatedAt = time.Now().UTC()
	fs.categories[id] = cat
	return cat, nil
}

func (fs *FakeServices) SetCategoryActive(ctx context.Context, id int64, isActive bool) (Category, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	cat, ok := fs.categories[id]
	if !ok {
		return Category{}, ErrNotFound
	}
	cat.IsActive = isActive
	cat.UpdatedAt = time.Now().UTC()
	fs.categories[id] = cat
	return cat, nil
}

// LedgerService implementation

func (fs *FakeServices) RecordIncome(ctx context.Context, in RecordIncomeInput) (TransactionView, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	acct, ok := fs.accounts[in.AccountID]
	if !ok {
		return TransactionView{}, ErrAccountNotFound
	}
	if !acct.IsActive {
		return TransactionView{}, ErrAccountInactive
	}

	cat, ok := fs.categories[in.CategoryID]
	if !ok {
		return TransactionView{}, ErrCategoryNotFound
	}
	if !cat.IsActive {
		return TransactionView{}, ErrCategoryInactive
	}
	if cat.Type != CategoryTypeIncome {
		return TransactionView{}, ErrCategoryTypeMismatch
	}

	curr := fs.currencies[acct.CurrencyCode]

	// Amount must be positive string
	if strings.HasPrefix(strings.TrimSpace(in.Amount), "-") {
		return TransactionView{}, ErrInvalidAmount
	}
	minor, err := parseAmount(in.Amount, curr.MinorUnit)
	if err != nil || minor <= 0 {
		return TransactionView{}, ErrInvalidAmount
	}

	effDate, err := time.Parse("2006-01-02", in.EffectiveDate)
	if err != nil {
		return TransactionView{}, ErrInvalidInput
	}

	var desc *string
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if d != "" {
			if len(d) > 255 {
				return TransactionView{}, ErrInvalidInput
			}
			desc = &d
		}
	}

	method, ok := fs.methods[in.PaymentMethodID]
	if !ok {
		return TransactionView{}, ErrPaymentMethodNotFound
	}
	if !method.IsActive {
		return TransactionView{}, ErrPaymentMethodInactive
	}
	methodID, methodName := method.ID, method.Name
	now := time.Now().UTC()

	tx := TransactionView{
		ID:                fs.nextTxID,
		Type:              TransactionTypeIncome,
		EffectiveDate:     effDate,
		Description:       desc,
		PaymentMethodID:   &methodID,
		PaymentMethodName: &methodName,
		CategoryID:        &cat.ID,
		CategoryName:      &cat.Name,
		CategoryType:      &cat.Type,
		MovementID:        fs.nextMovID,
		AccountID:         acct.ID,
		AccountName:       acct.Name,
		AccountType:       acct.Type,
		AccountSubtype:    acct.Subtype,
		Institution:       acct.Institution,
		CurrencyCode:      curr.Code,
		CurrencySymbol:    curr.Symbol,
		MinorUnit:         curr.MinorUnit,
		AmountMinor:       minor,
		AmountFormatted:   formatAmount(minor, curr.MinorUnit),
		CreatedAt:         now,
	}

	fs.nextTxID++
	fs.nextMovID++
	fs.transactions = append(fs.transactions, tx)

	return tx, nil
}

func (fs *FakeServices) RecordExpense(ctx context.Context, in RecordExpenseInput) (TransactionView, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	acct, ok := fs.accounts[in.AccountID]
	if !ok {
		return TransactionView{}, ErrAccountNotFound
	}
	if !acct.IsActive {
		return TransactionView{}, ErrAccountInactive
	}

	cat, ok := fs.categories[in.CategoryID]
	if !ok {
		return TransactionView{}, ErrCategoryNotFound
	}
	if !cat.IsActive {
		return TransactionView{}, ErrCategoryInactive
	}
	if cat.Type != CategoryTypeExpense {
		return TransactionView{}, ErrCategoryTypeMismatch
	}

	curr := fs.currencies[acct.CurrencyCode]

	// Amount must be positive string
	if strings.HasPrefix(strings.TrimSpace(in.Amount), "-") {
		return TransactionView{}, ErrInvalidAmount
	}
	minor, err := parseAmount(in.Amount, curr.MinorUnit)
	if err != nil || minor <= 0 {
		return TransactionView{}, ErrInvalidAmount
	}

	effDate, err := time.Parse("2006-01-02", in.EffectiveDate)
	if err != nil {
		return TransactionView{}, ErrInvalidInput
	}

	var desc *string
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if d != "" {
			if len(d) > 255 {
				return TransactionView{}, ErrInvalidInput
			}
			desc = &d
		}
	}

	method, ok := fs.methods[in.PaymentMethodID]
	if !ok {
		return TransactionView{}, ErrPaymentMethodNotFound
	}
	if !method.IsActive {
		return TransactionView{}, ErrPaymentMethodInactive
	}
	methodID, methodName := method.ID, method.Name
	now := time.Now().UTC()

	// Stored negative for expense
	storedMinor := -minor

	tx := TransactionView{
		ID:                fs.nextTxID,
		Type:              TransactionTypeExpense,
		EffectiveDate:     effDate,
		Description:       desc,
		PaymentMethodID:   &methodID,
		PaymentMethodName: &methodName,
		CategoryID:        &cat.ID,
		CategoryName:      &cat.Name,
		CategoryType:      &cat.Type,
		MovementID:        fs.nextMovID,
		AccountID:         acct.ID,
		AccountName:       acct.Name,
		AccountType:       acct.Type,
		AccountSubtype:    acct.Subtype,
		Institution:       acct.Institution,
		CurrencyCode:      curr.Code,
		CurrencySymbol:    curr.Symbol,
		MinorUnit:         curr.MinorUnit,
		AmountMinor:       storedMinor,
		AmountFormatted:   formatAmount(storedMinor, curr.MinorUnit),
		CreatedAt:         now,
	}

	fs.nextTxID++
	fs.nextMovID++
	fs.transactions = append(fs.transactions, tx)

	return tx, nil
}

func (fs *FakeServices) RecordOpeningBalance(ctx context.Context, in RecordOpeningBalanceInput) (*TransactionView, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	acct, ok := fs.accounts[in.AccountID]
	if !ok {
		return nil, ErrAccountNotFound
	}
	if !acct.IsActive {
		return nil, ErrAccountInactive
	}

	curr := fs.currencies[acct.CurrencyCode]

	minor, err := parseAmount(in.Amount, curr.MinorUnit)
	if err != nil {
		return nil, ErrInvalidAmount
	}

	// An opening balance of zero records nothing! (Section 9)
	if minor == 0 {
		return nil, nil
	}

	effDate, err := time.Parse("2006-01-02", in.EffectiveDate)
	if err != nil {
		return nil, ErrInvalidInput
	}

	now := time.Now().UTC()
	tx := TransactionView{
		ID:                fs.nextTxID,
		Type:              TransactionTypeOpeningBalance,
		EffectiveDate:     effDate,
		Description:       nil,
		PaymentMethodID:   nil,
		PaymentMethodName: nil,
		CategoryID:        nil,
		CategoryName:      nil,
		CategoryType:      nil,
		MovementID:        fs.nextMovID,
		AccountID:         acct.ID,
		AccountName:       acct.Name,
		AccountType:       acct.Type,
		AccountSubtype:    acct.Subtype,
		Institution:       acct.Institution,
		CurrencyCode:      curr.Code,
		CurrencySymbol:    curr.Symbol,
		MinorUnit:         curr.MinorUnit,
		AmountMinor:       minor,
		AmountFormatted:   formatAmount(minor, curr.MinorUnit),
		CreatedAt:         now,
	}

	fs.nextTxID++
	fs.nextMovID++
	fs.transactions = append(fs.transactions, tx)

	return &tx, nil
}

func (fs *FakeServices) ListTransactions(ctx context.Context, in ListTransactionsInput) ([]TransactionView, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	limit := in.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}

	txs := make([]TransactionView, len(fs.transactions))
	copy(txs, fs.transactions)

	// ListTransactions returns effective_date DESC, id DESC
	sort.Slice(txs, func(i, j int) bool {
		if !txs[i].EffectiveDate.Equal(txs[j].EffectiveDate) {
			return txs[i].EffectiveDate.After(txs[j].EffectiveDate)
		}
		return txs[i].ID > txs[j].ID
	})

	if len(txs) > limit {
		txs = txs[:limit]
	}

	return txs, nil
}

func (fs *FakeServices) GetTransaction(ctx context.Context, id int64) (TransactionView, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	for _, tx := range fs.transactions {
		if tx.ID == id {
			return tx, nil
		}
	}
	return TransactionView{}, ErrNotFound
}

func (fs *FakeServices) UpdateTransaction(ctx context.Context, id int64, in UpdateTransactionInput) (TransactionView, error) {
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
	// Named positively, exactly as the ledger does: everything this method is
	// not for is refused, including a transfer, which has two movements this
	// method has no way to rewrite.
	if existing.Type != TransactionTypeIncome && existing.Type != TransactionTypeExpense {
		return TransactionView{}, ErrTypeChangeNotAllowed
	}
	if in.Type != TransactionTypeIncome && in.Type != TransactionTypeExpense {
		return TransactionView{}, ErrTypeChangeNotAllowed
	}

	acct, ok := fs.accounts[in.AccountID]
	if !ok {
		return TransactionView{}, ErrAccountNotFound
	}
	if !acct.IsActive {
		return TransactionView{}, ErrAccountInactive
	}

	if acct.CurrencyCode != existing.CurrencyCode && !in.AllowCurrencyChange {
		return TransactionView{}, ErrCurrencyChangeNotConfirmed
	}

	cat, ok := fs.categories[in.CategoryID]
	if !ok {
		return TransactionView{}, ErrCategoryNotFound
	}
	if !cat.IsActive {
		return TransactionView{}, ErrCategoryInactive
	}
	if (in.Type == TransactionTypeIncome && cat.Type != CategoryTypeIncome) ||
		(in.Type == TransactionTypeExpense && cat.Type != CategoryTypeExpense) {
		return TransactionView{}, ErrCategoryTypeMismatch
	}

	method, ok := fs.methods[in.PaymentMethodID]
	if !ok {
		return TransactionView{}, ErrPaymentMethodNotFound
	}
	if !method.IsActive {
		return TransactionView{}, ErrPaymentMethodInactive
	}

	curr := fs.currencies[acct.CurrencyCode]
	if strings.HasPrefix(strings.TrimSpace(in.Amount), "-") {
		return TransactionView{}, ErrInvalidAmount
	}
	minor, err := parseAmount(in.Amount, curr.MinorUnit)
	if err != nil || minor <= 0 {
		return TransactionView{}, ErrInvalidAmount
	}

	effDate, err := time.Parse("2006-01-02", in.EffectiveDate)
	if err != nil {
		return TransactionView{}, ErrInvalidInput
	}

	var desc *string
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if d != "" {
			if len(d) > 255 {
				return TransactionView{}, ErrInvalidInput
			}
			desc = &d
		}
	}

	storedMinor := minor
	if in.Type == TransactionTypeExpense {
		storedMinor = -minor
	}

	methodID, methodName := method.ID, method.Name
	updated := TransactionView{
		ID:                existing.ID,
		Type:              in.Type,
		EffectiveDate:     effDate,
		Description:       desc,
		PaymentMethodID:   &methodID,
		PaymentMethodName: &methodName,
		CategoryID:        &cat.ID,
		CategoryName:      &cat.Name,
		CategoryType:      &cat.Type,
		MovementID:        existing.MovementID,
		AccountID:         acct.ID,
		AccountName:       acct.Name,
		AccountType:       acct.Type,
		AccountSubtype:    acct.Subtype,
		Institution:       acct.Institution,
		CurrencyCode:      curr.Code,
		CurrencySymbol:    curr.Symbol,
		MinorUnit:         curr.MinorUnit,
		AmountMinor:       storedMinor,
		AmountFormatted:   formatAmount(storedMinor, curr.MinorUnit),
		CreatedAt:         existing.CreatedAt,
	}

	fs.transactions[idx] = updated
	return updated, nil
}

func (fs *FakeServices) UpdateOpeningBalance(ctx context.Context, id int64, in UpdateOpeningBalanceInput) (TransactionView, error) {
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
	if existing.Type != TransactionTypeOpeningBalance {
		return TransactionView{}, ErrTypeChangeNotAllowed
	}

	curr := fs.currencies[existing.CurrencyCode]
	minor, err := parseAmount(in.Amount, curr.MinorUnit)
	if err != nil || minor == 0 {
		return TransactionView{}, ErrInvalidAmount
	}

	effDate, err := time.Parse("2006-01-02", in.EffectiveDate)
	if err != nil {
		return TransactionView{}, ErrInvalidInput
	}

	updated := existing
	updated.EffectiveDate = effDate
	updated.AmountMinor = minor
	updated.AmountFormatted = formatAmount(minor, curr.MinorUnit)

	fs.transactions[idx] = updated
	return updated, nil
}

// RecordTransfer moves money between two of the user's own accounts.
//
// It stores ONE row, as the real read model does: anchored on the source, with
// the whole transfer in the Transfer detail. movementInto is what makes both
// halves reach the balances.
func (fs *FakeServices) RecordTransfer(ctx context.Context, in RecordTransferInput) (TransactionView, error) {
	if err := ctx.Err(); err != nil {
		return TransactionView{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	resolved, err := fs.resolveTransfer(in.SourceAccountID, in.DestinationAccountID,
		in.Amount, in.Description, in.EffectiveDate)
	if err != nil {
		return TransactionView{}, err
	}

	now := time.Now().UTC()
	tx := TransactionView{
		ID:            fs.nextTxID,
		Type:          TransactionTypeTransfer,
		EffectiveDate: resolved.effectiveDate,
		Description:   resolved.description,
		// A transfer has neither, ever.
		CategoryID:      nil,
		PaymentMethodID: nil,
		Transfer: &TransferDetail{
			SourceMovementID:       fs.nextMovID,
			SourceAccountID:        resolved.source.ID,
			SourceAccountName:      resolved.source.Name,
			DestinationMovementID:  fs.nextMovID + 1,
			DestinationAccountID:   resolved.destination.ID,
			DestinationAccountName: resolved.destination.Name,
			AmountMinor:            resolved.amountMinor,
			AmountFormatted:        formatAmount(resolved.amountMinor, resolved.currency.MinorUnit),
		},
		MovementID:      fs.nextMovID,
		AccountID:       resolved.source.ID,
		AccountName:     resolved.source.Name,
		AccountType:     resolved.source.Type,
		AccountSubtype:  resolved.source.Subtype,
		Institution:     resolved.source.Institution,
		CurrencyCode:    resolved.currency.Code,
		CurrencySymbol:  resolved.currency.Symbol,
		MinorUnit:       resolved.currency.MinorUnit,
		AmountMinor:     -resolved.amountMinor,
		AmountFormatted: formatAmount(-resolved.amountMinor, resolved.currency.MinorUnit),
		CreatedAt:       now,
	}

	fs.nextTxID++
	fs.nextMovID += 2 // a transfer consumes two
	fs.transactions = append(fs.transactions, tx)

	return tx, nil
}

// UpdateTransfer corrects a transfer, keeping both movement ids.
func (fs *FakeServices) UpdateTransfer(ctx context.Context, id int64, in UpdateTransferInput) (TransactionView, error) {
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
	if existing.Type != TransactionTypeTransfer || existing.Transfer == nil {
		return TransactionView{}, ErrTypeChangeNotAllowed
	}

	resolved, err := fs.resolveTransfer(in.SourceAccountID, in.DestinationAccountID,
		in.Amount, in.Description, in.EffectiveDate)
	if err != nil {
		return TransactionView{}, err
	}

	if !in.AllowCurrencyChange && existing.CurrencyCode != resolved.currency.Code {
		return TransactionView{}, ErrCurrencyChangeNotConfirmed
	}

	updated := existing
	updated.EffectiveDate = resolved.effectiveDate
	updated.Description = resolved.description
	updated.Transfer = &TransferDetail{
		// The movement ids survive an edit: an edit corrects an event, it does
		// not replace it with a different one.
		SourceMovementID:       existing.Transfer.SourceMovementID,
		SourceAccountID:        resolved.source.ID,
		SourceAccountName:      resolved.source.Name,
		DestinationMovementID:  existing.Transfer.DestinationMovementID,
		DestinationAccountID:   resolved.destination.ID,
		DestinationAccountName: resolved.destination.Name,
		AmountMinor:            resolved.amountMinor,
		AmountFormatted:        formatAmount(resolved.amountMinor, resolved.currency.MinorUnit),
	}
	updated.MovementID = existing.Transfer.SourceMovementID
	updated.AccountID = resolved.source.ID
	updated.AccountName = resolved.source.Name
	updated.AccountType = resolved.source.Type
	updated.AccountSubtype = resolved.source.Subtype
	updated.Institution = resolved.source.Institution
	updated.CurrencyCode = resolved.currency.Code
	updated.CurrencySymbol = resolved.currency.Symbol
	updated.MinorUnit = resolved.currency.MinorUnit
	updated.AmountMinor = -resolved.amountMinor
	updated.AmountFormatted = formatAmount(-resolved.amountMinor, resolved.currency.MinorUnit)

	fs.transactions[idx] = updated
	return updated, nil
}

type resolvedFakeTransfer struct {
	source        Account
	destination   Account
	currency      Currency
	amountMinor   int64
	effectiveDate time.Time
	description   *string
}

// resolveTransfer applies the same rules, in the same order, as the ledger's
// resolveTransfer, so that a handler tested against this fake meets the same
// errors it will meet in production.
//
// The caller already holds the lock.
func (fs *FakeServices) resolveTransfer(
	sourceID, destinationID int64, amount string, description *string, effectiveDate string,
) (resolvedFakeTransfer, error) {
	var desc *string
	if description != nil {
		trimmed := strings.TrimSpace(*description)
		if trimmed == "" || len(trimmed) > 255 {
			return resolvedFakeTransfer{}, ErrInvalidInput
		}
		desc = &trimmed
	}

	source, ok := fs.accounts[sourceID]
	if !ok {
		return resolvedFakeTransfer{}, &TransferAccountError{
			Side: TransferSideSource, AccountID: sourceID, Err: ErrAccountNotFound}
	}
	if !source.IsActive {
		return resolvedFakeTransfer{}, &TransferAccountError{
			Side: TransferSideSource, AccountID: sourceID, Err: ErrAccountInactive}
	}

	destination, ok := fs.accounts[destinationID]
	if !ok {
		return resolvedFakeTransfer{}, &TransferAccountError{
			Side: TransferSideDestination, AccountID: destinationID, Err: ErrAccountNotFound}
	}
	if !destination.IsActive {
		return resolvedFakeTransfer{}, &TransferAccountError{
			Side: TransferSideDestination, AccountID: destinationID, Err: ErrAccountInactive}
	}

	if source.ID == destination.ID {
		return resolvedFakeTransfer{}, ErrSameAccount
	}
	if source.CurrencyCode != destination.CurrencyCode {
		return resolvedFakeTransfer{}, ErrCurrencyMismatch
	}

	curr := fs.currencies[source.CurrencyCode]
	minor, err := parseAmount(amount, curr.MinorUnit)
	if err != nil || minor <= 0 {
		return resolvedFakeTransfer{}, ErrInvalidAmount
	}

	effDate, err := time.Parse("2006-01-02", effectiveDate)
	if err != nil {
		return resolvedFakeTransfer{}, ErrInvalidInput
	}

	return resolvedFakeTransfer{
		source: source, destination: destination, currency: curr,
		amountMinor: minor, effectiveDate: effDate, description: desc,
	}, nil
}

// movementInto is the effect one stored transaction has on one account.
//
// The fake keeps one row per transaction, mirroring the real read model, so a
// transfer's two movements are folded into a single row: the source is the row's
// own account and the destination lives in the Transfer detail. Every balance
// has to see both, or a transfer would take money out of one account and put it
// nowhere.
func movementInto(tx TransactionView, accountID int64) int64 {
	if tx.Transfer != nil {
		var effect int64
		if tx.Transfer.SourceAccountID == accountID {
			effect -= tx.Transfer.AmountMinor
		}
		if tx.Transfer.DestinationAccountID == accountID {
			effect += tx.Transfer.AmountMinor
		}
		return effect
	}
	if tx.AccountID == accountID {
		return tx.AmountMinor
	}
	return 0
}

func (fs *FakeServices) GetAccountBalance(ctx context.Context, accountID int64) (AccountBalance, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	acct, ok := fs.accounts[accountID]
	if !ok {
		return AccountBalance{}, ErrNotFound
	}
	curr := fs.currencies[acct.CurrencyCode]

	var sum int64
	for _, tx := range fs.transactions {
		sum += movementInto(tx, accountID)
	}

	return AccountBalance{
		AccountID:        acct.ID,
		AccountName:      acct.Name,
		AccountType:      acct.Type,
		AccountSubtype:   acct.Subtype,
		Institution:      acct.Institution,
		IsActive:         acct.IsActive,
		CurrencyCode:     curr.Code,
		CurrencySymbol:   curr.Symbol,
		MinorUnit:        curr.MinorUnit,
		BalanceMinor:     sum,
		BalanceFormatted: formatAmount(sum, curr.MinorUnit),
	}, nil
}

func (fs *FakeServices) ListAccountBalances(ctx context.Context, includeInactive bool) ([]AccountBalance, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	var result []AccountBalance
	for _, acct := range fs.accounts {
		if !includeInactive && !acct.IsActive {
			continue
		}
		curr := fs.currencies[acct.CurrencyCode]

		var sum int64
		for _, tx := range fs.transactions {
			sum += movementInto(tx, acct.ID)
		}

		result = append(result, AccountBalance{
			AccountID:        acct.ID,
			AccountName:      acct.Name,
			AccountType:      acct.Type,
			AccountSubtype:   acct.Subtype,
			Institution:      acct.Institution,
			IsActive:         acct.IsActive,
			CurrencyCode:     curr.Code,
			CurrencySymbol:   curr.Symbol,
			MinorUnit:        curr.MinorUnit,
			BalanceMinor:     sum,
			BalanceFormatted: formatAmount(sum, curr.MinorUnit),
		})
	}

	// ListAccountBalances returns currency code, then account name, then id
	sort.Slice(result, func(i, j int) bool {
		if result[i].CurrencyCode != result[j].CurrencyCode {
			return result[i].CurrencyCode < result[j].CurrencyCode
		}
		if result[i].AccountName != result[j].AccountName {
			return result[i].AccountName < result[j].AccountName
		}
		return result[i].AccountID < result[j].AccountID
	})

	return result, nil
}

func (fs *FakeServices) ListCurrencyTotals(ctx context.Context, includeInactive bool) ([]CurrencyTotal, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	type agg struct {
		count int
		sum   int64
	}
	byCurr := make(map[string]*agg)

	for _, acct := range fs.accounts {
		if !includeInactive && !acct.IsActive {
			continue
		}
		if _, ok := byCurr[acct.CurrencyCode]; !ok {
			byCurr[acct.CurrencyCode] = &agg{}
		}
		byCurr[acct.CurrencyCode].count++

		for _, tx := range fs.transactions {
			byCurr[acct.CurrencyCode].sum += movementInto(tx, acct.ID)
		}
	}

	var result []CurrencyTotal
	for code, ag := range byCurr {
		curr := fs.currencies[code]
		result = append(result, CurrencyTotal{
			CurrencyCode:   curr.Code,
			CurrencyName:   curr.Name,
			CurrencySymbol: curr.Symbol,
			MinorUnit:      curr.MinorUnit,
			AccountCount:   ag.count,
			TotalMinor:     ag.sum,
			TotalFormatted: formatAmount(ag.sum, curr.MinorUnit),
		})
	}

	// Ordered by currency code
	sort.Slice(result, func(i, j int) bool {
		return result[i].CurrencyCode < result[j].CurrencyCode
	})

	return result, nil
}

// ApplicationService implementation

func (fs *FakeServices) OpenAccount(ctx context.Context, in OpenAccountInput) (OpenAccountResult, error) {
	// Atomic account creation + opening balance
	acct, err := fs.CreateAccount(ctx, in.Account)
	if err != nil {
		return OpenAccountResult{}, err
	}

	if in.OpeningBalance != nil && strings.TrimSpace(*in.OpeningBalance) != "" {
		date := in.OpeningBalanceDate
		if date == "" {
			date = time.Now().UTC().Format("2006-01-02")
		}

		ob, err := fs.RecordOpeningBalance(ctx, RecordOpeningBalanceInput{
			AccountID:     acct.ID,
			Amount:        *in.OpeningBalance,
			EffectiveDate: date,
		})
		if err != nil {
			// Rollback account in fake
			fs.mu.Lock()
			delete(fs.accounts, acct.ID)
			fs.mu.Unlock()
			return OpenAccountResult{}, err
		}
		return OpenAccountResult{
			Account:        acct,
			OpeningBalance: ob,
		}, nil
	}

	return OpenAccountResult{
		Account:        acct,
		OpeningBalance: nil,
	}, nil
}

// Ensure implementations satisfy consumer interfaces
var _ CurrencyService = (*FakeServices)(nil)
var _ LedgerService = (*FakeServices)(nil)
var _ ApplicationService = (*FakeServices)(nil)
var _ AccountService = (*fakeAccountService)(nil)
var _ CategoryService = (*fakeCategoryService)(nil)

// Adapter wrappers so that when AccountService / CategoryService share method names with CurrencyService,
// they can be cleanly separated if needed:
type fakeAccountService struct{ *FakeServices }

func (s fakeAccountService) List(ctx context.Context, includeInactive bool) ([]Account, error) {
	return s.ListAccounts(ctx, includeInactive)
}
func (s fakeAccountService) Get(ctx context.Context, id int64) (Account, error) {
	return s.GetAccount(ctx, id)
}
func (s fakeAccountService) Create(ctx context.Context, in CreateAccountInput) (Account, error) {
	return s.CreateAccount(ctx, in)
}
func (s fakeAccountService) UpdateMetadata(ctx context.Context, id int64, in UpdateAccountMetadataInput) (Account, error) {
	return s.UpdateAccountMetadata(ctx, id, in)
}
func (s fakeAccountService) SetActive(ctx context.Context, id int64, isActive bool) (Account, error) {
	return s.SetAccountActive(ctx, id, isActive)
}

type fakeCategoryService struct{ *FakeServices }

func (s fakeCategoryService) List(ctx context.Context, in ListCategoriesInput) ([]Category, error) {
	return s.ListCategories(ctx, in)
}
func (s fakeCategoryService) Get(ctx context.Context, id int64) (Category, error) {
	return s.GetCategory(ctx, id)
}
func (s fakeCategoryService) Create(ctx context.Context, in CreateCategoryInput) (Category, error) {
	return s.CreateCategory(ctx, in)
}
func (s fakeCategoryService) UpdateMetadata(ctx context.Context, id int64, in UpdateCategoryMetadataInput) (Category, error) {
	return s.UpdateCategoryMetadata(ctx, id, in)
}
func (s fakeCategoryService) SetActive(ctx context.Context, id int64, isActive bool) (Category, error) {
	return s.SetCategoryActive(ctx, id, isActive)
}

// BuildServices returns a configured *Services referencing the fake implementation
func (fs *FakeServices) BuildServices() *Services {
	if fs.Rates == nil {
		fs.Rates = NewFakeRateStore()
	}
	return &Services{
		Currencies:  fs,
		Accounts:    fakeAccountService{fs},
		Categories:  fakeCategoryService{fs},
		Methods:     fakePaymentMethodService{fs},
		Ledger:      fs,
		Backups:     fakeBackupService{fs},
		Application: fs,

		Rates:     fs.Rates,
		Settings:  fakeSettingsService{fs.Rates},
		Valuation: fs.Rates,
	}
}

// Reset clears all data except seeded currencies
func (fs *FakeServices) Reset() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fs.accounts = make(map[int64]Account)
	fs.nextAcctID = 1
	fs.categories = make(map[int64]Category)
	fs.nextCatID = 1
	fs.transactions = make([]TransactionView, 0)
	fs.nextTxID = 1
	fs.nextMovID = 1
	fs.backups = nil
}

// SeedSampleData helper for testing or quick dev
func (fs *FakeServices) SeedSampleData(ctx context.Context) error {
	fs.Reset()

	// Categories
	salaryCat, err := fs.CreateCategory(ctx, CreateCategoryInput{
		Name: "Salary",
		Type: CategoryTypeIncome,
	})
	if err != nil {
		return err
	}

	groceriesCat, err := fs.CreateCategory(ctx, CreateCategoryInput{
		Name: "Groceries",
		Type: CategoryTypeExpense,
	})
	if err != nil {
		return err
	}

	// Mercantil USD
	inst := "Mercantil"
	subChecking := AccountSubtypeChecking
	bal := "1000.00"
	today := time.Now().UTC().Format("2006-01-02")

	res, err := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{
			Name:         "Mercantil USD",
			Type:         AccountTypeBank,
			Subtype:      &subChecking,
			CurrencyCode: "USD",
			Institution:  &inst,
		},
		OpeningBalance:     &bal,
		OpeningBalanceDate: today,
	})
	if err != nil {
		return err
	}

	// Income $500
	_, err = fs.RecordIncome(ctx, RecordIncomeInput{
		AccountID:       res.Account.ID,
		CategoryID:      salaryCat.ID,
		Amount:          "500.00",
		PaymentMethodID: 1,
		EffectiveDate:   today,
	})
	if err != nil {
		return err
	}

	// Expense $25
	desc := "Supermarket"
	_, err = fs.RecordExpense(ctx, RecordExpenseInput{
		AccountID:       res.Account.ID,
		CategoryID:      groceriesCat.ID,
		Amount:          "25.00",
		PaymentMethodID: 2,
		Description:     &desc,
		EffectiveDate:   today,
	})
	if err != nil {
		return err
	}

	return nil
}

// FormatDate exports formatting for dates
func FormatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func StringPtr(s string) *string {
	return &s
}

// --- payment methods ---------------------------------------------------------
//
// Seeded with the four M3 migrated from M2's fixed enumeration, so a fake-backed
// run shows what a freshly upgraded database shows.

func (fs *FakeServices) ListPaymentMethods(ctx context.Context, in ListPaymentMethodsInput) ([]PaymentMethod, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	methods := make([]PaymentMethod, 0, len(fs.methods))
	for _, m := range fs.methods {
		if !in.IncludeInactive && !m.IsActive {
			continue
		}
		methods = append(methods, m)
	}
	sort.Slice(methods, func(i, j int) bool {
		if methods[i].Name != methods[j].Name {
			return methods[i].Name < methods[j].Name
		}
		return methods[i].ID < methods[j].ID
	})
	return methods, nil
}

func (fs *FakeServices) GetPaymentMethod(ctx context.Context, id int64) (PaymentMethod, error) {
	if err := ctx.Err(); err != nil {
		return PaymentMethod{}, err
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	m, ok := fs.methods[id]
	if !ok {
		return PaymentMethod{}, ErrNotFound
	}
	return m, nil
}

func (fs *FakeServices) CreatePaymentMethod(ctx context.Context, in CreatePaymentMethodInput) (PaymentMethod, error) {
	if err := ctx.Err(); err != nil {
		return PaymentMethod{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 60 {
		return PaymentMethod{}, ErrInvalidInput
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()

	// Exact uniqueness, matching the real service.
	for _, m := range fs.methods {
		if m.Name == name {
			return PaymentMethod{}, ErrConflict
		}
	}

	now := time.Now().UTC()
	m := PaymentMethod{ID: fs.nextMethodID, Name: name, IsActive: true, CreatedAt: now, UpdatedAt: now}
	fs.methods[m.ID] = m
	fs.nextMethodID++
	return m, nil
}

func (fs *FakeServices) UpdatePaymentMethodMetadata(
	ctx context.Context, id int64, in UpdatePaymentMethodMetadataInput,
) (PaymentMethod, error) {
	if err := ctx.Err(); err != nil {
		return PaymentMethod{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 60 {
		return PaymentMethod{}, ErrInvalidInput
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, ok := fs.methods[id]
	if !ok {
		return PaymentMethod{}, ErrNotFound
	}
	for otherID, other := range fs.methods {
		if otherID != id && other.Name == name {
			return PaymentMethod{}, ErrConflict
		}
	}

	m.Name = name
	m.UpdatedAt = time.Now().UTC()
	fs.methods[id] = m
	return m, nil
}

func (fs *FakeServices) SetPaymentMethodActive(ctx context.Context, id int64, isActive bool) (PaymentMethod, error) {
	if err := ctx.Err(); err != nil {
		return PaymentMethod{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, ok := fs.methods[id]
	if !ok {
		return PaymentMethod{}, ErrNotFound
	}
	m.IsActive = isActive
	m.UpdatedAt = time.Now().UTC()
	fs.methods[id] = m
	return m, nil
}

// fakePaymentMethodService adapts the method names above to the interface, the
// same shape the account and category fakes use.
type fakePaymentMethodService struct{ *FakeServices }

func (s fakePaymentMethodService) List(ctx context.Context, in ListPaymentMethodsInput) ([]PaymentMethod, error) {
	return s.ListPaymentMethods(ctx, in)
}

func (s fakePaymentMethodService) Get(ctx context.Context, id int64) (PaymentMethod, error) {
	return s.GetPaymentMethod(ctx, id)
}

func (s fakePaymentMethodService) Create(ctx context.Context, in CreatePaymentMethodInput) (PaymentMethod, error) {
	return s.CreatePaymentMethod(ctx, in)
}

func (s fakePaymentMethodService) UpdateMetadata(
	ctx context.Context, id int64, in UpdatePaymentMethodMetadataInput,
) (PaymentMethod, error) {
	return s.UpdatePaymentMethodMetadata(ctx, id, in)
}

func (s fakePaymentMethodService) SetActive(ctx context.Context, id int64, isActive bool) (PaymentMethod, error) {
	return s.SetPaymentMethodActive(ctx, id, isActive)
}

// --- backups -----------------------------------------------------------------

func (fs *FakeServices) ListBackups(ctx context.Context) ([]Backup, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	out := make([]Backup, len(fs.backups))
	copy(out, fs.backups)
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (fs *FakeServices) CreateManualBackup(ctx context.Context) (Backup, error) {
	if err := ctx.Err(); err != nil {
		return Backup{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	now := time.Now().UTC()
	b := Backup{
		Name:      fmt.Sprintf("backup_%s_manual.db", now.Format("20060102_150405")),
		Reason:    "manual",
		CreatedAt: now,
		SizeBytes: 40960,
	}
	fs.backups = append(fs.backups, b)
	return b, nil
}

type fakeBackupService struct{ *FakeServices }

func (s fakeBackupService) List(ctx context.Context) ([]Backup, error) {
	return s.ListBackups(ctx)
}

func (s fakeBackupService) CreateManual(ctx context.Context) (Backup, error) {
	return s.CreateManualBackup(ctx)
}
