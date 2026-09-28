package api

import (
	"context"
	"errors"
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
	detail, err := readFindingDetail(r.Context(), h.findingsStore, h.manager, h.activeTenantID(r),
		r.URL.Query().Get("cluster"), fingerprint, ClusterScopeFrom(r.Context()).May)
	switch {
	case errors.Is(err, errFindingNotFound):
		respondError(w, http.StatusNotFound, "finding not found")
	case err != nil:
		respondError(w, http.StatusInternalServerError, "failed to read the finding")
	default:
		respondJSON(w, http.StatusOK, detail)
	}
}

// errFindingNotFound covers both "no such finding" and "not yours to read". A
// caller outside the finding's cluster learns nothing from the difference, and
// must not: telling them it exists is already telling them something.
var errFindingNotFound = errors.New("finding not found")

// readFindingDetail is the drill-down behind GET /findings/{fingerprint} and
// Kobi's get_finding_detail: the stored finding plus a live re-read of the
// scanner. Shared so the chat and the Security page say the same thing about
// the same row.
//
// may is the caller's entitlement. The route checked the ORG and nothing else:
// a team-narrowed user who had a fingerprint and a cluster id could open a
// finding of a cluster their teams do not own — the list next to it was
// narrowed (findings_scope.go), the drill-down was not.
func readFindingDetail(ctx context.Context, store findings.Store, manager *cluster.Manager,
	tenantID, clusterID, fingerprint string, may func(string) bool) (*findings.Detail, error) {
	rec, ok, err := store.Get(tenantID, clusterID, fingerprint)
	if err != nil {
		return nil, err
	}
	if !ok || rec == nil || (may != nil && !may(rec.ClusterID)) {
		return nil, errFindingNotFound
	}

	resp := &findings.Detail{Record: *rec}
	if manager == nil {
		resp.LiveError = "the cluster is not connected — showing the stored finding only"
		return resp, nil
	}

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
	detailCtx := ctx
	contextName := manager.ContextNameForClusterID(tenantID, rec.ClusterID)
	if contextName != "" {
		key := cluster.RuntimeKeyFromContext(detailCtx)
		key.Cluster = contextName
		detailCtx = cluster.WithRuntimeKey(detailCtx, key)
	}
	// The cluster's own name, for any message shown to the operator — never the
	// UID or the internal agent-proxy URL (in-vivo 2026-09-15: a live re-read of
	// a finding on a disconnected agent surfaced the raw
	// `Get "https://<uid>.agent.local/..." : channel: no agent connected`
	// string, which names the cluster by a UUID nobody recognizes).
	clusterLabel := manager.DisplayNameForCluster(ctx, rec.ClusterID)
	if clusterLabel == "" {
		clusterLabel = "the cluster"
	}

	conn := manager.Connector(detailCtx)
	if conn == nil || conn.Dynamic() == nil {
		resp.LiveError = clusterLabel + " is not connected — showing the stored finding only"
		return resp, nil
	}

	liveCtx, cancel := context.WithTimeout(detailCtx, findingDetailTimeout)
	defer cancel()

	switch rec.Kind {
	case integrations.FindingCVE:
		images, err := collectAffectedImages(liveCtx, conn, rec)
		if err != nil {
			resp.LiveError = liveReadError(clusterLabel, err)
			return resp, nil
		}
		resp.Live = true
		resp.Images = images
	case integrations.FindingMisconfig:
		detail, err := collectComplianceDetail(liveCtx, conn, rec)
		if err != nil {
			resp.LiveError = liveReadError(clusterLabel, err)
			return resp, nil
		}
		resp.Live = true
		resp.Compliance = detail
	default:
		// A policy violation is already whole in the stored record.
		resp.Live = true
	}
	return resp, nil
}

// liveReadError turns a failed live scanner re-read into a message safe to show
// the operator. The common failure is an agent-proxy cluster whose agent went
// offline: client-go wraps that as `Get "https://<uid>.agent.local/…" :
// channel: no agent connected for cluster: <uid>` — the internal routing URL
// and a UUID nobody recognizes. Collapse that whole family to a sentence that
// names the cluster; leave a genuinely different error (an RBAC 403 on the CRD,
// say) legible, since that one the operator can act on.
func liveReadError(clusterLabel string, err error) string {
	msg := err.Error()
	if strings.Contains(msg, "no agent connected") || strings.Contains(msg, ".agent.local") {
		return clusterLabel + " is not reachable right now — showing the stored finding only"
	}
	return msg
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
func collectAffectedImages(ctx context.Context, conn findingDetailSource, rec *findings.Record) ([]findings.AffectedImage, error) {
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
	byImage := map[string]*findings.AffectedImage{}
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
		pkgs := make([]findings.VulnPackage, 0, 4)
		seen := map[string]bool{}
		for _, raw := range vulns {
			v, _ := raw.(map[string]interface{})
			if v == nil || asString(v["vulnerabilityID"]) != cve {
				continue
			}
			pkg := findings.VulnPackage{
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
			img = &findings.AffectedImage{
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

	out := make([]findings.AffectedImage, 0, len(order))
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
func collectComplianceDetail(ctx context.Context, conn findingDetailSource, rec *findings.Record) (*findings.ComplianceDetail, error) {
	dyn := conn.Dynamic()
	reports, err := dyn.Resource(integrations.TrivyComplianceReportGVR).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := &findings.ComplianceDetail{Control: rec.CISControl}
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
			if len(out.FailingResources) >= findings.ComplianceResourceCap {
				continue // keep counting, stop listing
			}
			msg := ""
			if msgs, _ := ch["messages"].([]interface{}); len(msgs) > 0 {
				msg = asString(msgs[0])
			}
			out.FailingResources = append(out.FailingResources, findings.FailingResource{
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
