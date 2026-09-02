package integrations

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

func policyReport() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "wgpolicyk8s.io/v1alpha2",
		"kind":       "PolicyReport",
		"metadata":   map[string]interface{}{"name": "polr-deployment-web", "namespace": "staging"},
		"results": []interface{}{
			map[string]interface{}{
				"policy": "require-signed-images", "rule": "check-signature",
				"result": "fail", "severity": "high",
				"message": "image docker.io/web:latest is not from a trusted registry",
				"resources": []interface{}{
					map[string]interface{}{"kind": "Deployment", "name": "web", "namespace": "staging"},
				},
			},
			map[string]interface{}{ // pass → not a violation
				"policy": "require-netpol", "result": "pass",
			},
			map[string]interface{}{ // warn → audit noise, not a violation
				"policy": "require-labels", "result": "warn", "severity": "low",
			},
			map[string]interface{}{ // engine hiccup → must not read as violation
				"policy": "broken-policy", "result": "error",
			},
			map[string]interface{}{ // fail with no severity → medium default
				"policy": "require-netpol", "rule": "require-netpol",
				"result": "fail", "message": "no NetworkPolicy selects this pod",
			},
		},
	}}
}

func TestKyvernoNormalize(t *testing.T) {
	p := NewKyverno().(CRDSignalProvider)
	if p.IngestMode() != IngestCRD || len(p.IngestGVRs()) != 2 {
		t.Fatalf("contract: mode=%q gvrs=%d", p.IngestMode(), len(p.IngestGVRs()))
	}

	sig, err := p.Normalize(context.Background(), policyReport())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(sig.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (only result=fail; pass/warn/error skipped)", len(sig.Findings))
	}

	f := sig.Findings[0]
	if f.Kind != FindingPolicyViolation || f.Source != "kyverno" || f.Severity != SeverityHigh {
		t.Fatalf("first finding wrong: %+v", f)
	}
	// The title is policy/rule and nothing more. It USED to carry the message,
	// which cannot be part of an identity: Kyverno embeds the JSON path in it, so
	// the same defect read differently on a Pod and on its controller and the two
	// never converged. The message is asserted below, where it now lives.
	if f.Title != "require-signed-images/check-signature" {
		t.Errorf("title = %q, want %q", f.Title, "require-signed-images/check-signature")
	}
	if !strings.Contains(f.Remediation, "docker.io/web:latest") {
		t.Errorf("the message must survive as remediation, got %q", f.Remediation)
	}
	if f.ResourceKind != "Deployment" || f.ResourceName != "web" || f.ResourceNamespace != "staging" {
		t.Errorf("resource identity: %+v", f)
	}

	if sig.Findings[1].Severity != SeverityMedium {
		t.Errorf("fail without severity must default to medium, got %q", sig.Findings[1].Severity)
	}
	// The no-resources fail falls back to the report's namespace.
	if sig.Findings[1].ResourceNamespace != "staging" {
		t.Errorf("namespace fallback: %+v", sig.Findings[1])
	}

	if _, err := p.Normalize(context.Background(), "nope"); err == nil {
		t.Fatal("non-unstructured payload must error")
	}
}

func TestKyvernoDetect(t *testing.T) {
	if snap, _ := NewKyverno().Detect(context.Background(), fake.NewSimpleClientset()); snap.Status != StatusNotInstalled {
		t.Fatalf("empty cluster: %v", snap.Status)
	}
	cs := fake.NewSimpleClientset(openCostPod("kyverno-admission-controller-x", "kyverno",
		map[string]string{"app.kubernetes.io/part-of": "kyverno", "app.kubernetes.io/version": "1.12.0"},
		true, "ghcr.io/kyverno/kyverno:v1.12.0"))
	snap, _ := NewKyverno().Detect(context.Background(), cs)
	if snap.Status != StatusInstalled || snap.Namespace != "kyverno" || snap.Version != "1.12.0" {
		t.Fatalf("installed: %+v", snap)
	}
}

// scopedReport is the shape Kyverno ≥1.9 actually writes: one report per
// resource, the resource named in the top-level `scope`, and
// `results[].resources` ABSENT.
func scopedReport(kind, name, rule string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "wgpolicyk8s.io/v1alpha2",
		"kind":       "PolicyReport",
		"metadata": map[string]interface{}{
			"name":      "7f79a72b-62cd-4f27-91b0-1f0e4b5275d6",
			"namespace": "monitoring",
			"ownerReferences": []interface{}{map[string]interface{}{
				"apiVersion": "apps/v1", "kind": kind, "name": name,
			}},
		},
		"scope": map[string]interface{}{
			"apiVersion": "apps/v1", "kind": kind, "name": name, "namespace": "monitoring",
		},
		"results": []interface{}{map[string]interface{}{
			"policy": "disallow-host-namespaces", "rule": rule,
			"result": "fail", "severity": "medium",
			"message": "validation error: Sharing the host namespaces is disallowed. " +
				"rule " + rule + " failed at path /spec/hostNetwork/",
		}},
	}}
}

