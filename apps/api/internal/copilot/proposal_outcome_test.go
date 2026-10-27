package copilot

import (
	"strings"
	"testing"
)

func TestAnnotateProposalOutcomes(t *testing.T) {
	msgs := []Message{{Role: RoleUser, ToolResults: []ToolResult{
		{ToolCallID: "a", Content: `{"kind":"action_proposal","action":"restart","executionStatus":"failed","executionResult":"forbidden"}`},
		{ToolCallID: "b", Content: `{"kind":"action_proposal","action":"scale"}`},
		{ToolCallID: "c", Content: `{"pods":[]}`},
	}}}
	AnnotateProposalOutcomes(msgs)
	if !strings.Contains(msgs[0].ToolResults[0].Content, `"executionNote":"The operator tried to execute this proposal and it failed`) {
		t.Errorf("failed proposal: %s", msgs[0].ToolResults[0].Content)
	}
	if strings.Contains(msgs[0].ToolResults[1].Content, "executionNote") || msgs[0].ToolResults[2].Content != `{"pods":[]}` {
		t.Error("annotated a pending proposal or another tool's result")
	}
	once := msgs[0].ToolResults[0].Content
	AnnotateProposalOutcomes(msgs)
	if msgs[0].ToolResults[0].Content != once {
		t.Error("annotated twice")
	}
}
