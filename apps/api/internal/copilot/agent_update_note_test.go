package copilot

import (
	"encoding/json"
	"strings"
	"testing"
)

// debt-24: Kobi was asked whether the agent was out of date and did not know.
// The rows it reads now say what to do with agentUpdate, and why the version
// may be unknown.
func TestAnnotateClusterList_AgentUpdate(t *testing.T) {
	in := `[
	  {"name":"lab","source":"agent-proxy","status":"connected","agentConnected":true,
	   "agentUpdate":{"before":"1.4.4","latest":"1.4.9","level":"recommended","reasons":["r1"],"upgrade":"helm upgrade …"}},
	  {"name":"prod","source":"in-cluster","status":"connected","agentConnected":true,
	   "agentUpdate":{"version":"1.4.9","latest":"1.4.9","level":"current"}},
	  {"name":"dev","source":"agent-proxy","status":"connected","agentConnected":true,
	   "agentUpdate":{"version":"1.4.9","latest":"1.4.10","level":"available","upgrade":"helm upgrade …"}}
	]`
	var rows []map[string]any
	if err := json.Unmarshal([]byte(AnnotateClusterList(in)), &rows); err != nil {
		t.Fatal(err)
	}
	lab, _ := rows[0]["agentUpdateNote"].(string)
	if !strings.Contains(lab, AgentUpdateNote) || !strings.Contains(lab, AgentVersionUnknownNote) {
		t.Errorf("lab note = %q, want the update note and the unknown-version note", lab)
	}
	if _, ok := rows[1]["agentUpdateNote"]; ok {
		t.Error("a current agent got an update note")
	}
	dev, _ := rows[2]["agentUpdateNote"].(string)
	if dev != AgentUpdateNote {
		t.Errorf("dev note = %q, want the update note alone", dev)
	}
}
