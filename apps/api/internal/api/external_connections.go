package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/extclassify"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
)

// get_external_connections: what a cluster's workloads talk to outside it,
// named. The flow data is the Cluster Map's (computeFlowEdges); what this adds
// is the reading an operator would do by hand — drop what is really inside
// the cluster, merge the addresses of one managed endpoint, say which workload
// calls it, classify it (extclassify) and say whether the org has a connector
// that can look at it.

const (
	// maxExternalDestinations keeps the answer a short list for a model to
	// read; the rest is counted, never silently dropped.
	maxExternalDestinations = 30
	// maxExternalCallers per destination: the top callers name the workload,
	// a long tail of pods of the same one only repeats it.
	maxExternalCallers = 10
	// externalDNSLookback is how far back a hostname is looked up for an
	// address the rate window saw without one. Pooled connections resolve
	// their database once at start-up, so the window itself rarely holds the
	// DNS answer for the traffic it holds.
	externalDNSLookback = "6h"
	// maxDNSIPFilter bounds the resolved_ip=~ filter of that lookup; beyond
	// it the query reads the namespace's whole DNS history instead.
	maxDNSIPFilter = 100
	// confidenceClusterState marks a classification read from the cluster's
	// own objects (the kubernetes Service's endpoints), not from a pattern.
	confidenceClusterState = "cluster-state"
	// allRows asks GetResources for every row of a list: the lookups below
	// need the whole set, not a page.
	allRows = 1 << 30
)

// Notes for the model. Fixed sentences: the answer must say WHY a list is
// empty, because "no external connections" and "I cannot see them" lead to
// opposite conclusions.
const (
	noteNoCluster    = "No cluster is selected or reachable, so there is no flow data to read."
	noteUIDUnknown   = "KubeBolt has not read this cluster's identity yet, so its flow data cannot be scoped and was not queried. This is NOT evidence that there are no external connections; try again in a minute."
	noteFlowsFailed  = "Flow data could not be read (%s). This is NOT evidence that there are no external connections."
	noteNoFlowSource = "There is no flow data for this cluster: Hubble flow collection is not enabled in the KubeBolt agent, or it is not shipping. This is NOT evidence that there are no external connections."
	noteQuietWindow  = "Flow data is present, but none of the matching pods sent traffic outside the cluster in the last %d minutes. Widen windowMinutes for a dependency that is called rarely."
	noteNoConnector  = "There is no live connection to this cluster, so pod, node and Service addresses cannot be told apart from external ones: some private addresses below may be inside the cluster, and callers are shown as pods."
	noteNoHostnames  = "Destinations without an fqdn are shown by address: hostnames are known only when Cilium DNS visibility is on and the pod resolved the name within the last " + externalDNSLookback + "."
	noteFallback     = "The flows do not show this workload's dependencies, so dependencyHosts lists the hosts and buckets its configuration names instead (env, ConfigMaps, args, and ExternalName and selectorless Services: those of its namespace, and those its configuration names in any namespace, followed to their targets). A configured host is a lead, not proof of traffic. Entries without a host are leads with no address to give: a Secret (never read by design), a ConfigMap that is missing or unreadable, a namespace whose Services KubeBolt cannot read, or a Service chain that ends without an address (no such Service, no endpoints, a loop, too many hops, back inside the cluster); the source says which."
)

// Notes for an address a cluster of the org exposes (inFleet). Kobi can offer
// the operator the switch; Autopilot has no such tool and reports it instead.
// Neither presumes the failure: the tool lists healthy dependencies too. The
// cluster is quoted by its sanitized name (fleetLabel); the switch takes the
// context, which is unique where a display name may not be. Kobi's note
// repeats the workload (indexed verbs) for the carried question: that cluster
// does not run the caller, so the question must name what to look at there.
const (
	noteFleetOtherKobi      = "This address is %s %s/%s in cluster \"%s\", another cluster of this organization: if the failure goes through it, it is investigated there — offer the operator the switch with offer_cluster_switch to inFleet.clusterContext, with a question that keeps the operator's words and adds the symptom, the caller and the cluster it runs in, and %[1]s %[2]s/%[3]s as what to investigate there."
	noteFleetOtherAutopilot = "This address is %s %s/%s in cluster \"%s\", another cluster of this organization: if the failure goes through it, name that cluster and workload in the root cause; it cannot be fixed from this cluster."
	noteFleetSame           = "This address is %s %s/%s of this same cluster, reached through its external address: the dependency is that workload, not something outside the cluster."
	noteFleetAmbiguous      = " %d more cluster(s) of this organization expose the same address (a private address can repeat across networks): confirm which one this workload reaches."
	noteFleetPrivate        = " It is a private address, which can repeat across networks: confirm this workload's network reaches that cluster before following it there."
	noteFleetShared         = " %d other object(s) of that cluster answer on the same address (sharedBy): the port or the host decides which one it is."
	noteFleetSkipped        = "%d of this organization's clusters were not searched for these addresses (KubeBolt has no live connection to them right now): an address without inFleet may still be one of theirs."
	noteFleetHostnames      = "Hostnames are matched only against the load-balancer hostnames the organization's clusters report, never resolved: a hostname without inFleet may still point at another cluster's load balancer, so it is not proven external."
	noteFleetUnscoped       = "The organization's other clusters were not searched for these addresses: the list of clusters this caller may see did not answer in time. An address may still be one of theirs."
)

var _ copilot.ExternalConnectionsSource = (*handlers)(nil)

// ExternalConnections implements copilot.ExternalConnectionsSource for the
// request's org, cluster and consumer (all from ctx: the Autopilot profiles
// mark their calls, anything unmarked is Kobi). It never fails: every reason
// it cannot see something becomes a note, so the model can tell a blind spot
// from an empty answer.
func (h *handlers) ExternalConnections(ctx context.Context, q copilot.ExternalConnectionsQuery) (copilot.ExternalConnectionsResult, error) {
	return h.externalConnections(ctx, h.flowScopeFor(ctx), q), nil
}

