package web

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeServices provides an in-memory, thread-safe implementation of all M2 services
// following the exact rules and invariants defined in docs/m2-contract.md.
type FakeServices struct {
	mu           sync.RWMutex
	currencies   map[string]Currency
	accounts     map[int64]Account
	nextAcctID   int64
	categories   map[int64]Category
	nextCatID    int64
	transactions []TransactionView
	nextTxID     int64
	nextMovID    int64
}

func NewFakeServices() *FakeServices {
	usdSymbol := "$"
	eurSymbol := "€"
	vesSymbol := "Bs."

	now := time.Now().UTC()

	fs := &FakeServices{
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

	pm := in.PaymentMethod
	now := time.Now().UTC()

	tx := TransactionView{
		ID:              fs.nextTxID,
		Type:            TransactionTypeIncome,
		EffectiveDate:   effDate,
		Description:     desc,
		PaymentMethod:   &pm,
		CategoryID:      &cat.ID,
		CategoryName:    &cat.Name,
		CategoryType:    &cat.Type,
		MovementID:      fs.nextMovID,
		AccountID:       acct.ID,
		AccountName:     acct.Name,
		AccountType:     acct.Type,
		AccountSubtype:  acct.Subtype,
		Institution:     acct.Institution,
		CurrencyCode:    curr.Code,
		CurrencySymbol:  curr.Symbol,
		MinorUnit:       curr.MinorUnit,
		AmountMinor:     minor,
		AmountFormatted: formatAmount(minor, curr.MinorUnit),
		CreatedAt:       now,
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

	pm := in.PaymentMethod
	now := time.Now().UTC()

	// Stored negative for expense
	storedMinor := -minor

	tx := TransactionView{
		ID:              fs.nextTxID,
		Type:            TransactionTypeExpense,
		EffectiveDate:   effDate,
		Description:     desc,
		PaymentMethod:   &pm,
		CategoryID:      &cat.ID,
		CategoryName:    &cat.Name,
		CategoryType:    &cat.Type,
		MovementID:      fs.nextMovID,
		AccountID:       acct.ID,
		AccountName:     acct.Name,
		AccountType:     acct.Type,
		AccountSubtype:  acct.Subtype,
		Institution:     acct.Institution,
		CurrencyCode:    curr.Code,
		CurrencySymbol:  curr.Symbol,
		MinorUnit:       curr.MinorUnit,
		AmountMinor:     storedMinor,
		AmountFormatted: formatAmount(storedMinor, curr.MinorUnit),
		CreatedAt:       now,
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
		ID:              fs.nextTxID,
		Type:            TransactionTypeOpeningBalance,
		EffectiveDate:   effDate,
		Description:     nil,
		PaymentMethod:   nil,
		CategoryID:      nil,
		CategoryName:    nil,
		CategoryType:    nil,
		MovementID:      fs.nextMovID,
		AccountID:       acct.ID,
		AccountName:     acct.Name,
		AccountType:     acct.Type,
		AccountSubtype:  acct.Subtype,
		Institution:     acct.Institution,
		CurrencyCode:    curr.Code,
		CurrencySymbol:  curr.Symbol,
		MinorUnit:       curr.MinorUnit,
		AmountMinor:     minor,
		AmountFormatted: formatAmount(minor, curr.MinorUnit),
		CreatedAt:       now,
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
		if tx.AccountID == accountID {
			sum += tx.AmountMinor
		}
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
			if tx.AccountID == acct.ID {
				sum += tx.AmountMinor
			}
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
			if tx.AccountID == acct.ID {
				byCurr[acct.CurrencyCode].sum += tx.AmountMinor
			}
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
	return &Services{
		Currencies:  fs,
		Accounts:    fakeAccountService{fs},
		Categories:  fakeCategoryService{fs},
		Ledger:      fs,
		Application: fs,
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
		AccountID:     res.Account.ID,
		CategoryID:    salaryCat.ID,
		Amount:        "500.00",
		PaymentMethod: PaymentMethodBankTransfer,
		EffectiveDate: today,
	})
	if err != nil {
		return err
	}

	// Expense $25
	desc := "Supermarket"
	_, err = fs.RecordExpense(ctx, RecordExpenseInput{
		AccountID:     res.Account.ID,
		CategoryID:    groceriesCat.ID,
		Amount:        "25.00",
		PaymentMethod: PaymentMethodPOS,
		Description:   &desc,
		EffectiveDate: today,
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
