package cluster

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func ciliumServed() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods"}}},
		{GroupVersion: "cilium.io/v2", APIResources: []metav1.APIResource{
			{Name: "ciliumnetworkpolicies"}, {Name: "ciliumclusterwidenetworkpolicies"}, {Name: "ciliumendpoints"},
		}},
	}
}

// What the probe leaves behind on a cluster where RBAC grants cilium.io but the
// CRDs are not installed yet (the lab: agent at 20:52Z, Cilium at 21:00Z).
func ciliumAbsentPerms() ResourcePermissions {
	return ResourcePermissions{
		"pods":                             {Resource: "pods", CanList: true, CanWatch: true, CanGet: true},
		"ciliumnetworkpolicies":            {Resource: "ciliumnetworkpolicies", Group: "cilium.io", Absent: true},
		"ciliumclusterwidenetworkpolicies": {Resource: "ciliumclusterwidenetworkpolicies", Group: "cilium.io", Absent: true},
		"certificates":                     {Resource: "certificates", Group: "cert-manager.io"}, // RBAC denied
	}
}

// Cilium installed after the connector was built: the refresh makes its policies
// listable, without an API restart, and a reader holding the old map keeps it.
func TestRefreshOptionalCRDs_CiliumInstalledAfterConnect(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.Resources = ciliumServed()
	c := &Connector{probeClient: cs, clusterName: "lab", permissions: ciliumAbsentPerms()}
	before := c.Permissions()

	c.RefreshOptionalCRDs()

	got := c.Permissions()
	for _, key := range []string{"ciliumnetworkpolicies", "ciliumclusterwidenetworkpolicies"} {
		if !got.CanListWatch(key) || got.Absent(key) {
			t.Errorf("%s after refresh: %+v, want listable and not absent", key, got[key])
		}
		if !before.Absent(key) || before.CanListWatch(key) {
			t.Errorf("%s: the map handed out before the refresh changed: %+v", key, before[key])
		}
	}
	if got.CanListWatch("certificates") || got.Absent("certificates") {
		t.Errorf("an RBAC denial must stay a denial: %+v", got["certificates"])
	}
}

func TestApplyOptionalCRDDiscovery(t *testing.T) {
	const cnp, ccnp = "cilium.io/ciliumnetworkpolicies", "cilium.io/ciliumclusterwidenetworkpolicies"

	t.Run("installed", func(t *testing.T) {
		next, changes := applyOptionalCRDDiscovery(ciliumAbsentPerms(),
			map[string]bool{cnp: true, ccnp: true}, map[string]bool{cnp: true, ccnp: true})
		if len(changes) != 2 || !next.CanListWatch("ciliumnetworkpolicies") || next.Absent("ciliumnetworkpolicies") {
			t.Fatalf("changes %+v, cnp %+v", changes, next["ciliumnetworkpolicies"])
		}
	})

	t.Run("removed", func(t *testing.T) {
		perms := ResourcePermissions{
			"ciliumnetworkpolicies": {Resource: "ciliumnetworkpolicies", Group: "cilium.io", CanList: true, CanWatch: true, CanGet: true},
		}
		next, changes := applyOptionalCRDDiscovery(perms, map[string]bool{cnp: false}, map[string]bool{cnp: true})
		if len(changes) != 1 || changes[0].installed || next.CanListWatch("ciliumnetworkpolicies") || !next.Absent("ciliumnetworkpolicies") {
			t.Fatalf("changes %+v, cnp %+v", changes, next["ciliumnetworkpolicies"])
		}
		if !perms.CanListWatch("ciliumnetworkpolicies") {
			t.Fatal("the input map was edited in place")
		}
	})

	t.Run("unchecked or unchanged keeps the same map", func(t *testing.T) {
		perms := ciliumAbsentPerms()
		next, changes := applyOptionalCRDDiscovery(perms, map[string]bool{cnp: true}, map[string]bool{})
		if len(changes) != 0 {
			t.Fatalf("an unchecked group changed: %+v", changes)
		}
		next, changes = applyOptionalCRDDiscovery(next, map[string]bool{}, map[string]bool{cnp: true, ccnp: true})
		if len(changes) != 0 {
			t.Fatalf("still absent is no change: %+v", changes)
		}
	})

	t.Run("RBAC denied stays denied", func(t *testing.T) {
		const cert = "cert-manager.io/certificates"
		next, changes := applyOptionalCRDDiscovery(ciliumAbsentPerms(), map[string]bool{cert: true}, map[string]bool{cert: true})
		if len(changes) != 0 || next.CanListWatch("certificates") {
			t.Fatalf("changes %+v, certificates %+v", changes, next["certificates"])
		}
	})
}

func TestDiscoverOptionalCRDs(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.Resources = ciliumServed()
	served, checked := discoverOptionalCRDs(cs.Discovery())
	if !served["cilium.io/ciliumnetworkpolicies"] || !served["cilium.io/ciliumclusterwidenetworkpolicies"] {
		t.Errorf("cilium served: %v", served)
	}
	// A group the cluster does not serve is checked and absent, not unknown.
	if !checked["cert-manager.io/certificates"] || served["cert-manager.io/certificates"] {
		t.Errorf("cert-manager: checked=%v served=%v", checked["cert-manager.io/certificates"], served["cert-manager.io/certificates"])
	}

	// A version that fails for another reason leaves its group unknown, so the
	// refresh keeps what it had instead of flipping a CRD to absent on a blip.
	cs.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("tunnel closed")
	})
	_, checked = discoverOptionalCRDs(cs.Discovery())
	if checked["cilium.io/ciliumnetworkpolicies"] {
		t.Error("a failed version read must leave cilium.io unchecked")
	}
}