func (h *handlers) externalConnections(ctx context.Context, sc flowScope, q copilot.ExternalConnectionsQuery) copilot.ExternalConnectionsResult {
	window := q.WindowMinutes
	if window < 1 {
		window = 15
	}
	if window > 60 {
		window = 60
	}
	out := copilot.ExternalConnectionsResult{
		ClusterUIDKnown: sc.uid != "",
		WindowMinutes:   window,
		Namespace:       q.Namespace,
		Destinations:    []copilot.ExternalDestination{},
	}
	v := h.externalViewFor(ctx, sc)
	var notes []string

	// The workload's kind decides the pod-name filter; its configuration is
	// the fallback. Both come from the informer caches, so resolving them up
	// front costs no round trip.
	kind := q.WorkloadKind
	var dep dependencyLookup
	if q.Workload != "" {
		dep = lookupDependencyHosts(sc.conn, kind, q.Namespace, q.Workload)
		if dep.kind != "" {
			kind = dep.kind
		}
		out.Workload = q.Workload
		if kind != "" {
			out.Workload = kind + "/" + q.Workload
		}
	}

	var edges []FlowEdge
	switch {
	case sc.uid == "" && sc.conn == nil:
		notes = append(notes, noteNoCluster)
	case sc.uid == "":
		notes = append(notes, noteUIDUnknown)
	default:
		flows, err := computeFlowEdgesIn(ctx, sc, window, q.Namespace)
		if err != nil {
			notes = append(notes, fmt.Sprintf(noteFlowsFailed, shortError(err)))
			break
		}
		out.SourcePresent = flows.SourcePresent
		if !flows.SourcePresent {
			notes = append(notes, noteNoFlowSource)
		}
		edges = selectExternalEdges(flows.Edges, q.Namespace, podFilter(kind, q.Workload))
		if len(edges) == 0 {
			break
		}
		// Only now is it worth walking the node and Service caches.
		v.internal, v.apiServer = clusterAddresses(sc.conn)
		if missing := addressesWithoutFQDN(edges); len(missing) > 0 {
			v.dnsHistory = dnsHistoryFor(ctx, sc, q.Namespace, missing)
		}
	}

	dests := aggregateExternal(edges, v)
	if len(dests) > maxExternalDestinations {
		out.OmittedDestinations = len(dests) - maxExternalDestinations
		dests = dests[:maxExternalDestinations]
	}
	out.Destinations = dests

	dependencies := hasDependency(dests)
	if out.SourcePresent && len(dests) == 0 {
		notes = append(notes, fmt.Sprintf(noteQuietWindow, window))
	}
	if len(dests) > 0 && sc.conn == nil {
		notes = append(notes, noteNoConnector)
	}
	if lacksHostname(dests) {
		notes = append(notes, noteNoHostnames)
	}

	// The fallback: the flows said nothing about this workload's
	// dependencies (no flow data, a quiet window, or only plumbing).
	if q.Workload != "" && !dependencies {
		if dep.problem != "" {
			notes = append(notes, dep.problem)
		}
		if len(dep.hosts) > 0 {
			out.DependencyHosts = dependencyRows(dep.hosts, v)
			notes = append(notes, noteFallback)
		}
	}

	// The way in: answers to callers inside the cluster say nothing about it.
	if sc.conn != nil {
		withTargetGroups := extclassify.NormalizeProvider(v.provider) == extclassify.ProviderAWS
		points, omitted, problem := sc.conn.EntryPoints(ctx, kind, q.Namespace, q.Workload, withTargetGroups)
		if len(points) > 0 {
			out.Entry, out.OmittedEntry = entryRows(points, v), omitted
			notes = append(notes, noteEntry)
		}
		if problem != "" {
			notes = append(notes, fmt.Sprintf(noteEntryUnread, problem))
		}
	}

	// An address left may be another cluster of the org, reached through its
	// load balancer: named, it is a workload there, not a cloud dependency.
	if h.manager != nil && len(fleetCandidates(out)) > 0 {
		if tenant, clusters, ok := h.fleetClustersFor(ctx, sc.uid); ok {
			book, search := h.fleetIndex().lookup(ctx, tenant, clusters)
			notes = append(notes, annotateFleet(&out, book, search, mcpcatalog.ConsumerFrom(ctx))...)
		} else {
			notes = append(notes, noteFleetUnscoped)
		}
	}

	if sc.conn != nil {
		azure, aws := leadsToProvider(out, "azure"), leadsToProvider(out, "aws")
		if nl := sc.conn.NodeLister(); nl != nil && (azure || aws) {
			if nodes, err := nl.List(labels.Everything()); err == nil {
				switch {
				case azure:
					canRead := v.gateway && v.enabled["azure"] && copilot.ConnectorToolsServed(ctx)
					out.ClusterNetwork = aksClusterNetwork(nodes, canRead)
				case aws:
					canRead := v.gateway && v.enabled["aws"] && copilot.ConnectorToolsServed(ctx) &&
						mcpcatalog.AWSCallAllowed(v.consumer, "ec2", "DescribeInstances")
					out.ClusterNetwork = eksClusterNetwork(nodes, canRead)
				}
			}
		}
	}

	notes = append(notes, connectorNotes(out, v)...)
	out.Notes = notes
	return out
}

// aksNetworkLabels are the node labels AKS writes with a resource group, in
// the order they are trusted: the group of the nodes' network, then the
// cluster's node resource group, which holds that network unless the cluster
// brought its own.
var aksNetworkLabels = []string{"kubernetes.azure.com/network-resourcegroup", "kubernetes.azure.com/cluster"}

