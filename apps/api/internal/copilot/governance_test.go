package copilot

import (
	"strings"
	"testing"
)

func countPropose(tools []ToolDefinition) (total, delete int) {
	for _, t := range tools {
		if strings.HasPrefix(t.Name, "propose_") {
			total++
		}
		if t.Name == "propose_delete_resource" {
			delete++
		}
	}
	return
}

func TestGovernedToolDefinitions(t *testing.T) {
	full := ToolDefinitions()
	allPropose, _ := countPropose(full)
	if allPropose == 0 {
		t.Fatal("expected the base tool set to include propose_* tools")
	}

	// Actions ON + destructive ON → identical to the full set.
	if got := GovernedToolDefinitions(true, true); len(got) != len(full) {
		t.Fatalf("actions+destructive on should return all %d tools, got %d", len(full), len(got))
	}

	// Actions ON + destructive OFF → delete withheld, other proposals kept.
	govNoDestr := GovernedToolDefinitions(true, false)
	if _, del := countPropose(govNoDestr); del != 0 {
		t.Fatalf("destructive off must withhold propose_delete_resource")
	}
	if p, _ := countPropose(govNoDestr); p != allPropose-1 {
		t.Fatalf("destructive off should drop exactly delete: want %d propose tools, got %d", allPropose-1, p)
	}

	// Actions OFF → ALL propose_* withheld (read-only advisory).
	govNone := GovernedToolDefinitions(false, true)
	if p, _ := countPropose(govNone); p != 0 {
		t.Fatalf("actions off must withhold all propose_* tools, got %d", p)
	}
	// ...but non-action (read) tools remain available.
	if len(govNone) == 0 || len(govNone) >= len(full) {
		t.Fatalf("actions off should keep read tools and drop only proposals: got %d of %d", len(govNone), len(full))
	}
}
