package api

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// The fleet address index: which Service, Ingress or Gateway of which cluster
// of the org answers on an address. A workload that calls another cluster of
// the same org through its load balancer shows, from its own cluster, only an
// external IP; the 2026-09-24 case was ORDERS_URL=http://57.165.100.6 in the
// front cluster, the public IP of the LoadBalancer Service
// shop-backend/orders-api in the back cluster, and Kobi stopped at "an
// external address I cannot attribute" while the 500s came from orders-api.
//
// The clusters searched are decided per caller before any lookup (fleetScope):
// for a person exactly those the fleet search covers (visibleClusters: org,
// then team); for a service principal (Autopilot) those every reader of the
// cluster under investigation may see. They are read from the connectors
// already running — a cluster with none is skipped and counted, never
// connected; a metrics-only one has nothing to read and is left out. What
// each cluster exposes is cached per (tenant, context) for a minute; the
// caller's set is applied on every lookup, so the cache never answers for a
// cluster the caller may not see.

const (
	// fleetAddressTTL is how long one cluster's addresses are reused. Load
	// balancer addresses change on the scale of a redeploy, and a model asks
	// several times in one investigation.
	fleetAddressTTL = time.Minute
	// fleetAddressBudget bounds the whole lookup: one per-cluster timeout (the
	// clusters are read in parallel) plus a little room to collect.
	fleetAddressBudget = fleetSearchPerClusterTimeout + 500*time.Millisecond
	// fleetAddressParallel bounds the reads (and the goroutines) of one lookup.
	fleetAddressParallel = 8
	// fleetLabelMax caps a cluster's name as a note quotes it: the cap the
	// session block's cluster line has too.
	fleetLabelMax = copilot.ClusterLabelMax
)

// fleetCluster is one cluster the caller may see, and how to read it.
type fleetCluster struct {
	context string
	label   string
	// current marks the cluster being investigated.
	current bool
	// read returns what the cluster exposes and whether that read is complete
	// (Connector.ExternalAddresses); nil when KubeBolt has no live connection
	// to it, and then it is skipped.
	read func(ctx context.Context) ([]cluster.ExternalAddress, bool)
}

type fleetAddrKey struct{ tenant, context string }

type fleetAddrEntry struct {
	at    time.Time
	addrs []cluster.ExternalAddress
}

// fleetAddressIndex caches per cluster what it exposes and fans a lookup out
// over the clusters it is given. It decides nothing about visibility: the
// caller passes the visible set on every call.
type fleetAddressIndex struct {
	mu         sync.Mutex
	entries    map[fleetAddrKey]fleetAddrEntry
	ttl        time.Duration
	perCluster time.Duration
	budget     time.Duration
	parallel   int
	now        func() time.Time
}

func newFleetAddressIndex() *fleetAddressIndex {
	return &fleetAddressIndex{
		entries:    map[fleetAddrKey]fleetAddrEntry{},
		ttl:        fleetAddressTTL,
		perCluster: fleetSearchPerClusterTimeout,
		budget:     fleetAddressBudget,
		parallel:   fleetAddressParallel,
		now:        time.Now,
	}
}

func (x *fleetAddressIndex) cached(k fleetAddrKey) ([]cluster.ExternalAddress, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	e, ok := x.entries[k]
	if !ok || x.now().Sub(e.at) >= x.ttl {
		return nil, false
	}
	return e.addrs, true
}

func (x *fleetAddressIndex) store(k fleetAddrKey, addrs []cluster.ExternalAddress) {
	x.mu.Lock()
	defer x.mu.Unlock()
	now := x.now()
	for key, e := range x.entries {
		if now.Sub(e.at) >= x.ttl {
			delete(x.entries, key)
		}
	}
	x.entries[k] = fleetAddrEntry{at: now, addrs: addrs}
}

// collect returns each cluster's addresses (nil for a skipped one) and the
// counts: a cluster is searched when its addresses came from the cache or a
// read that finished in time, skipped when it has no live connection or its
// read did not finish within the budget. At most x.parallel reads run at
// once; once the lookup stops waiting no new read starts, and one already
// running still fills the cache when it is complete.
func (x *fleetAddressIndex) collect(ctx context.Context, tenant string, clusters []fleetCluster) (sets [][]cluster.ExternalAddress, searched, skipped int) {
	sets = make([][]cluster.ExternalAddress, len(clusters))
	done := make([]bool, len(clusters))
	var pending []int
	for i, c := range clusters {
		if addrs, ok := x.cached(fleetAddrKey{tenant, c.context}); ok {
			sets[i], done[i] = addrs, true
			continue
		}
		if c.read != nil {
			pending = append(pending, i)
		}
	}
	if len(pending) > 0 {
		type result struct {
			i     int
			addrs []cluster.ExternalAddress
		}
		jobs := make(chan int, len(pending))
		for _, i := range pending {
			jobs <- i
		}
		close(jobs)
		// Buffered for every read, so a read that finishes after the lookup
		// gave up never blocks its worker.
		results := make(chan result, len(pending))
		stop := make(chan struct{})
		defer close(stop)
		for w := 0; w < min(max(x.parallel, 1), len(pending)); w++ {
			go func() {
				for i := range jobs {
					select {
					case <-stop:
						return
					default:
					}
					c := clusters[i]
					cctx, cancel := context.WithTimeout(ctx, x.perCluster)
					addrs, complete := c.read(cctx)
					// A read cut short or incomplete (the Gateway LIST not
					// done yet, or failing) answers this lookup but is not
					// reused: the next lookup reads again.
					if complete && cctx.Err() == nil {
						x.store(fleetAddrKey{tenant, c.context}, addrs)
					}
					cancel()
					results <- result{i, addrs}
				}
			}()
		}
		deadline := time.NewTimer(x.budget)
		defer deadline.Stop()
	wait:
		for n := len(pending); n > 0; n-- {
			select {
			case r := <-results:
				sets[r.i], done[r.i] = r.addrs, true
			case <-deadline.C:
				break wait
			case <-ctx.Done():
				break wait
			}
		}
	}
	for _, d := range done {
		if d {
			searched++
		} else {
			skipped++
		}
	}
	return sets, searched, skipped
}

