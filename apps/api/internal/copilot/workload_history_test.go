package copilot

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// fakeHistoryConnector stands in for *cluster.Connector's ReplicaSet lister:
// the objects go through the same builder the connector feeds its lister to.
type fakeHistoryConnector struct {
	replicaSets []*appsv1.ReplicaSet
	gotType     string
}

func (f *fakeHistoryConnector) GetWorkloadHistoryWithChanges(resourceType, namespace, name string) []map[string]interface{} {
	f.gotType = resourceType
	return cluster.DeploymentHistoryWithChanges(f.replicaSets, namespace, name)
}

func ordersAPIReplicaSet(name, revision string, created time.Time, replicas int32, env ...corev1.EnvVar) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "shop", Name: name,
			CreationTimestamp: metav1.NewTime(created),
			Annotations:       map[string]string{"deployment.kubernetes.io/revision": revision},
			OwnerReferences:   []metav1.OwnerReference{{Kind: "Deployment", Name: "orders-api"}},
		},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "app",
				Image: "python:3.12-alpine",
				Env:   append([]corev1.EnvVar{{Name: "PORT", Value: "8080"}}, env...),
			}},
		}}},
		Status: appsv1.ReplicaSetStatus{Replicas: replicas, ReadyReplicas: replicas},
	}
}

// The 2026-09-24 lab incident through the tool: revision 2 says it added
// FAULT_MODE=error, revision 1 says when it was replaced, and the note keeps
// revision 1's 0 / 0 from reading as "it was NotReady".
func TestWorkloadHistoryContent_ShowsChangesSupersededAtAndNote(t *testing.T) {
	rev2At := time.Date(2026, 9, 24, 18, 50, 33, 0, time.UTC)
	f := &fakeHistoryConnector{replicaSets: []*appsv1.ReplicaSet{
		ordersAPIReplicaSet("orders-api-5c7b", "1", rev2At.Add(-5*time.Hour), 0),
		ordersAPIReplicaSet("orders-api-6d8f", "2", rev2At, 2, corev1.EnvVar{Name: "FAULT_MODE", Value: "error"}),
	}}

	content := workloadHistoryContent(f, "deployments", "shop", "orders-api")
	if f.gotType != "deployments" {
		t.Errorf("read type %q", f.gotType)
	}
	var out struct {
		History []map[string]interface{} `json:"history"`
		Note    string                   `json:"note"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, content)
	}
	if len(out.History) != 2 {
		t.Fatalf("history: %s", content)
	}
	rev2, rev1 := out.History[0], out.History[1]
	if got := rev2["changes"]; !reflect.DeepEqual(got, []interface{}{`container app: env FAULT_MODE added = "error"`}) {
		t.Errorf("revision 2 changes = %v", got)
	}
	if rev1["supersededAt"] != rev2["createdAt"] || rev2["createdAt"] == nil {
		t.Errorf("revision 1 supersededAt = %v, want revision 2's createdAt %v", rev1["supersededAt"], rev2["createdAt"])
	}
	if _, ok := rev1["changes"]; ok {
		t.Errorf("the oldest revision has no previous one to differ from: %v", rev1["changes"])
	}
	if !strings.Contains(out.Note, "CURRENT counts") || !strings.Contains(out.Note, "replaced revision shows 0") {
		t.Errorf("note = %q", out.Note)
	}
}

// One revision: nothing was replaced, so no note to explain replaced counts.
func TestWorkloadHistoryContent_SingleRevisionHasNoNote(t *testing.T) {
	f := &fakeHistoryConnector{replicaSets: []*appsv1.ReplicaSet{
		ordersAPIReplicaSet("orders-api-5c7b", "1", time.Now().Add(-time.Hour), 2),
	}}
	content := workloadHistoryContent(f, "deployments", "shop", "orders-api")
	if strings.Contains(content, `"note"`) || strings.Contains(content, `"changes"`) || strings.Contains(content, `"supersededAt"`) {
		t.Errorf("single revision: %s", content)
	}
}

func TestWorkloadHistoryToolDescribesTheNewFields(t *testing.T) {
	for _, td := range ToolDefinitions() {
		if td.Name != "get_workload_history" {
			continue
		}
		for _, want := range []string{
			"`changes`", "`changeCause`", "CURRENT counts",
			// supersededAt is when the rollout away started, not proof the
			// revision stopped serving; and `active` is as current as the counts.
			"`supersededAt` when the rollout to the next revision started (it may still be serving if that rollout stalled)",
			"replicas / readyReplicas / active are CURRENT",
		} {
			if !strings.Contains(td.Description, want) {
				t.Errorf("description lacks %q: %s", want, td.Description)
			}
		}
		return
	}
	t.Fatal("get_workload_history is not a tool")
}

// The note says the same of `active`, which a replaced revision shows false.
func TestWorkloadHistoryNoteCoversActive(t *testing.T) {
	if !strings.Contains(revisionCountsNote, "replicas / readyReplicas / active are each revision's CURRENT counts") {
		t.Errorf("note = %q", revisionCountsNote)
	}
}

// A history larger than the executor's cap used to be cut mid-JSON by the
// generic truncation, and the note — marshalled after "history" — went with
// the cut. The note now comes first and the tool keeps whole rows within its
// own budget, saying how many older revisions it left out.
func TestWorkloadHistoryContent_StaysValidAndKeepsTheNoteWhenLarge(t *testing.T) {
	var sets []*appsv1.ReplicaSet
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 60; i++ {
		var env []corev1.EnvVar
		for j := 0; j < 30; j++ {
			env = append(env, corev1.EnvVar{
				Name:  fmt.Sprintf("SETTING_%02d_%s", j, strings.Repeat("X", 40)),
				Value: fmt.Sprintf("revision %d value %d <%s>", i, j, strings.Repeat("\"q\"", 20)),
			})
		}
		sets = append(sets, ordersAPIReplicaSet(fmt.Sprintf("orders-api-%02d", i), fmt.Sprint(i), start.Add(time.Duration(i)*time.Hour), 0, env...))
	}
	content := workloadHistoryContent(&fakeHistoryConnector{replicaSets: sets}, "deployments", "shop", "orders-api")

	if len(content) >= maxToolResultBytes {
		t.Fatalf("result of %d bytes reaches the executor's %d-byte cut", len(content), maxToolResultBytes)
	}
	var out struct {
		Note    string                   `json:"note"`
		History []map[string]interface{} `json:"history"`
		Omitted int                      `json:"omittedOlderRevisions"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if out.Note == "" || strings.Index(content, `"note"`) > strings.Index(content, `"history"`) {
		t.Errorf("the note must come first: %.200s", content)
	}
	if out.Omitted == 0 || len(out.History)+out.Omitted != 60 {
		t.Errorf("kept %d rows, omitted %d, of 60", len(out.History), out.Omitted)
	}
	if len(out.History) == 0 || out.History[0]["revision"] != "60" {
		t.Errorf("the newest revisions are the ones kept")
	}
}
