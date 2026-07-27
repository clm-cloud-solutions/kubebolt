// Package auth — rate_limiter.go is per-tenant rate limiting for the
// agent ingest channel.
//
// ENTERPRISE-CANDIDATE (plan-aware policies):
// The token-bucket algorithm here stays OSS — operators get a single
// global limit applied to every tenant. Per-tenant plan-based policies
// (free=1k rps, team=10k rps, enterprise=unlimited) are a candidate to
// move behind a license gate when the SaaS hospedado launches. The
// AllowN signature already takes a tenantID, so the Enterprise path is
// a config lookup and a per-tenant cfg map — no algorithm change.
package auth

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// RateLimitConfig is the token-bucket configuration applied to every
// tenant. Disabled returns the no-op behavior — every call allowed.
type RateLimitConfig struct {
	Enabled        bool
	RequestsPerSec float64 // refill rate (tokens / second)
	Burst          float64 // bucket capacity (max queued tokens)
}

// RateLimiter is a per-key token-bucket. Buckets are created lazily on the
// first call for a key. By default they are never evicted (maxBuckets==0),
// which is fine for the agent-ingest limiter (bounded, trusted tenant set).
// Limiters keyed by an ATTACKER-controlled space (client IP, login identity —
// Sec #5) MUST set a maxBuckets cap via SetMaxBuckets so a flood of distinct
// keys can't grow the map without bound; when the cap is hit, fully-refilled
// (idle) buckets are reclaimed on the next new-key insert.
type RateLimiter struct {
	cfg        RateLimitConfig
	nowFn      func() time.Time // overridable for tests
	mu         sync.Mutex
	buckets    map[string]*tokenBucket
	maxBuckets int // 0 = unbounded (no eviction)
}

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewRateLimiter builds the limiter. cfg.Enabled=false yields a no-op
// limiter (Allow always returns true).
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		cfg:     cfg,
		nowFn:   time.Now,
		buckets: make(map[string]*tokenBucket),
	}
}

// Enabled reports whether the limiter is on. Useful so callers can
// skip the lock when no rate limiting is configured.
func (r *RateLimiter) Enabled() bool { return r.cfg.Enabled }

// SetMaxBuckets caps how many buckets the limiter holds before it starts
// reclaiming idle ones. REQUIRED for limiters keyed by an attacker-controlled
// space (client IP, login identity). Returns the limiter for chaining.
func (r *RateLimiter) SetMaxBuckets(n int) *RateLimiter {
	r.mu.Lock()
	r.maxBuckets = n
	r.mu.Unlock()
	return r
}

// evictIdleLocked reclaims buckets that would be fully refilled by `now` — i.e.
// idle since their last use. Dropping one is harmless: the key just gets a
// fresh full bucket if it reappears. Caller holds r.mu.
func (r *RateLimiter) evictIdleLocked(now time.Time) {
	for k, b := range r.buckets {
		refilled := b.tokens + now.Sub(b.lastRefill).Seconds()*r.cfg.RequestsPerSec
		if refilled >= r.cfg.Burst {
			delete(r.buckets, k)
		}
	}
}

// Allow consumes one token for tenantID. Returns (allowed, retryAfter):
// when allowed=false, retryAfter is the duration to wait before the
// bucket would have enough tokens to satisfy the request.
//
// Empty tenantID is always allowed — the auth interceptor stamps a
// synthetic identity (Mode=disabled) with no TenantID, and we don't
// want the disabled migration window to suddenly start rejecting
// connections because of an unrelated rate limit.
func (r *RateLimiter) Allow(tenantID string) (bool, time.Duration) {
	return r.AllowN(tenantID, 1)
}

// AllowN attempts to consume n tokens. See Allow for behavior.
func (r *RateLimiter) AllowN(tenantID string, n float64) (bool, time.Duration) {
	if !r.cfg.Enabled || tenantID == "" {
		return true, 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFn()
	b, ok := r.buckets[tenantID]
	if !ok {
		// Reclaim idle buckets before growing past the cap (Sec #5) so an
		// attacker rotating keys can't OOM the map. No-op when maxBuckets==0.
		if r.maxBuckets > 0 && len(r.buckets) >= r.maxBuckets {
			r.evictIdleLocked(now)
		}
		// New key: bucket arrives full so the first burst is
		// permitted without warmup.
		b = &tokenBucket{
			tokens:     r.cfg.Burst,
			lastRefill: now,
		}
		r.buckets[tenantID] = b
	}

	// Refill since last update, capped at Burst.
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * r.cfg.RequestsPerSec
		if b.tokens > r.cfg.Burst {
			b.tokens = r.cfg.Burst
		}
		b.lastRefill = now
	}

	if b.tokens >= n {
		b.tokens -= n
		return true, 0
	}

	deficit := n - b.tokens
	retryAfter := time.Duration(deficit / r.cfg.RequestsPerSec * float64(time.Second))
	return false, retryAfter
}

// LoadRateLimitConfigFromEnv reads the relevant env vars. Defaults are
// conservative: enabled=false (Sprint A migration), 1000 rps + 2000
// burst when enabled.
func LoadRateLimitConfigFromEnv() RateLimitConfig {
	cfg := RateLimitConfig{
		Enabled:        os.Getenv("KUBEBOLT_AGENT_RATE_LIMIT_ENABLED") == "true",
		RequestsPerSec: 1000,
		Burst:          2000,
	}
	if v := os.Getenv("KUBEBOLT_AGENT_RATE_LIMIT_RPS"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			cfg.RequestsPerSec = n
		}
	}
	if v := os.Getenv("KUBEBOLT_AGENT_RATE_LIMIT_BURST"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			cfg.Burst = n
		}
	}
	return cfg
}
