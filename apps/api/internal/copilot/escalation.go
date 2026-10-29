package copilot

import (
	"encoding/json"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Model escalation: the chat runs on the configured model and moves to a
// stronger one, for the rest of the conversation, the first time it leaves the
// selected cluster. Measured on the lab: Haiku 5.5 reached one
// of these calls in 142 of 144 runs, after ~8 calls, and 97 of its 99 failing
// runs failed after it; inside one cluster the calls appear in ~4 % of runs,
// where Haiku answers as well as Sonnet at a fifteenth of the cost.

// EscalationRecord is what a session's escalation consumed.
type EscalationRecord struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// FromProvider and FromModel are the model the session moved from, the
	// configured one: SessionRecord.ModelShares prices what it consumed at
	// its own price. Empty on records written before they existed.
	FromProvider string `json:"fromProvider,omitempty"`
	FromModel    string `json:"fromModel,omitempty"`
	// Reason is what moved the conversation: "handoff" (it opened from a
	// cluster switch), "history" (an earlier turn had escalated), or the
	// tool whose call did it.
	Reason string `json:"reason"`
	Round  int    `json:"round"` // the round after which it moved; 0 = from the start
	Usage  Usage  `json:"usage"`
}

// EscalationTrigger reports whether a tool call, given its result, takes the
// investigation out of the selected cluster: a cloud connector, or a
// dependency whose address leads to something a connector can read.
//
// A dependency outside the cluster only leads somewhere when the org has a
// cloud connector to read it with (connectors). Without one, everything left
// to read is this cluster's own — the Service, its endpoints, the callers'
// logs — and Haiku reads those as well as Sonnet: on the kind outage of a
// selectorless shop-db it gave the exact duration for 3 credits where the
// escalated turn cost 35. Where a connector exists the
// escalation pays: the NSG activity log was read 3/12 times on Haiku and 8/8
// after escalating, so whether to go to the cloud stays the stronger
// model's call.
//
// But a dependency that is a workload of another cluster of the organization
// (inFleet) leads to that cluster, not to the cloud: the next step is the
// switch, and the conversation there starts escalated (the handoff). So
// get_external_connections escalates on what it found, not on being called:
// on the lab's front cluster, where both addresses were the back cluster's
// Services, the call moved the turn to Sonnet only to write the answer and
// the switch, 45 of its 49 credits (db-role-password-rotated, 2026-10-12).
// Loading the external-dependencies skill no longer escalates: it says
// nothing about where the dependency is. Nor does offer_cluster_switch: the
// investigation goes on in the other cluster's conversation, which starts
// escalated; here only the closing answer is left, and when Haiku offered the
// switch before writing it, that round alone cost $0.17 on Sonnet (simulated
// from the same conversation, 2 of 3 runs).
func EscalationTrigger(call ToolCall, result string, connectors bool) bool {
	switch {
	case IsMCPToolName(call.Name):
		return true
	case call.Name == "get_external_connections":
		return connectors && LeadsOutOfTheFleet(result)
	}
	return false
}

// LeadsOutOfTheFleet reports whether a get_external_connections answer names
// a dependency outside the organization's clusters that a connector may read:
// a cloud resource the org has a connector for, or an address nothing
// attributes (it may be a cloud resource behind a private address or a custom
// name). Cluster plumbing and the fleet's own workloads do not count; a
// provider the org has no connector for leads nowhere it can read. An answer
// it cannot read counts, as the call itself did before.
func LeadsOutOfTheFleet(result string) bool {
	var r struct {
		Error           string                   `json:"error"`
		Truncated       string                   `json:"truncated_result"`
		Destinations    []ExternalDestination    `json:"destinations"`
		DependencyHosts []ExternalDependencyHost `json:"dependencyHosts"`
	}
	if json.Unmarshal([]byte(result), &r) != nil || r.Error != "" || r.Truncated != "" {
		return true
	}
	for _, d := range r.Destinations {
		if outOfTheFleet(&d.ExternalClassification, d.InFleet) {
			return true
		}
	}
	for _, h := range r.DependencyHosts {
		if h.Host != "" && h.ExternalClassification != nil && outOfTheFleet(h.ExternalClassification, h.InFleet) {
			return true
		}
	}
	return false
}

// ResultFor is the content of the result answering call id, "" when there is
// none.
func ResultFor(results []ToolResult, id string) string {
	for _, r := range results {
		if r.ToolCallID == id {
			return r.Content
		}
	}
	return ""
}

func outOfTheFleet(c *ExternalClassification, fleet *FleetMatch) bool {
	if fleet != nil || c.Infrastructure {
		return false
	}
	return c.ConnectorEnabled || c.Provider == ""
}

// ConversationEscalated reports whether a conversation starts on the
// escalation model: it opened from a cluster switch (the handoff note on a
// user message), or an earlier turn already left the cluster. Derived from
// the transcript, so the choice sticks across requests with no state.
func ConversationEscalated(messages []Message, connectors bool) (bool, string) {
	results := map[string]string{}
	for _, m := range messages {
		for _, r := range m.ToolResults {
			results[r.ToolCallID] = r.Content
		}
	}
	for _, m := range messages {
		if m.Role == RoleUser && handoffPrefixRE.MatchString(m.Content) {
			return true, "handoff"
		}
		for _, tc := range m.ToolCalls {
			if EscalationTrigger(tc, results[tc.ID], connectors) {
				return true, "history"
			}
		}
	}
	return false, ""
}

// EscalationProvider is the configured escalation model, completed from the
// primary when it shares the provider (key, endpoint), or nil when there is
// none or it is the primary itself.
func EscalationProvider(cfg config.CopilotConfig) *config.ProviderConfig {
	if cfg.Escalation == nil || cfg.Escalation.Model == "" {
		return nil
	}
	e := *cfg.Escalation
	if e.Provider == "" {
		e.Provider = cfg.Primary.Provider
	}
	if e.Provider == cfg.Primary.Provider {
		if e.APIKey == "" {
			e.APIKey = cfg.Primary.APIKey
		}
		if e.BaseURL == "" {
			e.BaseURL = cfg.Primary.BaseURL
		}
		if e.Model == cfg.Primary.Model {
			return nil
		}
	}
	if e.APIKey == "" {
		return nil
	}
	return &e
}