const (
	noteClusterNetworkAzure = "This cluster's own network is in resource group %s. The pods' traffic to the Azure resources above leaves through it, and requests to the cluster's load balancer come in through it: its network security group (outbound rules included), load balancer, NAT gateway and public IPs, each with its own activity log, where a rule written into it is recorded, not in the destination's. "
	noteClusterNetworkRead  = "When the destination is up and its activity log shows no change but connections from the pods time out, list this group (group_resource_list) and read the activity log of its network security group; there is no need to list the account to find it."
	noteClusterNetworkName  = "When the destination is up but connections from the pods time out, name this group's network security group to the operator as the next place to look."
)

// aksClusterNetwork reads the cluster's network resource group from its
// nodes' labels, or nil when no node carries one (not AKS). canRead says the
// caller is served the Azure connector, so the note can name its tools.
func aksClusterNetwork(nodes []*corev1.Node, canRead bool) *copilot.ClusterNetwork {
	for _, label := range aksNetworkLabels {
		seen := map[string]bool{}
		for _, n := range nodes {
			if g := n.Labels[label]; g != "" {
				seen[g] = true
			}
		}
		if len(seen) == 0 {
			continue
		}
		groups := sortedSet(seen)
		note := fmt.Sprintf(noteClusterNetworkAzure, strings.Join(groups, ", "))
		if canRead {
			note += noteClusterNetworkRead
		} else {
			note += noteClusterNetworkName
		}
		return &copilot.ClusterNetwork{Provider: "azure", ResourceGroups: groups, From: "node label " + label, Note: note}
	}
	return nil
}

const (
	noteClusterNetworkAWS = "This cluster's nodes are the EC2 instances listed in instances. The pods' traffic to the AWS resources above leaves with their node's security groups (unless security groups for pods give a pod its own), " +
		"through their subnets' network ACLs and route tables, and requests through a load balancer reach the pods through those same groups. " +
		"A change to any of them is recorded in CloudTrail under that group's, network ACL's or route table's id, not under the destination's. "
	noteClusterNetworkAWSRead = "When the destination is up and unchanged but connections from the pods time out, call ec2 DescribeInstances with these InstanceIds for their security groups and subnets, " +
		"then DescribeSecurityGroups (outbound rules included) and DescribeNetworkAcls with those ids; there is no need to list the account."
	noteClusterNetworkAWSName = "When the destination is up but connections from the pods time out, name these nodes' security groups and network ACLs to the operator as the next place to look."
	// maxClusterNetworkInstances bounds the list: the nodes share their
	// groups, and a few instances lead to all of them.
	maxClusterNetworkInstances = 10
)

// eksClusterNetwork reads the nodes' EC2 instance ids from spec.providerID
// (aws:///<zone>/<instance-id>), or nil when no node has one (not EC2: kind,
// another cloud, Fargate only). canRead says the caller may call ec2
// DescribeInstances, so the note can name it.
func eksClusterNetwork(nodes []*corev1.Node, canRead bool) *copilot.ClusterNetwork {
	seen := map[string]bool{}
	region := ""
	for _, n := range nodes {
		id, ok := strings.CutPrefix(n.Spec.ProviderID, "aws://")
		if !ok {
			continue
		}
		inst := id[strings.LastIndex(id, "/")+1:]
		if !strings.HasPrefix(inst, "i-") {
			continue
		}
		seen[inst] = true
		if region == "" {
			region = n.Labels[corev1.LabelTopologyRegion]
		}
	}
	if len(seen) == 0 {
		return nil
	}
	instances := sortedSet(seen)
	if len(instances) > maxClusterNetworkInstances {
		instances = instances[:maxClusterNetworkInstances]
	}
	note := noteClusterNetworkAWS
	if canRead {
		note += noteClusterNetworkAWSRead
	} else {
		note += noteClusterNetworkAWSName
	}
	return &copilot.ClusterNetwork{Provider: "aws", Instances: instances, Region: region, From: "node spec.providerID", Note: note}
}

// leadsToProvider reports whether a row of r is a resource in that cloud:
// not cluster plumbing, and not a workload of one of the org's clusters.
func leadsToProvider(r copilot.ExternalConnectionsResult, provider string) bool {
	for _, d := range r.Destinations {
		if d.Provider == provider && !d.Infrastructure && d.InFleet == nil {
			return true
		}
	}
	for _, h := range r.DependencyHosts {
		if c := h.ExternalClassification; c != nil && c.Provider == provider && !c.Infrastructure && h.InFleet == nil {
			return true
		}
	}
	for _, e := range r.Entry {
		if c := e.ExternalClassification; c != nil && c.Provider == provider && !c.Infrastructure {
			return true
		}
	}
	return false
}

const (
	noteEntry = "entry is how requests from outside the cluster reach this workload: the Ingress or LoadBalancer Service in front of it, the address its status holds, classified. " +
		"Pods Ready and answers to callers inside the cluster do not show that this path works. " +
		"targetGroups are the target groups its pods are registered in: elbv2 DescribeTargetHealth with a TargetGroupArn says whether the load balancer reaches them, and targets that time out while the pods are Ready point at the network between the two (clusterNetwork)."
	noteEntryUnread = "Part of the way in could not be read: %s."
)

// entryRows classifies the address of each entry point. With target groups,
// the health of their targets is the first call: the load balancer's own
// description is one call further from the pods.
func entryRows(points []cluster.EntryPoint, v *externalView) []copilot.ExternalEntry {
	out := make([]copilot.ExternalEntry, 0, len(points))
	for _, p := range points {
		row := copilot.ExternalEntry{Via: p.Kind + " " + p.Namespace + "/" + p.Name, Address: p.Address, TargetGroups: p.TargetGroups}
		if p.Address != "" {
			fqdn, ip := p.Address, ""
			if _, ok := parseAddr(p.Address); ok {
				fqdn, ip = "", p.Address
			}
			c := classifyExternal(fqdn, ip, v)
			if len(p.TargetGroups) > 0 {
				c.FirstTools = targetHealthFirst(c.FirstTools)
			}
			row.ExternalClassification = &c
		}
		out = append(out, row)
	}
	return out
}

