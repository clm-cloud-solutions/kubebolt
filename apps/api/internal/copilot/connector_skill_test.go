package copilot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/skills"
)

func TestWithConnectorSkill_TheFirstAWSCallCarriesItsProcedure(t *testing.T) {
	skill, ok := skills.Get("aws-investigation")
	if !ok {
		t.Fatal("aws-investigation is not in the library")
	}
	call := ToolCall{ID: "t1", Name: "mcp__aws__call", Input: json.RawMessage(`{"service":"elbv2","operation":"DescribeTargetHealth"}`)}
	answer := ToolResult{ToolCallID: "t1", Content: `{"TargetHealthDescriptions":[]}`}

	first := WithConnectorSkill(nil, nil, call, answer)
	if !strings.HasPrefix(first.Content, "[KubeBolt] aws-investigation") || !strings.Contains(first.Content, skill.Body) ||
		!strings.HasSuffix(first.Content, answer.Content) {
		t.Fatalf("first call: %.200s", first.Content)
	}

	// Once per conversation: a later round, or the same round's next call.
	history := []Message{{Role: RoleUser, ToolResults: []ToolResult{first}}}
	if got := WithConnectorSkill(history, nil, call, answer); got.Content != answer.Content {
		t.Errorf("attached twice across rounds: %.120s", got.Content)
	}
	if got := WithConnectorSkill(nil, []ToolResult{first}, call, answer); got.Content != answer.Content {
		t.Errorf("attached twice in one round: %.120s", got.Content)
	}

	// Loaded by the model already, in this round or before.
	loaded := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "s", Name: "load_skill", Input: json.RawMessage(`{"name":"aws-investigation"}`)}}}}
	if got := WithConnectorSkill(loaded, nil, call, answer); got.Content != answer.Content {
		t.Errorf("attached after load_skill: %.120s", got.Content)
	}

	// Another skill loaded does not count; a connector without a procedure
	// and a tool that is not a connector's are left alone.
	other := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "s", Name: "load_skill", Input: json.RawMessage(`{"name":"external-dependencies"}`)}}}}
	if got := WithConnectorSkill(other, nil, call, answer); got.Content == answer.Content {
		t.Error("another skill counted as this one")
	}
	for _, name := range []string{"mcp__azure__monitor", "get_pod_logs"} {
		c := ToolCall{ID: "x", Name: name}
		if got := WithConnectorSkill(nil, nil, c, answer); got.Content != answer.Content {
			t.Errorf("%s got a procedure", name)
		}
	}
}
