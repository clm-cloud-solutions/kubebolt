package api

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// KobiMetrics are the AI observability series of doc #67, phase 1. They reach
// VictoriaMetrics through the API's own push (SelfWriteMetricsToVM), like the
// rest of the default registry.
//
// Kobi is the character; Copilot and Autopilot are its modes, and each has its
// own families: kubebolt_kobi_copilot_* are written here, by the chat handler;
// kubebolt_kobi_autopilot_* by the Autopilot service of the Enterprise
// edition, which pushes its own — this API writes none of them.
//
// Two kinds, kept apart on purpose:
//
//   - Health, labelled tenant_id + cluster_id: an alert says which org and
//     cluster it hits. Only small closed label sets ride along (model, trigger,
//     outcome, stop reason, error kind, tool source and result) — never a user,
//     a conversation or a tool name.
//   - Platform detail, no tenant: what multiplies (the tool name, latency
//     histograms) and is only ever read by the operator.
//
// Tokens and cost carry tenant_id (no cluster_id): what each org spends over
// time, a handful of series per org (model × token kind).
//
// Everything with a tenant_id is KubeBolt's own bookkeeping, so it stays out
// of the org's active-series cap and billing (seriesgate.ExcludeServerOwned)
// while customers cannot see it.
type KobiMetrics struct {
	sessions       *prometheus.CounterVec
	modelCalls     *prometheus.CounterVec
	providerErrors *prometheus.CounterVec
	toolResults    *prometheus.CounterVec

	toolCalls     *prometheus.CounterVec
	toolSeconds   *prometheus.HistogramVec
	modelCallSecs *prometheus.HistogramVec
	fallbacks     *prometheus.CounterVec

	tokens  *prometheus.CounterVec
	costUSD *prometheus.CounterVec

	feedback *prometheus.CounterVec
}

// NewKobiMetrics registers the series on reg (prometheus.DefaultRegisterer in
// production, a fresh registry in tests).
func NewKobiMetrics(reg prometheus.Registerer) *KobiMetrics {
	m := &KobiMetrics{
		sessions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_sessions_total",
			Help: "Kobi Copilot chat turns by how they ended (done, error, max_rounds, canceled).",
		}, []string{"tenant_id", "cluster_id", "model", "trigger", "outcome"}),
		modelCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_model_calls_total",
			Help: "Kobi Copilot model calls that answered, by normalized stop reason (end_turn, tool_use, max_tokens, refusal, other).",
		}, []string{"tenant_id", "cluster_id", "stop_reason"}),
		providerErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_provider_errors_total",
			Help: "Kobi Copilot model calls that failed, by kind (rate_limit, overloaded, server, timeout, network, auth, not_found, bad_request, other).",
		}, []string{"tenant_id", "cluster_id", "kind"}),
		toolResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_tool_results_total",
			Help: "Kobi Copilot tool calls by source (kubebolt, mcp:<connector>) and result (ok, empty, not_found, denied, timeout, error).",
		}, []string{"tenant_id", "cluster_id", "source", "result"}),

		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_tool_calls_total",
			Help: "Kobi Copilot tool calls by tool, source and result, across every org.",
		}, []string{"tool", "source", "result"}),
		toolSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kubebolt_kobi_copilot_tool_seconds",
			Help:    "Kobi Copilot tool call duration.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"tool", "source"}),
		modelCallSecs: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kubebolt_kobi_copilot_model_call_seconds",
			Help:    "Duration of Kobi Copilot model calls that answered.",
			Buckets: []float64{0.5, 1, 2, 4, 8, 16, 32, 64, 120},
		}, []string{"provider", "model"}),
		fallbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_fallback_total",
			Help: "Kobi Copilot calls retried on the fallback provider, and whether the fallback answered.",
		}, []string{"from_model", "to_model", "rescued"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_tokens_total",
			Help: "Kobi Copilot tokens by org, model and kind. Kinds are disjoint: input (uncached), output, thinking, cache_read, cache_write_5m, cache_write_1h.",
		}, []string{"tenant_id", "provider", "model", "kind"}),
		costUSD: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_cost_usd_total",
			Help: "Estimated AI provider cost of Kobi Copilot in USD, by org and model (copilot pricing table).",
		}, []string{"tenant_id", "provider", "model"}),
		feedback: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kubebolt_kobi_copilot_feedback_total",
			Help: "Ratings given to Kobi Copilot answers (up, down) and a down's reason (incorrect, incomplete, off_topic, slow, none). A rating changed counts again; a withdrawal does not.",
		}, []string{"tenant_id", "cluster_id", "rating", "reason"}),
	}
	if reg != nil {
		reg.MustRegister(m.sessions, m.modelCalls, m.providerErrors, m.toolResults,
			m.toolCalls, m.toolSeconds, m.modelCallSecs, m.fallbacks, m.tokens, m.costUSD, m.feedback)
	}
	return m
}

