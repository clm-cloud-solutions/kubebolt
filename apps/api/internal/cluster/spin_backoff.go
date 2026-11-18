package cluster

import (
	"fmt"
	"time"
)

// spinFailure is a pooled runtime's last failed connect.
type spinFailure struct {
	at       time.Time
	err      error
	attempts int
}

// spinBackoffBase and spinBackoffMax bound how long a failed pooled connect
// holds back the next one started by a request: 30 s, 1 min, 2 min, 4 min,
// then 5 min.
var (
	spinBackoffBase = 30 * time.Second
	spinBackoffMax  = 5 * time.Minute
)

func spinBackoff(attempts int) time.Duration {
	d := spinBackoffBase
	for i := 1; i < attempts && d < spinBackoffMax; i++ {
		d *= 2
	}
	if d > spinBackoffMax {
		d = spinBackoffMax
	}
	return d
}

// recordSpinFailureLocked remembers a failed pooled connect. Caller holds m.mu.
func (m *Manager) recordSpinFailureLocked(pk poolKey, err error, now time.Time) {
	if m.spinFailures == nil {
		m.spinFailures = map[poolKey]*spinFailure{}
	}
	attempts := 1
	if prev := m.spinFailures[pk]; prev != nil {
		attempts = prev.attempts + 1
	}
	m.spinFailures[pk] = &spinFailure{at: now, err: err, attempts: attempts}
}

// spinBackingOffLocked reports whether pk's last connect failed recently
// enough that a request must not start another. Caller holds m.mu.
func (m *Manager) spinBackingOffLocked(pk poolKey, now time.Time) bool {
	f := m.spinFailures[pk]
	return f != nil && now.Sub(f.at) < spinBackoff(f.attempts)
}

// spinFailureErrLocked is the error a request for pk is answered with while no
// runtime exists: why the last connect failed and when the next may start.
// Caller holds m.mu (read or write).
func (m *Manager) spinFailureErrLocked(pk poolKey, now time.Time) error {
	f := m.spinFailures[pk]
	if f == nil {
		return nil
	}
	if wait := spinBackoff(f.attempts) - now.Sub(f.at); wait > 0 {
		return fmt.Errorf("%w (next attempt in %s)", f.err, wait.Round(time.Second))
	}
	return f.err
}
