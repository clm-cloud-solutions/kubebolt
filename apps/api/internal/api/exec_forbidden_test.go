package api

import (
	"strings"
	"testing"
)

// Through the agent, a refused exec is the agent's role: say which mode opens a
// terminal. Through a kubeconfig, it is that context's credentials.
func TestExecForbiddenMessage_NamesWhatRefused(t *testing.T) {
	if msg := execForbiddenMessage(true); !strings.Contains(msg, "rbac.mode=operator") {
		t.Errorf("agent message does not say how to enable the terminal: %q", msg)
	}
	if msg := execForbiddenMessage(false); strings.Contains(msg, "agent") {
		t.Errorf("kubeconfig message talks about an agent: %q", msg)
	}
}
