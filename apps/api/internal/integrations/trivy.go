package integrations

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
)

// Trivy Operator integration (E2 SEC-B) — the first security
// SignalProvider on the E1 framework. Detection-card + Normalize:
// the operator writes VulnerabilityReport CRDs next to each workload;
// the ingest sweep lists them per cluster and hands each report here,
// which flattens it into normalized CVE findings for the
// FindingsStore (Security & Compliance dashboard + Kobi's
// query_findings).
const (
	TrivyID   = "trivy"
	TrivyName = "Trivy Operator"

	// trivyLabelSelector matches the official Helm chart's operator
	// Deployment.
	trivyLabelSelector = "app.kubernetes.io/name=trivy-operator"
)

var trivyFallbackNamespaces = []string{"trivy-system", "trivy-operator", "security"}

// TrivyVulnerabilityReportGVR is the CRD the ingest sweep lists.
var TrivyVulnerabilityReportGVR = schema.GroupVersionResource{
	Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "vulnerabilityreports",
}

// TrivyComplianceReportGVR — the operator's built-in CIS benchmark
// (SEC-D design adjustment: the roadmap listed standalone kube-bench
// via a cronParse rail; Trivy Operator ships the SAME CIS checks as a
// cluster-scoped CRD, so compliance rides the existing CRD rail with
// zero new machinery. Standalone kube-bench stays possible later.)
var TrivyComplianceReportGVR = schema.GroupVersionResource{
	Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "clustercompliancereports",
}

// TrivyConfigAuditReportGVR is Trivy's per-workload misconfiguration report.
//
// NOT yet part of the ingest sweep — adding it is a scoped follow-up (744
// failing checks on the dev cluster, 162 of them HIGH). It is declared here
// because the compliance DRILL-DOWN already needs it: a CIS control names the
// check ids it is evaluated by, and this is where those checks record which
// resource passed or failed. Without it, "42 failing" can never name the 42.
var TrivyConfigAuditReportGVR = schema.GroupVersionResource{
	Group:    "aquasecurity.github.io",
	Version:  "v1alpha1",
	Resource: "configauditreports",
}

// TrivyRbacAssessmentReportGVR and the two infra-assessment GVRs complete the
// set a CIS control can be evaluated by. Which one holds the answer depends on
// what the control is about, and the check-id prefix says which:
//
//	AVD-KSV-*  workload / RBAC     → configauditreports, rbacassessmentreports
//	AVD-KCV-*  control plane, node → infraassessmentreports (+ the cluster-scoped one)
//
// Searching only configauditreports made every control-plane control ("ensure
// --audit-log-maxage is set") come back with no resources, which reads as "we
// have nothing" rather than "we looked in the wrong drawer".
var TrivyRbacAssessmentReportGVR = schema.GroupVersionResource{
	Group:    "aquasecurity.github.io",
	Version:  "v1alpha1",
	Resource: "rbacassessmentreports",
}

// TrivyExposedSecretReportGVR is Trivy's scan for credentials baked into an
// image — API keys, tokens, private keys sitting in a layer.
//
// Same report shape as VulnerabilityReport (workload identity in the labels,
// artifact/registry for the image ref), so it reuses imageRefFromReport and the
// ReplicaSet→Deployment collapse without special-casing.
var TrivyExposedSecretReportGVR = schema.GroupVersionResource{
	Group:    "aquasecurity.github.io",
	Version:  "v1alpha1",
	Resource: "exposedsecretreports",
}

// TrivyClusterRbacAssessmentReportGVR is the cluster-scoped half of the RBAC
// assessment, and it is where the severity lives: measured on the dev cluster,
// the namespaced Roles produced 19 failing checks, ALL medium, while the
// ClusterRoles produced 76 — 30 of them critical. Ingesting only the namespaced
// one would have shown the harmless half of the picture.
var TrivyClusterRbacAssessmentReportGVR = schema.GroupVersionResource{
	Group:    "aquasecurity.github.io",
	Version:  "v1alpha1",
	Resource: "clusterrbacassessmentreports",
}

