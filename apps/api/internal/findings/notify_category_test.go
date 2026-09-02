package findings

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

func kindRec(kind integrations.FindingKind, cis string) *Record {
	return &Record{Finding: integrations.Finding{Kind: kind, CISControl: cis}}
}

// The routing categories must be a COARSENING of the dashboard lenses, never a
// second opinion about them. Anything SecurityGroup files somewhere has to land
// in exactly one lane, and anything it refuses to file must not be routed.
func TestNotifyCategory_IsACoarseningOfTheLenses(t *testing.T) {
	cases := []struct {
		name      string
		rec       *Record
		wantLens  string
		wantRoute string
	}{
		{"cve", kindRec(integrations.FindingCVE, ""), GroupVulnerability, CategoryVulnerability},
		// The decision that removes the second classifier: a leaked credential is
		// security's to rotate, but it rides WITH the CVEs and is made loud inside
		// that message instead of getting a lane of its own.
		{"exposed secret", kindRec(integrations.FindingExposedSecret, ""), GroupVulnerability, CategoryVulnerability},
		{"misconfig", kindRec(integrations.FindingMisconfig, ""), GroupConfiguration, CategoryPosture},
		{"policy violation", kindRec(integrations.FindingPolicyViolation, ""), GroupConfiguration, CategoryPosture},
		{"rbac", kindRec(integrations.FindingRBACIssue, ""), GroupRBAC, CategoryPosture},
		// A misconfig carrying a control identifier is compliance, and compliance
		// is posture — the precedence lives in SecurityGroup and is inherited here.
		{"cis-tagged misconfig", kindRec(integrations.FindingMisconfig, "5.2.5"), GroupCompliance, CategoryPosture},
		{"unclassifiable", kindRec("something-new", ""), "", ""},
	}
	for _, c := range cases {
		if got := SecurityGroup(c.rec); got != c.wantLens {
			t.Errorf("%s: SecurityGroup = %q, want %q", c.name, got, c.wantLens)
		}
		if got := NotifyCategory(c.rec); got != c.wantRoute {
			t.Errorf("%s: NotifyCategory = %q, want %q", c.name, got, c.wantRoute)
		}
	}
}

// Locks the coarsening itself: every lens maps somewhere, and no lens maps to a
// third category invented later. A new lens added to SecurityGroup without a
// decision here would otherwise silently stop being routed at all.
func TestNotifyCategory_EveryLensIsRouted(t *testing.T) {
	want := map[string]string{
		GroupVulnerability: CategoryVulnerability,
		GroupConfiguration: CategoryPosture,
		GroupRBAC:          CategoryPosture,
		GroupCompliance:    CategoryPosture,
	}
	// Reach each lens through a record that SecurityGroup actually files there,
	// so the test breaks if the precedence moves rather than testing a stub.
	reps := map[string]*Record{
		GroupVulnerability: kindRec(integrations.FindingCVE, ""),
		GroupConfiguration: kindRec(integrations.FindingMisconfig, ""),
		GroupRBAC:          kindRec(integrations.FindingRBACIssue, ""),
		GroupCompliance:    kindRec(integrations.FindingMisconfig, "5.2.5"),
	}
	for lens, rec := range reps {
		if got := SecurityGroup(rec); got != lens {
			t.Fatalf("the fixture for %s no longer lands there (got %s)", lens, got)
		}
		if got := NotifyCategory(rec); got != want[lens] {
			t.Errorf("lens %s routes to %q, want %q", lens, got, want[lens])
		}
	}
}
