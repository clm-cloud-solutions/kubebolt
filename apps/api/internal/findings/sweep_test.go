package findings

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"fmt"

	bolt "go.etcd.io/bbolt"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
)

func newTestBolt(t *testing.T) *BoltStore {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "t.db"), 0o600, nil)
	if err != nil {
		t.Fatalf("bolt: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	bucket := []byte("findings")
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return NewBoltStore(db, bucket)
}

func trivyReportObj(name string, cves ...string) *unstructured.Unstructured {
	vulns := make([]interface{}, 0, len(cves))
	for _, id := range cves {
		vulns = append(vulns, map[string]interface{}{
			"vulnerabilityID": id, "severity": "CRITICAL", "title": "t",
			"resource": "libx", "installedVersion": "1", "fixedVersion": "2",
		})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "VulnerabilityReport",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "production",
			"labels": map[string]interface{}{
				"trivy-operator.resource.kind": "Deployment",
				"trivy-operator.resource.name": name,
			},
		},
		"report": map[string]interface{}{"vulnerabilities": vulns},
	}}
}

// trivyReplicaSetReport mirrors what Trivy Operator actually emits for a
// Deployment's pods: the report is labelled with the REPLICASET, never the
// Deployment, and its name carries a pod-template-hash that changes on every
// rollout.
func trivyReplicaSetReport(rsName string, cves ...string) *unstructured.Unstructured {
	obj := trivyReportObj(rsName, cves...)
	labels := obj.Object["metadata"].(map[string]interface{})["labels"].(map[string]interface{})
	labels["trivy-operator.resource.kind"] = "ReplicaSet"
	labels["trivy-operator.resource.name"] = rsName
	return obj
}

func fakeDyn(t *testing.T, objs ...runtime.Object) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			integrations.TrivyVulnerabilityReportGVR: "VulnerabilityReportList",
			integrations.TrivyComplianceReportGVR:    "ClusterComplianceReportList",
			// Every GVR the sweep lists must be registered here — an unregistered
			// one makes the fake client PANIC rather than return empty, so this map
			// has to grow whenever IngestGVRs does.
			integrations.TrivyConfigAuditReportGVR:           "ConfigAuditReportList",
			integrations.TrivyRbacAssessmentReportGVR:        "RbacAssessmentReportList",
			integrations.TrivyClusterRbacAssessmentReportGVR: "ClusterRbacAssessmentReportList",
			integrations.TrivyExposedSecretReportGVR:         "ExposedSecretReportList",
		}, objs...)
}

func staticIterator(tenant, cluster string, dyn dynamic.Interface) ConnectorIterator {
	return staticIteratorWithOwner(tenant, cluster, dyn, nil)
}

// staticIteratorWithOwner is staticIterator plus a workload-owner resolver, so a
// test can exercise the ReplicaSet→Deployment collapse. A nil owner is the
// no-resolver case: findings keep whatever the scanner reported.
func staticIteratorWithOwner(tenant, cluster string, dyn dynamic.Interface, owner OwnerResolver) ConnectorIterator {
	return func(fn func(string, string, dynamic.Interface, OwnerResolver)) {
		fn(tenant, cluster, dyn, owner)
	}
}

func trivyAsCRDProvider(t *testing.T) integrations.CRDSignalProvider {
	t.Helper()
	p, ok := integrations.NewTrivy().(integrations.CRDSignalProvider)
	if !ok {
		t.Fatal("trivy must implement CRDSignalProvider")
	}
	return p
}

