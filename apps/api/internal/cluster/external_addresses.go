package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ExternalAddress is one address that reaches a workload of this cluster from
// outside it: a LoadBalancer Service's ingress, an Ingress's load balancer, a
// Gateway's status address. A workload of another cluster that calls it looks,
// from there, like an external dependency; the fleet address index
// (api/fleet_addresses.go) turns it back into a name.
//
// Only addresses a controller reports in status count. spec.externalIPs does
// not: anyone who may create a Service can write any address there (the
// CVE-2020-8554 class), and a claimed address would take a real cloud
// dependency's name and guidance away from it.
type ExternalAddress struct {
	// Address is an IP or a hostname, as the object reports it.
	Address   string
	Kind      string // Service, Ingress or Gateway
	Namespace string
	Name      string
	// Ports are what the object says it serves on ("80/TCP", "443/HTTPS");
	// empty when it does not say.
	Ports []string
}

// gatewayGVR is the Gateway API's Gateway, listed through the dynamic client:
// the CRD is optional and has no informer.
var gatewayGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}

// The Gateways' addresses are kept between reads: their LIST is live and
// cluster-wide — over the agent tunnel on an agent-proxy cluster, where it
// costs seconds and shares the informers' rate limiter — so it runs off the
// request path, one at a time, like the overview's optional counts
// (warmOptionalCounts).
const (
	// gatewayAddrsTTL is how long a LIST that answered is reused.
	gatewayAddrsTTL = time.Minute
	// gatewayAddrsRetry is how soon a LIST that failed is tried again.
	gatewayAddrsRetry = 15 * time.Second
	// gatewayListTimeout bounds one LIST; it runs in the background.
	gatewayListTimeout = 10 * time.Second
)

// gatewayAddrCache is one connector's Gateway addresses between reads.
type gatewayAddrCache struct {
	mu    sync.Mutex
	addrs []ExternalAddress
	// complete says the last LIST answered, or said there is nothing to list
	// (no Gateway API, no right to list it). A LIST that failed keeps the
	// addresses of the last one that answered, not complete.
	complete bool
	// at is when the last LIST ended; zero = none has.
	at time.Time
	// fetching is non-nil while a LIST runs, and closed when it ends.
	fetching chan struct{}
}

// ExternalAddresses reads the addresses this cluster exposes, and whether the
// read is complete. Services and Ingresses come from the informer caches;
// Gateways from the connector's own cache of them, which a cold read waits for
// only as long as ctx allows. It never fails: what cannot be read is not
// there, and complete is false when that was the Gateway LIST failing or not
// done yet rather than the cluster having none.
func (c *Connector) ExternalAddresses(ctx context.Context) ([]ExternalAddress, bool) {
	var out []ExternalAddress
	if c.serviceLister != nil {
		if svcs, err := c.serviceLister.List(everythingSelector()); err == nil {
			for _, s := range svcs {
				out = append(out, ServiceExternalAddresses(s)...)
			}
		}
	}
	if c.ingressLister != nil {
		if ings, err := c.ingressLister.List(everythingSelector()); err == nil {
			for _, ing := range ings {
				out = append(out, IngressExternalAddresses(ing)...)
			}
		}
	}
	gateways, complete := c.gatewayExternalAddresses(ctx)
	return append(out, gateways...), complete
}

// gatewayExternalAddresses answers from the cache, starting a LIST in the
// background when it is stale and none runs. Before the first LIST has ended
// it waits for it, until ctx is done.
func (c *Connector) gatewayExternalAddresses(ctx context.Context) ([]ExternalAddress, bool) {
	if c.dynamicClient == nil {
		return nil, true
	}
	g := &c.gatewayAddrs
	g.mu.Lock()
	addrs, complete, at, fetching := g.addrs, g.complete, g.at, g.fetching
	ttl := gatewayAddrsTTL
	if !complete {
		ttl = gatewayAddrsRetry
	}
	if fetching == nil && (at.IsZero() || time.Since(at) >= ttl) {
		fetching = make(chan struct{})
		g.fetching = fetching
		go c.refreshGatewayAddresses(fetching)
	}
	g.mu.Unlock()
	if !at.IsZero() {
		return addrs, complete
	}
	select {
	case <-fetching:
	default:
		select {
		case <-fetching:
		case <-ctx.Done():
			return nil, false
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.addrs, g.complete
}

// refreshGatewayAddresses runs one Gateway LIST and records what it said, then
// closes done.
func (c *Connector) refreshGatewayAddresses(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), gatewayListTimeout)
	defer cancel()
	list, err := c.dynamicClient.Resource(gatewayGVR).List(ctx, metav1.ListOptions{})
	var addrs []ExternalAddress
	complete := true
	switch {
	case err == nil:
		for _, item := range list.Items {
			addrs = append(addrs, GatewayExternalAddresses(item.GetNamespace(), item.GetName(), item.Object)...)
		}
	case apierrors.IsNotFound(err) || apierrors.IsForbidden(err):
		// No Gateway API in the cluster, or no right to list it: none to
		// read, and that is an answer.
	default:
		// Rate limited, the tunnel, a 5xx: not an answer about Gateways.
		complete = false
	}
	g := &c.gatewayAddrs
	g.mu.Lock()
	if complete {
		g.addrs = addrs
	}
	g.complete = complete
	g.at = time.Now()
	g.fetching = nil
	g.mu.Unlock()
	close(done)
}

