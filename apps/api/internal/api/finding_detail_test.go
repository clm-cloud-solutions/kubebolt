package api

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

type fakeDetailSource struct {
	dyn        dynamic.Interface
	rsToDeploy map[string]string
}

func (f *fakeDetailSource) Dynamic() dynamic.Interface { return f.dyn }

func (f *fakeDetailSource) CountPodsRunningImage(_, image string) int {
	if image == "" {
		return 0
	}
	return 3
}

func (f *fakeDetailSource) WorkloadOwner(_, kind, name string) (string, string, bool) {
	if kind == "ReplicaSet" {
		if d, ok := f.rsToDeploy[name]; ok {
			return "Deployment", d, true
		}
	}
	return kind, name, true
}

// vulnReport builds a VulnerabilityReport the way Trivy emits one: labelled
// with the ReplicaSet, one entry per affected PACKAGE for the same CVE.
func vulnReport(rs, container string, pkgs ...[2]string) *unstructured.Unstructured {
	vulns := make([]interface{}, 0, len(pkgs))
	for _, p := range pkgs {
		vulns = append(vulns, map[string]interface{}{
			"vulnerabilityID":  "CVE-2026-33814",
			"severity":         "HIGH",
			"title":            "net: DoS",
			"resource":         p[0],
			"installedVersion": "1.0",
			"fixedVersion":     p[1],
		})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "VulnerabilityReport",
		"metadata": map[string]interface{}{
			"name": rs, "namespace": "kube-system",
			"labels": map[string]interface{}{
				"trivy-operator.resource.kind":      "ReplicaSet",
				"trivy-operator.resource.name":      rs,
				"trivy-operator.resource.namespace": "kube-system",
				"trivy-operator.container.name":     container,
			},
		},
		"report": map[string]interface{}{
			"vulnerabilities": vulns,
			"registry":        map[string]interface{}{"server": "quay.io"},
			"artifact":        map[string]interface{}{"repository": "cilium/cilium", "tag": "v1.16.5", "digest": "sha256:abc"},
			"os":              map[string]interface{}{"family": "ubuntu", "name": "26.04"},
		},
	}}
}

func detailSource(t *testing.T, objs ...runtime.Object) *fakeDetailSource {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			integrations.TrivyVulnerabilityReportGVR: "VulnerabilityReportList",
			// Registered because the sweep now lists it too — an unregistered
			// GVR makes the fake client PANIC rather than return empty.
			integrations.TrivyConfigAuditReportGVR: "ConfigAuditReportList",
		}, objs...)
	return &fakeDetailSource{dyn: dyn, rsToDeploy: map[string]string{"cilium-abc123": "cilium"}}
}

func cveRecord() *findings.Record {
	rec := &findings.Record{}
	rec.Kind = integrations.FindingCVE
	rec.Title = "CVE-2026-33814: net: golang: Denial of Service"
	rec.ResourceKind = "Deployment"
	rec.ResourceName = "cilium"
	rec.ResourceNamespace = "kube-system"
	return rec
}

// The reason this endpoint exists: the table collapses one CVE across many
// packages into a single row with ONE arbitrary remediation. The detail must
// give every package back.
func TestFindingDetail_RecoversEveryCollapsedPackage(t *testing.T) {
	src := detailSource(t, vulnReport("cilium-abc123", "cilium-agent",
		[2]string{"stdlib", "1.23.4"},
		[2]string{"golang.org/x/net", "0.33.0"},
		[2]string{"github.com/foo/bar", ""},
	))

	cs, err := collectAffectedImages(context.Background(), src, cveRecord())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("got %d images, want 1: %+v", len(cs), cs)
	}
	pkgs := cs[0].Packages
	if len(pkgs) != 3 {
		t.Fatalf("got %d packages, want 3 — the collapsed detail was not recovered: %+v", len(pkgs), pkgs)
	}
	// Fixable first: a package with no fix is not something the operator can act
	// on, so it must not push an actionable one below the fold.
	if pkgs[len(pkgs)-1].FixedVersion != "" {
		t.Errorf("unfixable package is not last: %+v", pkgs)
	}
}

