package creditcard

import (
	"time"
)

// M8.2: reconciliation with the bank (R8). It INFORMS and never corrects: the
// difference between what a statement says was owed and what the ledger says
// is shown, broken down where the bank reported its figures, with the
// movements near the closing that could explain it. Fixing it is the user's
// act -- recording a missing charge, correcting a date, asserting that the bank
// billed a movement in another statement.

// candidateWindowDays is how far either side of a closing a movement is
// offered as a candidate for another statement: a purchase posted a few days
// late, a payment processed after the closing.
const candidateWindowDays = 5

// Component is one line of the breakdown: the bank's figure, the ledger's and
// their difference, bank minus ledger.
type Component struct {
	BankMinor       int64
	LedgerMinor     int64
	DifferenceMinor int64
}

func component(bank, ledger int64) *Component {
	return &Component{BankMinor: bank, LedgerMinor: ledger, DifferenceMinor: bank - ledger}
}

// Reconciliation compares one registered statement with the ledger. Amounts
// are owed positive, in the card currency's minor units.
type Reconciliation struct {
	Statement Statement
	Cycle     Cycle

	// Delta_j = B_j - L_j: what the bank billed that the ledger does not
	// explain. Zero is reconciled.
	BankOwedMinor   int64
	LedgerOwedMinor int64
	DifferenceMinor int64

	// The breakdown, each present only when the bank reported the figure.
	// B = previous - payments + debits, so the difference is
	// previous.Difference - payments.Difference + debits.Difference plus
	// whatever none of them explains (Unexplained, when all three exist).
	// Payments compares the bank's payments and credits with the ledger's
	// payments, refunds and other credits of the cycle.
	Previous    *Component
	Payments    *Component
	Debits      *Component
	Unexplained *int64

	// PastDue compares the bank's past-due amount with the one derived at the
	// closing from the earlier statements. A difference says the minimum is
	// not nested the way R2.3 assumes, or a payment is missing.
	PastDue *Component

	// Members are the movements belonging to the statement; Candidates are
	// those near its closing that belong to a neighbour.
	Members    []Placed
	Candidates []Placed
}

// Reconcile compares a registered statement with the ledger, from a standing
// derived as of any date after its closing. It returns false when the
// statement is not among the standing's cycles.
func (s Standing) Reconcile(statementID int64) (Reconciliation, bool) {
	j := -1
	for i, c := range s.Cycles {
		if c.Statement != nil && c.Statement.ID == statementID {
			j = i
		}
	}
	if j < 0 {
		return Reconciliation{}, false
	}
	c := s.Cycles[j]
	r := Reconciliation{
		Statement:       *c.Statement,
		Cycle:           c,
		BankOwedMinor:   c.OwedMinor,
		LedgerOwedMinor: c.LedgerOwedMinor,
		DifferenceMinor: c.OwedMinor - c.LedgerOwedMinor,
	}

	var previousLedger int64
	if j > 0 {
		previousLedger = s.Cycles[j-1].LedgerOwedMinor
	}
	st := c.Statement
	if st.PreviousBalanceMinor != nil {
		r.Previous = component(-*st.PreviousBalanceMinor, previousLedger)
	}
	if st.PaymentsMinor != nil {
		r.Payments = component(*st.PaymentsMinor, c.PaymentsMinor+c.CreditsMinor)
	}
	if st.DebitsMinor != nil {
		r.Debits = component(*st.DebitsMinor, c.DebitsMinor)
	}
	if r.Previous != nil && r.Payments != nil && r.Debits != nil {
		rest := r.DifferenceMinor - (r.Previous.DifferenceMinor - r.Payments.DifferenceMinor + r.Debits.DifferenceMinor)
		r.Unexplained = &rest
	}
	if st.PastDueMinor != nil {
		r.PastDue = component(*st.PastDueMinor, s.pastDueAtClosing(j))
	}

	from := c.ClosingDate.AddDate(0, 0, -candidateWindowDays)
	to := c.ClosingDate.AddDate(0, 0, candidateWindowDays)
	for _, p := range s.Placed {
		switch {
		case p.ClosingDate.Equal(c.ClosingDate):
			r.Members = append(r.Members, p)
		case !p.Date.Before(from) && !p.Date.After(to):
			r.Candidates = append(r.Candidates, p)
		}
	}
	return r, true
}

// pastDueAtClosing is V as the bank would have stated it on statement j: the
// largest unmet minimum of the earlier cycles due by then, counting the
// payments made up to the closing.
func (s Standing) pastDueAtClosing(j int) int64 {
	closing := s.Cycles[j].ClosingDate
	var worst int64
	for i := 0; i < j; i++ {
		c := s.Cycles[i]
		if c.DueDate == nil || !c.DueDate.Before(closing) || c.MinimumMinor == nil {
			continue
		}
		paid := s.paymentsAfter(c.ClosingDate, closing)
		worst = max(worst, *c.MinimumMinor-paid)
	}
	return worst
}

// paymentsAfter sums the payments belonging to a cycle closing after `after`
// and dated on or before `through`.
func (s Standing) paymentsAfter(after, through time.Time) int64 {
	var total int64
	for _, p := range s.Placed {
		if p.Role == RolePayment && p.ClosingDate.After(after) && !p.Date.After(through) {
			total += p.AmountMinor
		}
	}
	return total
}
