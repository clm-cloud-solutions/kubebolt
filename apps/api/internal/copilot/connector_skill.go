package copilot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/skills"
)

// A connector's procedure travels with its first call. aws-investigation
// says where each AWS symptom's cause lives and how to read CloudTrail, and
// its index entry asks for it before the first mcp__aws__ call; Kobi made
// five AWS calls without it and never reached the network ACL it names
// (alb-targets-unhealthy, 2026-10-22). What the model has to remember to
// load, it sometimes does not, and more text in the prompt would not change
// that. So the first answer from a connector in a conversation carries the
// connector's procedure, once.

// connectorSkills maps a connector to the procedure for its tools.
var connectorSkills = map[string]string{"aws": "aws-investigation"}

const connectorSkillHeader = "[KubeBolt] %s, the procedure for these tools, attached to this conversation's first call to them (it is what load_skill %s returns). The call's answer follows it."

// WithConnectorSkill puts the procedure of call's connector in front of its
// result, unless the conversation already loaded it or received it: history
// is the conversation so far, the calls of this round included, and round
// the results this round already holds.
func WithConnectorSkill(history []Message, round []ToolResult, call ToolCall, result ToolResult) ToolResult {
	id, ok := MCPConnectorID(call.Name)
	if !ok {
		return result
	}
	name, ok := connectorSkills[id]
	if !ok {
		return result
	}
	skill, ok := skills.Get(name)
	if !ok {
		return result
	}
	header := fmt.Sprintf(connectorSkillHeader, name, name)
	if skillDelivered(history, round, name, header) {
		return result
	}
	result.Content = header + "\n\n" + skill.Body + "\n\n" + result.Content
	return result
}

// skillDelivered reports whether the conversation called load_skill for name
// or holds a result that carried it.
func skillDelivered(history []Message, round []ToolResult, name, header string) bool {
	carried := func(results []ToolResult) bool {
		for _, r := range results {
			if strings.HasPrefix(r.Content, header) {
				return true
			}
		}
		return false
	}
	for _, m := range history {
		for _, c := range m.ToolCalls {
			if c.Name != "load_skill" {
				continue
			}
			var in struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(c.Input, &in) == nil && in.Name == name {
				return true
			}
		}
		if carried(m.ToolResults) {
			return true
		}
	}
	return carried(round)
}
