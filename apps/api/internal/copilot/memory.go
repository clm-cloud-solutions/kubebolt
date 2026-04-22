package copilot

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
