package copilot

import "strings"

// KobiGuidance returns Kobi's surface-agnostic operating guidance: the core
// identity layer plus the Copilot-mode communication contract. It deliberately
// omits the operational appendix that BuildSystemPrompt adds — that appendix
// covers mutating tools (the propose_* whitelist) and the provider tool-calling
// protocol, neither of which applies to a read-only MCP host that drives its
// own LLM.
//
// This is the text the MCP server exposes as a prompt so an external host
// (Claude Code, Cursor) can adopt Kobi's voice and diagnostic approach. The
// Copilot-mode layer already names "MCP server" as one of its surfaces, so the
// content is a designed fit rather than a repurpose.
func KobiGuidance() string {
	return strings.Join([]string{
		kobiIdentityPrompt,
		kobiCopilotPrompt,
	}, "\n\n---\n\n")
}
