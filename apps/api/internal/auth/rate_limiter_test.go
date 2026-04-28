package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiter_DisabledAlwaysAllows(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: false, RequestsPerSec: 1, Burst: 1})
	for i := 0; i < 1000; i++ {
		ok, _ := rl.Allow("t1")
		if !ok {
			t.Fatalf("disabled limiter must always allow, denied at iter %d", i)
		}
	}
}

func TestRateLimiter_EmptyTenantAlwaysAllows(t *testing.T) {
	// The auth interceptor stamps a synthetic identity with TenantID=""
	// in disabled / permissive-fallback mode. The rate limiter must
	// not start rejecting those — pin the contract.
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerSec: 1, Burst: 1})
	for i := 0; i < 100; i++ {
		ok, _ := rl.Allow("")
		if !ok {
			t.Fatalf("empty tenant must always allow, denied at iter %d", i)
		}
	}
}

func TestRateLimiter_BurstThenDeniesWithRetryAfter(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerSec: 10, Burst: 5})
	base := time.Now()
	rl.nowFn = func() time.Time { return base }

	for i := 0; i < 5; i++ {
		if ok, _ := rl.Allow("t1"); !ok {
			t.Fatalf("burst[%d] must be allowed", i)
		}
	}
	ok, retry := rl.Allow("t1")
	if ok {
		t.Fatal("call past burst must be denied")
	}
	// Refill rate is 10/s → 1 token in 100ms.
	if retry != 100*time.Millisecond {
		t.Errorf("retry-after = %v, want 100ms (1 token / 10 rps)", retry)
	}
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerSec: 10, Burst: 5})
	base := time.Now()
	rl.nowFn = func() time.Time { return base }

	// Drain bucket.
	for i := 0; i < 5; i++ {
		rl.Allow("t1")
	}

	// Advance 200ms → 2 tokens refill.
	rl.nowFn = func() time.Time { return base.Add(200 * time.Millisecond) }
	if ok, _ := rl.Allow("t1"); !ok {
		t.Error("after 200ms, 1st refilled token should be available")
	}
	if ok, _ := rl.Allow("t1"); !ok {
		t.Error("after 200ms, 2nd refilled token should be available")
	}
	if ok, _ := rl.Allow("t1"); ok {
		t.Error("3rd should be denied — only 2 tokens refilled in 200ms @ 10 rps")
	}
}

func TestRateLimiter_PerTenantIsolation(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerSec: 1, Burst: 2})
	base := time.Now()
	rl.nowFn = func() time.Time { return base }

	rl.Allow("t1")
	rl.Allow("t1")
	if ok, _ := rl.Allow("t1"); ok {
		t.Error("t1 should be exhausted after burst")
	}
	// t2's bucket is independent.
	if ok, _ := rl.Allow("t2"); !ok {
		t.Error("t2 must not be affected by t1 draining")
	}
	if ok, _ := rl.Allow("t2"); !ok {
		t.Error("t2 should still have a token")
	}
	if ok, _ := rl.Allow("t2"); ok {
		t.Error("t2 burst should now be exhausted independently")
	}
}

func TestRateLimiter_RefillCapsAtBurst(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerSec: 10, Burst: 5})
	base := time.Now()
	rl.nowFn = func() time.Time { return base }

	rl.Allow("t1") // tokens = 4
	rl.nowFn = func() time.Time { return base.Add(time.Hour) }
	// After a long idle, refill caps at Burst (=5), not infinite.
	for i := 0; i < 5; i++ {
		if ok, _ := rl.Allow("t1"); !ok {
			t.Errorf("burst refill[%d] should be allowed", i)
		}
	}
	if ok, _ := rl.Allow("t1"); ok {
		t.Error("6th call must be denied — refill is capped at Burst=5")
	}
}

func TestRateLimiter_Concurrency(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerSec: 100, Burst: 100})
	var wg sync.WaitGroup
	var allowed, denied atomic.Int32

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := rl.Allow("t1"); ok {
				allowed.Add(1)
			} else {
				denied.Add(1)
			}
		}()
	}
	wg.Wait()

	if allowed.Load()+denied.Load() != 200 {
		t.Errorf("total = %d, want 200", allowed.Load()+denied.Load())
	}
	// Burst is 100. Some refill may happen during execution so we don't
	// pin the exact count; we pin that at least Burst pass through.
	if allowed.Load() < 100 {
		t.Errorf("allowed = %d, expected >= Burst (100)", allowed.Load())
	}
}

func TestRateLimiter_AllowN(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerSec: 10, Burst: 10})
	base := time.Now()
	rl.nowFn = func() time.Time { return base }

	if ok, _ := rl.AllowN("t1", 7); !ok {
		t.Error("AllowN(7) should succeed when bucket has 10")
	}
	if ok, _ := rl.AllowN("t1", 5); ok {
		t.Error("AllowN(5) should fail — only 3 tokens left")
	}
	if ok, _ := rl.AllowN("t1", 3); !ok {
		t.Error("AllowN(3) should succeed — 3 tokens still available")
	}
}

// ─── LoadRateLimitConfigFromEnv ───────────────────────────────────────

func TestLoadRateLimitConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_ENABLED", "")
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_RPS", "")
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_BURST", "")
	cfg := LoadRateLimitConfigFromEnv()
	if cfg.Enabled {
		t.Error("Enabled should default to false")
	}
	if cfg.RequestsPerSec != 1000 {
		t.Errorf("default RequestsPerSec = %v, want 1000", cfg.RequestsPerSec)
	}
	if cfg.Burst != 2000 {
		t.Errorf("default Burst = %v, want 2000", cfg.Burst)
	}
}

func TestLoadRateLimitConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_ENABLED", "true")
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_RPS", "500")
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_BURST", "1000")
	cfg := LoadRateLimitConfigFromEnv()
	if !cfg.Enabled {
		t.Error("Enabled should be true")
	}
	if cfg.RequestsPerSec != 500 {
		t.Errorf("RequestsPerSec = %v", cfg.RequestsPerSec)
	}
	if cfg.Burst != 1000 {
		t.Errorf("Burst = %v", cfg.Burst)
	}
}

func TestLoadRateLimitConfigFromEnv_InvalidValuesIgnored(t *testing.T) {
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_ENABLED", "true")
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_RPS", "not a number")
	t.Setenv("KUBEBOLT_AGENT_RATE_LIMIT_BURST", "-50")
	cfg := LoadRateLimitConfigFromEnv()
	// Bad parses fall back to defaults silently — alternative would be
	// fail-loud at boot, but that lowers the bar for ad-hoc env edits.
	if cfg.RequestsPerSec != 1000 {
		t.Errorf("invalid RPS should fall back to default, got %v", cfg.RequestsPerSec)
	}
	if cfg.Burst != 2000 {
		t.Errorf("negative Burst should fall back to default, got %v", cfg.Burst)
	}
}
