package agent

import (
	"sync"
	"time"
)

// writeFailureEvery is the most often a failing metrics store is logged.
const writeFailureEvery = time.Minute

// writeFailureLog turns a metrics store that keeps failing into one log line
// a minute instead of one per batch from every agent: an install without
// VictoriaMetrics, or with one that is down, would otherwise log every few
// seconds for every node. The zero value is ready to use.
type writeFailureLog struct {
	mu     sync.Mutex
	last   time.Time
	failed int
}

// record counts one failed write. It says to log the first failure and then
// at most one a minute, with how many batches failed since the last line.
func (l *writeFailureLog) record(now time.Time) (logNow bool, failed int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failed++
	if !l.last.IsZero() && now.Sub(l.last) < writeFailureEvery {
		return false, 0
	}
	failed, l.failed, l.last = l.failed, 0, now
	return true, failed
}
