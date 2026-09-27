package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// Kobi was mono-cluster: list_clusters gives names and connectivity,
// get_insights answers only about the one selected. From Home or Fleet —
// where the operator is looking at everything — "which cluster is worst?" had
// no answer. Production carries 8 clusters across 13 orgs.

type stubFleet struct {
	recs  []insights.InsightRecord
	names map[string]string
	err   error
}

func (s *stubFleet) ActiveInsights(context.Context) ([]insights.InsightRecord, error) {
	return s.recs, s.err
}
func (s *stubFleet) ClusterName(_ context.Context, id string) string { return s.names[id] }

func fleetRec(clusterID, severity string) insights.InsightRecord {
	return insights.InsightRecord{ClusterID: clusterID, Severity: severity, Status: "active"}
}

func callFleet(t *testing.T, e *Executor) (ToolResult, map[string]any) {
	t.Helper()
	res := e.ExecuteCtx(context.Background(), ToolCall{ID: "c1", Name: "get_fleet_summary", Input: json.RawMessage(`{}`)})
	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, res.Content)
	}
	return res, payload
}

func TestFleet_RanksWorstFirstAndNamesTheClusters(t *testing.T) {
	src := &stubFleet{
		names: map[string]string{"uid-a": "cluster-processing", "uid-b": "cluster-orchestration"},
		recs: []insights.InsightRecord{
			fleetRec("uid-b", "critical"),
			fleetRec("uid-a", "warning"), fleetRec("uid-a", "warning"), fleetRec("uid-a", "info"),
			fleetRec("uid-c", "warning"),
		},
	}
	res, payload := callFleet(t, NewExecutor(nil).WithFleet(src))
	if res.IsError {
		t.Fatal(res.Content)
	}

	// One critical beats two warnings: severity leads, not volume.
	if payload["worstCluster"] != "cluster-orchestration" {
		t.Errorf("worstCluster = %v, want the one with the critical", payload["worstCluster"])
	}
	rows, _ := payload["clusters"].([]any)
	if len(rows) != 3 {
		t.Fatalf("clusters = %d, want 3", len(rows))
	}
	first, _ := rows[0].(map[string]any)
	if first["cluster"] != "cluster-orchestration" || first["critical"] != float64(1) {
		t.Errorf("first row = %v", first)
	}
	// A cluster with no display name keeps its uid rather than vanishing.
	var sawRaw bool
	for _, r := range rows {
		if m, _ := r.(map[string]any); m["cluster"] == "uid-c" {
			sawRaw = true
		}
	}
	if !sawRaw {
		t.Error("an unnamed cluster dropped out of the fleet")
	}
	totals, _ := payload["totals"].(map[string]any)
	if totals["clusters"] != float64(3) || totals["warning"] != float64(3) {
		t.Errorf("totals = %v", totals)
	}
}

// The named cluster keeps its uid alongside, so a follow-up can address it.
func TestFleet_NamedRowsStillCarryTheID(t *testing.T) {
	src := &stubFleet{names: map[string]string{"uid-a": "prod"}, recs: []insights.InsightRecord{fleetRec("uid-a", "critical")}}
	_, payload := callFleet(t, NewExecutor(nil).WithFleet(src))
	rows, _ := payload["clusters"].([]any)
	first, _ := rows[0].(map[string]any)
	if first["clusterId"] != "uid-a" {
		t.Errorf("clusterId = %v, want the uid kept next to the name", first["clusterId"])
	}
}

// Map iteration is random; a "worst cluster" that moves between identical
// calls is not an answer.
func TestFleet_OrderIsDeterministic(t *testing.T) {
	var recs []insights.InsightRecord
	for _, id := range []string{"uid-a", "uid-b", "uid-c", "uid-d"} {
		recs = append(recs, fleetRec(id, "warning"))
	}
	var first string
	for i := 0; i < 25; i++ {
		_, payload := callFleet(t, NewExecutor(nil).WithFleet(&stubFleet{recs: recs}))
		got := fmt.Sprint(payload["worstCluster"])
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("worstCluster flipped: %q vs %q", first, got)
		}
	}
}

// The case it exists for: a cluster nobody can reach is exactly the one you
// want counted, so this reads the persisted store and needs no connector.
func TestFleet_AnswersWithoutAConnector(t *testing.T) {
	src := &stubFleet{recs: []insights.InsightRecord{fleetRec("uid-a", "critical")}}
	res, _ := callFleet(t, NewExecutor(nil).WithFleet(src))
	if res.IsError {
		t.Fatalf("failed with no connector: %s", res.Content)
	}
	if strings.Contains(res.Content, "needsProxy") {
		t.Error("hit the connector gate; the fleet view is read from the store")
	}
}

func TestFleet_UnavailableIsNotAHealthyFleet(t *testing.T) {
	res := NewExecutor(nil).ExecuteCtx(context.Background(),
		ToolCall{ID: "c1", Name: "get_fleet_summary", Input: json.RawMessage(`{}`)})
	if !res.IsError {
		t.Error("reported success with no source")
	}
	if strings.Contains(res.Content, `"clusters"`) {
		t.Error("returned an empty fleet shape, which reads as 'everything is fine'")
	}
	if !strings.Contains(strings.ToLower(res.Content), "not a healthy fleet") {
		t.Errorf("does not distinguish absence from health: %s", res.Content)
	}
}

func TestFleet_EmptyFleetIsNotAnError(t *testing.T) {
	res, payload := callFleet(t, NewExecutor(nil).WithFleet(&stubFleet{}))
	if res.IsError {
		t.Fatalf("an org with nothing wrong is not an error: %s", res.Content)
	}
	totals, _ := payload["totals"].(map[string]any)
	if totals["clusters"] != float64(0) {
		t.Errorf("totals = %v", totals)
	}
	if _, ok := payload["worstCluster"]; ok {
		t.Error("named a worst cluster when there are none")
	}
}

func TestFleet_ReaderErrorSurfaces(t *testing.T) {
	src := &stubFleet{err: fmt.Errorf("bolt is closed")}
	res, _ := callFleet(t, NewExecutor(nil).WithFleet(src))
	if !res.IsError || !strings.Contains(res.Content, "bolt is closed") {
		t.Errorf("swallowed the cause: %s", res.Content)
	}
}

func TestFleet_IsPublishedAndReadOnly(t *testing.T) {
	var def *ToolDefinition
	for _, d := range GovernedToolDefinitions(false, false) {
		if d.Name == "get_fleet_summary" {
			dd := d
			def = &dd
		}
	}
	if def == nil {
		t.Fatal("get_fleet_summary is missing from the read-only catalogue")
	}
	low := strings.ToLower(def.Description)
	for _, cue := range []string{"per cluster", "worst", "down"} {
		if !strings.Contains(low, cue) {
			t.Errorf("the description does not orient the model: missing %q", cue)
		}
	}
}
