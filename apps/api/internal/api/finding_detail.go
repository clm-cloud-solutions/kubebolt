package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

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
type findingDetailResponse struct {
	findings.Record
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
	Images []affectedImage `json:"images,omitempty"`

	// Compliance carries the CIS side of the drill-down: what the control
	// actually requires, and WHICH resources fail it. The stored finding has
	// only the count ("42 failing"), which tells an operator there is work
	// without saying where — the least useful shape a number can take.
	Compliance *complianceDetail `json:"compliance,omitempty"`
}

type complianceDetail struct {
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
	FailingResources []failingResource `json:"failingResources,omitempty"`
	FailingTotal     int               `json:"failingTotal"`
}

type failingResource struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Message   string `json:"message,omitempty"`
}

// complianceResourceCap bounds the resource list. A control like "minimize root
// containers" fails on nearly every workload in a busy cluster, and a dialog is
// not a place to render four hundred rows.
const complianceResourceCap = 50

type affectedImage struct {
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
	Packages []vulnPackage `json:"packages"`
}

type vulnPackage struct {
	Name             string  `json:"name"`
	InstalledVersion string  `json:"installedVersion,omitempty"`
	FixedVersion     string  `json:"fixedVersion,omitempty"`
	Severity         string  `json:"severity,omitempty"`
	Score            float64 `json:"score,omitempty"`
	Link             string  `json:"link,omitempty"`
	// Container is which container of the workload carries it, when Trivy says.
	Container string `json:"container,omitempty"`
}

// findingDetailTimeout bounds the live lookup. Short on purpose: this is a
// click, and a slow answer is worse than a degraded one the user can read.
const findingDetailTimeout = 8 * time.Second

func (h *handlers) handleFindingDetail(w http.ResponseWriter, r *http.Request) {
	if h.findingsStore == nil {
		respondError(w, http.StatusServiceUnavailable, "findings are not available (persistence disabled)")
		return
	}
	fingerprint := chi.URLParam(r, "fingerprint")
	if fingerprint == "" {
		respondError(w, http.StatusBadRequest, "fingerprint is required")
		return
	}
	clusterID := r.URL.Query().Get("cluster")

	rec, ok, err := h.findingsStore.Get(h.activeTenantID(r), clusterID, fingerprint)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read the finding")
		return
	}
	if !ok || rec == nil {
		respondError(w, http.StatusNotFound, "finding not found")
		return
	}

	resp := findingDetailResponse{Record: *rec}

	// Resolve the connector for the FINDING's cluster, not the request's.
	//
	// A finding row knows which cluster it came from, and the operator may well
	// be looking at "all clusters" or at a different one when they click it.
	// Reading the connector off the request meant the panel queried whichever
	// cluster the UI happened to be pointed at — which, when that was not this
	// finding's cluster, surfaced as a bare 404 from the apiserver rather than
	// anything an operator could act on.
	//
	// The two ids also differ in shape: the record carries the kube-system UID,
	// while runtime routing keys on the context name — `agent:<uid>` for an
	// agent-proxy cluster, a plain name (`in-cluster`, a kubeconfig entry) for a
	// direct one. The resolver covers both; it only covered the first before, so
	// a direct cluster's UID came back unchanged and the routing key was set to
	// something no runtime answers to → "cluster not connected" about a cluster
	// that was live. That is the self-monitored case, which in EE self-hosted is
	// the customer's main cluster.
	//
	// The org is not optional: the persisted UID map is RLS-scoped, so resolving
	// with the wrong one finds nothing (finding #17).
	detailCtx := r.Context()
	if name := h.manager.ContextNameForClusterID(h.activeTenantID(r), rec.ClusterID); name != "" {
		key := cluster.RuntimeKeyFromContext(detailCtx)
		key.Cluster = name
		detailCtx = cluster.WithRuntimeKey(detailCtx, key)
	}

	conn := h.manager.Connector(detailCtx)
	if conn == nil || conn.Dynamic() == nil {
		resp.LiveError = "cluster not connected — showing the stored finding only"
		respondJSON(w, http.StatusOK, resp)
		return
	}

	ctx, cancel := context.WithTimeout(detailCtx, findingDetailTimeout)
	defer cancel()

	switch rec.Kind {
	case integrations.FindingCVE:
		images, err := collectAffectedImages(ctx, conn, rec)
		if err != nil {
			resp.LiveError = err.Error()
			respondJSON(w, http.StatusOK, resp)
			return
		}
		resp.Live = true
		resp.Images = images
	case integrations.FindingMisconfig:
		detail, err := collectComplianceDetail(ctx, conn, rec)
		if err != nil {
			resp.LiveError = err.Error()
			respondJSON(w, http.StatusOK, resp)
			return
		}
		resp.Live = true
		resp.Compliance = detail
	default:
		// A policy violation is already whole in the stored record.
		resp.Live = true
	}
	respondJSON(w, http.StatusOK, resp)
}

