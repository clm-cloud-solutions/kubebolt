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

// RateLimiter is a per-tenant token-bucket. Buckets are created lazily
// on the first call for a tenant and never evicted — Sprint A. A long-
// running process with many tenants (SaaS at scale) wants periodic
// eviction; tracked for Sprint B.
type RateLimiter struct {
	cfg     RateLimitConfig
	nowFn   func() time.Time // overridable for tests
	mu      sync.Mutex
	buckets map[string]*tokenBucket
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

	b, ok := r.buckets[tenantID]
	if !ok {
		// New tenant: bucket arrives full so the first burst is
		// permitted without warmup.
		b = &tokenBucket{
			tokens:     r.cfg.Burst,
			lastRefill: r.nowFn(),
		}
		r.buckets[tenantID] = b
	}

	// Refill since last update, capped at Burst.
	now := r.nowFn()
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
