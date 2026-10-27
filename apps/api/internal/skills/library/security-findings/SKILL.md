---
name: security-findings
audience: kobi
description: Reporting a cluster's security posture from the scanners (get_findings, get_finding_workloads, get_finding_detail): posture before rows, leading with the image, rollups, what unavailable means. Load it before you report on vulnerabilities, misconfigurations, policy violations or compliance.
---
get_insights covers what KubeBolt's own rules detect. It does NOT cover the
scanners: CVEs and misconfigurations from Trivy, policy violations from
Kyverno, CIS compliance controls. Those are get_findings, and a cluster with a
clean insight list can still be carrying hundreds of them.

Call it with no filter first. It returns the POSTURE — counts by severity, how
many workloads are affected, the worst one, and the images carrying the most
findings — because a list of a thousand rows answers nothing. "1,224 findings"
is not an answer; "1,224 across 18 workloads, concentrated in three images"
is. Then narrow by severity, image or resource to get rows, each with its
remediation.

**"Where do I start" is get_finding_workloads.** It ranks workloads the way
the Security page does — an exposed secret first, then severity before volume
— and names the images and the configuration checks that clear the most rows
at once. Present that order; do not re-rank by count.

**One finding in full is get_finding_detail**, with the fingerprint from a
get_findings row (and its clusterId when the rows came from cluster="all"). It
re-reads the scanner: every package carrying a CVE with its fixed version —
the stored row keeps only one remediation, often not the reachable one — or the
resources that actually fail a CIS control. Call it before recommending a
specific upgrade. live=false means the cluster could not be read, NOT that the
finding is fixed.

**Lead with the image when there is one.** It is the unit somebody actually
rebuilds, and two workloads running the same image are ONE fix — a list of
affected workloads hides that and makes a morning's work look like a quarter's.

**Counts exclude rollups by design.** A compliance control that aggregates
findings counted individually would otherwise report the same problem twice.
When the response says rollupsExcluded, that is why the numbers do not add up
to the row count; say so rather than presenting a discrepancy.

**Unavailable is not clean.** If the tool reports that findings are not
available on this install, no scanner is wired up — say exactly that. Reporting
a cluster as secure because you could not look is the worst answer available.
