package cluster

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	corelisters "k8s.io/client-go/listers/core/v1"
	networkinglisters "k8s.io/client-go/listers/networking/v1"
)

func addrStrings(rows []ExternalAddress) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Kind+" "+r.Namespace+"/"+r.Name+" "+r.Address)
	}
	return out
}

// The 2026-09-24 case: orders-api is a LoadBalancer Service whose public IP a
// workload of ANOTHER cluster calls. Its ingress IP is the address, with the
// ports it serves; a hostname ingress (AWS ELB) is one too.
func TestServiceExternalAddresses(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop-backend", Name: "orders-api"},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Port: 80, Protocol: corev1.ProtocolTCP}, {Port: 8443}},
		},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
			{IP: "57.165.100.6"},
			{Hostname: "k8s-shop-orders-0123.elb.us-east-1.amazonaws.com"},
		}}},
	}
	got := ServiceExternalAddresses(svc)
	want := []string{
		"Service shop-backend/orders-api 57.165.100.6",
		"Service shop-backend/orders-api k8s-shop-orders-0123.elb.us-east-1.amazonaws.com",
	}
	if !reflect.DeepEqual(addrStrings(got), want) {
		t.Fatalf("addresses = %v, want %v", addrStrings(got), want)
	}
	if !reflect.DeepEqual(got[0].Ports, []string{"80/TCP", "8443/TCP"}) {
		t.Errorf("ports = %v", got[0].Ports)
	}

	// spec.externalIPs never count: anyone who may create a Service can write
	// any address there, and a claimed address would take a real cloud
	// dependency's name away from it. Nor does a stale load balancer status on
	// a Service that is no longer a LoadBalancer.
	svc.Spec.ExternalIPs = []string{"203.0.113.7"}
	if got := addrStrings(ServiceExternalAddresses(svc)); !reflect.DeepEqual(got, want) {
		t.Errorf("a LoadBalancer's externalIPs were indexed: %v", got)
	}
	svc.Spec.Type = corev1.ServiceTypeClusterIP
	if got := ServiceExternalAddresses(svc); len(got) != 0 {
		t.Errorf("ClusterIP Service with externalIPs and a stale status: %v", addrStrings(got))
	}
	// A plain ClusterIP Service exposes nothing.
	if got := ServiceExternalAddresses(&corev1.Service{Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.12"}}); len(got) != 0 {
		t.Errorf("a ClusterIP is not an external address: %v", got)
	}
	if ServiceExternalAddresses(nil) != nil {
		t.Error("nil Service")
	}
}

func TestIngressExternalAddresses(t *testing.T) {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
		Spec:       networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}}}},
		Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{
			{IP: "20.1.2.3"}, {Hostname: "web.shop.example.com"},
		}}},
	}
	got := IngressExternalAddresses(ing)
	if want := []string{"Ingress shop/web 20.1.2.3", "Ingress shop/web web.shop.example.com"}; !reflect.DeepEqual(addrStrings(got), want) {
		t.Fatalf("addresses = %v, want %v", addrStrings(got), want)
	}
	if !reflect.DeepEqual(got[0].Ports, []string{"80/TCP", "443/TCP"}) {
		t.Errorf("an Ingress with TLS serves HTTP and HTTPS: %v", got[0].Ports)
	}
	// Ports the status reports win.
	ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "20.1.2.3", Ports: []networkingv1.IngressPortStatus{{Port: 8080, Protocol: corev1.ProtocolTCP}}}}
	if got := IngressExternalAddresses(ing); len(got) != 1 || !reflect.DeepEqual(got[0].Ports, []string{"8080/TCP"}) {
		t.Errorf("status ports: %+v", got)
	}
	// No load balancer yet: nothing to match.
	if got := IngressExternalAddresses(&networkingv1.Ingress{}); len(got) != 0 {
		t.Errorf("an Ingress without a load balancer: %v", got)
	}
}