// findingDetailSource is the slice of *cluster.Connector this file needs —
// declared locally so the detail path does not widen the handler's coupling,
// and so a test can drive it with a fake dynamic client.
type findingDetailSource interface {
	Dynamic() dynamic.Interface
	// The third return (settled) is deliberately ignored here: this path
	// MATCHES against an already-stored finding rather than minting its
	// identity, so an unresolved ReplicaSet name is a missed row on one
	// render, not a duplicate record. The sweep is where it must be honored.
	WorkloadOwner(namespace, kind, name string) (string, string, bool)
	CountPodsRunningImage(namespace, image string) int
}

// collectVulnPackages re-reads Trivy's VulnerabilityReports for the finding's
// namespace and returns every package entry matching its CVE.
//
// The list is NAMESPACED, not cluster-wide: this runs on a user click, and on
// an agent-proxy cluster every apiserver round-trip costs real seconds over the
// tunnel. Scoping to the one namespace keeps a click cheap.
//
// Matching walks the same collapse the sweep does — reports are labelled with
// the ReplicaSet, so each candidate's owner is resolved before comparing with
// the stored resource. Without that, a finding stored against a Deployment
// would never match its own reports.
func collectAffectedImages(ctx context.Context, conn findingDetailSource, rec *findings.Record) ([]affectedImage, error) {
	cve := cveIDFromTitle(rec.Title)
	if cve == "" {
		return nil, nil
	}
	// Cluster-wide LIST narrowed by a LABEL SELECTOR rather than a namespaced
	// one. Two reasons, and either alone would decide it:
	//
	//   - The namespaced path 404s over the agent-proxy transport. Verified
	//     against the live dev cluster: `-n argocd` returns 9 reports through
	//     kubectl and "the server could not find the requested resource"
	//     through the tunnel, while the cluster-wide form the sweep uses works.
	//   - The selector still filters SERVER-side, so this is not the over-fetch
	//     it looks like: 9 objects came back here, not the cluster's 57.
	sel := metav1.ListOptions{}
	if rec.ResourceNamespace != "" {
		sel.LabelSelector = "trivy-operator.resource.namespace=" + rec.ResourceNamespace
	}
	list, err := conn.Dynamic().Resource(integrations.TrivyVulnerabilityReportGVR).
		Namespace(metav1.NamespaceAll).List(ctx, sel)
	if err != nil {
		return nil, err
	}

	// Keyed by image: two containers sharing one are a single thing to rebuild.
	byImage := map[string]*affectedImage{}
	order := make([]string, 0, 2)
	for i := range list.Items {
		item := &list.Items[i]
		labels := item.GetLabels()
		kind, name := labels["trivy-operator.resource.kind"], labels["trivy-operator.resource.name"]
		if kind == "" || name == "" {
			continue
		}
		kind, name, _ = conn.WorkloadOwner(labels["trivy-operator.resource.namespace"], kind, name)
		if kind != rec.ResourceKind || name != rec.ResourceName {
			continue
		}

		vulns, found, _ := unstructuredSlice(item.Object, "report", "vulnerabilities")
		if !found {
			continue
		}
		pkgs := make([]vulnPackage, 0, 4)
		seen := map[string]bool{}
		for _, raw := range vulns {
			v, _ := raw.(map[string]interface{})
			if v == nil || asString(v["vulnerabilityID"]) != cve {
				continue
			}
			pkg := vulnPackage{
				Name:             asString(v["resource"]),
				InstalledVersion: asString(v["installedVersion"]),
				FixedVersion:     asString(v["fixedVersion"]),
				Severity:         asString(v["severity"]),
				Link:             asString(v["primaryLink"]),
			}
			if s, ok := v["score"].(float64); ok {
				pkg.Score = s
			}
			if key := pkg.Name + "|" + pkg.InstalledVersion; !seen[key] {
				seen[key] = true
				pkgs = append(pkgs, pkg)
			}
		}
		if len(pkgs) == 0 {
			continue // this image is not affected by THIS cve
		}

		ref := imageRef(item.Object)
		img, ok := byImage[ref]
		if !ok {
			img = &affectedImage{
				Image:  ref,
				Digest: nestedString(item.Object, "report", "artifact", "digest"),
				OS:     osLabel(item.Object),
				// Counted once per image, not per report — the pods running it
				// are a property of the image, and asking twice would both
				// double the work and invite two different answers.
				Pods:     conn.CountPodsRunningImage(rec.ResourceNamespace, ref),
				Packages: pkgs,
			}
			byImage[ref] = img
			order = append(order, ref)
		}
		if c := labels["trivy-operator.container.name"]; c != "" && !contains(img.Containers, c) {
			img.Containers = append(img.Containers, c)
		}
	}

	out := make([]affectedImage, 0, len(order))
	for _, ref := range order {
		img := byImage[ref]
		sort.Strings(img.Containers)
		// Fixable first: a package with no upstream fix is not actionable and
		// must not push one that is below the fold.
		sort.SliceStable(img.Packages, func(i, j int) bool {
			if (img.Packages[i].FixedVersion != "") != (img.Packages[j].FixedVersion != "") {
				return img.Packages[i].FixedVersion != ""
			}
			return img.Packages[i].Name < img.Packages[j].Name
		})
		out = append(out, *img)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Image < out[j].Image })
	return out, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// imageRef rebuilds the reference an operator can pull, from the three fields
