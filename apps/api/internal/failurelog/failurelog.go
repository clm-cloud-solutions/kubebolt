// Package failurelog keeps a failure that repeats on a schedule — a metrics
// store that is down or rejects the credentials — to one log line a minute
// instead of one per attempt.
package failurelog

import (
	"sync"
	"time"
)

// Every is the most often a repeating failure is logged.
const Every = time.Minute

// Log counts the failures of one recurring operation. The zero value is
// ready to use.
type Log struct {
	mu     sync.Mutex
	last   time.Time
	failed int
}

// Record counts one failure. It says to log the first one and then at most
// one a minute, with how many failed since the last line.
func (l *Log) Record(now time.Time) (logNow bool, failed int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failed++
	if !l.last.IsZero() && now.Sub(l.last) < Every {
		return false, 0
	}
	failed, l.failed, l.last = l.failed, 0, now
	return true, failed
}
