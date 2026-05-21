package copilot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSystemPromptLogFilterGuidance pins the log-filter guidance added when we
// extended get_pod_logs with sinceTime / endTime / previous. The strings below
// are NOT cosmetic — they steer the model's tool-arg selection. A refactor that
// silently drops one of them regresses Kobi's ability to handle past-incident
// queries ("yesterday at 14:00") or post-crash investigations
// ("why did the pod restart"). If you need to reword, update both the prompt
// AND the expected substring here, deliberately.
func TestSystemPromptLogFilterGuidance(t *testing.T) {
	prompt := BuildSystemPrompt()
	lower := strings.ToLower(prompt)

	// Match case-insensitively so cosmetic capitalisation changes don't break
	// the test — what matters is that the guidance is present.
	mustContain := []string{
		// Time window guidance.
		"sincetime",
		"endtime",
		"rfc3339",
		// Previous-container guidance — the only way to read pre-crash logs.
		"previous=true",
		// Anchors the model to the right scenarios.
		"crashloopbackoff",
		"closed window",
		// Hard retention limit so the model doesn't hallucinate ancient logs.
		"current container",
		"one previous",
	}

	for _, s := range mustContain {
		if !strings.Contains(lower, s) {
			t.Errorf("system prompt is missing required guidance substring %q — log-filter classification will degrade", s)
		}
	}
}

// TestGetPodLogsToolSchema pins the get_pod_logs tool schema so the model
// always sees the new params advertised. The schema is what the LLM API
// receives; a property dropped here is invisible to the model regardless of
// what the system prompt says.
func TestGetPodLogsToolSchema(t *testing.T) {
	var tool ToolDefinition
	for _, td := range ToolDefinitions() {
		if td.Name == "get_pod_logs" {
			tool = td
			break
		}
	}
	if tool.Name == "" {
		t.Fatalf("get_pod_logs tool definition not found")
	}

	// Description must coach the model on when to use the new params; a
	// schema-only addition without prose hints regresses tool selection.
	desc := tool.Description
	for _, s := range []string{"sinceTime", "endTime", "previous=true"} {
		if !strings.Contains(desc, s) {
			t.Errorf("get_pod_logs description is missing %q", s)
		}
	}

	// Round-trip the schema through JSON so we exercise the same shape the
	// provider adapters serialize to the API.
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal input schema: %v", err)
	}

	wantProps := map[string]string{
		"sinceTime": "string",
		"endTime":   "string",
		"previous":  "boolean",
		"since":     "string",
		"tailLines": "number",
		"grep":      "string",
		"container": "string",
		"namespace": "string",
		"name":      "string",
	}
	for prop, wantType := range wantProps {
		p, ok := schema.Properties[prop]
		if !ok {
			t.Errorf("get_pod_logs schema is missing property %q", prop)
			continue
		}
		if got, _ := p["type"].(string); got != wantType {
			t.Errorf("get_pod_logs property %q: type=%q, want %q", prop, got, wantType)
		}
	}
}

// TestSessionContextNowBlock pins the format of the "Now" block prepended
// to the operator's first user message. The block is what gives Kobi a
// clock anchor — without it the model guesses "today" from its training
// cutoff and produces day-off errors on relative-time queries
// ("ayer a las 10pm"). A refactor that drops these lines silently
// regresses every "yesterday at X" question into a clarification round-trip
// or, worse, into a query against the wrong day.
func TestSessionContextNowBlock(t *testing.T) {
	now := time.Date(2026, time.May, 15, 1, 53, 41, 0, time.UTC)

	t.Run("with valid IANA timezone", func(t *testing.T) {
		got := BuildSessionContext("prod-cluster", "/pods/demo/api-7b9", now, "Europe/Madrid")

		// Cluster + view header preserved.
		mustContainAll(t, got,
			"# Session context",
			"cluster: prod-cluster",
			"current_view: /pods/demo/api-7b9",
		)

		// Now block: dual UTC + local clock and concrete today/yesterday in
		// the user's TZ. May 15 in Madrid (CEST = UTC+2) → today=2026-05-15,
		// yesterday=2026-05-14. The dates are what Kobi reads first when
		// resolving "ayer".
		mustContainAll(t, got,
			"# Now",
			"now (UTC): 2026-05-15T01:53:41Z",
			"now (Europe/Madrid): 2026-05-15T03:53:41+02:00",
			"today (Europe/Madrid): 2026-05-15",
			"yesterday (Europe/Madrid): 2026-05-14",
		)
	})

	t.Run("with empty timezone falls back to UTC", func(t *testing.T) {
		got := BuildSessionContext("c", "/", now, "")

		mustContainAll(t, got,
			"now (UTC): 2026-05-15T01:53:41Z",
			"today (UTC): 2026-05-15",
			"yesterday (UTC): 2026-05-14",
		)
		// No second clock line when only UTC is known — avoids "now (UTC) /
		// now (UTC)" duplication that the model would have to ignore.
		if strings.Count(got, "now (") != 1 {
			t.Errorf("expected a single now line in UTC-only mode, got:\n%s", got)
		}
	})

	t.Run("with unparseable timezone falls back to UTC", func(t *testing.T) {
		got := BuildSessionContext("c", "/", now, "Not/A/Real/Zone")

		// Bad input is silently downgraded — never surface a Go parse error
		// into the user message.
		if strings.Contains(got, "Not/A/Real/Zone") {
			t.Errorf("invalid TZ leaked into output: %s", got)
		}
		if !strings.Contains(got, "today (UTC):") {
			t.Errorf("expected UTC fallback for unparseable TZ, got:\n%s", got)
		}
	})

	t.Run("yesterday rolls correctly across midnight in user TZ", func(t *testing.T) {
		// 2026-05-15 01:00 UTC = 2026-05-14 21:00 EDT — in EDT, "today"
		// is still the 14th and "yesterday" is the 13th. This is exactly
		// the case that bit us in the live session that motivated this fix.
		got := BuildSessionContext("c", "/", time.Date(2026, time.May, 15, 1, 0, 0, 0, time.UTC), "America/New_York")

		mustContainAll(t, got,
			"today (America/New_York): 2026-05-14",
			"yesterday (America/New_York): 2026-05-13",
		)
	})
}

