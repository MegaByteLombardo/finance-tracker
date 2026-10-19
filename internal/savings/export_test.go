package savings

import "time"

// SetClock fixes the date a test's "today" is read from: which policy versions
// may still start, and whether an income's date has arrived.
func SetClock(s *Service, now func() time.Time) { s.now = now }
