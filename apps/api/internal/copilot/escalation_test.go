package copilot

import (
	"encoding/json"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// The lab's front cluster (db-role-password-rotated, 2026-10-12): both
// addresses shop-web calls are the back cluster's Services.
const frontConnections = `{"sourcePresent":false,"clusterUIDKnown":true,"windowMinutes":15,"destinations":[],"dependencyHosts":[` +
	`{"source":"env ORDERS_URL (container app)","host":"57.165.100.6","kind":"unknown","confidence":"low","connectorEnabled":false,` +
	`"inFleet":{"cluster":"kubeshop-back","clusterContext":"agent:back","kind":"Service","namespace":"shop-backend","name":"orders-api","sameCluster":false}},` +
	`{"source":"env CATALOG_URL (container app)","host":"20.10.109.107","kind":"unknown","confidence":"low","connectorEnabled":false,` +
	`"inFleet":{"cluster":"kubeshop-back","clusterContext":"agent:back","kind":"Service","namespace":"shop-backend","name":"catalog-api","sameCluster":false}}]}`

// The back cluster: shop-db is an ExternalName to an Azure PostgreSQL server.
const backConnections = `{"sourcePresent":false,"destinations":[],"dependencyHosts":[` +
	`{"source":"Service shop-data/shop-db (ExternalName)","host":"pg-lab.postgres.database.azure.com","provider":"azure","service":"postgres",` +
	`"kind":"azure-postgres","confidence":"high","connectorEnabled":true}]}`

func TestEscalationTrigger(t *testing.T) {
	for name, want := range map[string]bool{
		"mcp__azure__monitor": true, "offer_cluster_switch": false,
		"get_pod_logs": false, "list_clusters": false, "propose_set_env": false,
	} {
		if got := EscalationTrigger(ToolCall{Name: name}, "", true); got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
	// Loading the procedure says nothing about where the dependency is.
	if EscalationTrigger(skill("external-dependencies"), "", true) {
		t.Error("loading the external-dependencies skill escalated")
	}
}

// get_external_connections escalates on what it found: a dependency a
// connector can read, or one nothing attributes; never the fleet's own
// workloads, which lead to the switch and an escalated conversation there.
func TestEscalationTrigger_ExternalConnectionsEscalateOnWhatTheyFound(t *testing.T) {
	ext := ToolCall{Name: "get_external_connections"}
	host := func(h string) string {
		return `{"destinations":[],"dependencyHosts":[` + h + `]}`
	}
	for name, c := range map[string]struct {
		result string
		want   bool
	}{
		"the back cluster's Services (the lab's front)": {frontConnections, false},
		"an Azure PostgreSQL with the connector":        {backConnections, true},
		"an address nothing attributes":                 {host(`{"source":"env DB_HOST","host":"10.20.0.4","kind":"unknown","confidence":"low","connectorEnabled":false}`), true},
		"a provider the org has no connector for":       {host(`{"source":"env DB_HOST","host":"db.rds.amazonaws.com","provider":"aws","kind":"aws-rds","confidence":"high","connectorEnabled":false}`), false},
		"cluster plumbing":                              {`{"destinations":[{"ip":"10.0.0.10","kind":"dns","provider":"azure","infrastructure":true,"connectorEnabled":true,"callers":[]}]}`, false},
		"a lead with no address (a Secret)":             {host(`{"source":"Secret db-credentials (not read)"}`), false},
		"a flow to a cloud endpoint":                    {`{"sourcePresent":true,"destinations":[{"fqdn":"sb-lab.servicebus.windows.net","provider":"azure","kind":"azure-servicebus","confidence":"high","connectorEnabled":true,"callers":[]}]}`, true},
		"nothing external":                              {`{"sourcePresent":true,"destinations":[]}`, false},
		"an error":                                      {`{"error":"cannot see external connections from this server"}`, true},
		"an answer cut by the size cap":                 {`{"truncated_result":"{\"destinations\":[","notice":"cut"}`, true},
	} {
		if got := EscalationTrigger(ext, c.result, true); got != c.want {
			t.Errorf("%s: escalated=%v, want %v", name, got, c.want)
		}
		if EscalationTrigger(ext, c.result, false) {
			t.Errorf("%s: escalated with no connector", name)
		}
	}
}

// With no connector the dependency outside can only be read from inside the
// cluster, which the primary model does as well: the kind shop-db outage cost
// 35 credits escalated and 3 on Haiku, with the same exact duration.
func TestEscalationTrigger_ExternalDependencyNeedsAConnector(t *testing.T) {
	if EscalationTrigger(ToolCall{Name: "get_external_connections"}, backConnections, false) {
		t.Error("escalated with no connector to follow the dependency")
	}
	// The other cluster's conversation starts escalated (the handoff); the
	// offer itself only leaves the closing answer here.
	if EscalationTrigger(ToolCall{Name: "offer_cluster_switch"}, "", true) {
		t.Error("offering the switch escalated this conversation")
	}
}

func skill(n string) ToolCall {
	return ToolCall{Name: "load_skill", Input: json.RawMessage(`{"name":"` + n + `"}`)}
}

func TestConversationEscalatedFromTheTranscript(t *testing.T) {
	plain := []Message{{Role: RoleUser, Content: "¿por qué se reinicia oomy-killer?"}}
	if ok, _ := ConversationEscalated(plain, true); ok {
		t.Error("a plain question escalated")
	}
	handoff := []Message{{Role: RoleUser, Content: "(From cluster front: 503 from orders-api)\n\n¿qué pasó?"}}
	if ok, why := ConversationEscalated(handoff, false); !ok || why != "handoff" {
		t.Errorf("handoff: %v %q", ok, why)
	}
	history := []Message{
		{Role: RoleUser, Content: "la tienda da 503"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "mcp__azure__monitor"}}},
		{Role: RoleUser, Content: "¿y ahora?"},
	}
	if ok, why := ConversationEscalated(history, true); !ok || why != "history" {
		t.Errorf("history: %v %q", ok, why)
	}
	// The history's get_external_connections is judged by its own result.
	connections := func(result string) []Message {
		return []Message{
			{Role: RoleUser, Content: "la tienda da 503"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "get_external_connections"}}},
			{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "c1", Content: result}}},
			{Role: RoleAssistant, Content: "orders-api está en kubeshop-back."},
			{Role: RoleUser, Content: "¿y ahora?"},
		}
	}
	if ok, _ := ConversationEscalated(connections(frontConnections), true); ok {
		t.Error("a history that only found the fleet's own workloads escalated")
	}
	if ok, why := ConversationEscalated(connections(backConnections), true); !ok || why != "history" {
		t.Errorf("a history that found the Azure server: %v %q", ok, why)
	}
}

func TestEscalationProviderInheritsThePrimary(t *testing.T) {
	cfg := config.CopilotConfig{Primary: config.ProviderConfig{Provider: "anthropic", APIKey: "k", Model: "claude-haiku-5-5"}}
	if EscalationProvider(cfg) != nil {
		t.Error("no escalation configured")
	}
	cfg.Escalation = &config.ProviderConfig{Model: "claude-sonnet-5-5"}
	e := EscalationProvider(cfg)
	if e == nil || e.Provider != "anthropic" || e.APIKey != "k" || e.Model != "claude-sonnet-5-5" {
		t.Errorf("got %+v", e)
	}
	cfg.Escalation = &config.ProviderConfig{Model: "claude-haiku-5-5"}
	if EscalationProvider(cfg) != nil {
		t.Error("escalating to the primary itself")
	}
	cfg.Escalation = &config.ProviderConfig{Provider: "openai", Model: "gpt-6-sol"}
	if EscalationProvider(cfg) != nil {
		t.Error("another provider with no key")
	}
}