var TrivyInfraAssessmentReportGVR = schema.GroupVersionResource{
	Group:    "aquasecurity.github.io",
	Version:  "v1alpha1",
	Resource: "infraassessmentreports",
}

var TrivyClusterInfraAssessmentReportGVR = schema.GroupVersionResource{
	Group:    "aquasecurity.github.io",
	Version:  "v1alpha1",
	Resource: "clusterinfraassessmentreports",
}

// TrivyCheckReportGVRs is every report type a compliance control's checks can
// land in, in the order the drill-down searches them.
var TrivyCheckReportGVRs = []schema.GroupVersionResource{
	TrivyConfigAuditReportGVR,
	TrivyRbacAssessmentReportGVR,
	TrivyClusterRbacAssessmentReportGVR,
	TrivyInfraAssessmentReportGVR,
	TrivyClusterInfraAssessmentReportGVR,
}

type trivyProvider struct{}

// NewTrivy constructs the Trivy Operator integration provider.
func NewTrivy() Provider { return &trivyProvider{} }

var _ CRDSignalProvider = (*trivyProvider)(nil)

// IngestGVRs — the five report types the sweep lists per cluster: CVEs, CIS
// controls, workload misconfiguration, and both halves of the RBAC assessment.
//
// Each one is a serial cluster-wide LIST that crosses the agent tunnel, so the
// count is not free. It is affordable because cost tracks PAYLOAD, not objects:
// measured on the dev cluster, 57 vulnerability reports took 110ms while 120
// config-audit reports took 29ms — a vulnerability report carries hundreds of
// CVEs, an assessment report carries a handful of checks. The RBAC pair is
// assessment-shaped, so it lands in the cheap band.
func (p *trivyProvider) IngestGVRs() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{
		TrivyVulnerabilityReportGVR,
		TrivyComplianceReportGVR,
		TrivyConfigAuditReportGVR,
		TrivyRbacAssessmentReportGVR,
		TrivyClusterRbacAssessmentReportGVR,
		TrivyExposedSecretReportGVR,
	}
}

func (p *trivyProvider) Meta() Integration {
	return Integration{
		ID:          TrivyID,
		Name:        TrivyName,
		Description: "Vulnerability and misconfiguration scanning. The operator's VulnerabilityReports feed KubeBolt's Security & Compliance dashboard as normalized CVE findings.",
		DocsURL:     "https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/trivy.md",
		Capabilities: []string{
			"security.cve",
			"security.misconfig",
		},
	}
}

func (p *trivyProvider) Detect(ctx context.Context, cs kubernetes.Interface) (Integration, error) {
	meta := p.Meta()
	if cs == nil {
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: "no cluster connection"}
		return meta, nil
	}
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: trivyLabelSelector,
		Limit:         32,
	})
	if err != nil {
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: fmt.Sprintf("could not list pods: %v", err)}
		return meta, nil
	}
	items := pods.Items
	if len(items) == 0 {
		for _, ns := range trivyFallbackNamespaces {
			nsPods, nsErr := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 32})
			if nsErr != nil {
				continue
			}
			for i := range nsPods.Items {
				if strings.HasPrefix(nsPods.Items[i].Name, "trivy-operator") {
					items = append(items, nsPods.Items[i])
				}
			}
			if len(items) > 0 {
				break
			}
		}
	}
	if len(items) == 0 {
		meta.Status = StatusNotInstalled
		return meta, nil
	}

	first := items[0]
	meta.Namespace = first.Namespace
	meta.Version = first.Labels["app.kubernetes.io/version"]
	meta.Managed = first.Labels["app.kubernetes.io/managed-by"] == "kubebolt"
	health := &Health{}
	for i := range items {
		if items[i].Namespace != first.Namespace {
			continue
		}
		health.PodsDesired++
		if isPodReady(&items[i]) {
			health.PodsReady++
		}
	}
	meta.Health = health
	if health.PodsReady == 0 || health.PodsReady < health.PodsDesired {
		meta.Status = StatusDegraded
		health.Message = "Trivy Operator pods not fully ready"
		return meta, nil
	}
	meta.Status = StatusInstalled
	return meta, nil
}

