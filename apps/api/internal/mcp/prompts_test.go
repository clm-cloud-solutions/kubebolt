package mcp

import (
	"strings"
	"testing"
)

func TestKobiPromptListAndGet(t *testing.T) {
	p := NewKobiPromptProvider()

	list := p.ListPrompts()
	if len(list) != 1 || list[0].Name != kobiGuidancePromptName {
		t.Fatalf("ListPrompts = %+v, want one %q", list, kobiGuidancePromptName)
	}

	res, err := p.GetPrompt(kobiGuidancePromptName, nil)
	if err != nil {
		t.Fatalf("GetPrompt error: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(res.Messages))
	}
	msg := res.Messages[0]
	if msg.Role != "user" {
		t.Errorf("role = %q, want user", msg.Role)
	}
	if msg.Content.Type != "text" || len(msg.Content.Text) < 200 {
		t.Errorf("guidance text looks too short to be the real Kobi guidance: %d bytes", len(msg.Content.Text))
	}
	// The read-only preamble must be present so a host knows mutations aren't
	// available through this server.
	if !strings.Contains(msg.Content.Text, "read-only") {
		t.Errorf("guidance missing read-only preamble")
	}
}
