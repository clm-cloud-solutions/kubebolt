package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Tests for handleCreateResource focus on the pure-logic surface:
// pre-flight validation that catches operator errors before the
// apiserver round-trip, plus the consistency-check between the URL
// and the body.
//
// The dynamic-client Create itself is exercised end-to-end in the
// in-vivo smoke against a kind cluster; here we cover:
//
//   1. Multi-document detection (the body-format guard).
//   2. URL :type → expected GroupVersion mapping.
//   3. URL :type → expected Kind mapping coverage (the inverse map
//      must stay in sync with cluster/connector.go's GVR table).
//   4. namespace-helper formatting for the AlreadyExists error.

func TestHasMultiDocSingleDoc(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"plain yaml", "apiVersion: v1\nkind: Pod\nmetadata:\n  name: foo\n"},
		{"json", `{"apiVersion":"v1","kind":"Pod"}`},
		{"with leading separator", "---\napiVersion: v1\nkind: Pod\n"},
		{"with trailing separator", "apiVersion: v1\nkind: Pod\n---\n"},
		{"with trailing whitespace doc", "apiVersion: v1\nkind: Pod\n---\n   \n"},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasMultiDoc([]byte(c.in)); got {
				t.Errorf("hasMultiDoc(%q) = true, want false", c.in)
			}
		})
	}
}

func TestHasMultiDocMultiDoc(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{
			name: "two pods",
			in: `apiVersion: v1
kind: Pod
metadata:
  name: first
---
apiVersion: v1
kind: Pod
metadata:
  name: second
`,
		},
		{
			name: "leading + trailing + middle",
			in: `---
apiVersion: v1
kind: Pod
metadata:
  name: first
---
apiVersion: v1
kind: Service
metadata:
  name: svc
---
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasMultiDoc([]byte(c.in)); !got {
				t.Errorf("hasMultiDoc(...) = false, want true; body:\n%s", c.in)
			}
		})
	}
}

func TestExpectedGroupVersionFor(t *testing.T) {
	cases := []struct {
		resourceType string
		want         schema.GroupVersion
	}{
		{"pods", schema.GroupVersion{Group: "", Version: "v1"}},
		{"namespaces", schema.GroupVersion{Group: "", Version: "v1"}},
		{"services", schema.GroupVersion{Group: "", Version: "v1"}},
		{"deployments", schema.GroupVersion{Group: "apps", Version: "v1"}},
		{"statefulsets", schema.GroupVersion{Group: "apps", Version: "v1"}},
		{"daemonsets", schema.GroupVersion{Group: "apps", Version: "v1"}},
		{"replicasets", schema.GroupVersion{Group: "apps", Version: "v1"}},
		{"jobs", schema.GroupVersion{Group: "batch", Version: "v1"}},
		{"cronjobs", schema.GroupVersion{Group: "batch", Version: "v1"}},
		{"ingresses", schema.GroupVersion{Group: "networking.k8s.io", Version: "v1"}},
		{"hpas", schema.GroupVersion{Group: "autoscaling", Version: "v1"}},
		{"horizontalpodautoscalers", schema.GroupVersion{Group: "autoscaling", Version: "v1"}},
		{"storageclasses", schema.GroupVersion{Group: "storage.k8s.io", Version: "v1"}},
		{"clusterroles", schema.GroupVersion{Group: "rbac.authorization.k8s.io", Version: "v1"}},
	}
	for _, c := range cases {
		t.Run(c.resourceType, func(t *testing.T) {
			if got := expectedGroupVersionFor(c.resourceType); got != c.want {
				t.Errorf("expectedGroupVersionFor(%q) = %v, want %v", c.resourceType, got, c.want)
			}
		})
	}
}

// TestCreateKindByTypeCoverage — the resourceType→Kind map must
// cover every type in the GVR table. This test fails if the maps
// drift; CI catches the regression before an operator hits a
// cryptic "URL says X, body says Y" error in production.
//
// The reference list is hand-maintained against cluster/connector.go's
// resourceTypeToGVR. Update both maps together.
func TestCreateKindByTypeCoverage(t *testing.T) {
	// Every URL :type the GVR map supports should have a Kind here.
	// The list below mirrors resourceTypeToGVR's keys.
	expected := []string{
		"pods", "nodes", "namespaces", "services", "configmaps", "secrets",
		"persistentvolumeclaims", "pvcs", "persistentvolumes", "pvs",
		"events",
		"deployments", "statefulsets", "daemonsets", "replicasets",
		"jobs", "cronjobs",
		"ingresses",
		"hpas", "horizontalpodautoscalers",
		"storageclasses",
		"roles", "clusterroles", "rolebindings", "clusterrolebindings",
	}
	for _, rt := range expected {
		if _, ok := createKindByType[rt]; !ok {
			t.Errorf("createKindByType missing entry for %q (resourceTypeToGVR has it; the maps must stay in sync)", rt)
		}
	}
}

func TestCreateKindByTypeKnownEntries(t *testing.T) {
	// Spot-check critical entries — drift on these causes immediate
	// "URL/body mismatch" rejection in production for the most
	// common kinds.
	cases := []struct {
		resourceType string
		wantKind     string
	}{
		{"pods", "Pod"},
		{"deployments", "Deployment"},
		{"services", "Service"},
		{"statefulsets", "StatefulSet"},
		{"configmaps", "ConfigMap"},
		{"secrets", "Secret"},
		{"jobs", "Job"},
		{"cronjobs", "CronJob"},
		{"hpas", "HorizontalPodAutoscaler"},
		{"horizontalpodautoscalers", "HorizontalPodAutoscaler"},
		{"clusterrolebindings", "ClusterRoleBinding"},
	}
	for _, c := range cases {
		t.Run(c.resourceType, func(t *testing.T) {
			if got := createKindByType[c.resourceType]; got != c.wantKind {
				t.Errorf("createKindByType[%q] = %q, want %q", c.resourceType, got, c.wantKind)
			}
		})
	}
}

func TestDescribeNamespace(t *testing.T) {
	if got := describeNamespace("default", false); got != `namespace "default"` {
		t.Errorf("namespaced: got %q", got)
	}
	if got := describeNamespace("", true); got != "the cluster" {
		t.Errorf("cluster-scoped: got %q", got)
	}
}