func (p *trivyProvider) IngestMode() IngestPattern { return IngestCRD }

// trivySeverities maps Trivy's ladder onto the normalized one. Only
// CRITICAL and HIGH become findings in v1 — the dashboard's job is
// the actionable list, and MEDIUM/LOW on a busy cluster is thousands
// of rows of noise (aggregate counts can ship later as samples).
var trivySeverities = map[string]FindingSeverity{
	"CRITICAL": SeverityCritical,
	"HIGH":     SeverityHigh,
}

// Normalize flattens one VulnerabilityReport (unstructured CRD
// object) into CVE findings. Pure: no cluster calls, no stores —
// the sweep owns I/O and tenant stamping.
//
// Report shape (aquasecurity.github.io/v1alpha1): the workload
// identity rides trivy-operator.resource.* labels; vulnerabilities
// live under report.vulnerabilities[].
func (p *trivyProvider) Normalize(ctx context.Context, raw any) (Signals, error) {
	obj, ok := raw.(*unstructured.Unstructured)
	if !ok {
		return Signals{}, fmt.Errorf("trivy normalize: want *unstructured.Unstructured, got %T", raw)
	}
	switch obj.GetKind() {
	case "ConfigAuditReport":
		return normalizeConfigAudit(obj)
	case "ClusterComplianceReport":
		return normalizeComplianceReport(obj), nil
	case "RbacAssessmentReport", "ClusterRbacAssessmentReport":
		return normalizeRbacAssessment(obj), nil
	case "ExposedSecretReport":
		return normalizeExposedSecrets(obj), nil
	}

	labels := obj.GetLabels()
	resourceKind := labels["trivy-operator.resource.kind"]
	resourceName := labels["trivy-operator.resource.name"]
	resourceNS := labels["trivy-operator.resource.namespace"]
	if resourceNS == "" {
		resourceNS = obj.GetNamespace()
	}

	detectedAt := time.Now().UTC()
	if ts, found, _ := unstructured.NestedString(obj.Object, "report", "updateTimestamp"); found {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			detectedAt = t
		}
	}

	vulns, found, err := unstructured.NestedSlice(obj.Object, "report", "vulnerabilities")
	if err != nil || !found {
		return Signals{}, nil // empty report — nothing to normalize
	}

	var out Signals
	for _, v := range vulns {
		vm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		rawSev, _ := vm["severity"].(string)
		sev, actionable := trivySeverities[strings.ToUpper(rawSev)]
		if !actionable {
			continue
		}
		cveID, _ := vm["vulnerabilityID"].(string)
		if cveID == "" {
			continue
		}
		title, _ := vm["title"].(string)
		pkg, _ := vm["resource"].(string)
		fixed, _ := vm["fixedVersion"].(string)
		installed, _ := vm["installedVersion"].(string)

		findingTitle := cveID
		if title != "" {
			findingTitle = cveID + ": " + title
		}
		remediation := ""
		if fixed != "" {
			remediation = fmt.Sprintf("upgrade %s %s → %s", pkg, installed, fixed)
		}

		out.Findings = append(out.Findings, Finding{
			Kind:              FindingCVE,
			Source:            TrivyID,
			Severity:          sev,
			Title:             findingTitle,
			ResourceKind:      resourceKind,
			ResourceNamespace: resourceNS,
			ResourceName:      resourceName,
			Remediation:       remediation,
			Image:             imageRefFromReport(obj),
			DetectedAt:        detectedAt,
		})
	}
	return out, nil
}

