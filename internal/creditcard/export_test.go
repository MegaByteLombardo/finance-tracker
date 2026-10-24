package creditcard

import "time"

// SetClock fixes the date a test's "today" is read from.
func SetClock(s *Service, now func() time.Time) { s.now = now }
