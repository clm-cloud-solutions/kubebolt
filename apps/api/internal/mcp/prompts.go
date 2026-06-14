package mcp

import "github.com/kubebolt/kubebolt/apps/api/internal/copilot"

// kobiGuidancePromptName is the single prompt this server exposes.
const kobiGuidancePromptName = "kobi-guidance"

// KobiPromptProvider exposes Kobi's operating guidance as an MCP prompt so a
// host LLM (Claude Code, Cursor) can adopt Kobi's voice and diagnostic
// approach when driving the read-only tools. It is sourced from the same
// embedded prompt layers the in-product Copilot uses (copilot.KobiGuidance),
// so the persona stays in lockstep with the product without duplication.
type KobiPromptProvider struct{}

// NewKobiPromptProvider builds the prompt provider.
func NewKobiPromptProvider() *KobiPromptProvider { return &KobiPromptProvider{} }

// ListPrompts advertises the single Kobi guidance prompt (no arguments).
func (p *KobiPromptProvider) ListPrompts() []Prompt {
	return []Prompt{
		{
			Name: kobiGuidancePromptName,
			Description: "Kobi's operating guidance — adopt this persona and " +
				"diagnostic approach when using the KubeBolt read-only tools to " +
				"investigate a Kubernetes cluster.",
		},
	}
}

// GetPrompt returns the guidance as a single user-role message. A read-only
// preamble is prepended so the host LLM knows mutations are not available
// through this server (the embedded guidance is written for the full Copilot,
// which can also propose actions).
func (p *KobiPromptProvider) GetPrompt(name string, _ map[string]string) (GetPromptResult, error) {
	if name != kobiGuidancePromptName {
		return GetPromptResult{}, ErrUnknownPrompt
	}
	const readOnlyPreamble = "You are operating through KubeBolt's read-only MCP " +
		"server. You can inspect the cluster (overview, resources, YAML, describe, " +
		"logs, events, insights, topology, metrics) but you CANNOT mutate it — there " +
		"are no restart/scale/delete/edit tools here. When a fix requires a change, " +
		"explain it and tell the operator to apply it themselves.\n\n" +
		"Adopt the following guidance:\n\n"
	return GetPromptResult{
		Description: "Kobi's operating guidance (read-only).",
		Messages: []PromptMessage{
			{
				Role:    "user",
				Content: Content{Type: "text", Text: readOnlyPreamble + copilot.KobiGuidance()},
			},
		},
	}, nil
}
