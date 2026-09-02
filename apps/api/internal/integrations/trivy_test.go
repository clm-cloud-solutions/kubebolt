package integrations

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

// vulnReport builds a realistic VulnerabilityReport unstructured
// object — the exact shape the Trivy Operator writes.
func vulnReport() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "VulnerabilityReport",
		"metadata": map[string]interface{}{
			"name":      "replicaset-payments-api-7d4f8b-app",
			"namespace": "production",
			"labels": map[string]interface{}{
				"trivy-operator.resource.kind":      "ReplicaSet",
				"trivy-operator.resource.name":      "payments-api-7d4f8b",
				"trivy-operator.resource.namespace": "production",
				"trivy-operator.container.name":     "app",
			},
		},
		"report": map[string]interface{}{
			"updateTimestamp": "2026-07-12T14:32:18Z",
			"vulnerabilities": []interface{}{
				map[string]interface{}{
					"vulnerabilityID": "CVE-2024-12345", "severity": "CRITICAL",
					"title":    "privilege escalation in libfoo",
					"resource": "libfoo", "installedVersion": "1.2.0", "fixedVersion": "1.2.9",
				},
				map[string]interface{}{
					"vulnerabilityID": "CVE-2025-777", "severity": "HIGH",
					"title": "buffer overflow", "resource": "zlib",
					"installedVersion": "1.3.0", "fixedVersion": "",
				},
				map[string]interface{}{ // below the actionable bar → skipped
					"vulnerabilityID": "CVE-2020-1", "severity": "MEDIUM", "title": "noise",
				},
				map[string]interface{}{ // malformed row → tolerated
					"severity": "CRITICAL",
				},
			},
		},
	}}
}

func TestTrivyNormalize(t *testing.T) {
	p := NewTrivy().(SignalProvider)
	if p.IngestMode() != IngestCRD {
		t.Fatalf("IngestMode = %q, want crd", p.IngestMode())
	}

	sig, err := p.Normalize(context.Background(), vulnReport())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(sig.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (CRITICAL+HIGH; MEDIUM and malformed skipped)", len(sig.Findings))
	}

	crit := sig.Findings[0]
	if crit.Kind != FindingCVE || crit.Source != "trivy" || crit.Severity != SeverityCritical {
		t.Fatalf("critical finding wrong: %+v", crit)
	}
	if crit.Title != "CVE-2024-12345: privilege escalation in libfoo" {
		t.Errorf("title = %q", crit.Title)
	}
	if crit.ResourceKind != "ReplicaSet" || crit.ResourceNamespace != "production" || crit.ResourceName != "payments-api-7d4f8b" {
		t.Errorf("workload identity from labels wrong: %+v", crit)
	}
	if !strings.Contains(crit.Remediation, "libfoo 1.2.0 → 1.2.9") {
		t.Errorf("remediation = %q", crit.Remediation)
	}
	if crit.DetectedAt.Format("2006-01-02") != "2026-07-12" {
		t.Errorf("detectedAt should come from report.updateTimestamp: %v", crit.DetectedAt)
	}

	high := sig.Findings[1]
	if high.Severity != SeverityHigh || high.Remediation != "" {
		t.Errorf("no-fix HIGH must have empty remediation: %+v", high)
	}

	// Wrong payload type errors instead of panicking.
	if _, err := p.Normalize(context.Background(), map[string]any{}); err == nil {
		t.Fatal("non-unstructured payload must error")
	}
	// Empty report → empty signals, no error.
	empty := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{}}}
	if sig, err := p.Normalize(context.Background(), empty); err != nil || len(sig.Findings) != 0 {
		t.Fatalf("empty report: %v / %+v", err, sig)
	}
}