func gatewayObject(ns, name string, addresses []interface{}) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata":   map[string]interface{}{"namespace": ns, "name": name},
		"spec": map[string]interface{}{
			"gatewayClassName": "envoy",
			"listeners": []interface{}{
				map[string]interface{}{"name": "http", "port": int64(80), "protocol": "HTTP"},
				map[string]interface{}{"name": "https", "port": int64(443), "protocol": "HTTPS"},
			},
		},
		"status": map[string]interface{}{"addresses": addresses},
	}
}

func TestGatewayExternalAddresses(t *testing.T) {
	obj := gatewayObject("edge", "public", []interface{}{
		map[string]interface{}{"type": "IPAddress", "value": "34.120.1.1"},
		map[string]interface{}{"type": "Hostname", "value": "gw.example.com"},
		map[string]interface{}{"value": "34.120.1.2"}, // unset type is an IPAddress
		map[string]interface{}{"type": "NamedAddress", "value": "my-reserved-ip"},
	})
	got := GatewayExternalAddresses("edge", "public", obj)
	want := []string{"Gateway edge/public 34.120.1.1", "Gateway edge/public 34.120.1.2", "Gateway edge/public gw.example.com"}
	if !reflect.DeepEqual(addrStrings(got), want) {
		t.Fatalf("addresses = %v, want %v", addrStrings(got), want)
	}
	if !reflect.DeepEqual(got[0].Ports, []string{"80/HTTP", "443/HTTPS"}) {
		t.Errorf("listener ports: %v", got[0].Ports)
	}
	if got := GatewayExternalAddresses("edge", "pending", gatewayObject("edge", "pending", nil)); len(got) != 0 {
		t.Errorf("a Gateway without addresses: %v", got)
	}
}

// Through the connector: Services and Ingresses from the informer caches, the
// Gateways from the dynamic client, and a cluster without the Gateway API (or
// without the right to read Services) contributes what it has, completely.
func TestConnectorExternalAddresses(t *testing.T) {
	svcIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	_ = svcIdx.Add(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop-backend", Name: "orders-api"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 80}}},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "57.165.100.6"}}}},
	})
	_ = svcIdx.Add(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop-backend", Name: "db"}, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.40"}})
	ingIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	_ = ingIdx.Add(&networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
		Status:     networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "20.1.2.3"}}}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dyn, _ := gatewayClient(t, ctx)
	c := &Connector{
		serviceLister: corelisters.NewServiceLister(svcIdx),
		ingressLister: networkinglisters.NewIngressLister(ingIdx),
		dynamicClient: dyn,
	}
	want := []string{
		"Service shop-backend/orders-api 57.165.100.6",
		"Ingress shop/web 20.1.2.3",
		"Gateway edge/public 34.120.1.1",
	}
	got, complete := c.ExternalAddresses(ctx)
	if !reflect.DeepEqual(addrStrings(got), want) || !complete {
		t.Errorf("addresses = %v (complete %v), want %v", addrStrings(got), complete, want)
	}

	// No Gateway API in the cluster, or no right to list it, and no Services
	// informer (not permitted): what is left, complete — there is nothing
	// more to read.
	for _, refusal := range []error{
		apierrors.NewNotFound(gatewayGVR.GroupResource(), ""),
		apierrors.NewForbidden(gatewayGVR.GroupResource(), "", nil),
	} {
		noGW, _ := gatewayClient(t, ctx)
		noGW.PrependReactor("list", "gateways", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, refusal
		})
		c = &Connector{ingressLister: networkinglisters.NewIngressLister(ingIdx), dynamicClient: noGW}
		got, complete := c.ExternalAddresses(ctx)
		if !reflect.DeepEqual(addrStrings(got), []string{"Ingress shop/web 20.1.2.3"}) || !complete {
			t.Errorf("%v: %v (complete %v)", refusal, addrStrings(got), complete)
		}
	}
}

// gatewayClient is a fake dynamic client holding one Gateway, and a counter
// of the Gateway LISTs it served.
func gatewayClient(t *testing.T, ctx context.Context) (*dynamicfake.FakeDynamicClient, *atomic.Int32) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gatewayGVR: "GatewayList"})
	gw := &unstructured.Unstructured{Object: gatewayObject("edge", "public", []interface{}{map[string]interface{}{"type": "IPAddress", "value": "34.120.1.1"}})}
	if _, err := dyn.Resource(gatewayGVR).Namespace("edge").Create(ctx, gw, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var lists atomic.Int32
	dyn.PrependReactor("list", "gateways", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return false, nil, nil
	})
	return dyn, &lists
}

