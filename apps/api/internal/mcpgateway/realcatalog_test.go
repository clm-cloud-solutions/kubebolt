package mcpgateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
)

// The shipped connectors behind the gateway, without touching any provider:
// every one renders a complete admin view in both kinds of install, and the
// connectors that only borrow the machine's identity are not offered where
// that identity would be KubeBolt's.
func TestShippedCatalogRendersThroughTheGateway(t *testing.T) {
	cat, err := mcpcatalog.New()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(cat.List()) == 0 {
		t.Skip("no connectors registered in this build")
	}
	for _, multi := range []bool{false, true} {
		h := newHarness(t, nil)
		if multi {
			multiTenant(auth.PlanTeam)(&h.opts)
		}
		gw := New(cat, h.st, h.opts)
		t.Cleanup(gw.Close)
		views, err := gw.Connectors(orgCtx("org-a"))
		if err != nil {
			t.Fatalf("multi=%v: %v", multi, err)
		}
		if len(views) != len(cat.List()) {
			t.Fatalf("multi=%v: %d views for %d connectors", multi, len(views), len(cat.List()))
		}
		for _, v := range views {
			if v.Status != StatusAvailable || !v.Entitled {
				t.Errorf("multi=%v %s: status %s entitled %v on a team plan", multi, v.ID, v.Status, v.Entitled)
			}
			def, _ := cat.Get(v.ID)
			selfHostedOnly := true
			for _, m := range def.AuthModes {
				selfHostedOnly = selfHostedOnly && m.SelfHostedOnly
			}
			if multi && selfHostedOnly && (len(v.AuthModes) != 0 || !strings.Contains(v.Description, "Not available on this installation")) {
				t.Errorf("%s offered in a multi-tenant install: %d modes", v.ID, len(v.AuthModes))
			}
			if !multi && len(v.AuthModes) != len(def.AuthModes) {
				t.Errorf("%s: %d of %d modes offered on a single-tenant install", v.ID, len(v.AuthModes), len(def.AuthModes))
			}
			raw, _ := json.Marshal(v)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			for _, k := range []string{"id", "name", "category", "description", "tags", "docsURL", "minPlan", "entitled", "status", "mode", "authModes", "families", "never", "tools", "config", "setupGuide"} {
				if m[k] == nil {
					t.Errorf("multi=%v %s: %q is missing or null", multi, v.ID, k)
				}
			}
		}
	}
}
