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

// Kyverno integration (E2 SEC-D) — the second CRD provider on the
// findings rail. Kyverno (and Gatekeeper via the same PolicyReport
// standard) writes wgpolicyk8s.io PolicyReports; failed results
// become policy_violation findings on the Security & Compliance
// dashboard, next to Trivy's CVEs, with zero new machinery — the
// sweep already speaks CRDSignalProvider.
const (
	KyvernoID   = "kyverno"
	KyvernoName = "Kyverno"

	kyvernoLabelSelector = "app.kubernetes.io/part-of=kyverno"
)

var kyvernoFallbackNamespaces = []string{"kyverno", "kyverno-system"}

// PolicyReport GVRs (wgpolicyk8s.io/v1alpha2) — the namespaced report
// plus the cluster-scoped one. Gatekeeper installs producing the same
// standard feed this provider transparently.
var (
	KyvernoPolicyReportGVR        = schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"}
	KyvernoClusterPolicyReportGVR = schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"}
)

type kyvernoProvider struct{}

// NewKyverno constructs the Kyverno integration provider.
func NewKyverno() Provider { return &kyvernoProvider{} }

var _ CRDSignalProvider = (*kyvernoProvider)(nil)

func (p *kyvernoProvider) IngestGVRs() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{KyvernoPolicyReportGVR, KyvernoClusterPolicyReportGVR}
}

func (p *kyvernoProvider) Meta() Integration {
	return Integration{
		ID:          KyvernoID,
		Name:        KyvernoName,
		Description: "Policy enforcement. Failed PolicyReport results (Kyverno, or Gatekeeper via the same wgpolicyk8s.io standard) surface as policy-violation findings.",
		DocsURL:     "https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/kyverno.md",
		Capabilities: []string{
			"security.policy",
		},
	}
}

func (p *kyvernoProvider) Detect(ctx context.Context, cs kubernetes.Interface) (Integration, error) {
	meta := p.Meta()
	if cs == nil {
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: "no cluster connection"}
		return meta, nil
	}
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: kyvernoLabelSelector,
		Limit:         32,
	})
	if err != nil {
		meta.Status = StatusUnknown
		meta.Health = &Health{Message: fmt.Sprintf("could not list pods: %v", err)}
		return meta, nil
	}
	items := pods.Items
	if len(items) == 0 {
		for _, ns := range kyvernoFallbackNamespaces {
			nsPods, nsErr := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 32})
			if nsErr != nil {
				continue
			}
			for i := range nsPods.Items {
				if strings.HasPrefix(nsPods.Items[i].Name, "kyverno") {
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
		health.Message = "Kyverno pods not fully ready"
		return meta, nil
	}
	meta.Status = StatusInstalled
	return meta, nil
}

func (p *kyvernoProvider) IngestMode() IngestPattern { return IngestCRD }

// kyvernoSeverities maps the PolicyReport severity vocabulary onto
// the normalized ladder. Unlike CVEs, EVERY failed policy result is a
// finding — an operator wrote that policy on purpose — so unknown/
// empty severities default to medium instead of being dropped.
var kyvernoSeverities = map[string]FindingSeverity{
	"critical": SeverityCritical,
	"high":     SeverityHigh,
	"medium":   SeverityMedium,
	"low":      SeverityLow,
	"info":     SeverityInfo,
}