// targetHealthFirst moves the target health call to the front of tools.
func targetHealthFirst(tools []string) []string {
	const th = "call elbv2 DescribeTargetHealth"
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if t == th {
			out = append([]string{th}, out...)
		} else {
			out = append(out, t)
		}
	}
	return out
}

// fleetCandidates are the addresses worth looking up in the fleet: every
// destination's hostname and address except cluster plumbing, and every
// configured host.
func fleetCandidates(r copilot.ExternalConnectionsResult) []string {
	var out []string
	for _, d := range r.Destinations {
		if d.Infrastructure {
			continue
		}
		for _, a := range []string{d.FQDN, d.IP} {
			if a != "" {
				out = append(out, a)
			}
		}
	}
	for _, h := range r.DependencyHosts {
		if h.Host != "" && !namesBucket(h.Scheme) {
			out = append(out, h.Host)
		}
	}
	return out
}

// annotateFleet names, on each row, the workload a cluster of the org exposes
// on its address, and records how much of the fleet the lookup covered. A
// match keeps the row's classification but replaces its note, and drops the
// cloud tools and the connector it suggested: the dependency is a workload,
// not a cloud resource. It returns the notes on what the lookup could not
// settle: clusters not searched while an address stayed unnamed, and
// hostnames it cannot resolve.
func annotateFleet(r *copilot.ExternalConnectionsResult, book fleetAddressBook, search copilot.FleetSearch, consumer mcpcatalog.Consumer) []string {
	unmatched, unresolvedHost := false, false
	for i := range r.Destinations {
		d := &r.Destinations[i]
		if d.Infrastructure {
			continue
		}
		f := book.find(d.FQDN, "")
		// A hostname that names a managed service identifies the
		// destination: its private IP matching an object elsewhere is an
		// address repeating across networks, not that service.
		if f.match == nil && !namesManagedService(&d.ExternalClassification) {
			f = book.find(d.IP, "")
		}
		if f.match == nil {
			unmatched = true
			if d.IP == "" && d.FQDN != "" && !namesManagedService(&d.ExternalClassification) {
				unresolvedHost = true
			}
			continue
		}
		d.InFleet = f.match
		d.Note = fleetNote(f, consumer)
		d.FirstTools = nil
		d.ConnectorEnabled = false
	}
	for i := range r.DependencyHosts {
		h := &r.DependencyHosts[i]
		if h.Host == "" || namesBucket(h.Scheme) {
			continue
		}
		f := book.find(h.Host, dialedPort(h.Scheme, h.Port))
		if f.match == nil {
			unmatched = true
			if _, isIP := parseAddr(strings.TrimSuffix(strings.TrimPrefix(h.Host, "["), "]")); !isIP && !namesManagedService(h.ExternalClassification) {
				unresolvedHost = true
			}
			continue
		}
		h.InFleet = f.match
		if h.ExternalClassification == nil {
			h.ExternalClassification = &copilot.ExternalClassification{}
		}
		h.Note = fleetNote(f, consumer)
		h.FirstTools = nil
		h.ConnectorEnabled = false
	}
	r.FleetSearch = &search
	var notes []string
	if search.ClustersSkipped > 0 && unmatched {
		notes = append(notes, fmt.Sprintf(noteFleetSkipped, search.ClustersSkipped))
	}
	if unresolvedHost {
		notes = append(notes, noteFleetHostnames)
	}
	return notes
}

// namesManagedService reports whether a row's hostname was recognised as a
// managed service (an RDS instance, an Azure SQL server behind a private
// endpoint): the name, not the address, then says what the destination is.
func namesManagedService(c *copilot.ExternalClassification) bool {
	return c != nil && c.Confidence == extclassify.ConfidenceFQDN && c.Service != ""
}

// dialedPort is the port a configured endpoint is called on: the one it
// names, else its scheme's (http 80, https 443), else unknown.
func dialedPort(scheme, port string) string {
	if port != "" {
		return port
	}
	switch strings.ToLower(scheme) {
	case "http", "ws":
		return "80"
	case "https", "wss":
		return "443"
	}
	return ""
}

// fleetNote tells the caller what to do with an address a cluster of the org
// exposes, and what could make the match wrong.
func fleetNote(f fleetFound, consumer mcpcatalog.Consumer) string {
	m := f.match
	var note string
	switch {
	case m.SameCluster:
		note = fmt.Sprintf(noteFleetSame, m.Kind, m.Namespace, m.Name)
	case consumer == mcpcatalog.ConsumerAutopilot:
		note = fmt.Sprintf(noteFleetOtherAutopilot, m.Kind, m.Namespace, m.Name, m.Cluster)
	default:
		note = fmt.Sprintf(noteFleetOtherKobi, m.Kind, m.Namespace, m.Name, m.Cluster)
	}
	switch {
	case m.OtherClusters > 0:
		note += fmt.Sprintf(noteFleetAmbiguous, m.OtherClusters)
	case f.private && !m.SameCluster:
		note += noteFleetPrivate
	}
	if f.undecided > 0 {
		note += fmt.Sprintf(noteFleetShared, f.undecided)
	}
	return note
}

