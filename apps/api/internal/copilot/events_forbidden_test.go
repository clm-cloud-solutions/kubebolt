package copilot

import (
	"encoding/json"
	"testing"
)

// get_events on a cluster whose events KubeBolt cannot list answers a
// permission error the model can read, never an empty list.
func TestEventsForbiddenContent_IsAPermissionError(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal([]byte(eventsForbiddenContent), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got["forbidden"] != true {
		t.Fatalf("forbidden = %v, want true", got["forbidden"])
	}
	if _, ok := got["items"]; ok {
		t.Fatalf("carries items: it would read as \"no events\"")
	}
}
