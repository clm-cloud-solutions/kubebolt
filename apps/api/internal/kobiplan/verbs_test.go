package kobiplan_test

import (
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// A plan's actions are Kobi's verbs: the propose_ tools. A verb added to Kobi
// without its plan action, or an action with no tool behind it, fails here.
func TestActionsAreKobisVerbs(t *testing.T) {
	tools := map[string]bool{}
	for _, d := range copilot.ToolDefinitions() {
		if strings.HasPrefix(d.Name, "propose_") {
			tools[strings.TrimPrefix(d.Name, "propose_")] = true
		}
	}
	actions := map[string]bool{}
	for _, a := range kobiplan.Actions {
		actions[string(a)] = true
		if !tools[string(a)] {
			t.Errorf("action %q has no propose_%s tool", a, a)
		}
	}
	for verb := range tools {
		if !actions[verb] {
			t.Errorf("Kobi's propose_%s has no plan action in kobiplan.Actions", verb)
		}
	}
}
