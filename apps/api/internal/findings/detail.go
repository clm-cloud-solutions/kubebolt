package findings

// Finding detail — the per-row drill-down behind the Security table.
//
// It exists because the stored Finding is deliberately lossy. A finding's
// identity excludes the package name, so one CVE affecting several packages of
// the same workload collapses into a single row — CVE-2026-33814 in cilium is
// reported by Trivy 17 times, once per affected binary. That collapse is right
// for a list (an operator has ONE problem there, not 17), but the surviving
// Remediation is an arbitrary one of the 17: the table can end up saying
// "upgrade stdlib" when the reachable path is golang.org/x/net.
//
// Rather than persist every package on every finding of every cluster — paying
// storage and cardinality forever for something read on a click — the detail is
// fetched from the cluster on demand. That matches how the whole pillar works:
// KubeBolt pulls, nothing is pushed at it.
//
// The trade is that the detail needs the cluster reachable. Findings survive a
// disconnected cluster by design (they are persisted, and the read route sits
// outside requireConnector), so the response degrades instead of failing: the
// stored record always comes back, with `live:false` and the reason.
//
// The types live here, not in the handler, because two readers return them:
// GET /findings/{fingerprint} (the Security page's drill-down) and Kobi's
// get_finding_detail. The live re-read itself stays with the API, which owns
// the connector.
type Detail struct {
	Record
	// Live reports whether the cluster answered. False means Packages is empty
	// because we could not look, NOT because there is nothing to show — the UI
	// must say which.
	Live      bool   `json:"live"`
	LiveError string `json:"liveError,omitempty"`
	// Images are the container images of this workload that carry the CVE.
	//
	// Grouped by IMAGE, not by container. Trivy emits one report per container,
	// so a workload whose initContainer and main container share an image
	// produced two identical blocks — same image, same packages, same fix, twice.
	// The vulnerability lives in the image: if two containers share one, that is
	// ONE thing to rebuild, listed once, naming the containers that use it.
	Images []AffectedImage `json:"images,omitempty"`

	// Compliance carries the CIS side of the drill-down: what the control
	// actually requires, and WHICH resources fail it. The stored finding has
	// only the count ("42 failing"), which tells an operator there is work
	// without saying where — the least useful shape a number can take.
	Compliance *ComplianceDetail `json:"compliance,omitempty"`
}

type ComplianceDetail struct {
	Benchmark string `json:"benchmark,omitempty"`
	Control   string `json:"control,omitempty"`
	// Description is the control's own text, which the stored title omits.
	Description string `json:"description,omitempty"`
	// Severity is the BENCHMARK's rating for this control, which can disagree
	// with the finding's — the normalizer defaults compliance findings to
	// medium, so a control the benchmark calls LOW still reads medium in the
	// list. Showing both is honest; silently picking one is not.
	Severity string `json:"severity,omitempty"`
	// FailingResources are the resources that actually fail the control,
	// resolved by following the control's check id into the config-audit
	// reports. Capped — a control can fail on hundreds of workloads.
	FailingResources []FailingResource `json:"FailingResources,omitempty"`
	FailingTotal     int               `json:"failingTotal"`
}

type FailingResource struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Message   string `json:"message,omitempty"`
}

// ComplianceResourceCap bounds the resource list. A control like "minimize root
// containers" fails on nearly every workload in a busy cluster, and a dialog is
// not a place to render four hundred rows.
const ComplianceResourceCap = 50

type AffectedImage struct {
	// Containers are the container names running this image, init and main
	// alike — an initContainer is just as much a place the code executes.
	Containers []string `json:"containers"`
	// Pods is how many pods currently run this image in the finding's
	// namespace: the live blast radius, which scaling changes and the finding
	// does not. -1 means unknown (no Pod informer), which must not render as 0.
	Pods int `json:"pods"`
	// Image is the fully-qualified reference an operator can pull and rebuild:
	// registry + repository + tag, e.g. quay.io/argoproj/argocd:v3.4.5.
	Image  string `json:"image,omitempty"`
	Digest string `json:"digest,omitempty"`
	// OS is the image's base distro ("ubuntu 26.04") — often the real answer to
	// "why do I have this CVE", since a stale base image drags in most of them.
	OS       string        `json:"os,omitempty"`
	Packages []VulnPackage `json:"packages"`
}

type VulnPackage struct {
	Name             string  `json:"name"`
	InstalledVersion string  `json:"installedVersion,omitempty"`
	FixedVersion     string  `json:"fixedVersion,omitempty"`
	Severity         string  `json:"severity,omitempty"`
	Score            float64 `json:"score,omitempty"`
	Link             string  `json:"link,omitempty"`
	// Container is which container of the workload carries it, when Trivy says.
	Container string `json:"container,omitempty"`
}
