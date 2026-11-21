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

// A call that reached nothing does not carry the procedure; the next one
// that reaches the cloud does.
func TestWithConnectorSkill_ARefusedCallDoesNotCarryIt(t *testing.T) {
	call := ToolCall{ID: "t1", Name: "mcp__aws__call", Input: json.RawMessage(`{"service":"ec2","operation":"DescribeInstances"}`)}
	for name, refused := range map[string]ToolResult{
		"tool not allowed": {ToolCallID: "t1", IsError: true, NotSent: true, Content: `Tool not allowed: "mcp__aws__call" is not available to this assistant in this organization. Do not call it again.`},
		"rate limited":     {ToolCallID: "t1", IsError: true, RateLimited: true, Content: "Rate limited: this organization's cloud calls are held to 120 a minute, at most 30 at once."},
	} {
		got := WithConnectorSkill(nil, nil, call, refused)
		if got.Content != refused.Content {
			t.Errorf("%s: the procedure went with a call that reached nothing: %.80s", name, got.Content)
		}
		answer := ToolResult{ToolCallID: "t2", Content: `{"Reservations":[]}`}
		if next := WithConnectorSkill([]Message{{Role: RoleUser, ToolResults: []ToolResult{got}}}, nil, call, answer); !strings.HasPrefix(next.Content, "[KubeBolt] aws-investigation") {
			t.Errorf("%s: the next call that reached AWS did not carry it", name)
		}
	}
}

// How a call ended is read from its own answer, never from the procedure in
// front of it: the procedure names AccessDenied and was the "reason" shown.
func TestCallStatus_ReadsTheAnswerNotTheAttachedProcedure(t *testing.T) {
	call := ToolCall{ID: "t1", Name: "mcp__aws__call", Input: json.RawMessage(`{"service":"ec2","operation":"DescribeInstances"}`)}
	cases := []struct {
		name       string
		answer     ToolResult
		wantStatus string
		wantReason string
	}{
		{"denied by AWS", ToolResult{IsError: true,
			Content: `<external_tool_output connector="aws" tool="call">An error occurred (UnauthorizedOperation) when calling the DescribeInstances operation: You are not authorized to perform this operation. User: arn:aws:sts::000000000000:assumed-role/kobi/x is not authorized to perform: ec2:DescribeInstances</external_tool_output>`},
			ToolResultDenied, "UnauthorizedOperation for ec2:DescribeInstances"},
		{"a provider error that is not a denial", ToolResult{IsError: true, Content: `{"error":"the connection was reset. Try again."}`},
			ToolResultError, "the connection was reset"},
	}
	for _, c := range cases {
		first := WithConnectorSkill(nil, nil, call, c.answer)
		if !strings.HasPrefix(first.Content, "[KubeBolt] aws-investigation") {
			t.Fatalf("%s: the procedure was not attached", c.name)
		}
		if got := WithoutConnectorSkill(first.Content); got != c.answer.Content {
			t.Fatalf("%s: WithoutConnectorSkill = %.80q", c.name, got)
		}
		status, reason := CallStatus(first, ClassifyToolResult(first), false)
		if status != c.wantStatus || reason != c.wantReason {
			t.Errorf("%s: CallStatus = (%q, %q), want (%q, %q)", c.name, status, reason, c.wantStatus, c.wantReason)
		}
	}
}