// lookup reads the clusters' addresses and returns them keyed for matching,
// with what the lookup covered.
func (x *fleetAddressIndex) lookup(ctx context.Context, tenant string, clusters []fleetCluster) (fleetAddressBook, copilot.FleetSearch) {
	sets, searched, skipped := x.collect(ctx, tenant, clusters)
	return newFleetAddressBook(clusters, sets), copilot.FleetSearch{ClustersSearched: searched, ClustersSkipped: skipped}
}

// fleetAddressBook is one lookup's addresses, keyed for matching.
type fleetAddressBook struct {
	clusters []fleetCluster
	byKey    map[string][]fleetHit
}

type fleetHit struct {
	cluster int
	addr    cluster.ExternalAddress
}

func newFleetAddressBook(clusters []fleetCluster, sets [][]cluster.ExternalAddress) fleetAddressBook {
	b := fleetAddressBook{clusters: clusters, byKey: map[string][]fleetHit{}}
	for i, addrs := range sets {
		seen := map[string]bool{}
		for _, a := range addrs {
			k := fleetAddressKey(a.Address)
			if k == "" {
				continue
			}
			// One object can report an address twice (an externalIP equal to
			// its load balancer's).
			obj := k + "\x00" + a.Kind + "\x00" + a.Namespace + "\x00" + a.Name
			if seen[obj] {
				continue
			}
			seen[obj] = true
			b.byKey[k] = append(b.byKey[k], fleetHit{cluster: i, addr: a})
		}
	}
	return b
}

// fleetAddressKey normalizes an address for matching: an IP in its canonical
// form, a hostname lower-cased without its trailing dot. "" is nothing to
// match.
func fleetAddressKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	if s == "" {
		return ""
	}
	if a, ok := parseAddr(s); ok {
		return "ip:" + a.String()
	}
	h := strings.TrimSuffix(strings.ToLower(s), ".")
	if h == "" {
		return ""
	}
	return "host:" + h
}

// fleetFound is what an address lookup found: the match (nil for none) and
// what makes it less than certain.
type fleetFound struct {
	match *copilot.FleetMatch
	// undecided counts the other objects on the address that neither the port
	// nor the kind ranking tells apart from the one named.
	undecided int
	// private marks a private address (RFC 1918, ULA, CGNAT), which repeats
	// across networks that do not reach each other.
	private bool
}

// match names the object behind an address, or nil (find without a port).
func (b fleetAddressBook) match(address string) *copilot.FleetMatch {
	return b.find(address, "").match
}

// find names the object behind an address. The cluster being investigated
// wins (a hairpin through its own load balancer); among the others the first
// by name, with the rest counted, since only a private address can repeat.
// Within a cluster, port (the one the caller dials, "" when unknown) keeps the
// objects that serve it, then pickFleetObject decides.
func (b fleetAddressBook) find(address, port string) fleetFound {
	k := fleetAddressKey(address)
	if k == "" {
		return fleetFound{}
	}
	hits := b.byKey[k]
	if len(hits) == 0 {
		return fleetFound{}
	}
	byCluster := map[int][]cluster.ExternalAddress{}
	for _, h := range hits {
		byCluster[h.cluster] = append(byCluster[h.cluster], h.addr)
	}
	idx := make([]int, 0, len(byCluster))
	for i := range byCluster {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(x, y int) bool {
		cx, cy := b.clusters[idx[x]], b.clusters[idx[y]]
		if cx.current != cy.current {
			return cx.current
		}
		if cx.label != cy.label {
			return cx.label < cy.label
		}
		return cx.context < cy.context
	})
	chosen := b.clusters[idx[0]]
	objs := byCluster[idx[0]]
	best, undecided := pickFleetObject(objs, port)
	private := false
	if a, ok := parseAddr(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(address), "["), "]")); ok {
		private = a.IsPrivate() || (a.Is4() && cgnatPrefix.Contains(a))
	}
	return fleetFound{
		match: &copilot.FleetMatch{
			Cluster:        chosen.label,
			ClusterContext: chosen.context,
			Kind:           best.Kind,
			Namespace:      best.Namespace,
			Name:           best.Name,
			Ports:          best.Ports,
			SameCluster:    chosen.current,
			SharedBy:       len(objs) - 1,
			OtherClusters:  len(idx) - 1,
		},
		undecided: undecided,
		private:   private,
	}
}

