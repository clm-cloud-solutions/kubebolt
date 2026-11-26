package copilot

import (
	"encoding/json"
	"fmt"
	"slices"
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
// connector's procedure, once. A built-in tool whose answer needs its own
// procedure to be presented well (create_plan → kobi-plans) gets the same:
// the procedure stays out of the tool definitions every turn pays for.

const connectorSkillHeader = "[KubeBolt] %s, the procedure for these tools, attached to this conversation's first call to them (it is what load_skill %s returns). The call's answer follows it."

// WithConnectorSkill puts the procedure of call's connector in front of its
// result, unless the conversation already loaded it or received it: history
// is the conversation so far, the calls of this round included, and round
// the results this round already holds. A call that reached nothing (refused
// by KubeBolt, held back by the rate limit) is not an answer from the
// connector and does not carry it: found live, about 9.7 KB went with a
// refusal of a tool the organization could not use at all. The procedure
// comes with the first call that does reach the cloud.
func WithConnectorSkill(history []Message, round []ToolResult, call ToolCall, result ToolResult) ToolResult {
	if result.NotSent || result.RateLimited {
		return result
	}
	name, ok := attachedProcedure(call.Name)
	if !ok {
		return result
	}
	// A built-in tool's procedure explains what the tool made; an error made
	// nothing. A connector's error is still the cloud answering, and the
	// procedure is how to read it.
	if result.IsError && !IsMCPToolName(call.Name) {
		return result
	}
	names := []string{name}
	var in struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(call.Input, &in) == nil {
		if extra, ok := skills.ForToolKind(call.Name, in.Kind); ok {
			names = append(names, extra)
		}
	}
	var prefix strings.Builder
	for _, n := range names {
		skill, ok := skills.Get(n)
		if !ok || skillDelivered(history, round, n) {
			continue
		}
		prefix.WriteString(procedurePrefix(skill))
	}
	result.Content = prefix.String() + result.Content
	return result
}

// procedurePrefix is how an attached procedure rides in front of an answer.
func procedurePrefix(s skills.Skill) string {
	return fmt.Sprintf(connectorSkillHeader, s.Name, s.Name) + "\n\n" + s.Body + "\n\n"
}

// attachedTo splits a result into the procedures put in front of it, in
// order (a consolidation plan carries two), and what the call answered.
func attachedTo(content string) ([]string, string) {
	var names []string
	for {
		rest, ok := strings.CutPrefix(content, "[KubeBolt] ")
		if !ok {
			return names, content
		}
		name, _, ok := strings.Cut(rest, ",")
		if !ok || !skills.IsAttachedProcedure(name) {
			return names, content
		}
		skill, ok := skills.Get(name)
		if !ok {
			return names, content
		}
		// The catalogue's own name, not the one read from the content: what
		// goes back into a string is never text the tool's answer supplied.
		answer, ok := strings.CutPrefix(content, procedurePrefix(skill))
		if !ok {
			return names, content
		}
		names = append(names, skill.Name)
		content = answer
	}
}

// attachedProcedure is the procedure that travels with a call: its
// connector's, or a built-in tool's (create_plan → kobi-plans, whose
// answer is only read well with it).
func attachedProcedure(tool string) (string, bool) {
	if id, ok := MCPConnectorID(tool); ok {
		return skills.ForConnector(id)
	}
	return skills.ForTool(tool)
}

// WithoutConnectorSkill is a result's content without the procedure
// WithConnectorSkill put in front of it: what the call itself answered. How a
// call ended is read from that, never from the procedure, whose own words
// (a "`not found`" an ECR tag gives, "an AccessDenied names the action") made a
// failed first AWS call read as something it was not and named the procedure
// as the reason. The header names the procedure; only an attached procedure
// (skills.IsAttachedProcedure) is ever put there.
func WithoutConnectorSkill(content string) string {
	_, answer := attachedTo(content)
	return answer
}

// skillDelivered reports whether the conversation called load_skill for name
// or holds a result that carried it.
func skillDelivered(history []Message, round []ToolResult, name string) bool {
	carried := func(results []ToolResult) bool {
		for _, r := range results {
			if names, _ := attachedTo(r.Content); slices.Contains(names, name) {
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