// The process-wide instance, set once at boot (cmd/server). A package-level
// handle rather than another NewRouter argument: the chat handler, the aux
// calls and the Autopilot usage endpoint all record into it. Nil until set —
// every method is nil-safe, so tests and builds without it record nothing.
var kobiMetricsPtr atomic.Pointer[KobiMetrics]

// SetKobiMetrics installs the instance the handlers record into.
func SetKobiMetrics(m *KobiMetrics) { kobiMetricsPtr.Store(m) }

func kobiMetrics() *KobiMetrics { return kobiMetricsPtr.Load() }

// knownTriggers is where a chat turn can start (CopilotTriggerType in the web
// app, plus the two the client sends on its own). The client names it, so an
// unknown value becomes "other" rather than a new series.
var knownTriggers = map[string]struct{}{
	"manual": {}, "insight": {}, "not_ready_resource": {}, "warning_event": {}, "flow_edge": {},
	"metric_anomaly": {}, "resource_inquiry": {}, "panel_inquiry": {}, "action_stalled": {},
	"continue_after_max_rounds": {}, "cluster_switch": {},
}

func triggerLabel(t string) string {
	if _, ok := knownTriggers[t]; ok {
		return t
	}
	return "other"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// ObserveSession records a finished chat turn from its session record: the
// health counters, and its tokens and cost.
func (m *KobiMetrics) ObserveSession(rec *copilot.SessionRecord) {
	if m == nil || rec == nil {
		return
	}
	// One org in this edition: the tenant label stays empty, so every series
	// has the same shape as the Enterprise one without naming an org.
	tenant, cluster := "", rec.ClusterID
	m.sessions.WithLabelValues(tenant, cluster, orUnknown(rec.Model), triggerLabel(rec.Trigger), orUnknown(rec.Reason)).Inc()
	for stop, n := range rec.StopReasons {
		m.modelCalls.WithLabelValues(tenant, cluster, stop).Add(float64(n))
	}
	for kind, n := range rec.ProviderErrors {
		m.providerErrors.WithLabelValues(tenant, cluster, kind).Add(float64(n))
	}
	for _, t := range rec.Tools {
		source := t.Source
		if source == "" {
			source = copilot.ToolSourceKubeBolt
		}
		for result, n := range t.Results {
			m.toolResults.WithLabelValues(tenant, cluster, source, result).Add(float64(n))
		}
	}
	m.ObserveUsage(tenant, rec.Provider, rec.Model, rec.Usage)
}

// ObserveModelCall records how long a model call that answered took.
func (m *KobiMetrics) ObserveModelCall(provider, model string, d time.Duration) {
	if m == nil {
		return
	}
	m.modelCallSecs.WithLabelValues(orUnknown(provider), orUnknown(model)).Observe(d.Seconds())
}

// ObserveToolCall records one tool call.
func (m *KobiMetrics) ObserveToolCall(tool, source, result string, d time.Duration) {
	if m == nil {
		return
	}
	m.toolCalls.WithLabelValues(tool, source, result).Inc()
	m.toolSeconds.WithLabelValues(tool, source).Observe(d.Seconds())
}

// ObserveFallback records a call retried on the fallback provider.
func (m *KobiMetrics) ObserveFallback(fromModel, toModel string, rescued bool) {
	if m == nil {
		return
	}
	r := "false"
	if rescued {
		r = "true"
	}
	m.fallbacks.WithLabelValues(orUnknown(fromModel), orUnknown(toModel), r).Inc()
}

// ObserveUsage adds a Copilot call's tokens, split into disjoint kinds so a
// sum over kind is the real total, and its estimated cost when the model is
// priced. tenant is the org ("" when the call has none).
func (m *KobiMetrics) ObserveUsage(tenant, provider, model string, u copilot.Usage) {
	if m == nil {
		return
	}
	tokens, cost := m.tokens, m.costUSD
	provider, model = orUnknown(provider), orUnknown(model)
	thinking := min(max(u.ThinkingTokens, 0), u.OutputTokens)
	writes1h := min(u.CacheCreation1hTokens, u.CacheCreationTokens)
	for kind, n := range map[string]int{
		"input":          u.InputTokens,
		"output":         u.OutputTokens - thinking,
		"thinking":       thinking,
		"cache_read":     u.CacheReadTokens,
		"cache_write_5m": u.CacheCreationTokens - writes1h,
		"cache_write_1h": writes1h,
	} {
		if n > 0 {
			tokens.WithLabelValues(tenant, provider, model, kind).Add(float64(n))
		}
	}
	if p, ok := copilot.PricingFor(provider, model); ok {
		if usd := copilot.EstimateUSD(u, p); usd > 0 {
			cost.WithLabelValues(tenant, provider, model).Add(usd)
		}
	}
}

// ObserveFeedback counts a rating given to an answer: the user's words never
// ride along, only the rating and a 👎's reason.
func (m *KobiMetrics) ObserveFeedback(tenant, cluster, rating, reason string) {
	if m == nil {
		return
	}
	if reason == "" {
		reason = "none"
	}
	m.feedback.WithLabelValues(tenant, cluster, rating, reason).Inc()
}