func TestTrivyDetect(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		snap, err := NewTrivy().Detect(context.Background(), fake.NewSimpleClientset())
		if err != nil || snap.Status != StatusNotInstalled {
			t.Fatalf("got %v / %+v", err, snap.Status)
		}
	})
	t.Run("installed healthy", func(t *testing.T) {
		cs := fake.NewSimpleClientset(openCostPod("trivy-operator-abc", "trivy-system",
			map[string]string{"app.kubernetes.io/name": "trivy-operator", "app.kubernetes.io/version": "0.18.1"},
			true, "ghcr.io/aquasecurity/trivy-operator:0.18.1"))
		snap, err := NewTrivy().Detect(context.Background(), cs)
		if err != nil || snap.Status != StatusInstalled {
			t.Fatalf("got %v / %+v", err, snap)
		}
		if snap.Namespace != "trivy-system" || snap.Version != "0.18.1" {
			t.Errorf("meta: %+v", snap)
		}
	})
	t.Run("fallback by name", func(t *testing.T) {
		cs := fake.NewSimpleClientset(openCostPod("trivy-operator-xyz", "security", nil, true, "trivy-operator:0.18.1"))
		snap, _ := NewTrivy().Detect(context.Background(), cs)
		if snap.Status != StatusInstalled || snap.Namespace != "security" {
			t.Fatalf("fallback: %+v", snap)
		}
	})
}

func TestTrivyNormalize_ComplianceReport(t *testing.T) {
	report := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "ClusterComplianceReport",
		"metadata":   map[string]interface{}{"name": "cis"},
		"spec": map[string]interface{}{
			"compliance": map[string]interface{}{"title": "CIS Kubernetes Benchmarks v1.23"},
		},
		"status": map[string]interface{}{
			"summaryReport": map[string]interface{}{
				"controlCheck": []interface{}{
					map[string]interface{}{"id": "1.1.1", "name": "API server pod file permissions", "severity": "HIGH", "totalFail": int64(2)},
					map[string]interface{}{"id": "5.1.1", "name": "Restrict cluster-admin", "severity": "CRITICAL", "totalFail": float64(1)},
					map[string]interface{}{"id": "1.2.1", "name": "Anonymous auth", "severity": "HIGH", "totalFail": int64(0)}, // passing → nothing
					map[string]interface{}{"name": "no id", "totalFail": int64(3)},                                             // malformed → skipped
				},
			},
		},
	}}

	sig, err := NewTrivy().(SignalProvider).Normalize(context.Background(), report)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(sig.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (passing + malformed skipped)", len(sig.Findings))
	}
	f := sig.Findings[0]
	if f.Kind != FindingMisconfig || f.CISControl != "1.1.1" || f.Severity != SeverityHigh {
		t.Fatalf("control finding wrong: %+v", f)
	}
	if !strings.Contains(f.Title, "CIS 1.1.1 — API server pod file permissions (2 failing)") {
		t.Errorf("title = %q", f.Title)
	}
	if !strings.Contains(f.Remediation, "CIS Kubernetes Benchmarks v1.23 control 1.1.1") {
		t.Errorf("remediation = %q", f.Remediation)
	}
	if sig.Findings[1].Severity != SeverityCritical {
		t.Errorf("float64 totalFail + CRITICAL: %+v", sig.Findings[1])
	}
}

func configAuditObj(checks ...map[string]interface{}) *unstructured.Unstructured {
	raw := make([]interface{}, 0, len(checks))
	for _, c := range checks {
		raw = append(raw, c)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "ConfigAuditReport",
		"metadata": map[string]interface{}{
			"name": "replicaset-web", "namespace": "prod",
			"labels": map[string]interface{}{
				"trivy-operator.resource.kind":      "ReplicaSet",
				"trivy-operator.resource.name":      "web-abc123",
				"trivy-operator.resource.namespace": "prod",
			},
		},
		"report": map[string]interface{}{"checks": raw},
	}}
}

func auditCheck(id, title, sev string, success bool) map[string]interface{} {
	return map[string]interface{}{
		"checkID": id, "title": title, "severity": sev, "success": success,
		"remediation": "do the thing",
	}
}

