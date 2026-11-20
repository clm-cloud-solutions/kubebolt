package copilot

import (
	"fmt"
	"strings"
	"testing"
)

func TestCallStatus(t *testing.T) {
	cases := []struct {
		name          string
		result        ToolResult
		budgetRefused bool
		wantStatus    string
		wantReason    string
	}{
		{"a plain read", ToolResult{Content: `{"items":[1]}`}, false, ToolResultOK, ""},
		{"past the per-question budget", MCPBudgetExhaustedResult(ToolCall{Name: "mcp__aws__call"}), true,
			ToolResultNotSent, fmt.Sprintf("this question already used its %d cloud calls", MaxMCPCallsPerTurn)},
		{"refused by the connector's allowlist",
			ToolResult{IsError: true, NotSent: true, Content: "Refused: the operation s3.PutObject is not read-only.\n" + notSentNote},
			false, ToolResultNotSent, "Refused: the operation s3.PutObject is not read-only"},
		{"held back by the organization's connector rate limit",
			ToolResult{IsError: true, RateLimited: true, Content: "Rate limited, try again later: this organization made too many external tool calls in the last minute."},
			false, ToolResultRateLimited, "cloud call limit reached for this minute"},
		{"the agent is not connected",
			ToolResult{IsError: true, Content: `{"error":"` + AgentOfflineError + `","agentConnected":false}`},
			false, ToolResultAgentOffline, ""},
		{"a kind the tool does not cover",
			ToolResult{IsError: true, Content: `{"error":"unsupported resource type: externalsecrets"}`},
			false, ToolResultUnsupported, "unsupported resource type: externalsecrets"},
		{"AWS access denied, inside the gateway's wrapper",
			ToolResult{IsError: true, Content: `<external_tool_output connector="aws" tool="call">AWS returned an error for kms.DescribeKey: An error occurred (AccessDeniedException) when calling the DescribeKey operation: User: arn:aws:sts::000000000000:assumed-role/kobi/x is not authorized to perform: kms:DescribeKey on resource: key/1 because no identity-based policy allows it</external_tool_output>`},
			false, ToolResultDenied, "AccessDenied for kms:DescribeKey"},
		{"a timeout says nothing more than its duration",
			ToolResult{IsError: true, Content: `{"error":"context deadline exceeded while reading pods"}`},
			false, ToolResultTimeout, ""},
		{"another error: its first sentence",
			ToolResult{IsError: true, Content: `{"error":"the metrics store answered 502. Try a shorter range or ask again."}`},
			false, ToolResultError, "the metrics store answered 502"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, reason := CallStatus(c.result, ClassifyToolResult(c.result), c.budgetRefused)
			if status != c.wantStatus || reason != c.wantReason {
				t.Errorf("CallStatus = (%q, %q)\n             want (%q, %q)", status, reason, c.wantStatus, c.wantReason)
			}
		})
	}
}

func TestErrorReasonIsBounded(t *testing.T) {
	got := errorReason(`{"error":"` + strings.Repeat("x", 400) + `"}`)
	if n := len([]rune(got)); n != reasonMax || !strings.HasSuffix(got, "…") {
		t.Errorf("reason has %d runes: %q", n, got)
	}
}

// Found live: a burst of refused calls counted as `error` in the tool metrics
// and fired the platform's «connector failing» alert. A call that reached
// nothing counts under its own result; everything else keeps its class.
func TestMetricToolResult(t *testing.T) {
	refused := ToolResult{Content: "Refused: ec2.describeinstances is blocked on this KubeBolt installation by its administrators.", IsError: true, NotSent: true}
	limited := ToolResult{Content: "Rate limited, try again later: this organization made too many external tool calls in the last minute.", IsError: true, RateLimited: true}
	failed := ToolResult{Content: `{"error":"the connection was reset"}`, IsError: true}
	budget := MCPBudgetExhaustedResult(ToolCall{ID: "t1", Name: "mcp__aws__call"})
	for _, c := range []struct {
		name   string
		r      ToolResult
		budget bool
		want   string
	}{
		{"refused before the provider", refused, false, ToolResultNotSent},
		{"past the turn's budget", budget, true, ToolResultNotSent},
		{"held back by the rate limit", limited, false, ToolResultRateLimited},
		{"sent and failed", failed, false, ToolResultError},
		{"answered", ToolResult{Content: `{"items":[1]}`}, false, ToolResultOK},
	} {
		if got := MetricToolResult(c.r, ClassifyToolResult(c.r), c.budget); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
