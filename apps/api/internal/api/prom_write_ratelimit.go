package api

import (
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// PromRateLimiter is a per-tenant token-bucket rate limiter for the
// /api/v1/prom/write receiver. Each tenant gets its own
// `*rate.Limiter`; calls from different tenants never share a bucket.
//
// Bucket sizing is driven by the tenant's effective limits
// (resolved on every Allow call from custom-overrides + fleet
// defaults). When the operator updates a tenant's overrides via the
// admin API the next Allow re-builds the bucket — no callback /
// invalidation needed because the comparison is free and the rate
// of admin edits is microscopic vs the rate of ingest calls.
//
// The limiter map is sync.RWMutex-guarded: hot path reads under
// RLock, recreates upgrade to write-lock. A double-checked locking
// pattern keeps the write-lock window minimal.
//
// Design note: the in-memory state (current token count) does NOT
// persist across restarts. That is the right semantic — restarting
// the backend means a fresh window starts, which is conservative
// (slightly more permissive than persisting count) and avoids a
// BoltDB write on every accepted request. The DURABLE configuration
// lives in BoltDB on the tenant; the RUNTIME state is ephemeral.
type PromRateLimiter struct {
	defaults auth.EffectiveLimits

	mu      sync.RWMutex
	buckets map[string]*rateBucket
}

// rateBucket bundles the active limiter with the EffectiveLimits it
// was built from. On the next Allow call we compare the resolved
// EffectiveLimits to the cached one; mismatch triggers a rebuild.
// Using value-equality on EffectiveLimits (== comparison) keeps the
// detection branch-free in the common case.
type rateBucket struct {
	limiter *rate.Limiter
	config  auth.EffectiveLimits
}

// NewPromRateLimiter constructs the limiter with the system defaults
// the tenant resolution will fall back to when a tenant has no
// per-field override.
func NewPromRateLimiter(defaults auth.EffectiveLimits) *PromRateLimiter {
	return &PromRateLimiter{
		defaults: defaults,
		buckets:  make(map[string]*rateBucket),
	}
}

// Allow returns whether the tenant may consume `sampleCount` samples
// right now. The boolean is the gate; the time.Duration is the
// suggested Retry-After if the gate denied — taken from
// `(*rate.Limiter).Reserve()` so the client gets an accurate hint
// instead of a fixed-1s wait. On Allow=true the returned duration
// is zero.
//
// tenantOverride is the tenant's custom Limits (may be nil — that's
// fine, ResolveLimits handles nil). sampleCount is the parsed sample
// total from the request body; must be >= 1.
//
// Concurrency: safe for arbitrary parallel calls across tenants.
// Within a single tenant, the limiter itself is goroutine-safe; the
// rebuild path is double-checked locked.
func (l *PromRateLimiter) Allow(tenantID string, tenantOverride *auth.TenantLimits, sampleCount int) (bool, time.Duration) {
	if sampleCount < 1 {
		// Zero / negative samples should never reach this path
		// (the parser produces non-negative counts), but be defensive
		// — accept the request without consuming bucket tokens.
		return true, 0
	}
	effective := auth.ResolveLimits(tenantOverride, l.defaults)

	// Explicit "block all" posture: a rate of 0 means deny every
	// request. rate.Limit(0) is a quirky edge case in the rate
	// package (NewLimiter(0, 0) accepts zero tokens, never refills);
	// short-circuit here for clarity and to avoid any divide-by-zero
	// surface in delay calculation.
	if effective.WriteSamplesPerSec == 0 {
		return false, time.Second
	}

	b := l.getOrCreate(tenantID, effective)
	now := time.Now()
	// ReserveN is preferred over AllowN because it gives us the
	// time-until-tokens-available even on denial, which we surface
	// as Retry-After. Cancel() returns the reserved tokens if we're
	// going to reject — important so the bucket state stays
	// honest about what the next request would see.
	res := b.limiter.ReserveN(now, sampleCount)
	if !res.OK() {
		// OK() == false when sampleCount > burst. This is the
		// "request can NEVER be served" branch — the client must
		// shrink its batch. We return a Retry-After hint, but the
		// real fix is on the client side (smaller batches).
		return false, time.Second
	}
	delay := res.DelayFrom(now)
	if delay == 0 {
		// Tokens were available immediately: accept.
		return true, 0
	}
	// Tokens weren't available yet. Cancel the reservation so the
	// tokens go back to the bucket — we're rejecting, not waiting.
	res.CancelAt(now)
	return false, delay
}

func (l *PromRateLimiter) getOrCreate(tenantID string, effective auth.EffectiveLimits) *rateBucket {
	// Hot path: RLock, exact-match config → reuse.
	l.mu.RLock()
	b, ok := l.buckets[tenantID]
	l.mu.RUnlock()
	if ok && b.config == effective {
		return b
	}
	// Cold path: upgrade to write lock and re-check.
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok = l.buckets[tenantID]
	if ok && b.config == effective {
		return b
	}
	// Build or rebuild.
	b = &rateBucket{
		limiter: rate.NewLimiter(rate.Limit(effective.WriteSamplesPerSec), effective.WriteBurstSamples),
		config:  effective,
	}
	l.buckets[tenantID] = b
	return b
}

// Forget drops the tenant's bucket from the cache. Used when a
// tenant is deleted so the map doesn't accumulate orphan entries
// over the process lifetime. Idempotent: forgetting a missing
// tenant is a no-op.
func (l *PromRateLimiter) Forget(tenantID string) {
	l.mu.Lock()
	delete(l.buckets, tenantID)
	l.mu.Unlock()
}
