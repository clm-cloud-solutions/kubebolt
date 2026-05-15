package api

import (
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// TestWithSessionContextPrefix_DoesNotMutateInput pins the non-mutation
// guarantee. Mutation here causes two visible bugs at once:
//
//  1. The chat UI renders the operator's user bubble with the cluster +
//     current_view + Now block on top of their question, because the
//     `done` SSE event echoes the canonical messages slice back to the
//     frontend (which then replaces its transcript with it).
//  2. On follow-up turns, the frontend re-sends the already-prefixed
//     content; without the non-mutation guarantee, the helper prepends
//     a second prefix, then a third, etc. (in production we observed 4
//     nested Session Context blocks after 4 turns).
//
// If a future refactor switches back to in-place mutation, this test
// catches it before either bug ships again.
func TestWithSessionContextPrefix_DoesNotMutateInput(t *testing.T) {
	original := []copilot.Message{
		{Role: copilot.RoleUser, Content: "what happened with my pod?"},
		{Role: copilot.RoleAssistant, Content: "let me check"},
	}
	// Snapshot the canonical content for comparison after the call.
	originalContent0 := original[0].Content
	originalContent1 := original[1].Content

	out := withSessionContextPrefix(original, "# Session context\ncluster: x")

	// The returned slice must carry the prefix on the first user message.
	if !strings.HasPrefix(out[0].Content, "# Session context\ncluster: x\n\n") {
		t.Errorf("returned slice missing prefix on first user message; got %q", out[0].Content)
	}
	if !strings.HasSuffix(out[0].Content, "what happened with my pod?") {
		t.Errorf("returned slice lost original content on first user message; got %q", out[0].Content)
	}
	// The original slice must be byte-for-byte unchanged.
	if original[0].Content != originalContent0 {
		t.Errorf("input first message was mutated; before=%q after=%q", originalContent0, original[0].Content)
	}
	if original[1].Content != originalContent1 {
		t.Errorf("input second message was mutated; before=%q after=%q", originalContent1, original[1].Content)
	}
	// Prefix must NOT touch non-user messages.
	if out[1].Content != originalContent1 {
		t.Errorf("returned slice modified an assistant message; got %q", out[1].Content)
	}
}

// TestWithSessionContextPrefix_PrefixesOnlyFirstUser checks that when a
// conversation has multiple user turns, only the FIRST one carries the
// session-context prefix on the LLM-facing slice. The model only needs
// the cluster / view / Now anchor once per request.
func TestWithSessionContextPrefix_PrefixesOnlyFirstUser(t *testing.T) {
	in := []copilot.Message{
		{Role: copilot.RoleUser, Content: "first question"},
		{Role: copilot.RoleAssistant, Content: "first answer"},
		{Role: copilot.RoleUser, Content: "follow-up"},
	}
	out := withSessionContextPrefix(in, "# Session context\nx")

	if !strings.Contains(out[0].Content, "# Session context") {
		t.Errorf("first user message should carry the prefix")
	}
	if strings.Contains(out[2].Content, "# Session context") {
		t.Errorf("follow-up user message should NOT carry the prefix; got %q", out[2].Content)
	}
}

// TestWithSessionContextPrefix_EmptyInputs covers the no-op paths so a
// future change can't silently start emitting empty Session Context
// headers ("# Session context\n\n<question>") which would confuse Kobi.
func TestWithSessionContextPrefix_EmptyInputs(t *testing.T) {
	t.Run("empty sessionCtx returns input unchanged", func(t *testing.T) {
		in := []copilot.Message{{Role: copilot.RoleUser, Content: "q"}}
		out := withSessionContextPrefix(in, "")
		if &in[0] != &out[0] {
			// Pointer equality on the underlying array element — the
			// helper returns the input slice directly when there is no
			// work to do (avoids a needless allocation per round).
			t.Errorf("empty sessionCtx should return input slice as-is")
		}
	})
	t.Run("empty messages returns input unchanged", func(t *testing.T) {
		out := withSessionContextPrefix(nil, "# Session context\nx")
		if out != nil {
			t.Errorf("nil messages should pass through")
		}
	})
}
