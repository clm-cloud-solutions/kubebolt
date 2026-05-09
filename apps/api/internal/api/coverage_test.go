package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// vmStub is a minimal VictoriaMetrics impostor for the coverage
// handler. Each query routed to it can be configured to return a
// specific count; absent configurations default to "no samples".
type vmStub struct {
	mu       sync.Mutex
	counts   map[string]float64 // PromQL → numeric result
	requests []string           // queries received, in order
}

func newVMStub() *vmStub {
	return &vmStub{counts: map[string]float64{}}
}

func (s *vmStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		s.mu.Lock()
		s.requests = append(s.requests, q)
		count, ok := s.counts[q]
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			// No matching configured response — emit empty result,
			// which the handler treats as "inactive".
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		// Single scalar result with the configured value.
		body := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"` +
			formatFloat(count) + `"]}]}}`
		_, _ = w.Write([]byte(body))
	}
}

func formatFloat(v float64) string {
	// Tests don't need fancy formatting; integer-ish values
	// keep the asserts readable.
	if v == float64(int64(v)) {
		return jsonInt(int64(v))
	}
	return jsonFloat(v)
}

func jsonInt(v int64) string {
	return strings.TrimSpace(jsonString(v))
}
func jsonFloat(v float64) string {
	return strings.TrimSpace(jsonString(v))
}
func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// configure accepts a fragment that the handler's PromQL is
// expected to contain (after cluster_id injection). Tests use
// substring matching to avoid brittle assertions on the exact
// scoped query string.
func (s *vmStub) configure(t *testing.T, fragment string, count float64) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Store under the fragment; the handler will issue the full
	// scoped query, but the stub matches by substring at lookup.
	s.counts[fragment] = count
}

// override the stub handler to do substring matching against the
// configured fragments rather than exact string equality.
func (s *vmStub) substringHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		s.mu.Lock()
		s.requests = append(s.requests, q)
		var matched float64
		var found bool
		for fragment, count := range s.counts {
			if strings.Contains(q, fragment) {
				matched = count
				found = true
				break
			}
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !found {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		body := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"` +
			formatFloat(matched) + `"]}]}}`
		_, _ = w.Write([]byte(body))
	}
}

func TestHandleCoverage_AllSourcesInactive(t *testing.T) {
	stub := newVMStub()
	ts := httptest.NewServer(stub.substringHandler())
	defer ts.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", ts.URL)

	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/coverage", nil)
	rec := httptest.NewRecorder()

	h.handleCoverage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var got CoverageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.LookbackMinutes != coverageLookbackMinutes {
		t.Errorf("LookbackMinutes = %d, want %d", got.LookbackMinutes, coverageLookbackMinutes)
	}
	if len(got.Sources) != len(coverageProbes) {
		t.Fatalf("expected %d sources, got %d", len(coverageProbes), len(got.Sources))
	}
	for _, s := range got.Sources {
		if s.Status != "inactive" {
			t.Errorf("source %q status = %q, want inactive", s.Name, s.Status)
		}
	}
}

func TestHandleCoverage_PartialActive(t *testing.T) {
	stub := newVMStub()
	// Match by bare metric name — scopeQueryByCluster injects
	// `cluster_id=""` BEFORE other selectors, so substring patterns
	// that include `{` or selector content won't survive the
	// rewrite. Metric names themselves pass through untouched.
	stub.configure(t, "kubebolt_agent_info", 2)     // 2 agent pods alive
	stub.configure(t, "pod_flow_events_total", 100) // hubble flowing
	// node-exporter and kube-state-metrics intentionally absent.

	ts := httptest.NewServer(stub.substringHandler())
	defer ts.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", ts.URL)

	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/coverage", nil)
	rec := httptest.NewRecorder()

	h.handleCoverage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got CoverageResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	want := map[string]string{
		"kubebolt-agent":     "active",
		"node-exporter":      "inactive",
		"kube-state-metrics": "inactive",
		"hubble":             "active",
	}
	for _, s := range got.Sources {
		if s.Status != want[s.Name] {
			t.Errorf("source %q status = %q, want %q", s.Name, s.Status, want[s.Name])
		}
	}
}

func TestHandleCoverage_AllSourcesActive(t *testing.T) {
	stub := newVMStub()
	// Configure each probe by metric name. See PartialActive
	// for why fragments can't include selectors.
	for _, name := range []string{
		"kubebolt_agent_info",
		"node_cpu_seconds_total",
		"kube_pod_info",
		"pod_flow_events_total",
	} {
		stub.configure(t, name, 1)
	}

	ts := httptest.NewServer(stub.substringHandler())
	defer ts.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", ts.URL)

	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/coverage", nil)
	rec := httptest.NewRecorder()

	h.handleCoverage(rec, req)

	var got CoverageResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	for _, s := range got.Sources {
		if s.Status != "active" {
			t.Errorf("source %q status = %q, want active", s.Name, s.Status)
		}
	}
}

func TestHandleCoverage_VMUnreachableMarksAllInactive(t *testing.T) {
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", "http://127.0.0.1:1")

	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/coverage", nil)
	rec := httptest.NewRecorder()

	h.handleCoverage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (VM down ≠ handler error), got %d", rec.Code)
	}
	var got CoverageResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	for _, s := range got.Sources {
		if s.Status != "inactive" {
			t.Errorf("source %q with unreachable VM = %q, want inactive", s.Name, s.Status)
		}
	}
}

func TestCoverageStatusForQuery_ZeroCountIsInactive(t *testing.T) {
	stub := newVMStub()
	stub.configure(t, "myquery", 0) // explicit zero — empty cluster, target not yet seen
	ts := httptest.NewServer(stub.substringHandler())
	defer ts.Close()
	t.Setenv("KUBEBOLT_METRICS_STORAGE_URL", ts.URL)

	got := coverageStatusForQuery(context.Background(), "myquery")
	if got != "inactive" {
		t.Errorf("zero-count query → %q, want inactive", got)
	}
}
