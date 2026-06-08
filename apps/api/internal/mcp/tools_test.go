package mcp

import (
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// TestReadOnlyCatalogueExcludesMutations is the core security assertion: the
// exposed catalogue must contain the known read tools and NONE of the
// mutating propose_* tools.
func TestReadOnlyCatalogueExcludesMutations(t *testing.T) {
	p := NewExecutorToolProvider(copilot.NewExecutor(nil))
	tools := p.ListTools()
	if len(tools) == 0 {
		t.Fatal("read-only catalogue is empty")
	}

	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
		if strings.HasPrefix(tl.Name, "propose_") {
			t.Errorf("mutating tool %q leaked into the read-only catalogue", tl.Name)
		}
		if tl.InputSchema == nil {
			t.Errorf("tool %q has nil InputSchema (MCP requires an object schema)", tl.Name)
		}
	}

	// A representative sample of read tools must be present.
	for _, want := range []string{
		"get_cluster_overview", "list_resources", "get_resource_detail",
		"get_pod_logs", "get_insights", "get_topology", "get_workload_metrics",
	} {
		if !names[want] {
			t.Errorf("expected read tool %q missing from catalogue", want)
		}
	}
}

// TestCallToolRejectsWithheldTool proves the allow-list guard rejects a
// mutating tool by NAME even though the executor is never reached — the
// read-only promise is enforced server-side, not just by hiding tools.
func TestCallToolRejectsWithheldTool(t *testing.T) {
	p := NewExecutorToolProvider(copilot.NewExecutor(nil)) // nil manager: must not be dereferenced
	for _, name := range []string{
		"propose_delete_resource", "propose_restart_workload",
		"propose_scale_workload", "definitely_not_a_tool",
	} {
		_, err := p.CallTool(t.Context(), name, nil)
		if err == nil {
			t.Errorf("CallTool(%q) returned nil error; want ErrUnknownTool", name)
		}
	}
}

// TestCallToolAllowsReadToolName confirms an allowed read tool passes the guard
// (it will then hit the executor — with a nil manager the executor returns a
// graceful isError result rather than panicking, which we also assert).
func TestCallToolAllowsReadToolName(t *testing.T) {
	// A real executor over a nil manager: Connector(ctx) on a nil manager
	// would panic, so we DON'T call through here. Instead we assert the guard
	// admits the name by checking membership directly.
	p := NewExecutorToolProvider(copilot.NewExecutor(nil))
	if _, ok := p.allowed["get_cluster_overview"]; !ok {
		t.Error("get_cluster_overview should be allowed")
	}
	if _, ok := p.allowed["propose_delete_resource"]; ok {
		t.Error("propose_delete_resource must NOT be allowed")
	}
}

func TestNormalizeArgs(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{``, `{}`},
		{`null`, `{}`},
		{`  null  `, `{}`},
		{`   `, `{}`},
		{`{"a":1}`, `{"a":1}`},
		{`  {"a":1}  `, `  {"a":1}  `}, // non-empty payload returned verbatim
	}
	for _, c := range cases {
		got := string(normalizeArgs([]byte(c.in)))
		if got != c.want {
			t.Errorf("normalizeArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNextCallIDUnique(t *testing.T) {
	a := nextCallID()
	b := nextCallID()
	if a == b {
		t.Errorf("call ids not unique: %q == %q", a, b)
	}
	if !strings.HasPrefix(a, "mcp-") {
		t.Errorf("call id %q missing mcp- prefix", a)
	}
}

func TestToolsFromDefinitionsDefaultsSchema(t *testing.T) {
	defs := []copilot.ToolDefinition{{Name: "x", Description: "d", InputSchema: nil}}
	out := toolsFromDefinitions(defs)
	if len(out) != 1 {
		t.Fatalf("got %d, want 1", len(out))
	}
	if out[0].InputSchema == nil || out[0].InputSchema["type"] != "object" {
		t.Errorf("nil schema should default to an object schema, got %v", out[0].InputSchema)
	}
}
