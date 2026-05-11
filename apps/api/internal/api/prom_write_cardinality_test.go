package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// cardinalityVMStub stands in for a real VictoriaMetrics for the
// cardinality tracker tests. It returns multi-row `count by (tenant_id)`
// responses, distinct in shape from the single-scalar `vmStub` used by
// coverage_test.go. Each call increments a counter so tests can assert
// refresh cadence.
type cardinalityVMStub struct {
	mu       sync.Mutex
	calls    int
	response map[string]int // tenantID → count
}

func newCardinalityVMStub() *cardinalityVMStub {
	return &cardinalityVMStub{response: map[string]int{}}
}

func (v *cardinalityVMStub) setCounts(m map[string]int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.response = m
}

func (v *cardinalityVMStub) callCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

func (v *cardinalityVMStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		v.calls++
		body := struct {
			Status string `json:"status"`
			Data   struct {
				ResultType string `json:"resultType"`
				Result     []struct {
					Metric map[string]string `json:"metric"`
					Value  [2]interface{}    `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}{
			Status: "success",
		}
		body.Data.ResultType = "vector"
		for tid, count := range v.response {
			body.Data.Result = append(body.Data.Result, struct {
				Metric map[string]string `json:"metric"`
				Value  [2]interface{}    `json:"value"`
			}{
				Metric: map[string]string{"tenant_id": tid},
				Value:  [2]interface{}{float64(time.Now().Unix()), formatFloat(float64(count))},
			})
		}
		v.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
}

func defaultsForCardinalityTest() auth.EffectiveLimits {
	return auth.EffectiveLimits{
		WriteSamplesPerSec: 1000,
		WriteBurstSamples:  5000,
		MaxActiveSeries:    100, // small so tests can drive the cap
	}
}

func TestCardinalityTracker_PermissiveBootBeforeFirstRefresh(t *testing.T) {
	stub := newCardinalityVMStub()
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	// DO NOT call RunRefreshLoop — hasFresh stays false.
	allowed, _ := tracker.Allow("tenant-A", nil)
	if !allowed {
		t.Fatalf("not-yet-fresh cache should allow (permissive boot)")
	}
}

func TestCardinalityTracker_BelowCapAllows(t *testing.T) {
	stub := newCardinalityVMStub()
	stub.setCounts(map[string]int{"tenant-A": 50})
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	tracker.SeenTenant("tenant-A")
	tracker.refresh(context.Background())

	allowed, _ := tracker.Allow("tenant-A", nil)
	if !allowed {
		t.Errorf("count 50 < cap 100 should allow")
	}
}

func TestCardinalityTracker_AtCapDenies(t *testing.T) {
	stub := newCardinalityVMStub()
	stub.setCounts(map[string]int{"tenant-A": 100})
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	tracker.SeenTenant("tenant-A")
	tracker.refresh(context.Background())

	allowed, retryAfter := tracker.Allow("tenant-A", nil)
	if allowed {
		t.Errorf("count 100 >= cap 100 should deny")
	}
	if retryAfter <= 0 {
		t.Errorf("denial should suggest Retry-After")
	}
}

func TestCardinalityTracker_CustomOverrideRaisesCap(t *testing.T) {
	stub := newCardinalityVMStub()
	stub.setCounts(map[string]int{"tenant-B": 150})
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	tracker.SeenTenant("tenant-B")
	tracker.refresh(context.Background())

	// Without override: 150 >= 100 default → deny.
	allowedDefault, _ := tracker.Allow("tenant-B", nil)
	if allowedDefault {
		t.Errorf("without override, 150 >= 100 should deny")
	}
	// Operator raises cap to 200 → allow.
	cap200 := 200
	override := &auth.TenantLimits{MaxActiveSeries: &cap200}
	allowedCustom, _ := tracker.Allow("tenant-B", override)
	if !allowedCustom {
		t.Errorf("with override 200, 150 < 200 should allow")
	}
}

func TestCardinalityTracker_ZeroCapBlocksAll(t *testing.T) {
	stub := newCardinalityVMStub()
	stub.setCounts(map[string]int{"tenant-C": 0})
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	tracker.SeenTenant("tenant-C")
	tracker.refresh(context.Background())

	// Operator pins tenant to cap=0 → block all.
	zero := 0
	override := &auth.TenantLimits{MaxActiveSeries: &zero}
	allowed, retryAfter := tracker.Allow("tenant-C", override)
	if allowed {
		t.Errorf("zero cap should deny everything")
	}
	if retryAfter <= 0 {
		t.Errorf("zero-cap denial should suggest Retry-After")
	}
}

func TestCardinalityTracker_RefreshUpdatesCount(t *testing.T) {
	stub := newCardinalityVMStub()
	stub.setCounts(map[string]int{"tenant-D": 50})
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	tracker.SeenTenant("tenant-D")
	tracker.refresh(context.Background())

	if c, fresh := tracker.CurrentCount("tenant-D"); !fresh || c != 50 {
		t.Errorf("first refresh: expected (50, true), got (%d, %v)", c, fresh)
	}
	// Server's view changed — refresh picks it up.
	stub.setCounts(map[string]int{"tenant-D": 75})
	tracker.refresh(context.Background())
	if c, _ := tracker.CurrentCount("tenant-D"); c != 75 {
		t.Errorf("after refresh: expected 75, got %d", c)
	}
}

func TestCardinalityTracker_KnownTenantWithNoVMRowsPinsToZero(t *testing.T) {
	stub := newCardinalityVMStub()
	// VM has no series for tenant-E yet.
	stub.setCounts(map[string]int{})
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	tracker.SeenTenant("tenant-E")
	tracker.refresh(context.Background())

	c, fresh := tracker.CurrentCount("tenant-E")
	if !fresh || c != 0 {
		t.Errorf("known tenant absent from VM should be pinned to 0, got (%d, %v)", c, fresh)
	}
}

func TestCardinalityTracker_UnknownTenantAllowedBeforeRegistration(t *testing.T) {
	stub := newCardinalityVMStub()
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	tracker.refresh(context.Background()) // mark fresh

	// Allow on unknown tenant should pass — lazy registration kicks
	// in via SeenTenant inside Allow.
	allowed, _ := tracker.Allow("brand-new-tenant", nil)
	if !allowed {
		t.Errorf("first-sight tenant should be allowed pending next refresh")
	}
}

func TestCardinalityTracker_Forget(t *testing.T) {
	stub := newCardinalityVMStub()
	stub.setCounts(map[string]int{"tenant-F": 50})
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 1*time.Hour)
	tracker.SeenTenant("tenant-F")
	tracker.refresh(context.Background())
	if c, _ := tracker.CurrentCount("tenant-F"); c != 50 {
		t.Fatalf("setup: expected 50, got %d", c)
	}

	tracker.Forget("tenant-F")
	stub.setCounts(map[string]int{}) // simulate cleanup downstream
	tracker.refresh(context.Background())
	if c, _ := tracker.CurrentCount("tenant-F"); c != 0 {
		t.Errorf("after Forget, count should be 0, got %d", c)
	}
}

func TestCardinalityTracker_RunRefreshLoopExitsOnContextCancel(t *testing.T) {
	stub := newCardinalityVMStub()
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	tracker := NewCardinalityTracker(srv.URL, defaultsForCardinalityTest(), srv.Client(), 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		tracker.RunRefreshLoop(ctx)
		close(done)
	}()

	// Wait long enough for at least 2 ticks to fire.
	time.Sleep(150 * time.Millisecond)
	calls := stub.callCount()
	if calls < 2 {
		t.Errorf("expected at least 2 refresh calls in 150ms, got %d", calls)
	}

	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatalf("RunRefreshLoop did not exit within 1s of cancel")
	}
}
