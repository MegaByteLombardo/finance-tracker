package ledger

import "time"

// SetClock fixes the date a test's "today" is read from, so that what counts
// as a future-dated movement does not depend on the day the suite runs.
func SetClock(s *Service, now func() time.Time) { s.now = now }
