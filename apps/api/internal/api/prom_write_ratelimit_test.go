package api

import (
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

func defaultsForTest() auth.EffectiveLimits {
	return auth.EffectiveLimits{
		WriteSamplesPerSec: 1000,
		WriteBurstSamples:  5000,
		MaxActiveSeries:    100_000,
	}
}

func TestPromRateLimiter_FreshTenantAllowsWithinBurst(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// First call consumes 100 samples — well within burst.
	ok, retryAfter := rl.Allow("tenant-a", nil, 100)
	if !ok {
		t.Fatalf("first call within burst should be allowed; retryAfter=%v", retryAfter)
	}
	if retryAfter != 0 {
		t.Errorf("allowed call should have zero retryAfter, got %v", retryAfter)
	}
}

func TestPromRateLimiter_DrainsBucketAcrossCalls(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// Default burst is 5000. Consume in 3 chunks of 2000, then a
	// 4th of 2000 must be denied.
	for i := 0; i < 2; i++ {
		ok, _ := rl.Allow("tenant-b", nil, 2000)
		if !ok {
			t.Fatalf("call %d within burst (2000×2=4000 < 5000): should pass", i)
		}
	}
	// Next 2000 puts cumulative usage at 6000, beyond the 5000 burst.
	ok, retryAfter := rl.Allow("tenant-b", nil, 2000)
	if ok {
		t.Fatalf("4th 2000-sample call past burst should be denied")
	}
	if retryAfter <= 0 {
		t.Errorf("denied call should suggest a positive Retry-After, got %v", retryAfter)
	}
}

func TestPromRateLimiter_PerTenantIsolation(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// Tenant A drains its bucket.
	if ok, _ := rl.Allow("A", nil, 5000); !ok {
		t.Fatalf("first A call should pass (==burst)")
	}
	// Tenant B should still have a full bucket.
	if ok, _ := rl.Allow("B", nil, 5000); !ok {
		t.Fatalf("B should have its own full bucket, got denial")
	}
	// A's second call gets denied — own bucket drained.
	if ok, _ := rl.Allow("A", nil, 1); ok {
		t.Fatalf("A's bucket is drained; should deny")
	}
}

func TestPromRateLimiter_CustomOverridesPickedUp(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// First call with custom burst of 100 — only 100 samples fit.
	burst := 100
	custom := &auth.TenantLimits{WriteBurstSamples: &burst}
	if ok, _ := rl.Allow("C", custom, 100); !ok {
		t.Fatalf("first 100 within custom burst should pass")
	}
	if ok, _ := rl.Allow("C", custom, 1); ok {
		t.Fatalf("next sample past custom burst should be denied")
	}
}

func TestPromRateLimiter_ConfigChangeRebuildsBucket(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// Drain tenant D against the system defaults.
	if ok, _ := rl.Allow("D", nil, 5000); !ok {
		t.Fatalf("drain should pass")
	}
	if ok, _ := rl.Allow("D", nil, 1); ok {
		t.Fatalf("drained bucket should deny")
	}
	// Operator raises the tenant's burst via admin API. Next Allow
	// must detect the new config and rebuild the bucket.
	newBurst := 100_000
	custom := &auth.TenantLimits{WriteBurstSamples: &newBurst}
	if ok, _ := rl.Allow("D", custom, 50_000); !ok {
		t.Fatalf("after config change with bigger burst, request should pass")
	}
}

func TestPromRateLimiter_ZeroRateBlocksAll(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// Operator pins a tenant to "blocked" by setting rate=0.
	zero := 0
	custom := &auth.TenantLimits{WriteSamplesPerSec: &zero}
	ok, retryAfter := rl.Allow("E", custom, 1)
	if ok {
		t.Fatalf("zero-rate tenant should be denied")
	}
	if retryAfter <= 0 {
		t.Errorf("denied (zero-rate) call should suggest non-zero Retry-After")
	}
}

func TestPromRateLimiter_ZeroSampleCountAccepted(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	if ok, _ := rl.Allow("F", nil, 0); !ok {
		t.Fatalf("zero samples should pass without consuming bucket")
	}
	// And the next real call should still have a full bucket.
	if ok, _ := rl.Allow("F", nil, 5000); !ok {
		t.Fatalf("bucket should be intact after zero-sample call")
	}
}

func TestPromRateLimiter_OverBurstRejectsImmediately(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// Request larger than the entire burst — `ReserveN(OK==false)` path.
	// This is the "client must shrink its batch" branch; we want a
	// non-zero retry-after so the client knows to wait/retry.
	ok, retryAfter := rl.Allow("G", nil, 10_000_000)
	if ok {
		t.Fatalf("oversized request should be denied")
	}
	if retryAfter <= 0 {
		t.Errorf("oversized denial should still suggest Retry-After, got %v", retryAfter)
	}
}

func TestPromRateLimiter_Forget(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// Build up state for a tenant.
	if ok, _ := rl.Allow("H", nil, 5000); !ok {
		t.Fatalf("setup drain")
	}
	if ok, _ := rl.Allow("H", nil, 1); ok {
		t.Fatalf("drained should deny")
	}
	// Forget the tenant.
	rl.Forget("H")
	// Next call recreates a fresh bucket — full burst available.
	if ok, _ := rl.Allow("H", nil, 5000); !ok {
		t.Fatalf("after Forget, bucket should be fresh")
	}
}

func TestPromRateLimiter_RetryAfterIsNonZeroOnDenial(t *testing.T) {
	rl := NewPromRateLimiter(defaultsForTest())
	// Drain the bucket.
	if ok, _ := rl.Allow("I", nil, 5000); !ok {
		t.Fatalf("setup drain")
	}
	ok, retryAfter := rl.Allow("I", nil, 100)
	if ok {
		t.Fatalf("post-drain should deny")
	}
	// Tokens refill at rate. Expected wait = 100/1000 = 100ms.
	// Allow some slop for clock granularity.
	if retryAfter < 50*time.Millisecond || retryAfter > 200*time.Millisecond {
		t.Errorf("retryAfter outside expected range (50-200ms): got %v", retryAfter)
	}
}
