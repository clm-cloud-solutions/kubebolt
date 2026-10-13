package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// writeRequestAt builds a WriteRequest whose series carry samples stamped at
// the given times (one series per slice entry, n samples each).
func writeRequestAt(tenantID string, n int, at ...time.Time) []byte {
	var wr []byte
	for i, ts := range at {
		var series []byte
		for _, kv := range [][2]string{{"__name__", "node_load1"}, {"tenant_id", tenantID}, {"node", string(rune('a' + i))}} {
			series = protowire.AppendTag(series, 1, protowire.BytesType)
			series = protowire.AppendBytes(series, buildLabel(kv[0], kv[1]))
		}
		for j := 0; j < n; j++ {
			var sample []byte
			sample = protowire.AppendTag(sample, 1, protowire.Fixed64Type) // Sample.value
			sample = protowire.AppendFixed64(sample, 0)
			sample = protowire.AppendTag(sample, 2, protowire.VarintType) // Sample.timestamp (ms)
			sample = protowire.AppendVarint(sample, uint64(ts.Add(time.Duration(j)*time.Second).UnixMilli()))
			series = protowire.AppendTag(series, 2, protowire.BytesType)
			series = protowire.AppendBytes(series, sample)
		}
		wr = protowire.AppendTag(wr, 1, protowire.BytesType)
		wr = protowire.AppendBytes(wr, series)
	}
	return wr
}

func TestNewestSampleMillisAndBacklog(t *testing.T) {
	now := time.Now()
	old, fresh := now.Add(-time.Hour), now.Add(-5*time.Second)
	newest, ok := newestSampleMillis(writeRequestAt("t", 3, old, fresh))
	if !ok || newest != fresh.Add(2*time.Second).UnixMilli() {
		t.Fatalf("newest = %d (%v), want the fresh series' last sample", newest, ok)
	}
	if isBacklog(writeRequestAt("t", 3, old, fresh), now) {
		t.Error("a batch with a live sample is not a backlog")
	}
	if !isBacklog(writeRequestAt("t", 3, old, now.Add(-10*time.Minute)), now) {
		t.Error("a batch whose newest sample is ten minutes old is a backlog")
	}
	if isBacklog(nil, now) {
		t.Error("an empty batch is not a backlog")
	}
}

// Over the limit, a sender catching up gets 503 + Retry-After (retried, not
// lost); live traffic over the limit keeps the 429.
func TestE2E_PromRemoteWrite_RateLimit_BacklogIsDeferred(t *testing.T) {
	prev := promCatchUp
	promCatchUp = newCatchUpWindows(time.Now()) // the API just started: catch-up window open
	t.Cleanup(func() { promCatchUp = prev })
	tight := auth.EffectiveLimits{WriteSamplesPerSec: 10, WriteBurstSamples: 10, MaxActiveSeries: 10_000_000}
	h, plaintext, tenantID, upstream, reg := newE2EHandler(t, promWriteAuthEnforced, tight)
	post := func(at time.Time) *httptest.ResponseRecorder {
		body := snappy.Encode(nil, writeRequestAt(tenantID, 50, at))
		req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		h.handlePromWrite(rec, req)
		return rec
	}

	rec := post(time.Now().Add(-30 * time.Minute))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("backlog over the limit: status %d, Retry-After %q; want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if v := counterByLabels(t, reg, "kubebolt_prom_write_requests_total", map[string]string{"tenant_id": tenantID, "status": PromWriteStatusDeferredBacklog}); v != 1 {
		t.Errorf("requests_total{status=rate_limit_deferred} = %v, want 1", v)
	}

	rec = post(time.Now().Add(-3 * time.Second))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("live traffic over the limit: status %d, want 429", rec.Code)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.lastBody) > 0 {
		t.Error("a throttled batch reached VictoriaMetrics")
	}
}

func TestCatchUpWindows(t *testing.T) {
	t0 := time.Now()
	c := newCatchUpWindows(t0.Add(-2 * catchUpWindow)) // started long ago
	if c.Open("org", t0) {
		t.Fatal("no interruption, yet the window is open")
	}
	c.Seen("org", t0)
	c.Seen("org", t0.Add(30*time.Second))
	if c.Open("org", t0.Add(30*time.Second)) {
		t.Fatal("steady traffic opened a window")
	}
	c.Seen("org", t0.Add(30*time.Second+catchUpGap+time.Second)) // came back after a silence
	if !c.Open("org", t0.Add(5*time.Minute)) {
		t.Fatal("a silence did not open the window")
	}
	if c.Open("org", t0.Add(2*catchUpWindow)) {
		t.Fatal("the window never closes")
	}
	c.Interrupted("other", t0)
	if !c.Open("other", t0.Add(time.Minute)) || c.Open("third", t0.Add(time.Minute)) {
		t.Fatal("an interruption must open that tenant's window only")
	}
	if !newCatchUpWindows(t0).Open("anyone", t0.Add(time.Minute)) {
		t.Fatal("the API's own start must open every window")
	}
}

// The case the window exists for: a tenant over its limit for good, whose
// sender retries its 429s and falls behind. Its batches are old, but with no
// interruption behind them they are an overrun — 429, shown as rate-limited.
func TestE2E_PromRemoteWrite_SustainedOverrunStays429(t *testing.T) {
	prev := promCatchUp
	promCatchUp = newCatchUpWindows(time.Now().Add(-2 * catchUpWindow))
	t.Cleanup(func() { promCatchUp = prev })

	tight := auth.EffectiveLimits{WriteSamplesPerSec: 10, WriteBurstSamples: 10, MaxActiveSeries: 10_000_000}
	h, plaintext, tenantID, _, reg := newE2EHandler(t, promWriteAuthEnforced, tight)
	for i := 0; i < 2; i++ { // back to back: no silence between them
		body := snappy.Encode(nil, writeRequestAt(tenantID, 50, time.Now().Add(-30*time.Minute)))
		req := httptest.NewRequest(http.MethodPost, "/prom/write", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		h.handlePromWrite(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d: status %d, want 429 — an overrun outside a catch-up window", i, rec.Code)
		}
	}
	if v := counterByLabels(t, reg, "kubebolt_prom_write_requests_total", map[string]string{"tenant_id": tenantID, "status": PromWriteStatusRejectedRateLimit}); v != 2 {
		t.Errorf("requests_total{status=rate_limit} = %v, want 2", v)
	}
}