// TestSystemPromptTimeAnchorGuidance pins the operator-facing guidance that
// teaches Kobi to read the Now block and stop asking for timezone by reflex.
// If a future refactor drops these phrases, every relative-time query
// regresses into a clarification round-trip — the exact UX bug this change
// was built to fix.
func TestSystemPromptTimeAnchorGuidance(t *testing.T) {
	prompt := strings.ToLower(BuildSystemPrompt())
	mustContain := []string{
		// The block exists and the model is told to read it.
		"# now",
		"resolve it against the now block",
		// Default-to-local rule (the "no, don't ask which TZ" guardrail).
		"assume their local tz",
		"do not ask",
	}
	for _, s := range mustContain {
		if !strings.Contains(prompt, s) {
			t.Errorf("system prompt is missing time-anchor guidance %q — Kobi will regress to asking for TZ on every relative-time query", s)
		}
	}
}

// TestRemediationMatrixGuidance pins the rule → tool mapping that
// teaches Kobi to pick the right propose_* for each insight rule.
// The strings below are NOT cosmetic — dropping them silently
// regresses Kobi to "restart everything" for resource-shape problems
// (OOM, throttle, etc.) where restart alone is the wrong fix.
//
// If you reword, update both the prompt and this test deliberately —
// these guardrails are what keep Kobi from re-proposing the same
// useless restart after each crash.
func TestRemediationMatrixGuidance(t *testing.T) {
	prompt := BuildSystemPrompt()
	lower := strings.ToLower(prompt)

	mustContain := []string{
		// Rule → tool anchors. Lowercased so cosmetic capitalization
		// changes don't false-fail.
		"oomkilled",
		"propose_set_resources",
		"propose_set_image",
		"propose_set_env",
		"propose_patch_hpa",
		"hpamaxedoutrule",
		// Negative guidance: cordon/drain and the diagnostic-only rules
		// must explicitly NOT be proposed.
		"nodenotready",
		"do not propose",
		"pvc pending",
		"service with no endpoints",
		// Anti-pattern callouts the LLM must internalize.
		"restart alone is not a fix",
		"wrong direction", // lowering minReplicas for max-pinned HPA
		// Server-side cap on patch_hpa must surface in the prompt so
		// the LLM knows the max it can request.
		"maxreplicas <= 1000",
	}

	for _, s := range mustContain {
		if !strings.Contains(lower, s) {
			t.Errorf("system prompt is missing remediation guidance %q — Kobi will regress to wrong-tool fixes", s)
		}
	}
}

// TestProposeToolDefinitionsRegistered locks in that the 4 new tools
// added in 06-insight-rule-coverage actually appear in ToolDefinitions.
// A tool that exists in the prompt but not in the registered tool list
// is invisible to the LLM and produces "unknown tool" errors when the
// LLM tries to call it.
func TestProposeToolDefinitionsRegistered(t *testing.T) {
	want := []string{
		"propose_set_resources",
		"propose_set_image",
		"propose_set_env",
		"propose_patch_hpa",
		// Spec #07 — metrics tool registration. Same invariant: if the
		// system prompt mentions it but ToolDefinitions doesn't, the LLM
		// will try to call it and crash on "unknown tool".
		"get_workload_metrics",
	}
	registered := map[string]bool{}
	for _, td := range ToolDefinitions() {
		registered[td.Name] = true
	}
	for _, name := range want {
		if !registered[name] {
			t.Errorf("tool %q not registered in ToolDefinitions()", name)
		}
	}
}

// TestWorkloadMetricsGuidance pins the system-prompt directives the new
// get_workload_metrics tool depends on (spec #07). The guarantee Kobi
// makes — "I will check the trend before proposing set_resources" — is
// only durable as long as the prompt explicitly tells it to. A casual
// rewrite that drops the "BEFORE every propose_set_resources" wording
// would silently regress the LLM to point-in-time guessing.
func TestWorkloadMetricsGuidance(t *testing.T) {
	prompt := BuildSystemPrompt()
	lower := strings.ToLower(prompt)

	mustContain := []string{
		// The tool itself must be named so the LLM knows it exists.
		"get_workload_metrics",
		// The mandatory pre-condition for set_resources proposals.
		"before every propose_set_resources",
		// Surface that the join-with-KSM behavior is automatic.
		"utilizationpercent",
		// Operators ask "is X bad over the last hour" — anchor the
		// prompt to that exact framing so the LLM picks the right tool.
		"saturated",
		// Negative guidance: disk metrics are deliberately absent.
		"disk is not exposed",
		// Acknowledgement that we DO have historical metrics (corrects
		// the long-standing "you don't have historical metrics" lie).
		"historical cpu / memory / network metrics",
	}
	for _, s := range mustContain {
		if !strings.Contains(lower, s) {
			t.Errorf("system prompt missing metrics-tool guidance %q — Kobi will regress to point-in-time guessing or call a non-existent disk tool", s)
		}
	}
}

func mustContainAll(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output is missing %q\n--- full output ---\n%s\n-------------------", w, got)
		}
	}
}
