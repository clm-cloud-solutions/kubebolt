package cluster

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Finding #88: the flows name a dependency behind an ExternalName by the
// Service it was dialed through; the chain must be followed to where it leaves
// the cluster.
func TestExternalNameTarget(t *testing.T) {
	ordinary := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop-data", Name: "cache"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "cache"}},
	}
	c := crossNSConnector(t, nil,
		externalNameService("shop-data", "shop-db", "db.shop.internal"),
		externalNameService("shop-backend", "db-alias", "shop-db.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "loop-a", "loop-b.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "loop-b", "loop-a.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "to-cache", "cache.shop-data.svc.cluster.local"),
		ordinary,
	)
	for _, tc := range []struct {
		host, target, via string
		ok                bool
	}{
		{"shop-db.shop-data.svc.cluster.local", "db.shop.internal", "shop-data/shop-db", true},
		{"shop-db.shop-data.svc.cluster.local.", "db.shop.internal", "shop-data/shop-db", true},
		{"db-alias.shop-backend.svc.cluster.local", "db.shop.internal", "shop-backend/db-alias → shop-data/shop-db", true},
		{"cache.shop-data.svc.cluster.local", "", "", false},    // an ordinary Service is in-cluster
		{"to-cache.shop-data.svc.cluster.local", "", "", false}, // ends at an ordinary Service
		{"loop-a.shop-data.svc.cluster.local", "", "", false},   // a loop
		{"missing.shop-data.svc.cluster.local", "", "", false},  // no such Service
		{"db.shop.internal", "", "", false},                     // not a Service name
		{"shop-db", "", "", false},                              // not a full Service name
	} {
		target, via, ok := c.ExternalNameTarget(tc.host)
		if ok != tc.ok || target != tc.target || via != tc.via {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", tc.host, target, via, ok, tc.target, tc.via, tc.ok)
		}
	}
	var none *Connector
	if _, _, ok := none.ExternalNameTarget("shop-db.shop-data.svc.cluster.local"); ok {
		t.Error("a nil connector followed a chain")
	}
}
