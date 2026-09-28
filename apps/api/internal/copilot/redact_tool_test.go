package copilot

import (
	"strings"
	"testing"
)

// A tool result reaches the model provider, Autopilot's run_events and the
// incident timeline. The JSON tools that carry Kubernetes objects are
// filtered on the way out; others are left as they were.
func TestRedactToolResult_FiltersObjectTools(t *testing.T) {
	detail := `{"spec":{"containers":[{"name":"api","env":[{"name":"DB_PASSWORD","value":"hunter2"},{"name":"LOG_LEVEL","value":"debug"}]}]}}`
	got := redactToolResult("get_resource_detail", ToolResult{Content: detail})
	if strings.Contains(got.Content, "hunter2") || !strings.Contains(got.Content, "debug") {
		t.Errorf("get_resource_detail: %s", got.Content)
	}

	events := `{"items":[{"reason":"Failed","message":"auth failed with password=hunter2 for db"}]}`
	if got := redactToolResult("get_events", ToolResult{Content: events}); strings.Contains(got.Content, "hunter2") {
		t.Errorf("get_events: %s", got.Content)
	}

	// An error result is the tool's own message — left alone.
	errRes := ToolResult{Content: `{"error":"x"}`, IsError: true}
	if got := redactToolResult("get_resource_detail", errRes); got.Content != errRes.Content {
		t.Errorf("error result changed: %s", got.Content)
	}
}