// The severity gate is the product decision behind this whole report type, and
// it is NOT the CVE gate. It was cut from the real distribution on a dev
// cluster: MEDIUM holds "runs as root" and "can elevate its own privileges",
// which matter more than several HIGHs, while LOW is dominated by UID/GID
// thresholds and CPU/memory limits — the latter a capacity concern KubeBolt
// already raises as an insight, so surfacing it under Security would be
// duplicate noise wearing a scarier badge.
func TestTrivyNormalize_ConfigAuditSeverityGate(t *testing.T) {
	obj := configAuditObj(
		auditCheck("AVD-KSV-0001", "Access to host network", "HIGH", false),
		auditCheck("AVD-KSV-0012", "Runs as root user", "MEDIUM", false),
		auditCheck("AVD-KSV-0011", "CPU not limited", "LOW", false),
		auditCheck("AVD-KSV-0999", "Something passing", "HIGH", true),
	)
	sig, err := normalizeConfigAudit(obj)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(sig.Findings) != 2 {
		t.Fatalf("got %d findings, want 2 (HIGH + MEDIUM): %+v", len(sig.Findings), sig.Findings)
	}
	for _, f := range sig.Findings {
		if f.Severity == SeverityLow {
			t.Errorf("LOW leaked in: %s", f.Title)
		}
		if f.ResourceName != "web-abc123" || f.ResourceNamespace != "prod" {
			t.Errorf("resource = %s/%s, want prod/web-abc123 — unlike a CIS control, "+
				"a config-audit finding names the workload and that is what makes it actionable",
				f.ResourceNamespace, f.ResourceName)
		}
		if f.Kind != FindingMisconfig {
			t.Errorf("kind = %s, want misconfig", f.Kind)
		}
	}
}

// A passing check is not a finding. Emitting them would turn a security list
// into an audit log and make every count meaningless.
func TestTrivyNormalize_ConfigAuditSkipsPassingChecks(t *testing.T) {
	sig, err := normalizeConfigAudit(configAuditObj(
		auditCheck("AVD-KSV-0001", "Access to host network", "HIGH", true),
	))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(sig.Findings) != 0 {
		t.Errorf("got %d findings for an all-passing report, want 0", len(sig.Findings))
	}
}

// The check id rides in the title so a reworded check between Trivy releases
// does not fork the identity — the same churn class a rollout used to cause.
func TestTrivyNormalize_ConfigAuditTitleCarriesTheCheckID(t *testing.T) {
	sig, _ := normalizeConfigAudit(configAuditObj(
		auditCheck("AVD-KSV-0012", "Runs as root user", "MEDIUM", false),
	))
	if len(sig.Findings) != 1 || !strings.HasPrefix(sig.Findings[0].Title, "AVD-KSV-0012: ") {
		t.Errorf("title = %q, want it prefixed with the stable check id", sig.Findings[0].Title)
	}
}

func complianceObj(controlID string, checkIDs []string, fails int64) *unstructured.Unstructured {
	checks := make([]interface{}, 0, len(checkIDs))
	for _, id := range checkIDs {
		checks = append(checks, map[string]interface{}{"id": id})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "ClusterComplianceReport",
		"metadata":   map[string]interface{}{"name": "cis"},
		"spec": map[string]interface{}{"compliance": map[string]interface{}{
			"title": "CIS Kubernetes Benchmarks v1.23",
			"controls": []interface{}{map[string]interface{}{
				"id": controlID, "name": "a control", "checks": checks,
			}},
		}},
		"status": map[string]interface{}{"summaryReport": map[string]interface{}{
			"controlCheck": []interface{}{map[string]interface{}{
				"id": controlID, "name": "a control", "totalFail": fails,
			}},
		}},
	}}
}

// A control evaluated by workload checks the sweep ALSO ingests is an aggregate
// of rows that exist on their own. Verified on a live cluster: for all 35
// controls where both sides had data, the control's "N failing" equalled the
// summed failing results of exactly those checks — the duplication is
// arithmetic, not approximate. Counting both inflates every KPI.
func TestTrivyNormalize_ComplianceControlIsMarkedRollup(t *testing.T) {
	sig := normalizeComplianceReport(complianceObj("5.2.7", []string{"AVD-KSV-0012"}, 52))
	if len(sig.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(sig.Findings))
	}
	if !sig.Findings[0].Rollup {
		t.Error("a control evaluated by an ingested workload check must be marked Rollup, " +
			"or its count is added on top of the very rows it summarizes")
	}
}

// Control-plane and node controls (AVD-KCV-*) live in the infra assessments,
// which the sweep does NOT ingest. They are the ONLY way to see that class of
// problem, so marking them as rollups would erase them from every count for no
// reason.
func TestTrivyNormalize_ControlPlaneControlIsNotARollup(t *testing.T) {
	sig := normalizeComplianceReport(complianceObj("1.2.20", []string{"AVD-KCV-0020"}, 1))
	if len(sig.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(sig.Findings))
	}
	if sig.Findings[0].Rollup {
		t.Error("a control-plane control has no separately-ingested check, so it is not " +
			"a rollup — marking it would drop a real finding out of the totals")
	}
}

