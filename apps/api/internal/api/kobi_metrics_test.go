package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// withKobiMetrics installs a fresh instance for the test and restores the
// previous one after.
func withKobiMetrics(t *testing.T) *KobiMetrics {
	t.Helper()
	m := NewKobiMetrics(prometheus.NewRegistry())
	prev := kobiMetrics()
	SetKobiMetrics(m)
	t.Cleanup(func() { SetKobiMetrics(prev) })
	return m
}

func TestKobiMetrics_ObserveSession(t *testing.T) {
	m := NewKobiMetrics(prometheus.NewRegistry())
	m.ObserveSession(&copilot.SessionRecord{
		ClusterID: "uid-1", Provider: "anthropic", Model: "claude-haiku-5-5",
		Trigger: "insight", Reason: "done",
		StopReasons:    map[string]int{copilot.StopToolUse: 2, copilot.StopRefusal: 1},
		ProviderErrors: map[string]int{copilot.ProviderErrRateLimit: 1},
		Tools: map[string]copilot.ToolStats{
			"get_pod_logs":         {Source: "kubebolt", Results: map[string]int{"ok": 2}},
			"list_resources":       {Source: "kubebolt", Results: map[string]int{"empty": 1}},
			"mcp__aws__cloudwatch": {Source: "mcp:aws", Results: map[string]int{"denied": 1}},
		},
		Usage: copilot.Usage{InputTokens: 100, OutputTokens: 50, ThinkingTokens: 20, CacheReadTokens: 900,
			CacheCreationTokens: 300, CacheCreation1hTokens: 200},
	})

	checks := []struct {
		got  float64
		want float64
		what string
	}{
		{testutil.ToFloat64(m.sessions.WithLabelValues("", "uid-1", "claude-haiku-5-5", "insight", "done")), 1, "session"},
		{testutil.ToFloat64(m.modelCalls.WithLabelValues("", "uid-1", "refusal")), 1, "refusal"},
		{testutil.ToFloat64(m.modelCalls.WithLabelValues("", "uid-1", "tool_use")), 2, "tool_use"},
		{testutil.ToFloat64(m.providerErrors.WithLabelValues("", "uid-1", "rate_limit")), 1, "rate limit"},
		{testutil.ToFloat64(m.toolResults.WithLabelValues("", "uid-1", "kubebolt", "ok")), 2, "kubebolt ok"},
		{testutil.ToFloat64(m.toolResults.WithLabelValues("", "uid-1", "kubebolt", "empty")), 1, "kubebolt empty"},
		{testutil.ToFloat64(m.toolResults.WithLabelValues("", "uid-1", "mcp:aws", "denied")), 1, "mcp denied"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.what, c.got, c.want)
		}
	}

	// Token kinds are disjoint: thinking leaves output, the 1h writes leave the
	// 5m ones, and the kinds add up to every token of the call.
	kinds := map[string]float64{"input": 100, "output": 30, "thinking": 20, "cache_read": 900, "cache_write_5m": 100, "cache_write_1h": 200}
	var total float64
	for kind, want := range kinds {
		got := testutil.ToFloat64(m.tokens.WithLabelValues("", "anthropic", "claude-haiku-5-5", kind))
		if got != want {
			t.Errorf("tokens[%s] = %v, want %v", kind, got, want)
		}
		total += got
	}
	if total != 100+50+900+300 {
		t.Errorf("kinds add up to %v, want every token once", total)
	}
	if cost := testutil.ToFloat64(m.costUSD.WithLabelValues("", "anthropic", "claude-haiku-5-5")); cost <= 0 {
		t.Errorf("a priced model must add cost, got %v", cost)
	}
}

// The client names the trigger. An unknown one must not open a new series.
func TestKobiMetrics_UnknownTriggerIsOther(t *testing.T) {
	m := NewKobiMetrics(prometheus.NewRegistry())
	m.ObserveSession(&copilot.SessionRecord{Model: "x", Trigger: "'; DROP TABLE", Reason: "done"})
	if got := testutil.ToFloat64(m.sessions.WithLabelValues("", "", "x", "other", "done")); got != 1 {
		t.Errorf("unknown trigger should land on other, got %v", got)
	}
}

func TestKobiMetrics_NilSafe(t *testing.T) {
	var m *KobiMetrics
	m.ObserveSession(&copilot.SessionRecord{})
	m.ObserveModelCall("a", "b", time.Second)
	m.ObserveToolCall("t", "kubebolt", "ok", time.Second)
	m.ObserveFallback("a", "b", true)
	m.ObserveUsage("o", "a", "b", copilot.Usage{InputTokens: 1})
}

// Through the real router: a turn that calls a KubeBolt tool and an MCP-named
// one, then refuses, lands on the health series, on the
// platform tool series and on the model-latency histogram.
func TestCopilotChat_RecordsKobiMetrics(t *testing.T) {
	m := withKobiMetrics(t)
	prov := newScripted(func(req copilot.ChatRequest) *copilot.ChatResponse {
		if len(lastToolResults(req)) == 0 {
			return &copilot.ChatResponse{StopReason: "tool_use", ToolCalls: []copilot.ToolCall{
				{ID: "t1", Name: "list_resources", Input: json.RawMessage(`{"type":"pods"}`)},
				{ID: "t2", Name: "mcp__aws__cloudwatch_query", Input: json.RawMessage(`{}`)},
			}}
		}
		return &copilot.ChatResponse{Text: "no", StopReason: "refusal"}
	})
	w := newWiringWithAgents(t, prov.name, newManagerOn(t, map[string]string{}), nil)
	body, _ := json.Marshal(map[string]any{"messages": []copilot.Message{{Role: copilot.RoleUser, Content: "hi"}}})
	if rec := w.do(t, w.admin, http.MethodPost, "/api/v1/copilot/chat", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	got := w.sessions.last(t)

	if v := testutil.ToFloat64(m.sessions.WithLabelValues("", got.ClusterID, "fake-model", "manual", "done")); v != 1 {
		t.Errorf("sessions_total = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.modelCalls.WithLabelValues("", got.ClusterID, "refusal")); v != 1 {
		t.Errorf("model_calls_total{refusal} = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.toolCalls.WithLabelValues("mcp__aws__cloudwatch_query", "mcp:aws", "error")); v != 1 {
		t.Errorf("tool_calls_total for the MCP tool = %v, want 1", v)
	}
	if n := testutil.CollectAndCount(m.modelCallSecs); n != 1 {
		t.Errorf("model_call_seconds series = %d, want 1 (one provider/model)", n)
	}
}