// normalizeComplianceReport flattens the operator's CIS report into
// one misconfig finding per FAILED control. Cluster-scoped: no
// resource identity; CISControl carries the control id, which the
// dashboard and Kobi's get_cis_score derive posture from. Passing
// controls produce nothing — the actionable list stays actionable.
func normalizeComplianceReport(obj *unstructured.Unstructured) Signals {
	// A control declares WHICH checks evaluate it, in the spec. Those ids are
	// how we know a control is a ROLLUP of findings we already store one by one
	// — see rolledUpByIngestedChecks.
	controlChecks := complianceControlChecks(obj)
	checks, found, err := unstructured.NestedSlice(obj.Object, "status", "summaryReport", "controlCheck")
	if err != nil || !found {
		return Signals{}
	}
	benchmark, _, _ := unstructured.NestedString(obj.Object, "spec", "compliance", "title")
	if benchmark == "" {
		benchmark = obj.GetName()
	}

	var out Signals
	for _, c := range checks {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		fails := int64(0)
		switch v := cm["totalFail"].(type) {
		case int64:
			fails = v
		case float64:
			fails = int64(v)
		}
		if fails <= 0 {
			continue
		}
		id, _ := cm["id"].(string)
		name, _ := cm["name"].(string)
		if id == "" {
			continue
		}
		sev := SeverityMedium
		if s, ok := trivySeverities[strings.ToUpper(fmt.Sprint(cm["severity"]))]; ok {
			sev = s
		}
		out.Findings = append(out.Findings, Finding{
			Kind:        FindingMisconfig,
			Source:      TrivyID,
			Severity:    sev,
			Title:       fmt.Sprintf("CIS %s — %s (%d failing)", id, name, fails),
			CISControl:  id,
			Remediation: "see " + benchmark + " control " + id,
			Benchmark:   benchmark,
			Rollup:      rolledUpByIngestedChecks(controlChecks[id]),
			DetectedAt:  time.Now().UTC(),
		})
	}
	return out
}

// configAuditSeverities is the ingest gate for workload misconfiguration.
//
// It is DELIBERATELY wider than trivySeverities (CVEs, critical+high only) and
// narrower than Kyverno's (everything). The cut was made from the real
// distribution on a dev cluster — 162 HIGH, 249 MEDIUM, 499 LOW — by reading
// what each band actually contains:
//
//   - HIGH   host network, host ports, writable root filesystem. Security.
//   - MEDIUM "runs as root", "can elevate its own privileges", "seccomp
//     disabled". These matter MORE than several of the HIGHs; dropping them to
//     mirror the CVE gate would have thrown away the findings an operator most
//     wants.
//   - LOW    dominated by "runs with UID/GID <= 10000" and "CPU/memory not
//     limited". The latter is a CAPACITY concern that KubeBolt already raises as
//     an insight — surfacing it again under Security would be duplicate noise
//     wearing a scarier badge.
//
// Widening to LOW is a cardinality decision (it would nearly triple the rows),
// which makes it a natural per-plan lever rather than a default.
var configAuditSeverities = map[string]FindingSeverity{
	"CRITICAL": SeverityCritical,
	"HIGH":     SeverityHigh,
	"MEDIUM":   SeverityMedium,
}

// normalizeConfigAudit turns a ConfigAuditReport into one finding per FAILING
// check. Unlike CIS controls — which describe the cluster and carry no resource
// — these name the exact workload, which is what makes them actionable.
func normalizeConfigAudit(obj *unstructured.Unstructured) (Signals, error) {
	labels := obj.GetLabels()
	resourceKind := labels["trivy-operator.resource.kind"]
	resourceName := labels["trivy-operator.resource.name"]
	resourceNS := labels["trivy-operator.resource.namespace"]
	if resourceNS == "" {
		resourceNS = obj.GetNamespace()
	}

	checks, found, _ := unstructured.NestedSlice(obj.Object, "report", "checks")
	if !found {
		return Signals{}, nil
	}

	detectedAt := time.Now().UTC()
	if ts, ok, _ := unstructured.NestedString(obj.Object, "report", "updateTimestamp"); ok {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			detectedAt = parsed
		}
	}

	sig := Signals{}
	for _, raw := range checks {
		check, _ := raw.(map[string]interface{})
		if check == nil {
			continue
		}
		if success, _ := check["success"].(bool); success {
			continue // a passing check is not a finding
		}
		sevRaw, _ := check["severity"].(string)
		severity, actionable := configAuditSeverities[strings.ToUpper(sevRaw)]
		if !actionable {
			continue
		}
		title, _ := check["title"].(string)
		checkID, _ := check["checkID"].(string)
		if title == "" {
			title = checkID
		}
		remediation, _ := check["remediation"].(string)
		if desc, _ := check["description"].(string); remediation == "" {
			remediation = desc
		}

		sig.Findings = append(sig.Findings, Finding{
			Kind:     FindingMisconfig,
			Source:   TrivyID,
			Severity: severity,
			// The check id rides in the title so the fingerprint stays stable
			// even if Trivy rewords a check between releases — a reworded title
			// would otherwise resolve the old finding and create a new one, the
			// same churn a rollout used to cause.
			Title:             checkID + ": " + title,
			ResourceKind:      resourceKind,
			ResourceNamespace: resourceNS,
			ResourceName:      resourceName,
			Remediation:       remediation,
			DetectedAt:        detectedAt,
		})
	}
	return sig, nil
}

