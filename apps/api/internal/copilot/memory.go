package copilot

import "encoding/json"

// ApproxTokens estimates token count from a message slice using a rough
// 4-chars-per-token heuristic. Good enough for budget thresholds; the actual
// provider-reported Usage is authoritative for billing.
func ApproxTokens(msgs []Message) int {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Name) + len(tc.Input)
		}
		for _, tr := range m.ToolResults {
			chars += len(tr.Content)
		}
	}
	return chars / 4
}

// ApproxSystemToolsTokens estimates the token footprint of the static
// prompt surface (system prompt + tool definitions) that every provider
// call carries alongside the messages. ApproxTokens(messages) ignores
// these, so the auto-compact check combines both to match what the
// provider actually bills.
func ApproxSystemToolsTokens(systemPrompt string, tools []ToolDefinition) int {
	chars := len(systemPrompt)
	for _, t := range tools {
		chars += len(t.Name) + len(t.Description)
		if schema, err := json.Marshal(t.InputSchema); err == nil {
			chars += len(schema)
		}
	}
	return chars / 4
}
