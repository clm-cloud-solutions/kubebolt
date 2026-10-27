package copilot

import (
	"encoding/json"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// AgentOfflineError is what a read that needed the cluster itself returns when
// the cluster's agent is not connected. client-go said `Get
// "https://<uid>.agent.local/…": channel: no agent connected for cluster:
// <uid>`, and in the AKS lab (2026-10-10 01:58) Kobi read that, next to a
// list_clusters that still said status "connected", as the cluster being down.
// It was the agent's channel: the cache kept answering.
const AgentOfflineError = "This cluster's agent is disconnected right now, so nothing can be read live from the cluster: " +
	"logs, YAML, describe, and whatever KubeBolt does not keep in cache. What it keeps in cache still answers (lists, " +
	"resource details, workload pods, events). This is the agent's channel, not the cluster being down. Do not retry " +
	"this read until it reconnects; tell the operator what you could not read and why."

// AgentOfflineNote marks, in list_clusters, an agent-proxy cluster whose
// connector is up (status "connected") while its agent is not.
const AgentOfflineNote = "Its agent is disconnected right now: cached views answer, live reads (logs, YAML, describe) " +
	"fail until it reconnects. The cluster itself is not known to be down."

// explainAgentOffline replaces the transport's error with AgentOfflineError.
// Any other result is returned as it is.
func explainAgentOffline(res ToolResult) ToolResult {
	if !res.IsError || !strings.Contains(res.Content, "no agent connected for cluster") {
		return res
	}
	res.Content = jsonString(map[string]any{"error": AgentOfflineError, "agentConnected": false})
	return res
}

// clusterListForKobi is the caller's cluster list as list_clusters returns it:
// GET /clusters' rows through AnnotateClusterList.
func clusterListForKobi(clusters []cluster.ClusterInfo) string {
	raw, err := json.Marshal(clusters)
	if err != nil {
		return jsonString(clusters)
	}
	return AnnotateClusterList(string(raw))
}

// AnnotateClusterList adds AgentOfflineNote to each agent-proxy row that
// reports "connected" while its agent is gone. The two fields disagree by
// design (status is the connector, agentConnected the channel); the note says
// which one a read depends on. A list with nothing to note comes back as it is.
func AnnotateClusterList(text string) string {
	var rows []map[string]any
	if json.Unmarshal([]byte(text), &rows) != nil {
		return text
	}
	noted := false
	for _, r := range rows {
		live, _ := r["agentConnected"].(bool)
		if r["source"] == "agent-proxy" && r["mode"] != "metrics-only" && r["status"] == "connected" && !live {
			r["agentNote"] = AgentOfflineNote
			noted = true
		}
	}
	if !noted {
		return text
	}
	return jsonString(rows)
}

// ExplainAgentOffline is explainAgentOffline for the eval replay, whose
// recordings carry the transport's error as it was.
func ExplainAgentOffline(res ToolResult) ToolResult { return explainAgentOffline(res) }
