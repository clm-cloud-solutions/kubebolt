package api

import (
	"sync"
	"time"
)

// A backlog is deferred (503, retried by the sender) instead of refused (429,
// dropped by Prometheus) only inside a catch-up window that a real
// interruption opened. The age of a batch alone cannot tell a catch-up from an
// overrun: a sender pushing more than its limit and retrying its 429s
// (retry_on_http_429) falls behind too, and its batches grow old. Without the
// window, that sustained overrun would be deferred for ever and never show as
// rate-limited.
//
// What opens a tenant's window — each one a reason its sender queued data it
// will now ship at full speed:
//
//   - this API process starting (the API was down, for everyone);
//   - the tenant's own silence: a request after more than catchUpGap without
//     one (its sender could not reach us);
//   - VictoriaMetrics failing to take the tenant's batch (it got a 5xx and
//     queued).
//
// The window lasts catchUpWindow after its last trigger. Past it, an over-limit
// batch gets 429 however old it is. State is per replica and in memory: each
// replica rate-limits its own share of the traffic.
const (
	catchUpWindow = time.Hour
	catchUpGap    = 2 * time.Minute
)

type catchUpWindows struct {
	mu       sync.Mutex
	started  time.Time
	lastSeen map[string]time.Time
	until    map[string]time.Time
}

func newCatchUpWindows(started time.Time) *catchUpWindows {
	return &catchUpWindows{started: started, lastSeen: map[string]time.Time{}, until: map[string]time.Time{}}
}

// promCatchUp is the process's windows, opened by its own start.
var promCatchUp = newCatchUpWindows(time.Now())

// Seen records a request from the tenant, opening its window when it comes
// after a silence.
func (c *catchUpWindows) Seen(tenant string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.lastSeen[tenant]; ok && now.Sub(last) > catchUpGap {
		c.until[tenant] = now.Add(catchUpWindow)
	}
	c.lastSeen[tenant] = now
}

// Interrupted opens the tenant's window: its batch could not be stored.
func (c *catchUpWindows) Interrupted(tenant string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until[tenant] = now.Add(catchUpWindow)
}

// Open reports whether an over-limit backlog from the tenant is a catch-up to
// defer rather than an overrun to refuse.
func (c *catchUpWindows) Open(tenant string, now time.Time) bool {
	if now.Before(c.started.Add(catchUpWindow)) {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Before(c.until[tenant])
}