// Trivy splits it across: registry.server + artifact.repository + artifact.tag.
// Falls back to the digest when the image was deployed without a tag.
func imageRef(obj map[string]interface{}) string {
	repo := nestedString(obj, "report", "artifact", "repository")
	if repo == "" {
		return ""
	}
	ref := repo
	if server := nestedString(obj, "report", "registry", "server"); server != "" {
		ref = server + "/" + ref
	}
	if tag := nestedString(obj, "report", "artifact", "tag"); tag != "" {
		return ref + ":" + tag
	}
	if dig := nestedString(obj, "report", "artifact", "digest"); dig != "" {
		return ref + "@" + dig
	}
	return ref
}

// osLabel is the image's base distro — frequently the actual explanation for a
// pile of CVEs on one workload, since a stale base image drags them all in.
func osLabel(obj map[string]interface{}) string {
	family := nestedString(obj, "report", "os", "family")
	name := nestedString(obj, "report", "os", "name")
	switch {
	case family != "" && name != "":
		return family + " " + name
	case family != "":
		return family
	default:
		return name
	}
}

func nestedString(obj map[string]interface{}, path ...string) string {
	cur := obj
	for _, p := range path[:len(path)-1] {
		next, ok := cur[p].(map[string]interface{})
		if !ok {
			return ""
		}
		cur = next
	}
	s, _ := cur[path[len(path)-1]].(string)
	return s
}