// externalView is what the answer is built from besides the flows.
type externalView struct {
	// provider and region are the cluster's cloud profile, the fallback for
	// an address with no usable hostname.
	provider, region string
	// internal are addresses inside the cluster (nodes, Service ClusterIPs)
	// and the metadata service: dropped, never destinations.
	internal map[netip.Addr]bool
	// apiServer are the kubernetes Service's endpoints: kept, marked as
	// control plane, so "talks to the API server" is not read as a
	// dependency in the cloud account.
	apiServer map[netip.Addr]bool
	// owner maps a pod to its workload ("Pod", name when unknown).
	owner func(namespace, pod string) (kind, name string)
	// gateway says the install has connectors at all; enabled, which the
	// org has working for the caller.
	gateway bool
	enabled map[string]bool
	// consumer is who asks. Kobi's connectors also follow each one's
	// minimum role, so for Kobi "not enabled" may mean "not for this user".
	consumer mcpcatalog.Consumer
	// toolsOf returns the local tool names a connector serves to the caller
	// (Kobi or Autopilot), or nil when not known yet.
	toolsOf func(id string) map[string]bool
	// dnsHistory maps an address to a hostname seen for it recently.
	dnsHistory map[dnsHistKey]string
}

type dnsHistKey struct{ namespace, ip string }

// imdsAddrs are the instance metadata service. A workload reaching it is
// fetching node credentials, never talking to a dependency.
var imdsAddrs = []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("fd00:ec2::254")}

func (h *handlers) externalViewFor(ctx context.Context, sc flowScope) *externalView {
	v := &externalView{enabled: map[string]bool{}}
	v.provider, v.region = h.cloudProfileOf(sc)
	v.owner = podOwnerResolver(sc.conn)
	v.toolsOf = func(string) map[string]bool { return nil }
	if gw := h.mcpGateway; gw != nil {
		v.gateway = true
		for _, id := range gw.EnabledConnectors(ctx) {
			v.enabled[id] = true
		}
		// One executor answers Kobi and the Autopilot profiles, and the two
		// are served different tools: Autopilot gets no Key Vault, no
		// storage and no S3 reads. A first tool the caller does not have
		// only earns it a refusal.
		consumer := mcpcatalog.ConsumerFrom(ctx)
		v.consumer = consumer
		if !copilot.ConnectorToolsServed(ctx) {
			// A door with no connector tools (the public /mcp endpoint, an
			// Autopilot profile without cloud access): an empty set, known,
			// so every suggestion is filtered out.
			v.toolsOf = func(string) map[string]bool { return map[string]bool{} }
			return v
		}
		memo := map[string]map[string]bool{}
		v.toolsOf = func(id string) map[string]bool {
			if t, ok := memo[id]; ok {
				return t
			}
			// The read-only door, not the admin view: a model's read must
			// never write the org's settings (the admin view generates the
			// AWS external ID, and that write would rebuild the live
			// binding). It never binds either: the names come from a live
			// binding (the caller's turn has usually made one) or the
			// binder's static list, else nothing — and nothing means "do not
			// filter". An empty list for one consumer next to the other's is
			// known: that consumer is served none of the connector's tools.
			var set map[string]bool
			if tools, err := gw.ConnectorToolNames(ctx, id); err == nil && (len(tools.Kobi) > 0 || len(tools.Autopilot) > 0) {
				names := tools.Kobi
				if consumer == mcpcatalog.ConsumerAutopilot {
					names = tools.Autopilot
				}
				set = make(map[string]bool, len(names))
				for _, t := range names {
					set[t] = true
				}
			}
			memo[id] = set
			return set
		}
	}
	return v
}

// cloudProfileOf is the cluster's provider and region: from the connector's
// nodes when there is one, else from the profile cache the overview fills
// (the only source for a metrics-only cluster). Display names ("AWS") are
// normalized by extclassify.
func (h *handlers) cloudProfileOf(sc flowScope) (provider, region string) {
	if sc.conn != nil {
		if _, p, r := sc.conn.CloudProfile(); p != "" {
			return p, r
		}
	}
	if h.manager != nil && sc.uid != "" {
		prof := h.manager.ProfileFor(sc.uid)
		return prof.Provider, prof.Region
	}
	return "", ""
}

// clusterAddresses reads, from the informer caches, the addresses that are
// the cluster itself. The flows already turn live pod IPs into pod edges and
// drop the pod CIDRs; this catches what they miss — hostNetwork pods and
// nodes, Service VIPs a CNI did not translate, and the metadata service.
func clusterAddresses(conn *cluster.Connector) (internal, apiServer map[netip.Addr]bool) {
	internal = map[netip.Addr]bool{}
	apiServer = map[netip.Addr]bool{}
	for _, a := range imdsAddrs {
		internal[a] = true
	}
	if conn == nil {
		return internal, apiServer
	}
	add := func(set map[netip.Addr]bool, s string) {
		if a, ok := parseAddr(s); ok {
			set[a] = true
		}
	}
	if nl := conn.NodeLister(); nl != nil {
		if nodes, err := nl.List(labels.Everything()); err == nil {
			for _, n := range nodes {
				for _, a := range n.Status.Addresses {
					if a.Type == corev1.NodeInternalIP || a.Type == corev1.NodeExternalIP {
						add(internal, a.Address)
					}
				}
			}
		}
	}
	for _, item := range conn.GetResources("services", "", "", "", "", "", "", 1, allRows).Items {
		if ip, ok := item["clusterIP"].(string); ok {
			add(internal, ip)
		}
	}
	for _, item := range conn.GetResources("endpoints", "default", "kubernetes", "", "", "", "", 1, allRows).Items {
		if item["name"] != "kubernetes" {
			continue
		}
		if addrs, ok := item["addresses"].([]string); ok {
			for _, a := range addrs {
				add(apiServer, a)
			}
		}
	}
	return internal, apiServer
}