// cgnatPrefix is carrier-grade NAT space, private in practice though not in
// netip's IsPrivate.
var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// pickFleetObject names the object behind an address within one cluster, and
// how many others could equally be it. The objects serving port are kept when
// any does (a shared load-balancer IP: MetalLB allow-shared-ip, k3s ServiceLB,
// a shared cloud IP). Then a Gateway, else a lone Ingress (it names the
// application), else the Service that owns the load balancer. A Gateway or
// Ingress is certain over one Service each (its controller's or generated
// one); anything else left on the address is undecided.
func pickFleetObject(objs []cluster.ExternalAddress, port string) (cluster.ExternalAddress, int) {
	cands := objs
	if port != "" {
		var serving []cluster.ExternalAddress
		for _, o := range objs {
			if servesPort(o, port) {
				serving = append(serving, o)
			}
		}
		if len(serving) > 0 {
			cands = serving
		}
	}
	var gateways, ingresses, services int
	for _, o := range cands {
		switch o.Kind {
		case "Gateway":
			gateways++
		case "Ingress":
			ingresses++
		case "Service":
			services++
		}
	}
	rank := func(o cluster.ExternalAddress) int {
		switch o.Kind {
		case "Gateway":
			return 0
		case "Ingress":
			if ingresses == 1 {
				return 1
			}
			return 3
		case "Service":
			return 2
		}
		return 4
	}
	sorted := append([]cluster.ExternalAddress(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool {
		ri, rj := rank(sorted[i]), rank(sorted[j])
		if ri != rj {
			return ri < rj
		}
		if sorted[i].Namespace != sorted[j].Namespace {
			return sorted[i].Namespace < sorted[j].Namespace
		}
		return sorted[i].Name < sorted[j].Name
	})
	best := sorted[0]
	var undecided int
	switch {
	case best.Kind == "Gateway":
		undecided = gateways - 1 + max(0, services-gateways)
	case best.Kind == "Ingress" && ingresses == 1:
		undecided = max(0, services-1)
	default:
		undecided = len(sorted) - 1
	}
	return best, undecided
}

// servesPort reports whether an object says it serves port ("8080" matches
// "8080/TCP", "8080/HTTPS" and "8080").
func servesPort(o cluster.ExternalAddress, port string) bool {
	for _, p := range o.Ports {
		if n, _, _ := strings.Cut(p, "/"); n == port {
			return true
		}
	}
	return false
}

// fleetClustersFor is the caller's clusters (callerScope), each with the
// connector already serving it; false when the scope could not be decided in
// time, and then nothing may be searched. currentUID also marks the cluster
// being investigated when its context is not the one ctx resolves to.
func (h *handlers) fleetClustersFor(ctx context.Context, currentUID string) (string, []fleetCluster, bool) {
	if h.manager == nil {
		return "", nil, false
	}
	tenant := cluster.RuntimeKeyFromContext(ctx).Tenant
	current, scope, ok := h.callerScope(ctx, currentUID)
	if !ok {
		return tenant, nil, false
	}
	var out []fleetCluster
	for _, ci := range scope {
		if ci.Mode == "metrics-only" {
			continue // no connector, ever: nothing to read, nothing to retry
		}
		fc := fleetCluster{
			context: ci.Context,
			label:   fleetLabel(ci),
			current: ci.Context == current || (currentUID != "" && ci.ClusterID == currentUID),
		}
		if conn := h.manager.LiveConnector(tenant, ci.Context); conn != nil {
			fc.read = conn.ExternalAddresses
		}
		out = append(out, fc)
	}
	return tenant, out, true
}

// fleetLabel is a cluster's name as the answer quotes it: its display name,
// else its context. The display name starts as the agent's
// kubebolt.io/cluster-name label, written by whoever installed the agent, and
// a note is guidance the model follows: one line of printable text, no
// quotes or backslashes, at most fleetLabelMax characters. copilot.ClusterLabel
// is the one spelling, shared with the session block's cluster line, so the
// cluster a note names is the cluster the other side's Kobi is told it is on.
func fleetLabel(ci cluster.ClusterInfo) string {
	if l := copilot.ClusterLabel(clusterLabel(ci)); l != "" {
		return l
	}
	if l := copilot.ClusterLabel(ci.Context); l != "" {
		return l
	}
	return "unnamed"
}

// fleetIndex is the handlers' shared index; a handlers built without one (a
// test fixture) gets an uncached one per call.
func (h *handlers) fleetIndex() *fleetAddressIndex {
	if h.fleetAddrs != nil {
		return h.fleetAddrs
	}
	return newFleetAddressIndex()
}
