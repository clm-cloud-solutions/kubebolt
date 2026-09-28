package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// Kobi could already tell the operator "switch to that cluster and I'll query
// it directly" — and then could not do it, and the question was lost, because
// switching clears the conversation (one conversation belongs to one cluster).
// offer_cluster_switch closes that loop.

func infos() []cluster.ClusterInfo {
	return []cluster.ClusterInfo{
		{Name: "kind-kubebolt-lab", Context: "kind-kubebolt-lab", Status: "connected", Active: true},
		{Name: "kind-kubebolt-dev", Context: "agent:uid-dev", DisplayName: "kind-kubebolt-dev (via agent)",
			ClusterID: "uid-dev", Status: "connected"},
		{Name: "prod-down", Context: "agent:uid-down", DisplayName: "prod", ClusterID: "uid-down",
			Status: "disconnected", Error: "no agent connected yet"},
	}
}

// Models name a cluster however the operator did — the display name, the
// context, or the uid from a fleet row. A near-miss on any of those would be a
// dead end for a question the tool exists to rescue.
func TestSwitchTarget_MatchesEveryNameTheModelMightUse(t *testing.T) {
	for _, want := range []string{
		"kind-kubebolt-dev (via agent)", "kind-kubebolt-dev", "agent:uid-dev", "uid-dev",
		"KIND-KUBEBOLT-DEV (VIA AGENT)", "  uid-dev  ",
	} {
		got, ok := findSwitchTarget(infos(), want)
		if !ok {
			t.Errorf("%q did not match any cluster", want)
			continue
		}
		if got.ClusterID != "uid-dev" {
			t.Errorf("%q matched %q", want, got.Context)
		}
	}
	if _, ok := findSwitchTarget(infos(), "does-not-exist"); ok {
		t.Error("matched a cluster that is not there")
	}
}

func TestSwitchLabel_PrefersWhatAHumanCallsIt(t *testing.T) {
	if got := switchLabel(cluster.ClusterInfo{Name: "n", Context: "c", DisplayName: "friendly"}); got != "friendly" {
		t.Errorf("label = %q", got)
	}
	if got := switchLabel(cluster.ClusterInfo{Context: "c"}); got != "c" {
		t.Errorf("label = %q, want the context as the last resort", got)
	}
}

func offerSwitch(t *testing.T, clusters []cluster.ClusterInfo, in string) (map[string]any, bool) {
	t.Helper()
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(in), &args); err != nil {
		t.Fatal(err)
	}
	content, failed := buildSwitchOffer(clusters, args)
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, content)
	}
	return payload, failed
}

func TestOfferSwitch_ProposesWhenTheTargetIsConnected(t *testing.T) {
	prop, failed := offerSwitch(t, infos(), `{"cluster":"kind-kubebolt-dev (via agent)","question":"Cuales son los dos criticos?","established":"la vista de flota reporta 2 criticos"}`)
	if failed {
		t.Fatalf("refused a connected cluster: %v", prop)
	}
	if prop["kind"] != "action_proposal" || prop["action"] != "switch_cluster" {
		t.Fatalf("not a switch proposal: %v", prop)
	}
	params, _ := prop["params"].(map[string]any)
	// The CONTEXT is what the switch endpoint takes, not the display name.
	if params["cluster"] != "agent:uid-dev" {
		t.Errorf("cluster param = %v, want the context", params["cluster"])
	}
	// The question travels verbatim: it is what the new conversation opens with.
	if params["question"] != "Cuales son los dos criticos?" {
		t.Errorf("question = %v", params["question"])
	}
	if params["established"] != "la vista de flota reporta 2 criticos" {
		t.Errorf("established = %v", params["established"])
	}
	if prop["reversible"] != true || prop["risk"] != "low" {
		t.Errorf("a view change is low risk and reversible: %v / %v", prop["risk"], prop["reversible"])
	}
}

// The decision that matters: validate BEFORE offering. The card is one click —
// it switches and asks immediately — so offering a switch to a cluster that
// cannot answer lands the operator on the waiting-for-agent screen with their
// question in mid-air.
func TestOfferSwitch_RefusesAnUnreachableTargetAndSaysWhy(t *testing.T) {
	payload, failed := offerSwitch(t, infos(), `{"cluster":"prod","question":"que pasa ahi?"}`)
	if !failed {
		t.Fatal("offered a switch to a disconnected cluster")
	}
	if payload["kind"] == "action_proposal" {
		t.Error("emitted a proposal card for an unreachable cluster")
	}
	if payload["offerable"] != false {
		t.Errorf("offerable = %v, want an explicit false", payload["offerable"])
	}
	// The operator needs the reason, not just a refusal.
	for _, cue := range []string{"disconnected", "no agent connected yet"} {
		if !strings.Contains(fmt.Sprint(payload), cue) {
			t.Errorf("the refusal does not carry %q: %v", cue, payload)
		}
	}
	if !strings.Contains(fmt.Sprint(payload), "do not offer the switch") {
		t.Error("does not tell the model what to do instead")
	}
}

func TestOfferSwitch_UnknownClusterListsWhatThereIs(t *testing.T) {
	payload, failed := offerSwitch(t, infos(), `{"cluster":"typo-cluster","question":"q"}`)
	if !failed {
		t.Fatal("accepted a cluster that does not exist")
	}
	// A near-miss should be one correction away, not a dead end.
	for _, cue := range []string{"kind-kubebolt-lab", "prod"} {
		if !strings.Contains(fmt.Sprint(payload), cue) {
			t.Errorf("the error does not list %q as an available cluster", cue)
		}
	}
}

func TestOfferSwitch_RequiresBothClusterAndQuestion(t *testing.T) {
	for _, in := range []string{`{}`, `{"cluster":"kind-kubebolt-lab"}`, `{"question":"q"}`} {
		_, failed := offerSwitch(t, infos(), in)
		if !failed {
			t.Errorf("%s was accepted; without the question there is nothing to re-ask", in)
		}
	}
}

// It must NOT be withheld when actions are disabled: it mutates nothing, and
// the read-only operator is the one who most needs it — they cannot do
// anything else from the chat.
func TestOfferSwitch_SurvivesActionsBeingDisabled(t *testing.T) {
	var found bool
	for _, d := range GovernedToolDefinitions(false, false) {
		if d.Name == "offer_cluster_switch" {
			found = true
		}
	}
	if !found {
		t.Fatal("withheld with the mutation tools; it changes the view, not the cluster")
	}
	if strings.HasPrefix("offer_cluster_switch", "propose_") {
		t.Error("the propose_ prefix would have it withheld")
	}
}

type stubClusterList struct{ list []cluster.ClusterInfo }

func (s stubClusterList) CallerClusters(context.Context) ([]cluster.ClusterInfo, bool) {
	return s.list, true
}

// list_clusters is how an MCP client with no cluster selected finds out which
// ones exist. It answered "enable the agent-proxy" to that question, because
// the connector gate sent it away before it could read the list.
func TestListClusters_AnswersWithoutAConnector(t *testing.T) {
	res := NewExecutor(nil).WithClusterList(stubClusterList{list: infos()}).ExecuteCtx(context.Background(),
		ToolCall{ID: "c1", Name: "list_clusters", Input: json.RawMessage(`{}`)})
	if res.IsError || strings.Contains(res.Content, "needsProxy") {
		t.Fatalf("hit the connector gate: %s", res.Content)
	}
	if !strings.Contains(res.Content, "kind-kubebolt-dev") {
		t.Errorf("did not list the clusters: %s", res.Content)
	}
}
