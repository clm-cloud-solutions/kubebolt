package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var (
	sliceGVR         = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	coreEndpointsGVR = schema.GroupVersionResource{Version: "v1", Resource: "endpoints"}
)

func slice(name, managedBy, service, ip string) *unstructured.Unstructured {
	labels := map[string]interface{}{}
	if managedBy != "" {
		labels[sliceManagedByLabel] = managedBy
	}
	if service != "" {
		labels[sliceServiceLabel] = service
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
		"metadata":    map[string]interface{}{"name": name, "namespace": "shop-data", "resourceVersion": "4", "labels": labels},
		"addressType": "IPv4",
		"endpoints":   []interface{}{map[string]interface{}{"addresses": []interface{}{ip}}},
		"ports":       []interface{}{map[string]interface{}{"port": int64(5432), "protocol": "TCP"}},
	}}
}

func shopDBEndpoints(ip string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Endpoints",
		"metadata": map[string]interface{}{"name": "shop-db", "namespace": "shop-data", "resourceVersion": "9"},
		"subsets": []interface{}{map[string]interface{}{
			"addresses": []interface{}{map[string]interface{}{"ip": ip}},
			"ports":     []interface{}{map[string]interface{}{"port": int64(5432), "protocol": "TCP"}},
		}},
	}}
}

func sliceConnector(objs ...runtime.Object) *Connector {
	return &Connector{dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		sliceGVR:         "EndpointSliceList",
		coreEndpointsGVR: "EndpointsList",
	}, objs...)}
}

func TestManagedSlice_SaysWhoRewritesItAndWhatToEditInstead(t *testing.T) {
	cases := []struct {
		managedBy, service string
		want               *ManagedSliceError
	}{
		{sliceControllerName, "checkout", &ManagedSliceError{Source: "service", Service: "checkout"}},
		{sliceMirroringName, "shop-db", &ManagedSliceError{Source: "endpoints", Service: "shop-db"}},
		// By hand or by another tool: nobody rewrites it, the edit stands.
		{"", "shop-db", nil},
		{"my-operator", "shop-db", nil},
	}
	for _, tc := range cases {
		got := managedSlice(slice("s", tc.managedBy, tc.service, "10.0.0.1").Object)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("managed-by %q: got %+v, want %+v", tc.managedBy, got, tc.want)
		}
	}
	if msg := (&ManagedSliceError{Source: "endpoints", Service: "shop-db"}).Error(); !strings.Contains(msg, `Edit the Endpoints "shop-db"`) {
		t.Errorf("a mirrored slice must point at its Endpoints: %s", msg)
	}
	if msg := (&ManagedSliceError{Source: "service"}).Error(); !strings.Contains(msg, "its Service") {
		t.Errorf("without the service label the message still reads: %s", msg)
	}
}

// debt-20: the apply used to succeed on a slice the mirroring controller
// copies back within seconds. It is refused and the slice is left alone.
func TestApplyResourceYAML_RefusesASliceAControllerRewrites(t *testing.T) {
	for _, managedBy := range []string{sliceMirroringName, sliceControllerName} {
		c := sliceConnector(slice("shop-db-qm99s", managedBy, "shop-db", "172.18.0.7"))
		shown, err := c.GetResourceYAML("endpoints", "shop-data", "shop-db-qm99s")
		if err != nil {
			t.Fatal(err)
		}
		edited := strings.Replace(string(shown), "172.18.0.7", "172.18.0.9", 1)
		err = c.ApplyResourceYAML(context.Background(), "endpoints", "shop-data", "shop-db-qm99s", []byte(edited), WriteOptions{})
		var managed *ManagedSliceError
		if !errors.As(err, &managed) || managed.Service != "shop-db" {
			t.Fatalf("managed-by %s: want a ManagedSliceError for shop-db, got %v", managedBy, err)
		}
		live, _ := c.dynamicClient.Resource(sliceGVR).Namespace("shop-data").Get(context.Background(), "shop-db-qm99s", metav1.GetOptions{})
		eps, _, _ := unstructured.NestedSlice(live.Object, "endpoints")
		if got := eps[0].(map[string]interface{})["addresses"].([]interface{})[0]; got != "172.18.0.7" {
			t.Fatalf("managed-by %s: the refused edit reached the slice: %v", managedBy, got)
		}
	}
}

// A slice nobody rewrites stays editable, and the source of a mirrored one is
// editable as coreendpoints: that is where the IP of shop-db changes.
func TestApplyResourceYAML_EditsAnUnmanagedSliceAndTheMirroredSource(t *testing.T) {
	c := sliceConnector(slice("manual", "", "", "10.0.0.1"), shopDBEndpoints("172.18.0.7"))

	shown, err := c.GetResourceYAML("endpoints", "shop-data", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyResourceYAML(context.Background(), "endpoints", "shop-data", "manual",
		[]byte(strings.Replace(string(shown), "10.0.0.1", "10.0.0.2", 1)), WriteOptions{}); err != nil {
		t.Fatalf("an unmanaged slice must stay editable: %v", err)
	}

	shown, err = c.GetResourceYAML("coreendpoints", "shop-data", "shop-db")
	if err != nil {
		t.Fatalf("the mirrored source must be readable as coreendpoints: %v", err)
	}
	if err := c.ApplyResourceYAML(context.Background(), "coreendpoints", "shop-data", "shop-db",
		[]byte(strings.Replace(string(shown), "172.18.0.7", "172.18.0.9", 1)), WriteOptions{}); err != nil {
		t.Fatalf("the mirrored source must be editable: %v", err)
	}
	live, _ := c.dynamicClient.Resource(coreEndpointsGVR).Namespace("shop-data").Get(context.Background(), "shop-db", metav1.GetOptions{})
	subsets, _, _ := unstructured.NestedSlice(live.Object, "subsets")
	addr := subsets[0].(map[string]interface{})["addresses"].([]interface{})[0].(map[string]interface{})["ip"]
	if addr != "172.18.0.9" {
		t.Fatalf("the Endpoints edit did not land: %v", addr)
	}
}
