package copilot

import (
	"strings"
	"testing"
	"time"
)

// Below a connector's floor Kobi had no tools and no word about the connector,
// so it answered as if there were none.
func TestConnectorsOutOfReachBlock(t *testing.T) {
	if got := ConnectorsOutOfReachBlock(nil, "viewer"); got != "" {
		t.Fatalf("no connector out of reach, yet a block: %q", got)
	}
	got := ConnectorsOutOfReachBlock([]ConnectorOutOfReach{{ID: "azure", MinRole: "admin"}, {ID: "aws", MinRole: "editor"}}, "viewer")
	for _, want := range []string{
		"AWS (needs the editor role), Azure (needs the admin role)",
		"Their role is viewer",
		"an admin can raise this person's role or open the connector to everyone",
		"Who can use it in Kobi",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}
	// This edition has no teams: the block never mentions one.
	if strings.Contains(strings.ToLower(got), "team") {
		t.Errorf("the block mentions teams:\n%s", got)
	}
	// The order the connectors come in does not change the bytes: the
	// conversation keeps its cache.
	again := ConnectorsOutOfReachBlock([]ConnectorOutOfReach{{ID: "aws", MinRole: "editor"}, {ID: "azure", MinRole: "admin"}}, "viewer")
	if again != got {
		t.Errorf("the block depends on order:\n%s\n---\n%s", got, again)
	}
}

func TestSessionNotesNameTheConnectorsOutOfReach(t *testing.T) {
	in := SessionNotesInput{ClusterName: "prod", Now: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC), Connectors: []string{"aws"}}
	if notes := SessionNotes(in); strings.Contains(notes, "cannot use") {
		t.Fatalf("an editor's notes name a connector out of reach:\n%s", notes)
	}
	in.Connectors = nil
	in.ConnectorsOutOfReach = []ConnectorOutOfReach{{ID: "aws", MinRole: "editor"}}
	in.OrgRole = "viewer"
	notes := SessionNotes(in)
	if !strings.Contains(notes, "# Cloud connectors this person cannot use") || !strings.Contains(notes, "AWS (needs the editor role)") {
		t.Errorf("a viewer's notes do not name the connector:\n%s", notes)
	}
	if strings.Contains(notes, "# Cloud connectors\n") {
		t.Errorf("a viewer's notes carry the rules for a connector they cannot call:\n%s", notes)
	}
}