func TestSweep_UpsertResolveLifecycle(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	ctx := context.Background()

	// Sweep 1: two reports → 3 findings, tenant-stamped by the SWEEP.
	dyn := fakeDyn(t, trivyReportObj("payments-api", "CVE-1", "CVE-2"), trivyReportObj("auth-svc", "CVE-3"))
	s := NewSweeper(store, []integrations.CRDSignalProvider{prov}, staticIterator("org-a", "prod-us", dyn), time.Hour)
	s.SweepOnce(ctx)

	out, _ := store.List(Query{TenantID: "org-a", Status: StatusActive})
	if len(out) != 3 {
		t.Fatalf("after sweep 1: %d active, want 3", len(out))
	}
	if out[0].TenantID != "org-a" || out[0].ClusterID != "prod-us" || out[0].Source != "trivy" {
		t.Fatalf("identity stamping wrong: %+v", out[0])
	}
	var firstSeen time.Time
	for _, r := range out {
		if r.ResourceName == "payments-api" {
			firstSeen = r.FirstSeen
		}
	}

	// Sweep 2 (later): CVE-2 disappears (patched) → resolved; CVE-1
	// persists → FirstSeen preserved, LastSeen refreshed.
	time.Sleep(1100 * time.Millisecond) // Bolt stores time at second precision via JSON — make the delta visible
	dyn2 := fakeDyn(t, trivyReportObj("payments-api", "CVE-1"), trivyReportObj("auth-svc", "CVE-3"))
	s2 := NewSweeper(store, []integrations.CRDSignalProvider{prov}, staticIterator("org-a", "prod-us", dyn2), time.Hour)
	s2.SweepOnce(ctx)

	active, _ := store.List(Query{TenantID: "org-a", Status: StatusActive})
	if len(active) != 2 {
		t.Fatalf("after sweep 2: %d active, want 2 (CVE-2 resolved)", len(active))
	}
	resolved, _ := store.List(Query{TenantID: "org-a", Status: StatusResolved})
	if len(resolved) != 1 || resolved[0].ResolvedAt == nil {
		t.Fatalf("resolved set wrong: %+v", resolved)
	}
	for _, r := range active {
		if r.ResourceName == "payments-api" {
			if !r.FirstSeen.Equal(firstSeen) {
				t.Errorf("FirstSeen must survive re-ingest: %v vs %v", r.FirstSeen, firstSeen)
			}
			if !r.LastSeen.After(firstSeen) {
				t.Errorf("LastSeen must refresh: %v", r.LastSeen)
			}
		}
	}
}

func TestSweep_FailedListNeverMassResolves(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	ctx := context.Background()

	dyn := fakeDyn(t, trivyReportObj("payments-api", "CVE-1"))
	NewSweeper(store, []integrations.CRDSignalProvider{prov}, staticIterator("org-a", "prod-us", dyn), time.Hour).SweepOnce(ctx)

	// Second sweep against a client whose LIST fails (apiserver
	// hiccup / CRD uninstalled) → the sweep must NOT resolve the
	// existing finding.
	brokenDyn := fakeDyn(t).(*dynamicfake.FakeDynamicClient)
	brokenDyn.PrependReactor("list", "vulnerabilityreports", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("the server is currently unable to handle the request")
	})
	NewSweeper(store, []integrations.CRDSignalProvider{prov}, staticIterator("org-a", "prod-us", brokenDyn), time.Hour).SweepOnce(ctx)

	active, _ := store.List(Query{TenantID: "org-a", Status: StatusActive})
	if len(active) != 1 {
		t.Fatalf("a failed CRD list must never mass-resolve: %d active, want 1", len(active))
	}
}

func TestSweep_TenantIsolation(t *testing.T) {
	store := newTestBolt(t)
	prov := trivyAsCRDProvider(t)
	ctx := context.Background()

	// Two orgs, same-named report — the SWEEP stamps each with its
	// runtime's tenant; org filters never cross.
	iter := func(fn func(string, string, dynamic.Interface, OwnerResolver)) {
		fn("org-a", "cluster-a", fakeDyn(t, trivyReportObj("web", "CVE-A")), nil)
		fn("org-b", "cluster-b", fakeDyn(t, trivyReportObj("web", "CVE-B")), nil)
	}
	NewSweeper(store, []integrations.CRDSignalProvider{prov}, iter, time.Hour).SweepOnce(ctx)

	a, _ := store.List(Query{TenantID: "org-a"})
	b, _ := store.List(Query{TenantID: "org-b"})
	if len(a) != 1 || len(b) != 1 || a[0].Title == b[0].Title {
		t.Fatalf("tenant stamping/filtering leaked: a=%+v b=%+v", a, b)
	}
}
