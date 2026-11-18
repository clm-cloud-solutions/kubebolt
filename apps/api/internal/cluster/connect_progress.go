package cluster

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// maxConnectDuration caps a connect that keeps making progress. The connect and
// cache-sync timeouts are idle budgets: a connect
// fails when nothing arrives for that long, not when a slow tunnel is still
// delivering pages. A large cluster can send tens of MB through its agent at
// under 1 MB/s, which no fixed 45 s budget fits; this cap only stops a connect
// that trickles forever.
var maxConnectDuration = 10 * time.Minute

// connectProgress records when a connector last received a successful
// non-watch response from the API server: a LIST page, a discovery document,
// a GET. A watch is not progress: on a busy cluster the watches of the
// informers that already synced deliver events all the time, which would hide
// a LIST that is stuck.
type connectProgress struct {
	last atomic.Int64 // unix nanos
}

func newConnectProgress() *connectProgress {
	p := &connectProgress{}
	p.touch()
	return p
}

func (p *connectProgress) touch() { p.last.Store(time.Now().UnixNano()) }

// idleFor is how long ago the last response arrived.
func (p *connectProgress) idleFor() time.Duration {
	return time.Since(time.Unix(0, p.last.Load()))
}

// progressRoundTripper marks progress on every 2xx response to a request that
// is not a watch. It sits in rest.Config.WrapTransport, so the informers, the
// discovery client and the dedicated probe/UID clients (CopyConfig keeps the
// wrapper) all report through it.
type progressRoundTripper struct {
	next http.RoundTripper
	p    *connectProgress
}

func (t *progressRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && !isWatchRequest(req) {
		t.p.touch()
	}
	return resp, err
}

// WrappedRoundTripper lets client-go's transport helpers reach the transport
// underneath (utilnet.RoundTripperWrapper).
func (t *progressRoundTripper) WrappedRoundTripper() http.RoundTripper { return t.next }

func isWatchRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	switch req.URL.Query().Get("watch") {
	case "true", "1":
		return true
	}
	return false
}

// connectBudgetError says which budget ended a connect: no response for the
// idle budget (stalled), or the overall cap.
type connectBudgetError struct {
	stalled bool
	budget  time.Duration
}

func (e *connectBudgetError) Error() string {
	if e.stalled {
		return "no response from the cluster for " + e.budget.String()
	}
	return "still connecting after " + e.budget.String()
}

// progressContext is canceled when p sees no progress for idle, or when the
// connect has run for maxTotal, whichever comes first. The cause is available
// from context.Cause. idle <= 0 disables the idle budget (the cap still holds).
func progressContext(parent context.Context, p *connectProgress, idle, maxTotal time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	tick := idle / 10
	if tick < 50*time.Millisecond {
		tick = 50 * time.Millisecond
	}
	if tick > time.Second {
		tick = time.Second
	}
	start := time.Now()
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if maxTotal > 0 && time.Since(start) >= maxTotal {
					cancel(&connectBudgetError{budget: maxTotal})
					return
				}
				if idle > 0 && p.idleFor() >= idle {
					cancel(&connectBudgetError{stalled: true, budget: idle})
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(context.Canceled) }
}