// normalizeRbacAssessment turns an RbacAssessmentReport (namespaced Role) or a
// ClusterRbacAssessmentReport (ClusterRole) into one finding per failing check.
//
// This is the first source of FindingRBACIssue, and it answers a question no
// other lens does: not how a pod is configured, but WHO CAN DO WHAT. An
// over-permissive ClusterRole is invisible to image scanning and to workload
// misconfiguration, and it is how a contained compromise becomes a cluster one.
//
// Trivy records ONLY failures here — every check in these reports has
// success:false — so there is no passing branch to skip. Verified on the dev
// cluster: 95 checks, 95 failing.
func normalizeRbacAssessment(obj *unstructured.Unstructured) Signals {
	labels := obj.GetLabels()
	resourceKind := labels["trivy-operator.resource.kind"]
	resourceName := rbacSubjectName(obj)
	resourceNS := labels["trivy-operator.resource.namespace"]
	if resourceNS == "" {
		resourceNS = obj.GetNamespace()
	}
	if resourceName == "" || isKubernetesBuiltinRole(resourceName) {
		return Signals{}
	}

	checks, found, _ := unstructured.NestedSlice(obj.Object, "report", "checks")
	if !found {
		return Signals{}
	}

	var out Signals
	for _, raw := range checks {
		check, _ := raw.(map[string]interface{})
		if check == nil {
			continue
		}
		if success, _ := check["success"].(bool); success {
			continue
		}
		sevRaw, _ := check["severity"].(string)
		severity, actionable := configAuditSeverities[strings.ToUpper(sevRaw)]
		if !actionable {
			continue
		}
		checkID, _ := check["checkID"].(string)
		title, _ := check["title"].(string)
		if title == "" {
			title = checkID
		}
		remediation, _ := check["remediation"].(string)

		out.Findings = append(out.Findings, Finding{
			Kind:     FindingRBACIssue,
			Source:   TrivyID,
			Severity: severity,
			// Same check-id-first shape as config audit, for the same reason: the
			// title is part of the fingerprint and Trivy rewords checks between
			// releases, which would resolve the old finding and mint a new one.
			Title:             checkID + ": " + title,
			ResourceKind:      resourceKind,
			ResourceNamespace: resourceNS,
			ResourceName:      resourceName,
			Remediation:       remediation,
			// The report carries no updateTimestamp — unlike the vulnerability and
			// config-audit reports, which do. Ingest time is the honest stand-in;
			// the sweep preserves FirstSeen across passes, so age still tracks the
			// finding, not this field.
			DetectedAt: time.Now().UTC(),
		})
	}
	return out
}