// The operator's first question is "which container, running what" — Trivy
// scans an IMAGE mounted in a container, and a flat package list never says so.
func TestFindingDetail_CarriesTheContainerAndItsImage(t *testing.T) {
	src := detailSource(t, vulnReport("cilium-abc123", "cilium-agent", [2]string{"stdlib", "1.23.4"}))
	cs, err := collectAffectedImages(context.Background(), src, cveRecord())
	if err != nil || len(cs) != 1 {
		t.Fatalf("collect: %v (%d images)", err, len(cs))
	}
	if len(cs[0].Containers) != 1 || cs[0].Containers[0] != "cilium-agent" {
		t.Errorf("containers = %v, want [cilium-agent]", cs[0].Containers)
	}
	if cs[0].Pods != 3 {
		t.Errorf("pods = %d, want 3 — the live blast radius is what a CVE row cannot say", cs[0].Pods)
	}
	if cs[0].Image != "quay.io/cilium/cilium:v1.16.5" {
		t.Errorf("image = %q, want the pullable reference quay.io/cilium/cilium:v1.16.5", cs[0].Image)
	}
	if cs[0].OS != "ubuntu 26.04" {
		t.Errorf("os = %q, want ubuntu 26.04 — a stale base image is often the real cause", cs[0].OS)
	}
}

// The stored finding names the DEPLOYMENT (the sweep collapses ReplicaSets), so
// matching has to walk the same collapse. Comparing raw labels would find
// nothing and the panel would look empty for every CVE.
func TestFindingDetail_MatchesThroughTheReplicaSetCollapse(t *testing.T) {
	src := detailSource(t, vulnReport("cilium-abc123", "cilium-agent", [2]string{"stdlib", "1.23.4"}))
	cs, err := collectAffectedImages(context.Background(), src, cveRecord())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("got %d, want 1 — the Deployment never matched its ReplicaSet's report", len(cs))
	}
}

// A report belonging to a DIFFERENT workload in the same namespace must not
// leak into this finding's detail.
func TestFindingDetail_IgnoresOtherWorkloads(t *testing.T) {
	src := detailSource(t,
		vulnReport("cilium-abc123", "cilium-agent", [2]string{"stdlib", "1.23.4"}),
		vulnReport("coredns-zzz999", "coredns", [2]string{"stdlib", "1.23.4"}),
	)
	cs, err := collectAffectedImages(context.Background(), src, cveRecord())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cs) != 1 {
		t.Errorf("got %d images, want 1 — another workload's report leaked in", len(cs))
	}
}

// A different CVE on the same workload is a different finding, not extra detail.
func TestFindingDetail_IgnoresOtherCVEs(t *testing.T) {
	other := vulnReport("cilium-abc123", "cilium-agent", [2]string{"stdlib", "1.23.4"})
	vulns := other.Object["report"].(map[string]interface{})["vulnerabilities"].([]interface{})
	vulns[0].(map[string]interface{})["vulnerabilityID"] = "CVE-2000-11111"

	cs, err := collectAffectedImages(context.Background(), detailSource(t, other), cveRecord())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cs) != 0 {
		t.Errorf("got %d images for an unrelated CVE, want 0", len(cs))
	}
}

func TestCVEIDFromTitle(t *testing.T) {
	for in, want := range map[string]string{
		"CVE-2026-33814: net: golang: DoS":  "CVE-2026-33814",
		"GHSA-hrxh-6v49-42gf: gRPC-Go: xDS": "GHSA-hrxh-6v49-42gf",
		"no-colon-at-all":                   "no-colon-at-all",
		"CVE-2025-26519: musl libc 0.9.13":  "CVE-2025-26519",
	} {
		if got := cveIDFromTitle(in); got != want {
			t.Errorf("cveIDFromTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// An initContainer and a main container sharing an image is ONE thing to
// rebuild, not two. Grouping by container rendered the same image, packages and
// fix twice — reported from the live cluster on prometheus-config-reloader.
func TestFindingDetail_OneImageSharedByTwoContainersIsOneEntry(t *testing.T) {
	a := vulnReport("cilium-abc123", "config-reloader", [2]string{"stdlib", "1.23.4"})
	b := vulnReport("cilium-abc123", "init-config-reloader", [2]string{"stdlib", "1.23.4"})
	b.Object["metadata"].(map[string]interface{})["name"] = "replicaset-cilium-init"

	cs, err := collectAffectedImages(context.Background(), detailSource(t, a, b), cveRecord())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("got %d images, want 1 — the shared image was listed twice", len(cs))
	}
	if len(cs[0].Containers) != 2 {
		t.Errorf("containers = %v, want both names on the single image entry", cs[0].Containers)
	}
	if len(cs[0].Packages) != 1 {
		t.Errorf("packages = %d, want 1 — the package list was duplicated too", len(cs[0].Packages))
	}
}
