package copilot

import (
	"encoding/json"
	"strings"
	"testing"
)

// Tests for the pure helpers behind the get_pod_logs container
// auto-selection added after the gitlab-webservice multi-container
// failure on yagan-eks-prod-v2. The full executor flow (which
// requires a live Connector) is exercised in vivo; here we lock
// down the deterministic pieces:
//   1. extractPodContainerNames — handles the GetResourceDetail
//      output shapes (multi, single, missing, malformed).
//   2. formatPodLogs — the new `extra` parameter merges into the
//      response without clobbering the truncation hint when both
//      apply.

func TestExtractPodContainerNames(t *testing.T) {
	cases := []struct {
		name   string
		detail map[string]interface{}
		want   []string
	}{
		{
			name: "multi-container pod (gitlab-webservice shape)",
			detail: map[string]interface{}{
				"containers": []map[string]interface{}{
					{"name": "certificates"},
					{"name": "configure"},
					{"name": "dependencies"},
					{"name": "webservice"},
					{"name": "gitlab-workhorse"},
				},
			},
			want: []string{"certificates", "configure", "dependencies", "webservice", "gitlab-workhorse"},
		},
		{
			name: "single-container pod",
			detail: map[string]interface{}{
				"containers": []map[string]interface{}{
					{"name": "app"},
				},
			},
			want: []string{"app"},
		},
		{
			name:   "detail missing containers field",
			detail: map[string]interface{}{"name": "x"},
			want:   nil,
		},
		{
			name: "container row missing name",
			detail: map[string]interface{}{
				"containers": []map[string]interface{}{
					{"name": "good"},
					{"image": "no-name-here"},
					{"name": ""},
					{"name": "also-good"},
				},
			},
			// Empty / missing names are filtered out so the
			// auto-selection picks a real container.
			want: []string{"good", "also-good"},
		},
		{
			name:   "nil detail",
			detail: nil,
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPodContainerNames(tc.detail)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("idx %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestFormatPodLogsMergesExtra(t *testing.T) {
	t.Run("extra fields merged into response", func(t *testing.T) {
		raw := "line1\nline2\nline3\n"
		extra := map[string]any{
			"containerSelected":     "webservice",
			"containerAutoSelected": true,
			"availableContainers":   []string{"certificates", "webservice"},
			"hint":                  "auto-selected; can re-call with another container",
		}
		out := formatPodLogs(raw, "", extra)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("response is not JSON: %v\n%s", err, out)
		}
		if decoded["containerSelected"] != "webservice" {
			t.Errorf("containerSelected missing or wrong: %v", decoded["containerSelected"])
		}
		if decoded["containerAutoSelected"] != true {
			t.Errorf("containerAutoSelected flag missing: %v", decoded["containerAutoSelected"])
		}
		if decoded["hint"] == nil {
			t.Errorf("hint missing")
		}
		// Standard log fields still present.
		if _, ok := decoded["logs"]; !ok {
			t.Errorf("logs field missing from response: %v", decoded)
		}
	})

	t.Run("truncation hint wins when both apply", func(t *testing.T) {
		// 60KB of logs → truncated. Pass extra with its own "hint";
		// the truncation-hint should remain because operators care
		// more about "your data is incomplete" than "I picked a
		// container".
		var b strings.Builder
		for i := 0; i < 5000; i++ {
			b.WriteString("a much longer log line so we actually exceed maxLogBytes\n")
		}
		extra := map[string]any{
			"containerSelected": "webservice",
			"hint":              "i should NOT appear because truncation hint wins",
		}
		out := formatPodLogs(b.String(), "", extra)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		// Truncation set its own hint first; merge logic should NOT
		// overwrite it with the caller's hint.
		hint, _ := decoded["hint"].(string)
		if !strings.Contains(hint, "truncated") {
			t.Errorf("truncation hint got overwritten by extra hint: %q", hint)
		}
		// But other caller fields still came through.
		if decoded["containerSelected"] != "webservice" {
			t.Errorf("containerSelected missing despite hint conflict: %v", decoded["containerSelected"])
		}
	})

	t.Run("nil extra is safe (existing callers unaffected)", func(t *testing.T) {
		out := formatPodLogs("line1\n", "", nil)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		if _, ok := decoded["logs"]; !ok {
			t.Errorf("expected logs field with nil extra")
		}
	})
}