// ServiceExternalAddresses is a Service's load balancer ingress (type
// LoadBalancer only: a Service switched to another type can keep a stale
// status), with the ports it serves. Never its spec.externalIPs (see
// ExternalAddress).
func ServiceExternalAddresses(svc *corev1.Service) []ExternalAddress {
	if svc == nil {
		return nil
	}
	var addrs []string
	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
		for _, in := range svc.Status.LoadBalancer.Ingress {
			addrs = append(addrs, in.IP, in.Hostname)
		}
	}
	var ports []string
	for _, p := range svc.Spec.Ports {
		proto := string(p.Protocol)
		if proto == "" {
			proto = string(corev1.ProtocolTCP)
		}
		ports = append(ports, fmt.Sprintf("%d/%s", p.Port, proto))
	}
	return externalAddressRows("Service", svc.Namespace, svc.Name, addrs, ports)
}

// IngressExternalAddresses is an Ingress's load balancer address. Its ports
// are the ones the status reports; when it reports none they are HTTP and, with
// TLS configured, HTTPS: the only two an Ingress exposes.
func IngressExternalAddresses(ing *networkingv1.Ingress) []ExternalAddress {
	if ing == nil {
		return nil
	}
	var addrs, ports []string
	seenPort := map[string]bool{}
	addPort := func(p string) {
		if !seenPort[p] {
			seenPort[p] = true
			ports = append(ports, p)
		}
	}
	for _, in := range ing.Status.LoadBalancer.Ingress {
		addrs = append(addrs, in.IP, in.Hostname)
		for _, p := range in.Ports {
			proto := string(p.Protocol)
			if proto == "" {
				proto = string(corev1.ProtocolTCP)
			}
			addPort(fmt.Sprintf("%d/%s", p.Port, proto))
		}
	}
	if len(ports) == 0 {
		addPort("80/TCP")
		if len(ing.Spec.TLS) > 0 {
			addPort("443/TCP")
		}
	}
	return externalAddressRows("Ingress", ing.Namespace, ing.Name, addrs, ports)
}

// GatewayExternalAddresses is a Gateway's status.addresses, from the object's
// unstructured content, with its listeners' ports. Only addresses a client can
// dial count: IPAddress and Hostname (an unset type is an IPAddress); a
// NamedAddress is an implementation's handle, not an address.
func GatewayExternalAddresses(namespace, name string, obj map[string]interface{}) []ExternalAddress {
	status, _ := obj["status"].(map[string]interface{})
	spec, _ := obj["spec"].(map[string]interface{})
	var addrs []string
	if list, ok := status["addresses"].([]interface{}); ok {
		for _, a := range list {
			am, ok := a.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := am["type"].(string)
			if typ != "" && typ != "IPAddress" && typ != "Hostname" {
				continue
			}
			if v, ok := am["value"].(string); ok {
				addrs = append(addrs, v)
			}
		}
	}
	var ports []string
	if listeners, ok := spec["listeners"].([]interface{}); ok {
		seen := map[string]bool{}
		for _, l := range listeners {
			lm, ok := l.(map[string]interface{})
			if !ok {
				continue
			}
			port := unstructuredInt(lm["port"])
			if port <= 0 {
				continue
			}
			proto, _ := lm["protocol"].(string)
			p := fmt.Sprintf("%d/%s", port, proto)
			if proto == "" {
				p = fmt.Sprintf("%d", port)
			}
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	return externalAddressRows("Gateway", namespace, name, addrs, ports)
}

// unstructuredInt reads a JSON number the way the dynamic client decodes it
// (int64), or the way a hand-built object carries it (int, float64).
func unstructuredInt(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// externalAddressRows is one row per distinct, non-empty address of an object,
// in a stable order.
func externalAddressRows(kind, namespace, name string, addrs, ports []string) []ExternalAddress {
	seen := map[string]bool{}
	var out []ExternalAddress
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, ExternalAddress{Address: a, Kind: kind, Namespace: namespace, Name: name, Ports: ports})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}