// normalizeExposedSecrets turns an ExposedSecretReport into one finding per
// credential found baked into an image.
//
// NO SEVERITY GATE, deliberately, and it is the only source without one. The
// gates on CVEs and config-audit exist to control cardinality — thousands of
// rows where the marginal one adds little. Exposed secrets are a handful, and a
// LOW one is still a live credential sitting in a layer anyone who can pull the
// image can read. There is no volume to protect against here, so a cut would
// buy nothing and could hide the finding that matters most on the page.
//
// THE MATCHED TEXT IS NOT STORED. Trivy ships a `match` field with the
// surrounding line, partially masked. Masking is its heuristic, not a guarantee,
// and persisting credential material into the findings table — which is then
// served to a browser and read by Kobi — trades a real secret for a nicer UI.
// The finding says WHERE (file and image), never WHAT. Whoever fixes it has to
// open the image anyway.
func normalizeExposedSecrets(obj *unstructured.Unstructured) Signals {
	labels := obj.GetLabels()
	resourceKind := labels["trivy-operator.resource.kind"]
	resourceName := labels["trivy-operator.resource.name"]
	resourceNS := labels["trivy-operator.resource.namespace"]
	if resourceNS == "" {
		resourceNS = obj.GetNamespace()
	}

	secrets, found, _ := unstructured.NestedSlice(obj.Object, "report", "secrets")
	if !found {
		return Signals{}
	}

	detectedAt := time.Now().UTC()
	if ts, ok, _ := unstructured.NestedString(obj.Object, "report", "updateTimestamp"); ok {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			detectedAt = parsed
		}
	}
	image := imageRefFromReport(obj)

	var out Signals
	for _, raw := range secrets {
		s, _ := raw.(map[string]interface{})
		if s == nil {
			continue
		}
		ruleID, _ := s["ruleID"].(string)
		title, _ := s["title"].(string)
		target, _ := s["target"].(string)
		if ruleID == "" && title == "" {
			continue
		}
		if title == "" {
			title = ruleID
		}
		severity, ok := trivyAllSeverities[strings.ToUpper(fmt.Sprint(s["severity"]))]
		if !ok {
			severity = SeverityHigh // unknown band: a credential is not a footnote
		}

		// The TARGET is part of the identity, not decoration: the same rule
		// matching two files is two secrets to remove, and collapsing them would
		// resolve one the moment the other is fixed.
		findingTitle := ruleID + ": " + title
		if target != "" {
			findingTitle += " in " + target
		}

		out.Findings = append(out.Findings, Finding{
			Kind:              FindingExposedSecret,
			Source:            TrivyID,
			Severity:          severity,
			Title:             findingTitle,
			ResourceKind:      resourceKind,
			ResourceNamespace: resourceNS,
			ResourceName:      resourceName,
			// Rotation is the half people skip. Removing the string from the layer
			// does nothing for a key that has already been pushed to a registry and
			// pulled by anyone — the credential must be assumed compromised.
			Remediation: "remove the credential from " + targetOrImage(target, image) +
				", rebuild the image, and ROTATE it — assume it is already compromised",
			Image:      image,
			DetectedAt: detectedAt,
		})
	}
	return out
}

// trivyAllSeverities maps every band Trivy can report. Separate from
// trivySeverities (which is a GATE, critical+high only) so that widening one
// never silently widens the other.
var trivyAllSeverities = map[string]FindingSeverity{
	"CRITICAL": SeverityCritical,
	"HIGH":     SeverityHigh,
	"MEDIUM":   SeverityMedium,
	"LOW":      SeverityLow,
}

func targetOrImage(target, image string) string {
	if target != "" {
		return target
	}
	if image != "" {
		return "image " + image
	}
	return "the image"
}

// rbacSubjectName recovers the Role/ClusterRole name the report is about.
//
// The obvious source is wrong for the cluster-scoped report. A ClusterRole name
// like `system:controller:persistent-volume-binder` is not a legal label value,
// so Trivy writes `trivy-operator.resource.name-hash: 54ccb57cc4` instead of
// `trivy-operator.resource.name` — and reading the usual label yields "", which
// would have stored 96 findings pointing at nothing. The full name is in the
// annotation, and in the ownerReference as a second source.
func rbacSubjectName(obj *unstructured.Unstructured) string {
	if n := obj.GetLabels()["trivy-operator.resource.name"]; n != "" {
		return n
	}
	if n := obj.GetAnnotations()["trivy-operator.resource.name"]; n != "" {
		return n
	}
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Name != "" {
			return ref.Name
		}
	}
	return ""
}