// Without reading `scope` the finding names nothing. Measured on a live cluster:
// all 70 violations arrived with no resource and would have been counted into
// the "unassigned" bucket — visible as a number, unreachable as a row.
func TestKyvernoNormalize_ResourceComesFromScope(t *testing.T) {
	sig, err := NewKyverno().(*kyvernoProvider).Normalize(context.Background(),
		scopedReport("DaemonSet", "kube-prom-node-exporter", "autogen-host-namespaces"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(sig.Findings))
	}
	f := sig.Findings[0]
	if f.ResourceKind != "DaemonSet" || f.ResourceName != "kube-prom-node-exporter" {
		t.Errorf("resource = %s/%s, want DaemonSet/kube-prom-node-exporter — Kyverno ≥1.9 "+
			"leaves results[].resources empty and names the subject in scope",
			f.ResourceKind, f.ResourceName)
	}
	if f.ResourceNamespace != "monitoring" {
		t.Errorf("namespace = %q, want monitoring", f.ResourceNamespace)
	}
}

// The pod-level and controller-level halves of the SAME defect must produce the
// same title, or they never converge into one row however well the resource is
// collapsed. Two things break that: the `autogen-` prefix on the rule, and the
// message — which embeds the JSON path and so differs by level.
func TestKyvernoNormalize_SameDefectSameTitleAcrossLevels(t *testing.T) {
	p := NewKyverno().(*kyvernoProvider)
	pod, _ := p.Normalize(context.Background(), scopedReport("Pod", "node-exporter-pwqzh", "host-namespaces"))
	ctl, _ := p.Normalize(context.Background(), scopedReport("DaemonSet", "kube-prom-node-exporter", "autogen-host-namespaces"))

	if pod.Findings[0].Title != ctl.Findings[0].Title {
		t.Errorf("titles diverge:\n pod: %q\n ctl: %q\nOne defect must read as one title",
			pod.Findings[0].Title, ctl.Findings[0].Title)
	}
	if want := "disallow-host-namespaces/host-namespaces"; pod.Findings[0].Title != want {
		t.Errorf("Title = %q, want %q", pod.Findings[0].Title, want)
	}
	// The message is not lost — it moves to where it is read, not compared.
	if !strings.Contains(pod.Findings[0].Remediation, "host namespaces is disallowed") {
		t.Errorf("the message must survive as remediation, got %q", pod.Findings[0].Remediation)
	}
}

// The title must not carry the message: Kyverno rewords it and embeds a path, so
// a title built from it churns the fingerprint for no change in the world.
func TestKyvernoNormalize_TitleExcludesTheMessage(t *testing.T) {
	f := mustNormalizeOne(t, scopedReport("Pod", "web-abc", "host-namespaces"))
	if strings.Contains(f.Title, "failed at path") || strings.Contains(f.Title, "validation error") {
		t.Errorf("Title = %q — the message belongs in the remediation, not the identity", f.Title)
	}
}

// The wgpolicyk8s standard field still wins when a producer populates it —
// Gatekeeper, and Kyverno before 1.9.
func TestKyvernoNormalize_StandardResourcesFieldStillWins(t *testing.T) {
	obj := scopedReport("DaemonSet", "from-scope", "host-namespaces")
	results := obj.Object["results"].([]interface{})
	results[0].(map[string]interface{})["resources"] = []interface{}{
		map[string]interface{}{"kind": "Deployment", "name": "from-resources", "namespace": "prod"},
	}
	f := mustNormalizeOne(t, obj)
	if f.ResourceName != "from-resources" || f.ResourceKind != "Deployment" || f.ResourceNamespace != "prod" {
		t.Errorf("resource = %s %s/%s, want Deployment prod/from-resources",
			f.ResourceKind, f.ResourceNamespace, f.ResourceName)
	}
}

func mustNormalizeOne(t *testing.T, obj *unstructured.Unstructured) Finding {
	t.Helper()
	sig, err := NewKyverno().(*kyvernoProvider).Normalize(context.Background(), obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(sig.Findings))
	}
	return sig.Findings[0]
}
