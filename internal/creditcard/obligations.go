package creditcard

import (
	"sort"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// M8.2: layer O, the obligations (plan R2.3, TD-092).
//
// Derive answers "what does the bank ask of me, and what did each payment
// settle?" from FACTS alone -- the card's terms, the statements the bank
// emitted, the card's movements and the user's assertions -- as of a date. It is
// a pure function: the same facts give the same answer, nothing is read from or
// written to the database, and nothing it computes is ever stored. A purchase
// recorded late, a corrected statement or a payment dated in the past changes
// the answer on the next read, and nothing has to be kept in step.
//
// Notation (owed is positive throughout; the ledger's sign is reversed):
//
//	B_j  what the bank's statement j says was owed at its closing C_j
//	L_j  what the ledger says: the net of every movement belonging to j or earlier
//	O_j  the billed obligation, max(0, B_j) -- or max(0, L_j) for a cycle
//	     with no statement registered, labelled as computed
//	M_j  the minimum (only from a statement), D_j the due date
//	P_j(t)  payments belonging to a LATER cycle than j, dated on or before t
//	R_j(t)  refunds belonging to a later cycle than j, dated on or before t
//
// Obligations are NESTED: B_{j+1} includes whatever of B_j was not paid, and a
// payment after C_{j+1} is also after C_j. A payment therefore reduces every
// open obligation at once, and nothing is counted twice.

// Role is what a movement is to the card's obligations, decided structurally
// from its type and direction (TD-044). Each case names what it accepts
// (TD-045).
type Role string

const (
	// RolePayment is a TRANSFER or an FX_EXCHANGE INTO the card (TD-091).
	RolePayment Role = "PAYMENT"
	// RoleRefund is a REFUND on the card (TD-090). It is never a payment: it
	// never counts towards the minimum or what is overdue (R2.5).
	RoleRefund Role = "REFUND"
	// RoleDebit is anything else that raised what is owed: a purchase, a
	// charge, a cash advance, a previous debt.
	RoleDebit Role = "DEBIT"
	// RoleCredit is anything else that lowered it: cashback, an opening
	// credit balance.
	RoleCredit Role = "CREDIT"
)

// Movement is one movement of a card, as the engine reads it.
type Movement struct {
	TransactionID int64
	Type          ledger.TransactionType
	Date          time.Time
	Description   *string
	// AmountMinor is the movement on the card, in the ledger's sign: negative
	// raises the debt.
	AmountMinor int64
}

// Role classifies the movement.
func (m Movement) Role() Role {
	switch {
	case (m.Type == ledger.Transfer || m.Type == ledger.FXExchange) && m.AmountMinor > 0:
		return RolePayment
	case m.Type == ledger.Refund && m.AmountMinor > 0:
		return RoleRefund
	case m.AmountMinor < 0:
		return RoleDebit
	default:
		return RoleCredit
	}
}

// Facts are everything the obligations are derived from.
type Facts struct {
	Card Card
	// Statements are the card's, any order.
	Statements []Statement
	// Movements are the card's, any order, future-dated ones included: they
	// are left out of every figure as of a date before them (TD-063).
	Movements []Movement
	// Assertions name the statement a movement belongs to when the bank
	// included it somewhere its date does not imply, by transaction id.
	Assertions map[int64]int64
}

// CycleStatus is a cycle's standing, derived and never stored.
type CycleStatus string

const (
	// StatusOpen: the cycle has not closed yet.
	StatusOpen CycleStatus = "OPEN"
	// StatusPaid: nothing was billed, or the billed balance has been paid.
	StatusPaid CycleStatus = "PAID"
	// StatusOverdue: the due date passed with the minimum unmet. Only a KNOWN
	// due date and a KNOWN minimum make a cycle overdue.
	StatusOverdue CycleStatus = "OVERDUE"
	// StatusMinimumMet: the minimum was paid, the billed balance was not.
	StatusMinimumMet CycleStatus = "MINIMUM_MET"
	// StatusDueUnknown: billed and not paid, with no due date to judge it by.
	StatusDueUnknown CycleStatus = "DUE_UNKNOWN"
	// StatusPartiallyPaid: something was paid, the minimum was not (or is
	// unknown) and the due date has not passed.
	StatusPartiallyPaid CycleStatus = "PARTIALLY_PAID"
	// StatusBilled: billed, nothing paid yet, not overdue.
	StatusBilled CycleStatus = "BILLED"
)

// Cycle is one billing cycle as of the standing's date. Amounts are owed
// positive, in the card currency's minor units.
type Cycle struct {
	PeriodStart time.Time
	ClosingDate time.Time

	// Statement is the bank's, or nil when the cycle is computed from the
	// calendar and the ledger.
	Statement *Statement

	// DebitsMinor, PaymentsMinor and CreditsMinor are the movements belonging
	// to this cycle: what raised the debt, the payments, and every other
	// credit (refunds included). MovementCount counts them.
	DebitsMinor   int64
	PaymentsMinor int64
	CreditsMinor  int64
	MovementCount int

	// LedgerOwedMinor is L_j. OwedMinor is B_j when a statement is
	// registered, L_j otherwise; BilledMinor is max(0, OwedMinor).
	LedgerOwedMinor int64
	OwedMinor       int64
	BilledMinor     int64

	// MinimumMinor is the statement's minimum; nil when unknown.
	MinimumMinor *int64
	// DueDate is the statement's, or estimated from the terms
	// (DueEstimated); nil when unknown.
	DueDate      *time.Time
	DueEstimated bool

	// PaidAfterMinor is P_j and RefundedAfterMinor R_j, as of the standing's
	// date.
	PaidAfterMinor     int64
	RefundedAfterMinor int64

	Status CycleStatus

	// For a cycle whose due date has passed: what was missing at the due date
	// from the minimum (nil when the minimum is unknown) and from the billed
	// balance -- the base on which the bank would charge interest, which this
	// application never computes.
	MissedMinimumMinor *int64
	UnpaidAtDueMinor   *int64
}

// Registered reports whether the bank's statement for this cycle is recorded.
func (c Cycle) Registered() bool { return c.Statement != nil }

// Target is what part of the obligations a slice of a payment settled.
type Target string

const (
	TargetOverdue       Target = "OVERDUE"
	TargetMinimum       Target = "MINIMUM"
	TargetBilled        Target = "BILLED"
	TargetPrepaid       Target = "PREPAID"
	TargetCreditBalance Target = "CREDIT_BALANCE"
)

// AllocationLine is one slice of a payment.
type AllocationLine struct {
	Target      Target
	AmountMinor int64
	// Cycles are the closings of the overdue statements, for TargetOverdue.
	Cycles []time.Time
}

// Thresholds are the bounds of the cumulative line a payment was placed on:
// [0, Overdue) overdue, [Overdue, Minimum) minimum, [Minimum, Billed) rest of
// the billed balance, [Billed, Billed+Unbilled) prepaid, and beyond that a
// credit balance.
type Thresholds struct {
	OverdueMinor  int64
	MinimumMinor  int64
	BilledMinor   int64
	UnbilledMinor int64
}

// PaymentAllocation is what one payment settled: a partition of its amount
// (I5), with the trace that produced it.
type PaymentAllocation struct {
	TransactionID int64
	Date          time.Time
	AmountMinor   int64

	// Against is the closing of the statement in force when the payment was
	// made, nil when none had closed yet.
	Against *time.Time
	// PaidBeforeMinor is p: what earlier payments had already placed on the
	// same line.
	PaidBeforeMinor int64
	Thresholds      Thresholds
	// Clamped reports that the bank's figures were not in the order the line
	// needs -- a minimum below what was overdue, or above the billed balance --
	// and were bounded to make it monotone. The reconciliation shows why.
	Clamped bool

	Lines []AllocationLine
}

// WarningKind names something the engine noticed and worked around, never
// silently.
type WarningKind string

const (
	// WarningAssertionBeforeMovement: a movement is asserted into a statement
	// that closed before it happened -- its date was corrected after the
	// assertion. The calendar's rule is used instead.
	WarningAssertionBeforeMovement WarningKind = "ASSERTION_BEFORE_MOVEMENT"
	// WarningAssertionOrphan: an assertion names a transaction that no longer
	// moves this card.
	WarningAssertionOrphan WarningKind = "ASSERTION_ORPHAN"
	// WarningThresholdsClamped: a payment's line had to be bounded.
	WarningThresholdsClamped WarningKind = "THRESHOLDS_CLAMPED"
)

// Warning is one thing the reconciliation should show.
type Warning struct {
	Kind          WarningKind
	TransactionID int64
	ClosingDate   *time.Time
}

// Placed is a movement with the cycle it belongs to.
type Placed struct {
	Movement
	Role        Role
	ClosingDate time.Time
	// Asserted reports that the membership comes from an assertion, not the
	// calendar.
	Asserted bool
}

// Standing is the card's obligations as of a date.
type Standing struct {
	AsOf         time.Time
	RefundPolicy RefundPolicy

	// The ledger's figures as of AsOf: BalanceMinor in the ledger's sign,
	// DebtMinor and CreditBalanceMinor its two positive readings.
	BalanceMinor       int64
	DebtMinor          int64
	CreditBalanceMinor int64

	// Cycles are oldest first, through the open one.
	Cycles []Cycle
	// CurrentIndex is k, the statement in force: the last cycle that closed
	// before AsOf; -1 when none has. OpenIndex is the cycle in progress.
	CurrentIndex int
	OpenIndex    int

	// Against the statement in force (all zero when there is none).
	BilledPendingMinor       int64
	BilledNetOfRefundsMinor  int64
	RefundsAfterClosingMinor int64
	// MinimumPendingMinor is nil when the minimum is unknown.
	MinimumPendingMinor *int64
	PrepaidMinor        int64

	// PastDueMinor is V: the largest unmet minimum among the cycles whose due
	// date has passed -- the MAXIMUM, not the sum, because one payment raises
	// every P_j at once. PastDueCycles are those with a deficit.
	PastDueMinor  int64
	PastDueCycles []time.Time

	// PostClosingDebitsMinor are debits belonging to cycles after the one in
	// force: what the next statement will bill.
	PostClosingDebitsMinor int64

	// Payments are oldest first, each partitioned (I5).
	Payments []PaymentAllocation
	// Placed are every movement on or before AsOf with its cycle.
	Placed   []Placed
	Warnings []Warning
}

// Current returns the statement in force, and false when none has closed.
func (s Standing) Current() (Cycle, bool) {
	if s.CurrentIndex < 0 {
		return Cycle{}, false
	}
	return s.Cycles[s.CurrentIndex], true
}

// Open returns the cycle in progress.
func (s Standing) Open() Cycle { return s.Cycles[s.OpenIndex] }

// engine holds the placed movements while a standing is derived.
type engine struct {
	policy RefundPolicy
	cycles []Cycle
	placed []placed
}

type placed struct {
	m     Movement
	role  Role
	cycle int
}

// before reports whether a sorts before b: by date, then transaction id.
func (p placed) before(date time.Time, id int64) bool {
	return p.m.Date.Before(date) || (p.m.Date.Equal(date) && p.m.TransactionID < id)
}

// sumAfter adds the amounts (owed-positive magnitude) of a role belonging to
// a cycle after j that satisfy keep.
func (e *engine) sumAfter(role Role, j int, keep func(placed) bool) int64 {
	var total int64
	for _, p := range e.placed {
		if p.role == role && p.cycle > j && keep(p) {
			total += abs(p.m.AmountMinor)
		}
	}
	return total
}

// billedEffective is O_j less the refunds after it when the policy says the
// bank lowers the billed balance by them.
func (e *engine) billedEffective(j int, refunded int64) int64 {
	if e.policy == RefundReducesBilled {
		return max(0, e.cycles[j].BilledMinor-refunded)
	}
	return e.cycles[j].BilledMinor
}

// pastDue is V at a date, counting only the payments keep accepts: the
// largest unmet minimum among cycles due before the date, and which cycles
// have one.
func (e *engine) pastDue(at time.Time, keep func(placed) bool) (int64, []time.Time) {
	var (
		worst  int64
		cycles []time.Time
	)
	for j, c := range e.cycles {
		if c.DueDate == nil || !c.DueDate.Before(at) || c.MinimumMinor == nil {
			continue
		}
		deficit := *c.MinimumMinor - e.sumAfter(RolePayment, j, keep)
		if deficit > 0 {
			worst = max(worst, deficit)
			cycles = append(cycles, c.ClosingDate)
		}
	}
	return worst, cycles
}

// Derive computes the card's obligations as of a date from the facts alone.
func Derive(f Facts, asOf time.Time) Standing {
	e := &engine{policy: f.Card.RefundPolicy}
	if !e.policy.Valid() {
		e.policy = RefundNextStatement
	}
	st := Standing{AsOf: asOf, RefundPolicy: e.policy, CurrentIndex: -1}

	from := asOf
	known := make(map[int64]bool, len(f.Movements))
	for _, m := range f.Movements {
		known[m.TransactionID] = true
		if m.Date.After(asOf) {
			continue
		}
		if m.Date.Before(from) {
			from = m.Date
		}
	}
	statements := append([]Statement(nil), f.Statements...)
	sort.Slice(statements, func(i, j int) bool { return statements[i].ClosingDate.Before(statements[j].ClosingDate) })
	bounds := schedule(f.Card.StatementDay, statements, from, asOf)

	byStatement := make(map[int64]int, len(statements))
	e.cycles = make([]Cycle, len(bounds))
	for i, b := range bounds {
		c := Cycle{ClosingDate: b.closing, Statement: b.statement}
		switch {
		case b.statement != nil:
			c.PeriodStart = b.statement.PeriodStart
			byStatement[b.statement.ID] = i
		case i > 0:
			c.PeriodStart = bounds[i-1].closing.AddDate(0, 0, 1)
		default:
			c.PeriodStart = ClosingDate(f.Card.StatementDay, b.closing.Year(), b.closing.Month()-1).AddDate(0, 0, 1)
		}
		e.cycles[i] = c
	}

	// Place every movement dated on or before asOf.
	for _, m := range f.Movements {
		if m.Date.After(asOf) {
			continue
		}
		p := placed{m: m, role: m.Role(), cycle: cycleOf(bounds, m.Date)}
		asserted := false
		if sid, ok := f.Assertions[m.TransactionID]; ok {
			if i, ok := byStatement[sid]; ok && !bounds[i].closing.Before(m.Date) {
				p.cycle, asserted = i, true
			} else {
				st.Warnings = append(st.Warnings, Warning{Kind: WarningAssertionBeforeMovement, TransactionID: m.TransactionID})
			}
		}
		e.placed = append(e.placed, p)
		st.Placed = append(st.Placed, Placed{Movement: m, Role: p.role, ClosingDate: bounds[p.cycle].closing, Asserted: asserted})
		st.BalanceMinor += m.AmountMinor
	}
	sort.Slice(e.placed, func(i, j int) bool { return e.placed[i].before(e.placed[j].m.Date, e.placed[j].m.TransactionID) })
	sort.SliceStable(st.Placed, func(i, j int) bool {
		a, b := st.Placed[i], st.Placed[j]
		return a.Date.Before(b.Date) || (a.Date.Equal(b.Date) && a.TransactionID < b.TransactionID)
	})
	for id := range f.Assertions {
		if !known[id] {
			st.Warnings = append(st.Warnings, Warning{Kind: WarningAssertionOrphan, TransactionID: id})
		}
	}
	sort.SliceStable(st.Warnings, func(i, j int) bool { return st.Warnings[i].TransactionID < st.Warnings[j].TransactionID })
	st.DebtMinor, st.CreditBalanceMinor = max(0, -st.BalanceMinor), max(0, st.BalanceMinor)

	// Each cycle's own movements, L_j, B_j, O_j, M_j, D_j.
	for _, p := range e.placed {
		c := &e.cycles[p.cycle]
		c.MovementCount++
		switch p.role {
		case RolePayment:
			c.PaymentsMinor += p.m.AmountMinor
		case RoleDebit:
			c.DebitsMinor -= p.m.AmountMinor
		case RoleRefund, RoleCredit:
			c.CreditsMinor += p.m.AmountMinor
		}
	}
	var ledgerOwed int64
	for i := range e.cycles {
		c := &e.cycles[i]
		ledgerOwed += c.DebitsMinor - c.PaymentsMinor - c.CreditsMinor
		c.LedgerOwedMinor = ledgerOwed
		c.OwedMinor = ledgerOwed
		if c.Statement != nil {
			c.OwedMinor = c.Statement.OwedMinor()
			c.MinimumMinor = c.Statement.MinimumPaymentMinor
			c.DueDate = c.Statement.DueDate
		}
		c.BilledMinor = max(0, c.OwedMinor)
		if c.DueDate == nil && f.Card.DueDaysAfterStatement != nil {
			due := c.ClosingDate.AddDate(0, 0, *f.Card.DueDaysAfterStatement)
			c.DueDate, c.DueEstimated = &due, true
		}
		if c.ClosingDate.Before(asOf) {
			st.CurrentIndex = i
		}
	}
	st.OpenIndex = st.CurrentIndex + 1

	upTo := func(t time.Time) func(placed) bool { return func(p placed) bool { return !p.m.Date.After(t) } }
	asOfKeep := upTo(asOf)

	// Each cycle's status as of asOf.
	for j := range e.cycles {
		c := &e.cycles[j]
		c.PaidAfterMinor = e.sumAfter(RolePayment, j, asOfKeep)
		c.RefundedAfterMinor = e.sumAfter(RoleRefund, j, asOfKeep)
		if !c.ClosingDate.Before(asOf) {
			c.Status = StatusOpen
			continue
		}
		if c.DueDate != nil && c.DueDate.Before(asOf) {
			atDue := e.sumAfter(RolePayment, j, upTo(*c.DueDate))
			unpaid := max(0, c.BilledMinor-atDue)
			c.UnpaidAtDueMinor = &unpaid
			if c.MinimumMinor != nil {
				missed := max(0, *c.MinimumMinor-atDue)
				c.MissedMinimumMinor = &missed
			}
		}
		billed, paid := e.billedEffective(j, c.RefundedAfterMinor), c.PaidAfterMinor
		switch {
		case paid >= billed:
			c.Status = StatusPaid
		case c.DueDate != nil && c.DueDate.Before(asOf) && c.MinimumMinor != nil && paid < *c.MinimumMinor:
			c.Status = StatusOverdue
		case c.MinimumMinor != nil && paid >= *c.MinimumMinor:
			c.Status = StatusMinimumMet
		case c.DueDate == nil:
			c.Status = StatusDueUnknown
		case paid > 0:
			c.Status = StatusPartiallyPaid
		default:
			c.Status = StatusBilled
		}
	}

	// The figures against the statement in force.
	st.PastDueMinor, st.PastDueCycles = e.pastDue(asOf, asOfKeep)
	if k := st.CurrentIndex; k >= 0 {
		c := e.cycles[k]
		billed := e.billedEffective(k, c.RefundedAfterMinor)
		st.RefundsAfterClosingMinor = c.RefundedAfterMinor
		st.BilledPendingMinor = max(0, billed-c.PaidAfterMinor)
		st.BilledNetOfRefundsMinor = max(0, c.BilledMinor-c.PaidAfterMinor-c.RefundedAfterMinor)
		st.PrepaidMinor = max(0, c.PaidAfterMinor-billed)
		if c.MinimumMinor != nil {
			pending := max(0, *c.MinimumMinor-c.PaidAfterMinor)
			st.MinimumPendingMinor = &pending
		}
	}
	for _, p := range e.placed {
		if p.role == RoleDebit && p.cycle > st.CurrentIndex {
			st.PostClosingDebitsMinor -= p.m.AmountMinor
		}
	}

	// What each payment settled, on the line of the statement in force when
	// it was made.
	for _, p := range e.placed {
		if p.role != RolePayment {
			continue
		}
		allocation := e.allocate(p)
		if allocation.Clamped {
			closing := *allocation.Against
			st.Warnings = append(st.Warnings, Warning{Kind: WarningThresholdsClamped, TransactionID: p.m.TransactionID, ClosingDate: &closing})
		}
		st.Payments = append(st.Payments, allocation)
	}

	st.Cycles = e.cycles
	return st
}

// allocate partitions one payment over the thresholds of the statement in
// force on its date (R2.3).
func (e *engine) allocate(q placed) PaymentAllocation {
	d, id, x := q.m.Date, q.m.TransactionID, q.m.AmountMinor
	a := PaymentAllocation{TransactionID: id, Date: d, AmountMinor: x}

	k := -1
	for j, c := range e.cycles {
		if c.ClosingDate.Before(d) {
			k = j
		}
	}
	earlier := func(p placed) bool { return p.before(d, id) }
	sameDayOrEarlier := func(p placed) bool { return !p.m.Date.After(d) }

	p := e.sumAfter(RolePayment, k, earlier)
	overdue, overdueCycles := e.pastDue(d, earlier)

	var billed int64
	var minimum *int64
	if k >= 0 {
		closing := e.cycles[k].ClosingDate
		a.Against = &closing
		billed = e.billedEffective(k, e.sumAfter(RoleRefund, k, sameDayOrEarlier))
		minimum = e.cycles[k].MinimumMinor
	}

	// U: what the open cycle owes so far -- its debits less its other
	// credits, and less its refunds unless the policy already took them off
	// the billed balance.
	var unbilled int64
	for _, o := range e.placed {
		if o.cycle <= k || o.m.Date.After(d) {
			continue
		}
		switch o.role {
		case RoleDebit:
			unbilled -= o.m.AmountMinor
		case RoleCredit:
			unbilled -= o.m.AmountMinor
		case RoleRefund:
			if e.policy == RefundNextStatement {
				unbilled -= o.m.AmountMinor
			}
		case RolePayment:
		}
	}
	unbilled = max(0, unbilled)

	// Bound the thresholds so that the line is monotone, and say so. The
	// overdue bound sits where the earlier payments on this line left the
	// largest deficit: p + V. With nothing overdue the segment is empty.
	var v int64
	if overdue > 0 {
		v = min(p+overdue, billed)
	}
	mRaw := v
	if minimum != nil {
		mRaw = *minimum
	}
	m := min(max(mRaw, v), billed)
	a.Clamped = k >= 0 && ((overdue > 0 && p+overdue > billed) || (minimum != nil && (*minimum < v || *minimum > billed)))
	a.PaidBeforeMinor = p
	a.Thresholds = Thresholds{OverdueMinor: v, MinimumMinor: m, BilledMinor: billed, UnbilledMinor: unbilled}

	segments := []struct {
		target   Target
		from, to int64
		bounded  bool
	}{
		{TargetOverdue, 0, v, true},
		{TargetMinimum, v, m, true},
		{TargetBilled, m, billed, true},
		{TargetPrepaid, billed, billed + unbilled, true},
		{TargetCreditBalance, billed + unbilled, 0, false},
	}
	lo, hi := p, p+x
	for _, s := range segments {
		start := max(lo, s.from)
		end := hi
		if s.bounded {
			end = min(hi, s.to)
		}
		if end <= start {
			continue
		}
		line := AllocationLine{Target: s.target, AmountMinor: end - start}
		if s.target == TargetOverdue {
			line.Cycles = overdueCycles
		}
		a.Lines = append(a.Lines, line)
	}
	return a
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