// podOwnerResolver maps a pod to its workload through the informer caches,
// memoized per call. Without a connector every caller stays a pod.
func podOwnerResolver(conn *cluster.Connector) func(namespace, pod string) (string, string) {
	if conn == nil {
		return func(_, pod string) (string, string) { return "Pod", pod }
	}
	type ref struct{ kind, name string }
	memo := map[string]ref{}
	return func(namespace, pod string) (string, string) {
		key := namespace + "/" + pod
		if r, ok := memo[key]; ok {
			return r.kind, r.name
		}
		kind, name, _ := conn.WorkloadOwner(namespace, "Pod", pod)
		memo[key] = ref{kind, name}
		return kind, name
	}
}

func parseAddr(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}

// podFilter matches the pod names of one workload, past pods included; nil
// matches every pod.
func podFilter(kind, name string) *regexp.Regexp {
	if name == "" {
		return nil
	}
	re, err := regexp.Compile("^(?:" + copilot.PodNamePattern(kind, name) + ")$")
	if err != nil {
		// Unreachable (the name is quoted), and an unfiltered answer would be
		// about the wrong workload: match nothing instead.
		return regexp.MustCompile(`^\b$`)
	}
	return re
}

// selectExternalEdges keeps the flows that leave the cluster, from the
// requested namespace and workload.
func selectExternalEdges(edges []FlowEdge, namespace string, pods *regexp.Regexp) []FlowEdge {
	var out []FlowEdge
	for _, e := range edges {
		if e.DstIP == "" || e.DstPod != "" {
			continue
		}
		if namespace != "" && e.SrcNamespace != namespace {
			continue
		}
		if pods != nil && !pods.MatchString(e.SrcPod) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func addressesWithoutFQDN(edges []FlowEdge) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range edges {
		if e.DstFqdn == "" && !seen[e.DstIP] {
			seen[e.DstIP] = true
			out = append(out, e.DstIP)
		}
	}
	sort.Strings(out)
	return out
}

// dnsHistoryFor looks up hostnames the pods resolved to the given addresses
// over the last few hours, per namespace and cluster-wide. Best-effort: a
// failure leaves the destinations shown by address.
func dnsHistoryFor(ctx context.Context, sc flowScope, namespace string, ips []string) map[dnsHistKey]string {
	selector := `source="hubble"`
	if namespace != "" {
		selector = fmt.Sprintf(`source_namespace=%q,source="hubble"`, namespace)
	}
	if len(ips) <= maxDNSIPFilter {
		quoted := make([]string, len(ips))
		for i, ip := range ips {
			quoted[i] = regexp.QuoteMeta(ip)
		}
		selector += fmt.Sprintf(`,resolved_ip=~%q`, strings.Join(quoted, "|"))
	}
	rows, err := runInstantQuery(ctx, sc.scope(fmt.Sprintf(
		`max by (source_namespace, resolved_ip, fqdn) (last_over_time(pod_dns_resolutions_total{%s}[%s]))`,
		selector, externalDNSLookback,
	)))
	if err != nil {
		return nil
	}
	type best struct {
		fqdn  string
		count float64
	}
	picked := map[dnsHistKey]best{}
	for _, row := range rows {
		ip, fqdn := row.Labels["resolved_ip"], normalizeFQDN(row.Labels["fqdn"])
		if ip == "" || fqdn == "" {
			continue
		}
		// Per namespace first; the cluster-wide entry ("") serves a pod that
		// never resolved the name itself (a sidecar, or a restart since).
		for _, k := range []dnsHistKey{{row.Labels["source_namespace"], ip}, {"", ip}} {
			cur, ok := picked[k]
			if !ok || row.Value > cur.count || (row.Value == cur.count && fqdn < cur.fqdn) {
				picked[k] = best{fqdn, row.Value}
			}
		}
	}
	out := make(map[dnsHistKey]string, len(picked))
	for k, b := range picked {
		out[k] = b.fqdn
	}
	return out
}

func normalizeFQDN(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// aggregateExternal merges external edges into destinations: one per
// hostname (a managed endpoint answers on several addresses, and a failover
// moves it), else one per address. Busiest first; plumbing last.
func aggregateExternal(edges []FlowEdge, v *externalView) []copilot.ExternalDestination {
	type callerKey struct{ namespace, workload, pod string }
	type acc struct {
		key      string
		fqdn     string
		ips      map[string]float64
		rate     float64
		verdicts map[string]bool
		callers  map[callerKey]float64
	}
	byKey := map[string]*acc{}
	for _, e := range edges {
		addr, ok := parseAddr(e.DstIP)
		if ok && v.internal[addr] {
			continue
		}
		fqdn := normalizeFQDN(e.DstFqdn)
		if fqdn == "" && v.dnsHistory != nil {
			fqdn = v.dnsHistory[dnsHistKey{e.SrcNamespace, e.DstIP}]
			if fqdn == "" {
				fqdn = v.dnsHistory[dnsHistKey{"", e.DstIP}]
			}
		}
		key := "ip:" + e.DstIP
		if fqdn != "" {
			key = "fqdn:" + fqdn
		}
		a := byKey[key]
		if a == nil {
			a = &acc{key: key, fqdn: fqdn, ips: map[string]float64{}, verdicts: map[string]bool{}, callers: map[callerKey]float64{}}
			byKey[key] = a
		}
		a.ips[e.DstIP] += e.RatePerSec
		a.rate += e.RatePerSec
		if e.Verdict != "" {
			a.verdicts[e.Verdict] = true
		}
		ck := callerKey{namespace: e.SrcNamespace}
		if kind, name := v.owner(e.SrcNamespace, e.SrcPod); kind != "Pod" && name != "" {
			ck.workload = kind + "/" + name
		} else {
			ck.pod = e.SrcPod
		}
		a.callers[ck] += e.RatePerSec
	}

	type built struct {
		key string
		d   copilot.ExternalDestination
	}
	all := make([]built, 0, len(byKey))
	for _, a := range byKey {
		ip := topKey(a.ips)
		d := copilot.ExternalDestination{
			FQDN:                   a.fqdn,
			IP:                     ip,
			ExternalClassification: classifyExternal(a.fqdn, ip, v),
			RatePerSec:             roundRate(a.rate),
			Verdict:                strings.Join(sortedSet(a.verdicts), ","),
		}
		if len(a.ips) > 1 {
			d.IPCount = len(a.ips)
		}
		callers := make([]copilot.ExternalCaller, 0, len(a.callers))
		for k, r := range a.callers {
			callers = append(callers, copilot.ExternalCaller{Namespace: k.namespace, Workload: k.workload, Pod: k.pod, RatePerSec: roundRate(r)})
		}
		sort.Slice(callers, func(i, j int) bool {
			ci, cj := callers[i], callers[j]
			if ci.RatePerSec != cj.RatePerSec {
				return ci.RatePerSec > cj.RatePerSec
			}
			if ci.Namespace != cj.Namespace {
				return ci.Namespace < cj.Namespace
			}
			if ci.Workload != cj.Workload {
				return ci.Workload < cj.Workload
			}
			return ci.Pod < cj.Pod
		})
		if len(callers) > maxExternalCallers {
			d.MoreCallers = len(callers) - maxExternalCallers
			callers = callers[:maxExternalCallers]
		}
		d.Callers = callers
		all = append(all, built{a.key, d})
	}
	sort.Slice(all, func(i, j int) bool {
		di, dj := all[i].d, all[j].d
		if di.Infrastructure != dj.Infrastructure {
			return !di.Infrastructure
		}
		if di.RatePerSec != dj.RatePerSec {
			return di.RatePerSec > dj.RatePerSec
		}
		return all[i].key < all[j].key
	})
	out := make([]copilot.ExternalDestination, len(all))
	for i, b := range all {
		out[i] = b.d
	}
	return out
}

// classifyExternal names a destination and says whether the org can look at
// it.
func classifyExternal(fqdn, ip string, v *externalView) copilot.ExternalClassification {
	if a, ok := parseAddr(ip); ok && v.apiServer[a] {
		return copilot.ExternalClassification{
			Provider:       extclassify.NormalizeProvider(v.provider),
			Service:        "kube-apiserver",
			Kind:           extclassify.KindInfrastructure,
			Infrastructure: true,
			Confidence:     confidenceClusterState,
			Note:           "This cluster's Kubernetes API server: cluster plumbing, not a dependency.",
		}
	}
	c := extclassify.Classify(fqdn, ip, v.provider, v.region)
	out := copilot.ExternalClassification{
		Provider:       c.Provider,
		Service:        c.Service,
		Kind:           c.Kind,
		ResourceHint:   c.ResourceHint,
		Region:         c.Region,
		Infrastructure: c.Infrastructure,
		Confidence:     c.Confidence,
		Note:           c.Note,
	}
	if out.Provider != "" && v.enabled[out.Provider] {
		out.ConnectorEnabled = true
		if !out.Infrastructure {
			out.FirstTools = filterFirstTools(c.FirstTools, v.toolsOf(out.Provider))
			if out.Provider == extclassify.ProviderAWS {
				// Both consumers are served `call`; the service decides.
				out.FirstTools = awsCallsAdmitted(v.consumer, out.FirstTools)
			}
		}
	}
	return out
}

// filterFirstTools keeps the suggestions whose tool the connector actually
// serves to the caller. A suggestion reads "<local tool> [arguments…]" ("call
// rds DescribeDBInstances"). An unknown tool set (nil) filters nothing; an
// empty one filters everything.
func filterFirstTools(suggested []string, served map[string]bool) []string {
	if served == nil {
		return suggested
	}
	var out []string
	for _, s := range suggested {
		if f := strings.Fields(s); len(f) > 0 && served[f[0]] {
			out = append(out, s)
		}
	}
	return out
}

// awsCallsAdmitted drops an AWS "call <service> <Operation>" suggestion the
// caller's allowlist refuses: both consumers have `call`, only Kobi may call s3.
func awsCallsAdmitted(consumer mcpcatalog.Consumer, suggested []string) []string {
	var out []string
	for _, s := range suggested {
		if f := strings.Fields(s); len(f) == 3 && f[0] == "call" && !mcpcatalog.AWSCallAllowed(consumer, f[1], f[2]) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// dependencyLookup is a workload's configured hosts, or why they are not
// available.
type dependencyLookup struct {
	kind    string
	hosts   []cluster.DependencyHost
	problem string
}

// lookupDependencyHosts reads the hosts a workload's configuration names.
// With no kind it tries the controllers in order and takes the first that
// exists, which also tells the flow filter which naming rule applies.
func lookupDependencyHosts(conn *cluster.Connector, kind, namespace, name string) dependencyLookup {
	switch {
	case conn == nil:
		return dependencyLookup{problem: "The workload's configuration cannot be read without a live connection to the cluster."}
	case namespace == "":
		return dependencyLookup{problem: "Pass namespace too: the workload's configuration can only be read in its namespace."}
	}
	kinds := []string{kind}
	if kind == "" {
		kinds = []string{"Deployment", "StatefulSet", "DaemonSet", "CronJob", "Job", "Pod"}
	}
	var denied []string
	for _, k := range kinds {
		hosts, err := conn.WorkloadDependencyHosts(k, namespace, name)
		if err == nil {
			return dependencyLookup{kind: k, hosts: hosts}
		}
		var pd *cluster.PermissionDeniedError
		switch {
		case isNotFound(err):
		case errors.As(err, &pd):
			denied = append(denied, pd.Resource)
		default:
			return dependencyLookup{problem: "Could not read the workload's configuration: " + err.Error()}
		}
	}
	if len(denied) > 0 {
		return dependencyLookup{problem: "Cannot see the workload's configuration: KubeBolt is not allowed to read " + strings.Join(denied, ", ") + " in this cluster."}
	}
	target := namespace + "/" + name
	if kind != "" {
		target = kind + " " + target
	}
	return dependencyLookup{problem: "Workload " + target + " was not found in this cluster."}
}

// isNotFound recognises a lister miss. The shared informers return an
// apierrors NotFound; the multi-namespace listers used for namespace-scoped
// ServiceAccounts (nslister.go) return a plain "<kind> \"x\" not found".
func isNotFound(err error) bool {
	return apierrors.IsNotFound(err) || strings.HasSuffix(err.Error(), " not found")
}

// shortError keeps an upstream error readable in a note: a metrics store's
// error body can echo the whole query.
func shortError(err error) string {
	const max = 200
	s := err.Error()
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// dependencyRows classifies configured hosts. A reference (no host: a
// Secret, an unreadable ConfigMap or namespace, a Service chain that ends
// without an address) is passed through with its source only: there is no
// address to classify, and nothing must be invented for it.
func dependencyRows(hosts []cluster.DependencyHost, v *externalView) []copilot.ExternalDependencyHost {
	out := make([]copilot.ExternalDependencyHost, 0, len(hosts))
	for _, h := range hosts {
		row := copilot.ExternalDependencyHost{Source: h.Source, Scheme: h.Scheme, Host: h.Host, Port: h.Port}
		if h.Host != "" {
			if fqdn, ok := dependencyFQDN(h, v); ok {
				c := classifyExternal(fqdn, "", v)
				if h.Scheme == "bucket" {
					c.Note = strings.TrimSpace(bucketVarNote + " " + c.Note)
				}
				row.ExternalClassification = &c
			}
		}
		out = append(out, row)
	}
	return out
}

const bucketVarNote = "A bucket name read from a variable named like one; on this AWS cluster it is taken as an S3 bucket."

// dependencyFQDN is the name a dependency row is classified by. A bucket is
// classified through the endpoint that serves it, <bucket>.s3.amazonaws.com,
// so the row gets S3's classification and first calls; a bucket from a
// variable only on an AWS cluster, the one cloud its name can be read in.
// Any other row is its own host.
func dependencyFQDN(h cluster.DependencyHost, v *externalView) (string, bool) {
	switch h.Scheme {
	case "s3":
		return h.Host + ".s3.amazonaws.com", true
	case "bucket":
		if extclassify.NormalizeProvider(v.provider) != extclassify.ProviderAWS {
			return "", false
		}
		return h.Host + ".s3.amazonaws.com", true
	}
	return h.Host, true
}

// namesBucket reports whether a dependency row's host is a bucket name, not
// an address any cluster of the fleet could expose.
func namesBucket(scheme string) bool { return scheme == "s3" || scheme == "bucket" }

// connectorNotes says, once per provider, when a destination sits in a cloud
// the org cannot inspect — so the model names the resource for the operator
// instead of reaching for tools it does not have.
func connectorNotes(r copilot.ExternalConnectionsResult, v *externalView) []string {
	seen := map[string]bool{}
	// A row a cluster of the org exposes (inFleet) is a workload there, not a
	// resource in a cloud account: no connector would look at it.
	for _, d := range r.Destinations {
		if d.Provider != "" && !d.Infrastructure && !d.ConnectorEnabled && d.InFleet == nil {
			seen[d.Provider] = true
		}
	}
	for _, h := range r.DependencyHosts {
		if c := h.ExternalClassification; c != nil && c.Provider != "" && !c.Infrastructure && !c.ConnectorEnabled && h.InFleet == nil {
			seen[c.Provider] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	ids := sortedSet(seen)
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = copilot.ConnectorDisplayName(id)
	}
	if !v.gateway {
		return []string{"Cloud connectors are not available on this installation, so these " + strings.Join(names, " and ") +
			" resources cannot be inspected from here: name each resource and its region for the operator."}
	}
	out := make([]string, len(names))
	for i, n := range names {
		if v.consumer == mcpcatalog.ConsumerKobi {
			// The gateway leaves out a connector whose minimum role is above
			// the user's, and cannot say which without building an admin
			// view: both readings are named, so the model never tells a
			// viewer that nothing is set up when something is.
			out[i] = "No " + n + " connector is available here: none is enabled for this organization, or its minimum role for Kobi is above this user's. An admin can set one up, or change that role, in Administration → AI → Connectors. Until then, name the resource and its region for the operator."
			continue
		}
		out[i] = "No " + n + " connector is enabled for this organization; an admin can set one up in Administration → AI → Connectors. Until then, name the resource and its region for the operator."
	}
	return out
}

// hasDependency reports whether any destination is something other than
// cluster plumbing.
func hasDependency(dests []copilot.ExternalDestination) bool {
	for _, d := range dests {
		if !d.Infrastructure {
			return true
		}
	}
	return false
}

func lacksHostname(dests []copilot.ExternalDestination) bool {
	for _, d := range dests {
		if d.FQDN == "" && !d.Infrastructure {
			return true
		}
	}
	return false
}

// topKey is the key with the highest value; ties go to the smallest key so
// the answer is stable.
func topKey(m map[string]float64) string {
	best, bestV := "", -1.0
	for k, val := range m {
		if val > bestV || (val == bestV && k < best) {
			best, bestV = k, val
		}
	}
	return best
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// roundRate keeps three significant digits: enough to rank and compare, and
// a quiet dependency (0.0021 req/s) does not round to zero. NaN and ±Inf
// become 0: encoding/json refuses them, and one such rate would turn the
// whole answer into a marshal error.
func roundRate(x float64) float64 {
	if x <= 0 || math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	r, err := strconv.ParseFloat(strconv.FormatFloat(x, 'g', 3, 64), 64)
	if err != nil {
		return x
	}
	return r
}