// The Gateway LIST is live and cluster-wide (over the agent tunnel on an
// agent-proxy cluster), so it never runs on the read path once a first one
// has ended: reads answer from the connector's cache, one LIST runs at a
// time in the background, and a LIST that failed is an incomplete read (the
// fleet index does not reuse it), not "no Gateways".
func TestConnectorGatewayAddressesAreCachedOffTheRequestPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A cold read waits for the first LIST only as long as the caller can:
	// with the LIST hung, the caller's deadline answers what the caches hold.
	dyn, lists := gatewayClient(t, ctx)
	release := make(chan struct{})
	dyn.PrependReactor("list", "gateways", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})
	c := &Connector{dynamicClient: dyn}
	short, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	start := time.Now()
	if got, complete := c.ExternalAddresses(short); len(got) != 0 || complete {
		t.Errorf("cold read past its deadline: %v (complete %v)", addrStrings(got), complete)
	}
	if time.Since(start) > time.Second {
		t.Errorf("the read waited %v for a hung LIST", time.Since(start))
	}
	// Concurrent reads while it runs start no second LIST.
	for i := 0; i < 3; i++ {
		c.ExternalAddresses(short)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.gatewayAddrs.mu.Lock()
		ended := !c.gatewayAddrs.at.IsZero()
		c.gatewayAddrs.mu.Unlock()
		if ended || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := lists.Load(); n != 1 {
		t.Errorf("%d Gateway LISTs ran for one cold cache, want 1", n)
	}
	// The LIST that ended now answers every read, without another LIST.
	for i := 0; i < 3; i++ {
		got, complete := c.ExternalAddresses(ctx)
		if !reflect.DeepEqual(addrStrings(got), []string{"Gateway edge/public 34.120.1.1"}) || !complete {
			t.Fatalf("warm read: %v (complete %v)", addrStrings(got), complete)
		}
	}
	if n := lists.Load(); n != 1 {
		t.Errorf("warm reads ran %d LISTs, want the first one only", n)
	}

	// Past the TTL a read answers from the cache at once and refreshes it in
	// the background. That refresh failing (rate limited, the tunnel) keeps
	// the last answer but marks it incomplete; the next one to answer makes
	// it complete again.
	fail := true
	var failMu sync.Mutex
	dyn.PrependReactor("list", "gateways", func(k8stesting.Action) (bool, runtime.Object, error) {
		failMu.Lock()
		defer failMu.Unlock()
		if fail {
			return true, nil, apierrors.NewTooManyRequests("client rate limiter", 1)
		}
		return false, nil, nil
	})
	age := func(d time.Duration) {
		c.gatewayAddrs.mu.Lock()
		c.gatewayAddrs.at = time.Now().Add(-d)
		c.gatewayAddrs.mu.Unlock()
	}
	settle := func() {
		for i := 0; i < 400; i++ {
			c.gatewayAddrs.mu.Lock()
			idle := c.gatewayAddrs.fetching == nil
			c.gatewayAddrs.mu.Unlock()
			if idle {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("the background LIST did not end")
	}
	age(gatewayAddrsTTL)
	if got, complete := c.ExternalAddresses(ctx); len(got) != 1 || !complete {
		t.Errorf("stale read: %v (complete %v)", addrStrings(got), complete)
	}
	settle()
	if got, complete := c.ExternalAddresses(ctx); len(got) != 1 || complete {
		t.Errorf("after a failed refresh: %v (complete %v), want the last answer, incomplete", addrStrings(got), complete)
	}
	failMu.Lock()
	fail = false
	failMu.Unlock()
	age(gatewayAddrsRetry)
	c.ExternalAddresses(ctx)
	settle()
	if got, complete := c.ExternalAddresses(ctx); len(got) != 1 || !complete {
		t.Errorf("after the retry: %v (complete %v)", addrStrings(got), complete)
	}
}
