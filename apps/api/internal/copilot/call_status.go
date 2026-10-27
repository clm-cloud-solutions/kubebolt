package copilot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Display statuses the chat tells apart beyond ClassifyToolResult's classes,
// which the metrics keep as they are.
const (
	// ToolResultAgentOffline: the cluster's agent is not connected, so no live
	// read can be made until it reconnects. Nobody's error in the call.
	ToolResultAgentOffline = "agent_offline"
	// ToolResultUnsupported: the tool does not cover the kind it was asked
	// for. Not a failure of anything.
	ToolResultUnsupported = "unsupported"
)

// reasonMax bounds the one line a card shows for a call that did not read.
const reasonMax = 140

// CallStatus is how the chat shows a finished call, with a short reason for
// one that did not read: class is ClassifyToolResult's, refined by the cases
// only the chat tells apart — a call KubeBolt never sent, an agent that is
// not connected, a kind the tool does not cover. The reason is the error's
// first sentence, never what the result tells the model to do.
func CallStatus(r ToolResult, class string, budgetRefused bool) (status, reason string) {
	switch {
	case budgetRefused:
		return ToolResultNotSent, fmt.Sprintf("this question already used its %d cloud calls", MaxMCPCallsPerTurn)
	case r.NotSent:
		return ToolResultNotSent, errorReason(r.Content)
	case agentOffline(r.Content):
		return ToolResultAgentOffline, ""
	}
	if !r.IsError && class != ToolResultError && class != ToolResultDenied && class != ToolResultTimeout {
		return class, ""
	}
	reason = errorReason(r.Content)
	lower := strings.ToLower(reason)
	if strings.Contains(lower, "unsupported") && strings.Contains(lower, "type") {
		return ToolResultUnsupported, reason
	}
	// A cloud's own words for a refused read, which the metrics' classes
	// count as errors: the chat shows them as denied.
	if class == ToolResultError && cloudDenied(r.Content) {
		return ToolResultDenied, reason
	}
	if class == ToolResultTimeout {
		// The card says how long it waited; the error adds nothing.
		return class, ""
	}
	return class, reason
}

// cloudDenied: AWS (AccessDenied, "is not authorized to perform"), Azure
// (AuthorizationFailed) and Google (PERMISSION_DENIED) refusing a read.
func cloudDenied(content string) bool {
	for _, s := range []string{"AccessDenied", "is not authorized to perform", "UnauthorizedOperation", "AuthorizationFailed", "PERMISSION_DENIED"} {
		if strings.Contains(content, s) {
			return true
		}
	}
	return false
}

func agentOffline(content string) bool {
	var obj map[string]any
	if json.Unmarshal([]byte(content), &obj) != nil {
		return false
	}
	connected, ok := obj["agentConnected"].(bool)
	return ok && !connected
}

var (
	// The wrapper the gateway puts around a provider's answer
	// (<external_tool_output connector="aws" tool="call">…).
	externalWrapperRE = regexp.MustCompile(`(?s)<external_tool_output[^>]*>(.*?)(</external_tool_output>|$)`)
	// AWS: "An error occurred (AccessDeniedException) when calling the
	// DescribeKey operation: … is not authorized to perform: kms:DescribeKey …"
	awsErrorCodeRE   = regexp.MustCompile(`\(([A-Za-z]+?)(?:Exception)?\) when calling the (\w+) operation`)
	awsErrorActionRE = regexp.MustCompile(`perform: ([a-z0-9-]+:[A-Za-z0-9*]+)`)
)

// errorReason is the first sentence of a failed call's error: the "error"
// field of a JSON result, or the provider's own text inside the gateway's
// wrapper. AWS errors read as "AccessDenied for kms:DescribeKey".
func errorReason(content string) string {
	text := strings.TrimSpace(content)
	if m := externalWrapperRE.FindStringSubmatch(text); m != nil {
		text = strings.TrimSpace(m[1])
	}
	var obj map[string]any
	if json.Unmarshal([]byte(text), &obj) == nil {
		if msg, ok := obj["error"].(string); ok {
			text = msg
		}
	}
	if m := awsErrorCodeRE.FindStringSubmatch(text); m != nil {
		code := m[1]
		if a := awsErrorActionRE.FindStringSubmatch(text); a != nil {
			return code + " for " + a[1]
		}
		return code + " in " + m[2]
	}
	return firstSentence(text)
}

// firstSentence cuts text at its first sentence end or line break, bounded.
func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexAny(text, "\n"); i >= 0 {
		text = text[:i]
	}
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimSuffix(strings.TrimSpace(text), ".")
	if utf8.RuneCountInString(text) > reasonMax {
		r := []rune(text)
		text = string(r[:reasonMax-1]) + "…"
	}
	return text
}
