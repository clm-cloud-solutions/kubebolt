package findings

// The NOTIFICATION categories — the axis routing keys on, and deliberately
// coarser than the dashboard's lenses.
//
// Two, not five. Five knobs is a configuration screen; two is a decision: does
// this go to whoever owns the workload, or to whoever owns security posture.
const (
	// CategoryVulnerability is what the workload's owner fixes by rebuilding the
	// image — and the leaked credentials that ride along with it.
	CategoryVulnerability = "vulnerability"
	// CategoryPosture is what security decides on: misconfiguration, permissions,
	// policy, compliance. A manifest or a role changes, not an image.
	CategoryPosture = "posture"
)

// NotifyCategory answers which routing lane a finding belongs to.
//
// Defined ON TOP of SecurityGroup rather than beside it. That is the whole point:
// with exposed secrets kept on the vulnerability side, the two routing categories
// are a strict COARSENING of the four dashboard lenses —
//
//	vulnerability                      →  vulnerability
//	configuration + rbac + compliance  →  posture
//
// — so exactly one function decides what a finding IS, and this one only groups
// its answer. Two independent classifiers over the same set is what produced an
// email announcing 85 criticals over a dashboard reading 61 (finding #20 §6.2.1);
// the fix was to make there be one, and building this on top keeps it that way.
//
// Why secrets travel with the CVEs, when security is who rotates them: they are
// 6 findings against 1446. A lane of its own for something that punctual is a
// second message that is empty almost every time and, when it is not, arrives
// with no context — while the same reader receives the vulnerability message
// anyway. Prominence instead of separation: a secret sorts FIRST inside that
// message and carries the severity accent, mirroring the Vulnerabilities tab
// (see KindMix in SecurityPage.tsx), and it is one of the two things that copy
// security on an otherwise team-only roll-up (the other being a critical).
//
// "" for a record that belongs to no lens — the same non-answer SecurityGroup
// gives, and callers must treat it as "do not route" rather than as a default.
func NotifyCategory(rec *Record) string {
	switch SecurityGroup(rec) {
	case GroupVulnerability:
		return CategoryVulnerability
	case GroupConfiguration, GroupRBAC, GroupCompliance:
		return CategoryPosture
	default:
		return ""
	}
}