// The row must survive being uncounted. Compliance posture is a real question,
// and a control whose remediation is only a pointer still tells the operator
// which benchmark they fail.
func TestTrivyNormalize_RollupIsStillEmittedAsAFinding(t *testing.T) {
	sig := normalizeComplianceReport(complianceObj("5.2.7", []string{"AVD-KSV-0012"}, 52))
	f := sig.Findings[0]
	if f.CISControl != "5.2.7" || f.Title == "" || f.Remediation == "" {
		t.Errorf("rollup lost its content: %+v", f)
	}
}

// rbacReport builds a ClusterRbacAssessmentReport the way Trivy actually writes
// one: the role name is HASHED in the label and only spelled out in the
// annotation and the ownerReference.
func rbacReport(roleName string, hashedLabel bool) *unstructured.Unstructured {
	labels := map[string]interface{}{"trivy-operator.resource.kind": "ClusterRole"}
	if hashedLabel {
		labels["trivy-operator.resource.name-hash"] = "54ccb57cc4"
	} else {
		labels["trivy-operator.resource.name"] = roleName
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "ClusterRbacAssessmentReport",
		"metadata": map[string]interface{}{
			"name":        "clusterrole-54ccb57cc4",
			"labels":      labels,
			"annotations": map[string]interface{}{"trivy-operator.resource.name": roleName},
		},
		"report": map[string]interface{}{"checks": []interface{}{map[string]interface{}{
			"checkID":     "AVD-KSV-0041",
			"title":       "Manage secrets",
			"severity":    "CRITICAL",
			"success":     false,
			"remediation": "Remove resource 'secrets' from role",
		}}},
	}}
}

// The cluster-scoped report cannot put the role name in a label — names like
// `system:controller:persistent-volume-binder` are not legal label values — so
// Trivy writes a hash and spells the name out in the annotation. Reading the
// usual label yields "", which would store findings pointing at nothing.
func TestTrivyNormalize_RbacNameSurvivesTheLabelHash(t *testing.T) {
	sig := normalizeRbacAssessment(rbacReport("argocd-server", true))
	if len(sig.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(sig.Findings))
	}
	f := sig.Findings[0]
	if f.ResourceName != "argocd-server" {
		t.Errorf("ResourceName = %q, want argocd-server — the name lives in the annotation "+
			"when the label carries only a hash", f.ResourceName)
	}
	if f.Kind != FindingRBACIssue {
		t.Errorf("Kind = %q, want %q", f.Kind, FindingRBACIssue)
	}
	if !strings.HasPrefix(f.Title, "AVD-KSV-0041:") {
		t.Errorf("Title = %q, want the check id first so a reworded title does not "+
			"resolve and recreate the finding", f.Title)
	}
}

// Kubernetes' own bootstrap roles are reconciled by the control plane: edit one
// and it comes back. They are also identical on every conformant cluster, so
// they say nothing about this customer. On the dev cluster they were 48 of 95
// failing checks, 15 of them critical — they would have outranked every real
// finding with advice nobody can follow.
func TestTrivyNormalize_KubernetesBuiltinRolesAreNotFindings(t *testing.T) {
	for _, role := range []string{"system:node", "system:kube-controller-manager", "cluster-admin", "edit"} {
		if sig := normalizeRbacAssessment(rbacReport(role, true)); len(sig.Findings) != 0 {
			t.Errorf("%s produced %d findings; built-in roles are unfixable and must not be reported",
				role, len(sig.Findings))
		}
	}
}

// The exclusion must stay narrow. A customer role is the whole point of the
// lens, and the namespaced report DOES carry a plain name label.
func TestTrivyNormalize_CustomerRoleIsReported(t *testing.T) {
	sig := normalizeRbacAssessment(rbacReport("kube-prom-kube-prometheus-operator", false))
	if len(sig.Findings) != 1 {
		t.Fatalf("got %d findings, want 1 — a customer-installed role is exactly what "+
			"this lens exists to show", len(sig.Findings))
	}
	if sig.Findings[0].Severity != SeverityCritical {
		t.Errorf("Severity = %q, want critical", sig.Findings[0].Severity)
	}
}