// Normalize flattens one PolicyReport into policy_violation findings —
// one per result with result=fail. warn/pass/skip/error are not
// violations (warn is audit-noise by design; error means the policy
// engine itself hiccupped and must not read as a resource violation).
func (p *kyvernoProvider) Normalize(ctx context.Context, raw any) (Signals, error) {
	obj, ok := raw.(*unstructured.Unstructured)
	if !ok {
		return Signals{}, fmt.Errorf("kyverno normalize: want *unstructured.Unstructured, got %T", raw)
	}

	results, found, err := unstructured.NestedSlice(obj.Object, "results")
	if err != nil || !found {
		return Signals{}, nil
	}

	var out Signals
	for _, r := range results {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if res, _ := rm["result"].(string); res != "fail" {
			continue
		}
		policy, _ := rm["policy"].(string)
		rule, _ := rm["rule"].(string)
		message, _ := rm["message"].(string)
		if policy == "" {
			continue
		}

		sev, ok := kyvernoSeverities[strings.ToLower(fmt.Sprint(rm["severity"]))]
		if !ok {
			sev = SeverityMedium
		}

		resKind, resNS, resName := policyReportSubject(obj, rm)

		// Title is POLICY/RULE and nothing else. The message used to ride here
		// too, and it cannot: Kyverno writes the JSON path into it, so the SAME
		// violation reads differently depending on where it was caught —
		//
		//	Pod:        …rule host-namespaces failed at path /spec/hostNetwork/
		//	DaemonSet:  …rule autogen-host-namespaces failed at path /spec/template/spec/hostNetwork/
		//
		// Since the title is part of the fingerprint, that made one problem into
		// two findings on the same workload, and any reword by Kyverno resolved
		// the old finding and minted a new one with the clock back at zero.
		// The message keeps all its value as the remediation, where it is read
		// and not compared.
		title := policy
		if r := canonicalRule(rule); r != "" && r != policy {
			title = policy + "/" + r
		}
		// Timestamp: PolicyReport results carry a per-result timestamp
		// only sporadically; the sweep's LastSeen carries recency, so
		// "now" is honest enough for DetectedAt here.
		out.Findings = append(out.Findings, Finding{
			Kind:              FindingPolicyViolation,
			Source:            KyvernoID,
			Severity:          sev,
			Title:             title,
			ResourceKind:      resKind,
			ResourceNamespace: resNS,
			ResourceName:      resName,
			Remediation:       message,
			DetectedAt:        time.Now().UTC(),
		})
	}
	return out, nil
}

// policyReportSubject finds the resource a PolicyReport result is about.
//
// Kyverno ≥1.9 writes ONE report per resource and names it in the report's
// top-level `scope`, mirrored in `ownerReferences`. It leaves
// `results[].resources` empty — which is where this looked, and only there, so
// on a real cluster all 70 violations arrived with no resource at all and would
// have landed in the "unassigned" bucket: counted, invisible, unfixable.
//
// `results[].resources` stays as the FIRST source because it is the field the
// wgpolicyk8s standard defines, and a Gatekeeper install (or an older Kyverno)
// still populates it. Scope is the fallback that makes current Kyverno work.
func policyReportSubject(obj *unstructured.Unstructured, result map[string]interface{}) (kind, namespace, name string) {
	namespace = obj.GetNamespace()

	if resources, ok := result["resources"].([]interface{}); ok && len(resources) > 0 {
		if r0, ok := resources[0].(map[string]interface{}); ok {
			kind, _ = r0["kind"].(string)
			name, _ = r0["name"].(string)
			if ns, _ := r0["namespace"].(string); ns != "" {
				namespace = ns
			}
			if kind != "" || name != "" {
				return kind, namespace, name
			}
		}
	}

	if scope, found, _ := unstructured.NestedMap(obj.Object, "scope"); found {
		kind, _ = scope["kind"].(string)
		name, _ = scope["name"].(string)
		if ns, _ := scope["namespace"].(string); ns != "" {
			namespace = ns
		}
		if kind != "" || name != "" {
			return kind, namespace, name
		}
	}

	for _, ref := range obj.GetOwnerReferences() {
		if ref.Name != "" {
			return ref.Kind, namespace, ref.Name
		}
	}
	return "", namespace, ""
}

// canonicalRule strips Kyverno's `autogen-` prefix.
//
// Kyverno validates a workload twice: the pod against the rule, and the
// controller's template against an auto-generated twin of it. Same policy, same
// defect, two names — and since the rule rides in the title, and the title in
// the fingerprint, the two never converge even after the pod is collapsed onto
// its owner. Stripping the prefix is what lets one problem read as one row.
func canonicalRule(rule string) string {
	return strings.TrimPrefix(rule, "autogen-")
}
