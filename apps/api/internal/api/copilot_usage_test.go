package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

func setupUsageTest(t *testing.T) (*handlers, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := bolt.Open(filepath.Join(dir, "test.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("bolt open: %v", err)
	}
	bucket := []byte("sessions")
	if err := db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(bucket)
		return e
	}); err != nil {
		t.Fatalf("bucket create: %v", err)
	}

	h := &handlers{copilotUsage: copilot.NewUsageStore(db, bucket)}
	cleanup := func() { _ = db.Close() }
	return h, cleanup
}

func recordFixture(t *testing.T, h *handlers, ts time.Time, rec *copilot.SessionRecord) {
	t.Helper()
	rec.Timestamp = ts
	if err := h.copilotUsage.Record(rec); err != nil {
		t.Fatalf("record: %v", err)
	}
}

func TestUsageSummary_ServiceUnavailableWhenStoreNil(t *testing.T) {
	h := &handlers{} // no copilotUsage
	req := httptest.NewRequest(http.MethodGet, "/admin/copilot/usage/summary?range=24h", nil)
	rec := httptest.NewRecorder()

	h.handleCopilotUsageSummary(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestUsageSummary_AggregatesRecords(t *testing.T) {
	h, cleanup := setupUsageTest(t)
	defer cleanup()

	now := time.Now()
	// 3 done sessions, 1 error session, all within last hour
	recordFixture(t, h, now.Add(-30*time.Minute), &copilot.SessionRecord{
		UserID: "u1", Provider: "anthropic", Model: "claude-sonnet-4-6",
		Trigger: "manual", Reason: "done", Rounds: 2,
		Usage:      copilot.Usage{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 500},
		ToolCalls:  1, ToolBytes: 2000, DurationMs: 3000,
		Tools: map[string]copilot.ToolStats{"get_pods": {Calls: 1, Bytes: 2000}},
	})
	recordFixture(t, h, now.Add(-20*time.Minute), &copilot.SessionRecord{
		UserID: "u1", Provider: "anthropic", Model: "claude-sonnet-4-6",
		Trigger: "insight", Reason: "done", Rounds: 3,
		Usage:      copilot.Usage{InputTokens: 2000, OutputTokens: 400, CacheReadTokens: 1500},
		ToolCalls:  2, ToolBytes: 4000, DurationMs: 5000,
		Tools: map[string]copilot.ToolStats{"get_pods": {Calls: 1, Bytes: 2000}, "get_events": {Calls: 1, Bytes: 2000}},
	})
	recordFixture(t, h, now.Add(-10*time.Minute), &copilot.SessionRecord{
		UserID: "u2", Provider: "openai", Model: "gpt-5-mini",
		Trigger: "manual", Reason: "done", Rounds: 1,
		Usage:      copilot.Usage{InputTokens: 500, OutputTokens: 100},
		ToolCalls:  0, DurationMs: 1500,
	})
	recordFixture(t, h, now.Add(-5*time.Minute), &copilot.SessionRecord{
		UserID: "u2", Provider: "openai", Model: "gpt-5-mini",
		Trigger: "manual", Reason: "error", Rounds: 1,
		Usage:    copilot.Usage{InputTokens: 100, OutputTokens: 0},
		DurationMs: 500,
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/copilot/usage/summary?range=24h", nil)
	rec := httptest.NewRecorder()
	h.handleCopilotUsageSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp usageSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Sessions != 4 {
		t.Errorf("sessions = %d, want 4", resp.Sessions)
	}
	if resp.ErrorSessions != 1 {
		t.Errorf("errorSessions = %d, want 1", resp.ErrorSessions)
	}
	if resp.InputTokens != 3600 {
		t.Errorf("inputTokens = %d, want 3600", resp.InputTokens)
	}
	if resp.OutputTokens != 700 {
		t.Errorf("outputTokens = %d, want 700", resp.OutputTokens)
	}
	if resp.CacheRead != 2000 {
		t.Errorf("cacheRead = %d, want 2000", resp.CacheRead)
	}
	if resp.TotalBilled != 4300 {
		t.Errorf("totalBilled = %d, want 4300", resp.TotalBilled)
	}
	if resp.TopTriggers["manual"] != 3 {
		t.Errorf("manual trigger count = %d, want 3", resp.TopTriggers["manual"])
	}
	if resp.TopTriggers["insight"] != 1 {
		t.Errorf("insight trigger count = %d, want 1", resp.TopTriggers["insight"])
	}
	// Cost should be > 0 since we have known-pricing models
	if resp.EstimatedUSD <= 0 {
		t.Errorf("estimatedUSD should be > 0 with known models, got %v", resp.EstimatedUSD)
	}
	// Cache hit pct: 2000 / (3600 + 2000) = ~35.7%
	if resp.CacheHitPct < 30 || resp.CacheHitPct > 40 {
		t.Errorf("cacheHitPct out of range: %v", resp.CacheHitPct)
	}
	// Top tools should list get_pods first (2 calls)
	if len(resp.TopTools) == 0 || resp.TopTools[0].Name != "get_pods" {
		t.Errorf("expected get_pods first, got %+v", resp.TopTools)
	}
}

func TestUsageSummary_EmptyStore(t *testing.T) {
	h, cleanup := setupUsageTest(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/admin/copilot/usage/summary?range=7d", nil)
	rec := httptest.NewRecorder()
	h.handleCopilotUsageSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp usageSummaryResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Sessions != 0 {
		t.Errorf("empty store should report 0 sessions, got %d", resp.Sessions)
	}
	if resp.EstimatedUSD != 0 {
		t.Errorf("empty store should report $0, got %v", resp.EstimatedUSD)
	}
}

func TestUsageTimeseries_BucketsByDay(t *testing.T) {
	h, cleanup := setupUsageTest(t)
	defer cleanup()

	now := time.Now()
	for i := 0; i < 3; i++ {
		recordFixture(t, h, now.Add(-time.Duration(i)*24*time.Hour), &copilot.SessionRecord{
			UserID: fmt.Sprintf("u%d", i), Reason: "done", Rounds: 1,
			Usage: copilot.Usage{InputTokens: 100, OutputTokens: 50},
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/copilot/usage/timeseries?range=7d", nil)
	rec := httptest.NewRecorder()
	h.handleCopilotUsageTimeseries(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var buckets []timeseriesBucket
	if err := json.Unmarshal(rec.Body.Bytes(), &buckets); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 7d with day bucketing should produce 7 or 8 buckets (start/end partial)
	if len(buckets) < 6 || len(buckets) > 9 {
		t.Errorf("buckets count = %d, want ~7-8", len(buckets))
	}
	// Total sessions across buckets should equal our 3 records
	var total int
	for _, b := range buckets {
		total += b.Sessions
	}
	if total != 3 {
		t.Errorf("total sessions across buckets = %d, want 3", total)
	}
}

func TestUsageSessions_RespectsLimit(t *testing.T) {
	h, cleanup := setupUsageTest(t)
	defer cleanup()

	now := time.Now()
	for i := 0; i < 15; i++ {
		recordFixture(t, h, now.Add(-time.Duration(i)*time.Minute), &copilot.SessionRecord{
			UserID: "u", Reason: "done", Rounds: 1,
			Usage: copilot.Usage{InputTokens: 10},
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/copilot/usage/sessions?range=24h&limit=5", nil)
	rec := httptest.NewRecorder()
	h.handleCopilotUsageSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 5 {
		t.Errorf("limit=5 should return 5, got %d", len(list))
	}
	// Should include the enriched estimatedUsd field
	if _, ok := list[0]["estimatedUsd"]; !ok {
		t.Error("session response missing estimatedUsd field")
	}
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		input string
		wantH time.Duration
	}{
		{"24h", 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"", 7 * 24 * time.Hour},       // default
		{"weird", 7 * 24 * time.Hour},  // default
		{"48h", 48 * time.Hour},        // numeric hours
		{"3d", 3 * 24 * time.Hour},     // numeric days
	}
	for _, c := range cases {
		from, to := parseRange(c.input)
		span := to.Sub(from)
		// Allow ±2s slop for test timing variance
		if span < c.wantH-2*time.Second || span > c.wantH+2*time.Second {
			t.Errorf("parseRange(%q) span = %v, want ~%v", c.input, span, c.wantH)
		}
	}
}
