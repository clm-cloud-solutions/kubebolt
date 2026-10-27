package copilot

import (
	"encoding/json"
	"strings"
	"testing"
)

func row(rev string, ready int, image string) map[string]interface{} {
	return map[string]interface{}{"revision": rev, "readyReplicas": int32(ready), "image": image}
}

func TestSummarizeRollout(t *testing.T) {
	for name, tc := range map[string]struct {
		rows     []map[string]interface{}
		serving  []string
		target   string
		summary  string
		noTarget bool
	}{
		// The lab's image-app: 223 does not pull, 222 still serves.
		"stalled rollout": {
			rows:    []map[string]interface{}{row("223", 0, "nginx:1.299-alpine"), row("222", 1, "nginx:1.27-alpine"), row("221", 0, "x")},
			serving: []string{"222"}, target: "222",
			summary: "The rollout to revision 223 has not finished: revision 222 is still serving. rollback_deployment returns to 222.",
		},
		"two bad deploys in a row": {
			rows:    []map[string]interface{}{row("5", 0, "bad2"), row("4", 0, "bad1"), row("3", 2, "good")},
			serving: []string{"3"}, target: "4",
			summary: "toRevision=3",
		},
		"born broken": {
			rows:    []map[string]interface{}{row("1", 0, "nginx:notify-broken-2")},
			serving: []string{}, noTarget: true,
			summary: "Revision 1 is the only one: there is nothing to roll back to.",
		},
		"finished rollout": {
			rows:    []map[string]interface{}{row("2", 2, "v2"), row("1", 0, "v1")},
			serving: []string{"2"}, target: "1",
			summary: "its 0 ready replicas are current, not how it ran",
		},
		"nothing serving": {
			rows:    []map[string]interface{}{row("2", 0, "v2"), row("1", 0, "v1")},
			serving: []string{}, target: "1",
			summary: "No revision is serving",
		},
		"in progress": {
			rows:    []map[string]interface{}{row("2", 1, "v2"), row("1", 1, "v1")},
			serving: []string{"2", "1"}, target: "1",
			summary: "in progress",
		},
	} {
		st := summarizeRollout("deployments", tc.rows)
		if st == nil {
			t.Fatalf("%s: no state", name)
		}
		if strings.Join(st.Serving, ",") != strings.Join(tc.serving, ",") {
			t.Errorf("%s: serving %v, want %v", name, st.Serving, tc.serving)
		}
		if tc.noTarget != (st.RollbackTarget == nil) || (!tc.noTarget && st.RollbackTarget.Revision != tc.target) {
			t.Errorf("%s: target %+v, want %q", name, st.RollbackTarget, tc.target)
		}
		if !strings.Contains(st.Summary, tc.summary) {
			t.Errorf("%s: summary %q lacks %q", name, st.Summary, tc.summary)
		}
	}
}

func TestSummarizeRolloutOnlyForDeployments(t *testing.T) {
	rows := []map[string]interface{}{row("2", 0, "v2"), row("1", 1, "v1")}
	for _, kind := range []string{"statefulsets", "daemonsets", ""} {
		if st := summarizeRollout(kind, rows); st != nil {
			t.Errorf("%s got a rollout state: rollback_deployment is for Deployments", kind)
		}
	}
}

// A recorded history went through JSON: its counts are float64 and its rows
// may come in any order. The state must be the same as live.
func TestSummarizeRolloutFromARecording(t *testing.T) {
	var rec struct {
		History []map[string]interface{} `json:"history"`
	}
	_ = json.Unmarshal([]byte(`{"history":[{"revision":"221","readyReplicas":0},{"revision":"223","readyReplicas":0},{"revision":"222","readyReplicas":1,"image":"nginx:1.27-alpine"}]}`), &rec)
	st := summarizeRollout("deployments", rec.History)
	if st.Current != "223" || st.RollbackTarget.Revision != "222" || !st.RollbackTarget.Serving {
		t.Errorf("from a recording: %+v %+v", st, st.RollbackTarget)
	}
}

func TestWorkloadHistoryCarriesTheRollout(t *testing.T) {
	out := workloadHistoryContent(fakeHistory{row("2", 0, "bad"), row("1", 1, "good")}, "deployments", "lab", "app")
	if !strings.Contains(out, `"rollout":`) || !strings.Contains(out, "rollback_deployment returns to 1") {
		t.Errorf("history without its rollout state: %s", out)
	}
	if strings.Contains(workloadHistoryContent(fakeHistory{row("2", 0, "bad"), row("1", 1, "good")}, "statefulsets", "lab", "app"), `"rollout"`) {
		t.Error("a StatefulSet's history got a rollout state")
	}
}
