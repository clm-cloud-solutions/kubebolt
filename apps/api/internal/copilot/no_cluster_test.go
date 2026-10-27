package copilot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The no-cluster note used to say "Do NOT call any cluster tool except
// get_kubebolt_docs — without a cluster they will only return errors", while
// the executor answered the fleet, findings and insight history without a
// connector: Kobi with no cluster selected refused what it had. The note now
// names the tools that answer, and these tests keep its list and the gate
// telling the same story.

func TestNoClusterToolsPassTheConnectorGate(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range NoClusterTools() {
		listed[name] = true
	}
	known := map[string]bool{}
	e := NewExecutor(nil) // no manager: no connector, every call meets the gate
	for _, d := range ToolDefinitions() {
		known[d.Name] = true
		res := e.ExecuteCtx(context.Background(), ToolCall{ID: "c1", Name: d.Name, Input: json.RawMessage("{}")})
		gated := strings.Contains(res.Content, "needsProxy")
		switch {
		case listed[d.Name] && gated:
			t.Errorf("%s is named as working with no cluster, but the gate refuses it", d.Name)
		case !listed[d.Name] && !gated && d.Name != "get_external_connections":
			t.Errorf("%s answers with no connector but the no-cluster note does not name it", d.Name)
		}
	}
	for name := range listed {
		if !known[name] {
			t.Errorf("NoClusterTools names %s, which is not a tool", name)
		}
	}
}

func TestNoClusterBlockNamesTheToolsThatAnswer(t *testing.T) {
	for fn, block := range map[string]string{
		"NoClusterContextBlock":               NoClusterContextBlock(),
		"NoClusterContextBlockWithConnectors": NoClusterContextBlockWithConnectors([]string{"aws"}),
	} {
		if strings.Contains(block, "except get_kubebolt_docs") {
			t.Errorf("%s still forbids every tool but the docs", fn)
		}
		for _, name := range NoClusterTools() {
			if !strings.Contains(block, name) {
				t.Errorf("%s does not name %s", fn, name)
			}
		}
		if !strings.Contains(block, "Do not call the tools that read a live cluster") {
			t.Errorf("%s no longer keeps Kobi off the cluster tools", fn)
		}
	}
}

// The two notes are one text: the connector variant adds its sentence and
// changes nothing else, so a correction to one reaches the other.
func TestNoClusterBlockWithConnectorsOnlyAddsTheConnectors(t *testing.T) {
	plain := NoClusterContextBlock()
	with := NoClusterContextBlockWithConnectors([]string{"azure"})
	cut := strings.Index(plain, "If the operator asks about live state")
	if cut < 0 {
		t.Fatal("the closing sentence moved; update this test")
	}
	if !strings.HasPrefix(with, plain[:cut]) || !strings.HasSuffix(with, plain[cut:]) {
		t.Errorf("the connector variant diverges from the plain note:\n%s\n---\n%s", plain, with)
	}
}
