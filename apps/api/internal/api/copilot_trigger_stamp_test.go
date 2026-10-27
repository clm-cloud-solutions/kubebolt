package api

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// The request's trigger lands on the user message it came with, not on the
// operator's earlier words nor on a tool-result turn; a typed turn stamps
// nothing, and a trigger the client already set stays.
func TestStampTrigger(t *testing.T) {
	msgs := []copilot.Message{
		{Role: copilot.RoleUser, Content: "Puedes escalar a 0 a oomy-app?"},
		{Role: copilot.RoleAssistant, Content: "Listo."},
		{Role: copilot.RoleUser, Content: "I ran the action `scale_workload`…"},
	}
	stampTrigger(msgs, "action_stalled")
	if msgs[0].Trigger != "" || msgs[2].Trigger != "action_stalled" {
		t.Errorf("stamped %q / %q", msgs[0].Trigger, msgs[2].Trigger)
	}

	typed := []copilot.Message{{Role: copilot.RoleUser, Content: "ok"}}
	stampTrigger(typed, "")
	if typed[0].Trigger != "" {
		t.Errorf("typed turn stamped %q", typed[0].Trigger)
	}

	kept := []copilot.Message{
		{Role: copilot.RoleUser, Trigger: "insight", Content: "Diagnose this insight."},
		{Role: copilot.RoleUser, ToolResults: []copilot.ToolResult{{Content: "x"}}},
	}
	stampTrigger(kept, "panel_inquiry")
	if kept[0].Trigger != "insight" || kept[1].Trigger != "" {
		t.Errorf("got %q / %q", kept[0].Trigger, kept[1].Trigger)
	}
}
