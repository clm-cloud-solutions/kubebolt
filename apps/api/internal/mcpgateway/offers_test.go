package mcpgateway

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
)

// Offers is the install's mode policy seen from a price list: a connector
// with a mode that does not borrow the machine's identity is offered
// everywhere, one whose every mode does only where ambient credentials are
// allowed, and one with no mode at all nowhere.
func TestOffersFollowsTheAmbientPolicy(t *testing.T) {
	sidecarOnly := &mcpcatalog.ConnectorDef{
		ID: "sidecar", Name: "Sidecar", MinPlan: auth.PlanTeam, Binder: newFakeBinder(),
		AuthModes: []mcpcatalog.AuthMode{{ID: "sidecar", Label: "Sidecar", SelfHostedOnly: true}},
	}
	noModes := &mcpcatalog.ConnectorDef{ID: "nomodes", Name: "No modes", MinPlan: auth.PlanTeam, Binder: newFakeBinder()}

	for _, ambient := range []bool{false, true} {
		h := newHarness(t, func(o *Options) { o.MultiTenant = true; o.AmbientAllowed = ambient })
		mixed := fakeDef(fakeID, h.fb) // an access key plus the machine's identity
		if !h.gw.Offers(mixed) {
			t.Errorf("ambient=%v: a connector with an access-key mode is not offered", ambient)
		}
		if got := h.gw.Offers(sidecarOnly); got != ambient {
			t.Errorf("ambient=%v: sidecar-only connector offered = %v", ambient, got)
		}
		if h.gw.Offers(noModes) {
			t.Errorf("ambient=%v: a connector with no mode is offered", ambient)
		}
	}
}

