package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// One Kobi turn through the real router: the model calls a KubeBolt tool and
// an MCP-named tool, then refuses. The session record must say how every call
// ended — a refusal is invisible in Reason, which still reads "done" — and
// where each tool came from and what it gave back.
func TestCopilotChat_SessionRecordCarriesHealthSignals(t *testing.T) {
	prov := newScripted(func(req copilot.ChatRequest) *copilot.ChatResponse {
		if len(lastToolResults(req)) == 0 {
			return &copilot.ChatResponse{StopReason: "tool_use", ToolCalls: []copilot.ToolCall{
				{ID: "t1", Name: "list_resources", Input: json.RawMessage(`{"type":"pods"}`)},
				{ID: "t2", Name: "mcp__aws__cloudwatch_query", Input: json.RawMessage(`{}`)},
			}}
		}
		return &copilot.ChatResponse{Text: "I can't help with that.", StopReason: "refusal"}
	})
	w := newWiringWithAgents(t, prov.name, newManagerOn(t, map[string]string{}), nil)

	body, _ := json.Marshal(map[string]any{"messages": []copilot.Message{{Role: copilot.RoleUser, Content: "why is orders-db down?"}}})
	rec := w.do(t, w.admin, http.MethodPost, "/api/v1/copilot/chat", string(body))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}

	got := w.sessions.last(t)
	if got.Reason != "done" {
		t.Errorf("Reason = %q, want done", got.Reason)
	}
	if got.StopReasons[copilot.StopToolUse] != 1 || got.StopReasons[copilot.StopRefusal] != 1 || len(got.StopReasons) != 2 {
		t.Errorf("StopReasons = %v, want one tool_use and one refusal", got.StopReasons)
	}
	if len(got.ProviderErrors) != 0 || got.FallbackTried {
		t.Errorf("no call failed, got ProviderErrors=%v FallbackTried=%v", got.ProviderErrors, got.FallbackTried)
	}

	kb := got.Tools["list_resources"]
	if kb.Source != copilot.ToolSourceKubeBolt || sum(kb.Results) != 1 || kb.Results[copilot.ToolResultOK] != 0 {
		// No cluster is connected, so the call cannot have answered.
		t.Errorf("list_resources = %+v, want one non-ok result from kubebolt", kb)
	}
	mcp := got.Tools["mcp__aws__cloudwatch_query"]
	if mcp.Source != "mcp:aws" || mcp.Results[copilot.ToolResultError] != 1 {
		t.Errorf("mcp tool = %+v, want source mcp:aws and one error", mcp)
	}
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
