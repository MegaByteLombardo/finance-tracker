package savings

import "time"

// SetClock fixes the date a test's "today" is read from: which policy versions
// may still start, and whether an income's date has arrived.
func SetClock(s *Service, now func() time.Time) { s.now = now }

// Share is a goal as the distribution sees it, for testing distribute alone.
type Share struct {
	ID        int64
	BP        int64
	Remaining int64
}

// Distribute exposes the distribution arithmetic (TD-085) to tests.
func Distribute(amountMinor int64, shares []Share) ([]DistributionLine, error) {
	in := make([]goalShare, len(shares))
	for i, s := range shares {
		in[i] = goalShare{id: s.ID, bp: s.BP, remaining: s.Remaining}
	}
	return distribute(amountMinor, in)
}