// secretReport builds an ExposedSecretReport the way Trivy writes one — same
// label + artifact shape as a VulnerabilityReport.
func secretReport(sev, target string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "ExposedSecretReport",
		"metadata": map[string]interface{}{
			"name":      "replicaset-leaky-app-7d4f8b-app",
			"namespace": "production",
			"labels": map[string]interface{}{
				"trivy-operator.resource.kind":      "ReplicaSet",
				"trivy-operator.resource.name":      "leaky-app-7d4f8b",
				"trivy-operator.resource.namespace": "production",
			},
		},
		"report": map[string]interface{}{
			"registry": map[string]interface{}{"server": "docker.io"},
			"artifact": map[string]interface{}{"repository": "acme/leaky", "tag": "v1"},
			"secrets": []interface{}{map[string]interface{}{
				"ruleID":   "aws-access-key-id",
				"title":    "AWS Access Key ID",
				"category": "AWS",
				"severity": sev,
				"target":   target,
				"match":    "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
			}},
		},
	}}
}

// The matched line carries credential material. Trivy masks it heuristically,
// not reliably, and this table is served to a browser and read by Kobi —
// persisting it would trade a real secret for a nicer row.
func TestTrivyNormalize_ExposedSecretNeverStoresTheMatch(t *testing.T) {
	sig := normalizeExposedSecrets(secretReport("CRITICAL", "/app/config.yaml"))
	if len(sig.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(sig.Findings))
	}
	f := sig.Findings[0]
	blob := f.Title + "|" + f.Remediation + "|" + f.Image
	if strings.Contains(blob, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(blob, "AWS_ACCESS_KEY_ID=") {
		t.Errorf("the matched secret leaked into the finding: %q", blob)
	}
	if !strings.Contains(f.Title, "/app/config.yaml") {
		t.Errorf("Title = %q, want it to name WHERE the secret is", f.Title)
	}
	if f.Kind != FindingExposedSecret {
		t.Errorf("Kind = %q, want %q", f.Kind, FindingExposedSecret)
	}
	if f.Image != "docker.io/acme/leaky:v1" {
		t.Errorf("Image = %q, want the pullable ref", f.Image)
	}
}

// The remediation must say ROTATE. Deleting the string from the layer does
// nothing for a key already pushed to a registry and pulled by whoever.
func TestTrivyNormalize_ExposedSecretRemediationSaysRotate(t *testing.T) {
	f := normalizeExposedSecrets(secretReport("HIGH", "/etc/creds")).Findings[0]
	if !strings.Contains(strings.ToUpper(f.Remediation), "ROTATE") {
		t.Errorf("Remediation = %q, want it to demand rotation", f.Remediation)
	}
}

// No severity gate here, unlike CVEs. There is no volume to protect against —
// a handful of rows — and a low-rated credential is still a live credential.
func TestTrivyNormalize_ExposedSecretIngestsEverySeverity(t *testing.T) {
	for raw, want := range map[string]FindingSeverity{
		"CRITICAL": SeverityCritical, "HIGH": SeverityHigh,
		"MEDIUM": SeverityMedium, "LOW": SeverityLow,
	} {
		sig := normalizeExposedSecrets(secretReport(raw, "/x"))
		if len(sig.Findings) != 1 {
			t.Fatalf("%s produced %d findings; every band must be ingested", raw, len(sig.Findings))
		}
		if got := sig.Findings[0].Severity; got != want {
			t.Errorf("%s mapped to %q, want %q", raw, got, want)
		}
	}
}

// Two files matching the same rule are two secrets to remove. Identity must
// include the target, or fixing one resolves the other.
func TestTrivyNormalize_ExposedSecretIdentityIncludesTheTarget(t *testing.T) {
	a := normalizeExposedSecrets(secretReport("HIGH", "/app/a.yaml")).Findings[0]
	b := normalizeExposedSecrets(secretReport("HIGH", "/app/b.yaml")).Findings[0]
	if a.Title == b.Title {
		t.Errorf("both targets produced the same title %q — one fix would resolve both", a.Title)
	}
}