// isKubernetesBuiltinRole reports whether a role is part of Kubernetes' own
// bootstrap RBAC, which the sweep does not turn into findings.
//
// Not a cost cut — a correctness one. These roles carry
// `kubernetes.io/bootstrapping: rbac-defaults` and are RECONCILED BY THE
// CONTROL PLANE: edit one and kube-controller-manager puts it back. They are
// also byte-identical on every conformant cluster, so they say nothing about
// THIS customer's posture. "system:node should not manage pods" is a
// description of how Kubernetes works, filed as a critical finding.
//
// Measured on the dev cluster: 48 of the 95 failing RBAC checks sat on these
// roles, 15 of them critical — they would have outranked every real finding on
// the page and none of them could be acted on.
//
// Matching by name rather than by the label is what keeps Normalize pure (no
// cluster call), and it is sound: `system:` is reserved by Kubernetes for
// control-plane use, and the four unprefixed defaults are cluster-scoped names
// that cannot be taken by anything else. Verified against the live label — the
// bootstrap set is exactly `system:*` plus those four.
func isKubernetesBuiltinRole(name string) bool {
	if strings.HasPrefix(name, "system:") {
		return true
	}
	switch name {
	case "cluster-admin", "admin", "edit", "view":
		return true
	}
	return false
}

// complianceControlChecks maps control id → the check ids that evaluate it,
// read from the report's SPEC (the summary carries counts, not provenance).
func complianceControlChecks(obj *unstructured.Unstructured) map[string][]string {
	controls, found, err := unstructured.NestedSlice(obj.Object, "spec", "compliance", "controls")
	if err != nil || !found {
		return nil
	}
	out := make(map[string][]string, len(controls))
	for _, raw := range controls {
		c, _ := raw.(map[string]interface{})
		if c == nil {
			continue
		}
		id, _ := c["id"].(string)
		if id == "" {
			continue
		}
		checks, _ := c["checks"].([]interface{})
		for _, rawCheck := range checks {
			if ch, _ := rawCheck.(map[string]interface{}); ch != nil {
				if cid, _ := ch["id"].(string); cid != "" {
					out[id] = append(out[id], cid)
				}
			}
		}
	}
	return out
}

// rolledUpByIngestedChecks reports whether a control is evaluated by checks the
// sweep ALREADY stores individually — making the control's finding an aggregate
// of rows that are also present on their own.
//
// The marker is the AVD-KSV prefix: those are workload checks, published in the
// ConfigAuditReports this provider ingests. AVD-KCV-* are control-plane and node
// checks that live in the infra assessments, which the sweep does NOT ingest —
// so those controls are the ONLY way to see that class of problem and are never
// rollups.
//
// Verified on a live cluster: for all 35 controls where both sides had data, the
// control's own "N failing" equalled the summed failing results of exactly these
// checks. The duplication is arithmetic, not approximate.
func rolledUpByIngestedChecks(checkIDs []string) bool {
	for _, id := range checkIDs {
		if strings.HasPrefix(id, "AVD-KSV-") {
			return true
		}
	}
	return false
}

// imageRefFromReport rebuilds the pullable reference Trivy splits across three
// fields: registry.server + artifact.repository + artifact.tag. It is what an
// operator actually rebuilds, so a list of affected workloads without it names
// the symptom and hides the cure — two workloads sharing an image are ONE fix,
// and nothing in the row said so.
func imageRefFromReport(obj *unstructured.Unstructured) string {
	repo, _, _ := unstructured.NestedString(obj.Object, "report", "artifact", "repository")
	if repo == "" {
		return ""
	}
	ref := repo
	if server, _, _ := unstructured.NestedString(obj.Object, "report", "registry", "server"); server != "" {
		ref = server + "/" + ref
	}
	if tag, _, _ := unstructured.NestedString(obj.Object, "report", "artifact", "tag"); tag != "" {
		return ref + ":" + tag
	}
	return ref
}
