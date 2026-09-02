package findings

import "github.com/kubebolt/kubebolt/apps/api/internal/integrations"

// The Security lenses — the sub-tabs of the dashboard, and the ONE place that
// decides which one a finding belongs to.
const (
	GroupVulnerability = "vulnerability"
	GroupCompliance    = "compliance"
	GroupConfiguration = "configuration"
	GroupRBAC          = "rbac"
)

// SecurityGroup classifies a finding into the lens the dashboard files it under.
//
// It lives here, and not next to the HTTP filter that used to own it, because
// it now has two readers: the Security page and the first-scan email. That email
// carries a button to the page, so any divergence is discovered by the customer
// at the worst possible moment — the summary announced 85 criticals over a
// dashboard reading 61, both derived honestly from the same store, and the click
// between them made KubeBolt look wrong about its own data. Measured 2026-08-12.
//
// The lenses cut across SOURCES, which is why counting per scanner does not
// work: Trivy alone produces vulnerabilities, configuration and permission
// findings, and the reader has no screen showing their sum.
//
// Precedence matters where a finding could arguably fit twice. Vulnerabilities
// win over compliance because a CVE is a CVE whatever control also mentions it;
// compliance then claims anything carrying a control identifier, which is what
// makes "misconfig with a CIS control" a compliance row rather than a
// configuration one. Verified against live data: no stored finding matches two
// lenses today, so the order is a guard rather than a live tie-break.
func SecurityGroup(rec *Record) string {
	switch {
	case rec.Kind == integrations.FindingCVE || rec.Kind == integrations.FindingExposedSecret:
		return GroupVulnerability
	case rec.CISControl != "":
		return GroupCompliance
	case rec.Kind == integrations.FindingRBACIssue:
		return GroupRBAC
	case rec.Kind == integrations.FindingMisconfig || rec.Kind == integrations.FindingPolicyViolation:
		return GroupConfiguration
	default:
		return ""
	}
}
