package copilot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// The read failed because the agent's channel was down, which client-go
// reports as an internal URL and a UUID. Kobi must read what that means.
func TestExplainAgentOffline(t *testing.T) {
	failed := ToolResult{IsError: true, Content: `{"containerSelected":"app","error":"failed to get logs: Get \"https://d530.agent.local/api/v1/namespaces/shop/pods/x/log\": channel: no agent connected for cluster: d530"}`}
	got := explainAgentOffline(failed)
	if !got.IsError || strings.Contains(got.Content, "agent.local") || !strings.Contains(got.Content, "disconnected right now") {
		t.Errorf("not explained: %+v", got)
	}
	for _, keep := range []ToolResult{
		{IsError: true, Content: `{"error":"pods \"x\" not found"}`},
		{IsError: false, Content: `{"logs":"channel: no agent connected for cluster: a"}`},
	} {
		if explainAgentOffline(keep) != keep {
			t.Errorf("rewrote %+v", keep)
		}
	}
}

// status "connected" with agentConnected false: the connector is up, the
// channel is not. The row says which one a read depends on; the others are
// GET /clusters' rows unchanged.
func TestClusterListForKobi_NotesAnOfflineAgent(t *testing.T) {
	clusters := []cluster.ClusterInfo{
		{Context: "agent:a", Source: "agent-proxy", Status: "connected", AgentConnected: false, DisplayName: "kubeshop-front"},
		{Context: "agent:b", Source: "agent-proxy", Status: "connected", AgentConnected: true, DisplayName: "kubeshop-back"},
		{Context: "agent:c", Source: "agent-proxy", Status: "disconnected", AgentConnected: false},
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(clusterListForKobi(clusters)), &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["agentNote"] != AgentOfflineNote {
		t.Errorf("offline agent not noted: %v", rows[0])
	}
	for _, r := range rows[1:] {
		if _, ok := r["agentNote"]; ok {
			t.Errorf("noted a row that needs none: %v", r)
		}
	}
	plain, _ := json.Marshal(clusters[1:])
	if clusterListForKobi(clusters[1:]) != string(plain) {
		t.Error("a list with nothing to note changed shape")
	}
}
