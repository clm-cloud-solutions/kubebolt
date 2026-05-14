package copilot

import (
	"encoding/json"
	"strings"
	"testing"
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