// cveIDFromTitle recovers the identifier from a stored title, which the Trivy
// provider formats as "<id>: <description>". The id is not persisted on its own
// — this is the seam where that shows.
func cveIDFromTitle(title string) string {
	if i := strings.Index(title, ":"); i > 0 {
		return strings.TrimSpace(title[:i])
	}
	return strings.TrimSpace(title)
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func unstructuredSlice(obj map[string]interface{}, path ...string) ([]interface{}, bool, error) {
	cur := obj
	for _, p := range path[:len(path)-1] {
		next, ok := cur[p].(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	s, ok := cur[path[len(path)-1]].([]interface{})
	return s, ok, nil
}

// collectComplianceDetail answers "which 42?" for a failing CIS control.
//
// The stored finding carries only a count, because that is all Trivy's SUMMARY
// report publishes. The names live one hop away and the hop is documented in
// the data: a control declares `checks: [{id: AVD-KSV-0004}]`, and every
// ConfigAuditReport records that same check id per resource with success
// true/false. Following it turns "42 failing" into 42 workloads an operator can
// go fix.
//
// Two LISTs, both cluster-wide because compliance IS cluster-wide and because
// the namespaced path 404s over the agent-proxy tunnel. Runs on a click, not on
// the sweep's timer.
func collectComplianceDetail(ctx context.Context, conn findingDetailSource, rec *findings.Record) (*complianceDetail, error) {
	dyn := conn.Dynamic()
	reports, err := dyn.Resource(integrations.TrivyComplianceReportGVR).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := &complianceDetail{Control: rec.CISControl}
	checkIDs := map[string]bool{}
	for i := range reports.Items {
		spec, ok, _ := unstructuredMap(reports.Items[i].Object, "spec", "compliance")
		if !ok {
			continue
		}
		controls, _ := spec["controls"].([]interface{})
		for _, raw := range controls {
			c, _ := raw.(map[string]interface{})
			if c == nil || asString(c["id"]) != rec.CISControl {
				continue
			}
			out.Benchmark = asString(spec["title"])
			out.Description = asString(c["description"])
			out.Severity = strings.ToLower(asString(c["severity"]))
			checks, _ := c["checks"].([]interface{})
			for _, rawCheck := range checks {
				if ch, _ := rawCheck.(map[string]interface{}); ch != nil {
					if id := asString(ch["id"]); id != "" {
						checkIDs[id] = true
					}
				}
			}
		}
	}
	if len(checkIDs) == 0 {
		// The control exists but declares no check we can follow (several
		// control-plane controls are node-level, audited by a different rail).
		// Returning the description alone is still more than the count.
		return out, nil
	}

	// Search EVERY report type a control's checks can land in. Which one holds
	// the answer depends on what the control is about — a workload control lands
	// in configauditreports, an RBAC one in rbacassessmentreports, and a
	// control-plane one ("ensure --audit-log-maxage is set") in the infra
	// assessments, keyed by node. Searching only the first made every
	// control-plane control come back empty, which reads as "nothing fails"
	// rather than "we looked in the wrong drawer".
	items := make([]unstructured.Unstructured, 0, 16)
	for _, gvr := range integrations.TrivyCheckReportGVRs {
		list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue // CRD absent for this scanner build; the others still answer
		}
		items = append(items, list.Items...)
	}
	if len(items) == 0 {
		// The description survives even when no report could be read.
		return out, nil
	}
	for i := range items {
		item := &items[i]
		labels := item.GetLabels()
		checks, found, _ := unstructuredSlice(item.Object, "report", "checks")
		if !found {
			continue
		}
		for _, raw := range checks {
			ch, _ := raw.(map[string]interface{})
			if ch == nil || !checkIDs[asString(ch["checkID"])] {
				continue
			}
			if success, _ := ch["success"].(bool); success {
				continue
			}
			out.FailingTotal++
			if len(out.FailingResources) >= complianceResourceCap {
				continue // keep counting, stop listing
			}
			msg := ""
			if msgs, _ := ch["messages"].([]interface{}); len(msgs) > 0 {
				msg = asString(msgs[0])
			}
			out.FailingResources = append(out.FailingResources, failingResource{
				Kind:      labels["trivy-operator.resource.kind"],
				Namespace: labels["trivy-operator.resource.namespace"],
				Name:      labels["trivy-operator.resource.name"],
				Message:   msg,
			})
		}
	}
	sort.SliceStable(out.FailingResources, func(i, j int) bool {
		a, b := out.FailingResources[i], out.FailingResources[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out, nil
}

func unstructuredMap(obj map[string]interface{}, path ...string) (map[string]interface{}, bool, error) {
	cur := obj
	for _, p := range path {
		next, ok := cur[p].(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return cur, true, nil
}
