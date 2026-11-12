package mcpgateway

import (
	"reflect"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// A person below a connector's Kobi role floor got no tools and Kobi never
// said why. ConnectorsOutOfReach is what Kobi is told instead: the ready
// connectors the caller's role is below, with the role each needs.
func TestConnectorsOutOfReach(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.gw.ConnectorsOutOfReach(h.roleCtx("org-a", auth.RoleViewer)); got != nil {
		t.Fatalf("no connector enabled, yet out of reach: %v", got)
	}
	h.enable(orgCtx("org-a"), regionFieldA)
	viewer, editor, admin := h.roleCtx("org-a", auth.RoleViewer), h.roleCtx("org-a", auth.RoleEditor), h.roleCtx("org-a", auth.RoleAdmin)
	needs := func(role string) []copilot.ConnectorOutOfReach {
		return []copilot.ConnectorOutOfReach{{ID: fakeID, MinRole: role}}
	}

	// The default floor: editor.
	if got := h.gw.ConnectorsOutOfReach(viewer); !reflect.DeepEqual(got, needs("editor")) {
		t.Errorf("viewer under the default floor: %v", got)
	}
	if got := h.gw.ConnectorsOutOfReach(editor); got != nil {
		t.Errorf("editor under the default floor: %v", got)
	}
	// No claims is a request with authentication disabled: admin.
	if got := h.gw.ConnectorsOutOfReach(orgCtx("org-a")); got != nil {
		t.Errorf("unauthenticated install: %v", got)
	}
	// Another tenant's connector is never named.
	if got := h.gw.ConnectorsOutOfReach(h.roleCtx("org-b", auth.RoleViewer)); got != nil {
		t.Errorf("org-b viewer is told about org-a's connector: %v", got)
	}

	// Admins only: an editor is now below it and told it needs admin.
	if _, err := h.gw.Put(orgCtx("org-a"), fakeID, PutRequest{KobiMinRole: ptr("admin")}, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := h.gw.ConnectorsOutOfReach(editor); !reflect.DeepEqual(got, needs("admin")) {
		t.Errorf("editor under an admin floor: %v", got)
	}
	if got := h.gw.ConnectorsOutOfReach(admin); got != nil {
		t.Errorf("admin under an admin floor: %v", got)
	}

	// Everyone: nobody is below it.
	if _, err := h.gw.Put(orgCtx("org-a"), fakeID, PutRequest{KobiMinRole: ptr("viewer")}, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := h.gw.ConnectorsOutOfReach(viewer); got != nil {
		t.Errorf("viewer under an Everyone floor: %v", got)
	}

	// A paused connector is not offered to anyone, so nobody is told about it.
	if _, err := h.gw.Put(orgCtx("org-a"), fakeID, PutRequest{KobiMinRole: ptr("editor"), Paused: ptr(true)}, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := h.gw.ConnectorsOutOfReach(viewer); got != nil {
		t.Errorf("paused connector named to a viewer: %v", got)
	}
}
