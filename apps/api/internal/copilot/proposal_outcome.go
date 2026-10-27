package copilot

import "encoding/json"

// The UI writes executionStatus into a proposal's tool result when the
// operator executes or dismisses the card. What each status asks of Kobi
// used to be a paragraph in every turn's prompt; it now travels with the
// status itself, where it applies.
var proposalOutcomeNotes = map[string]string{
	"executed": "The operator executed this proposal: the cluster has changed since. Read the current state before answering, and do not propose it again.",
	"failed": "The operator tried to execute this proposal and it failed (executionResult says why). The cluster is likely in its prior state: read executionResult and the current state before deciding — a retry with other parameters, or a different action.",
	"dismissed": "The operator dismissed this proposal. Acknowledge it; do not propose it again unless something new justifies it.",
}

// AnnotateProposalOutcomes adds an executionNote to every proposal result in
// the conversation that carries an executionStatus.
func AnnotateProposalOutcomes(messages []Message) {
	for i := range messages {
		for j := range messages[i].ToolResults {
			tr := &messages[i].ToolResults[j]
			var obj map[string]interface{}
			if json.Unmarshal([]byte(tr.Content), &obj) != nil || obj["kind"] != "action_proposal" {
				continue
			}
			status, _ := obj["executionStatus"].(string)
			note, ok := proposalOutcomeNotes[status]
			if !ok {
				continue
			}
			if _, done := obj["executionNote"]; done {
				continue
			}
			obj["executionNote"] = note
			tr.Content = jsonString(obj)
		}
	}
}
