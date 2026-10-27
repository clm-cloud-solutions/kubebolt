package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ----- external connector tools (mcp__<connector>__<tool>) -----

// mcpToolPrefix marks a tool served by one of the org's external MCP
// connectors (AWS, Google Cloud, Azure). The gateway builds these names; this
// package only routes them.
const mcpToolPrefix = "mcp__"

// MaxMCPCallsPerTurn caps the external connector calls in one chat turn. The
// prompt asks for at most five per question; the hard cap is higher so a
// legitimate follow-up still fits, and low enough that a model stuck in a
// loop cannot spend a minute of provider calls on one answer.
const MaxMCPCallsPerTurn = 12

// MCPRouter executes an external connector tool call for the request's org.
// It is implemented in the api package over the MCP gateway; an interface
// here because this package must not import the gateway (it imports us).
//
// Call never panics and reports every failure — a denial, a connector that
// was disabled mid-conversation, a provider error — as an IsError result,
// never as a Go error: the model has to read it either way.
type MCPRouter interface {
	Call(ctx context.Context, call ToolCall) ToolResult
}

// IsMCPToolName reports whether a tool name belongs to an external connector.
func IsMCPToolName(name string) bool { return strings.HasPrefix(name, mcpToolPrefix) }

// MCPConnectorID returns the connector id of an external tool name
// ("mcp__aws__call" → "aws"). ok is false for any other name.
func MCPConnectorID(name string) (id string, ok bool) {
	rest, found := strings.CutPrefix(name, mcpToolPrefix)
	if !found {
		return "", false
	}
	id, local, found := strings.Cut(rest, "__")
	if !found || id == "" || local == "" {
		return "", false
	}
	return id, true
}

// MCPBudgetExhaustedResult is the synthetic result the chat loop returns for
// an external call past MaxMCPCallsPerTurn. Worded for the model: Anthropic
// tool results carry no is_error flag, so the sentence itself must say that
// nothing was called and what to do instead.
func MCPBudgetExhaustedResult(call ToolCall) ToolResult {
	return ToolResult{
		ToolCallID: call.ID,
		// jsonString, not Sprintf: the name is whatever the model emitted.
		Content: jsonString(map[string]string{"error": fmt.Sprintf(
			"external call budget exhausted: %d cloud connector calls already ran in this answer, so %s was NOT called. Conclude with the evidence you already have and say what you would read next, with its region only if a tool returned it: the next question gets a new budget, so offer to continue rather than asking the operator to run it.",
			MaxMCPCallsPerTurn, call.Name)}),
		IsError: true,
	}
}

// executeMCP routes an external connector call. It runs before the executor
// resolves any cluster: these tools read the org's cloud account, not the
// cluster, and a metrics-only or disconnected cluster is exactly when
// following a failure outside it is most useful.
func (e *Executor) executeMCP(ctx context.Context, call ToolCall) ToolResult {
	if e.mcp == nil {
		// A transcript can carry calls to tools this executor never offered
		// (a connector disabled mid-conversation, the MCP server's executor).
		return ToolResult{
			ToolCallID: call.ID,
			Content:    `{"error":"external connector tools are not enabled here, so this tool cannot be called. Say you cannot read that cloud account from here."}`,
			IsError:    true,
		}
	}
	out := e.mcp.Call(ctx, call)
	out.ToolCallID = call.ID
	// The gateway already caps its output below this; the cap here is what
	// every other tool gets, so an adapter that does not cap cannot be the one
	// that floods the context.
	out.Content = truncateToolResult(out.Content, call.Name)
	if out.NotSent {
		// The prompt's calls-per-question rule is counted by the model:
		// it has to read that this one read nothing.
		out.Content += "\n" + notSentNote
	}
	return out
}

const notSentNote = "Nothing reached the cloud account: this refusal does not count toward the connector calls per question."

// ----- get_external_connections -----

// Window bounds for get_external_connections. The default is wider than the
// Cluster Map's one minute: a dependency a workload calls every few minutes
// (a nightly job's database, a pooled connection's keep-alive) must still
// show up, and 60 is the flows endpoint's own cap.
const (
	defaultExternalWindowMinutes = 15
	maxExternalWindowMinutes     = 60
	// externalConnectionsBudget keeps the answer under the generic tool cap,
	// which would otherwise cut the JSON in the middle of a destination.
	externalConnectionsBudget = maxToolResultBytes - 1024
	// callersKeptWhenTrimming is how many callers each destination keeps
	// once the answer has to shrink: the top callers name the workload, the
	// long tail only repeats it.
	callersKeptWhenTrimming = 3
)

// ExternalConnectionsSource answers get_external_connections for the
// request's org and cluster. Implemented in the api package, where the flow
// queries, the informer caches and the connector gateway live; the org and
// the cluster come from ctx, never from the query.
type ExternalConnectionsSource interface {
	ExternalConnections(ctx context.Context, q ExternalConnectionsQuery) (ExternalConnectionsResult, error)
}

// noConnectorToolsKey marks a request from a door that serves no connector
// tools.
type noConnectorToolsKey struct{}

// WithoutConnectorTools marks ctx as a caller that is served no connector
// tools at all: an external MCP host on the public /mcp door, or an Autopilot
// profile without cloud access. get_external_connections still says which
// connectors the org has on, but suggests no first tool, which that caller
// could only have refused.
func WithoutConnectorTools(ctx context.Context) context.Context {
	return context.WithValue(ctx, noConnectorToolsKey{}, true)
}

// ConnectorToolsServed reports whether the caller in ctx is served connector
// tools. An unmarked request is: Kobi's chat, and the Autopilot profiles with
// cloud access.
func ConnectorToolsServed(ctx context.Context) bool {
	none, _ := ctx.Value(noConnectorToolsKey{}).(bool)
	return !none
}

// ExternalConnectionsQuery is what the model asked for.
type ExternalConnectionsQuery struct {
	// Namespace narrows to callers in one namespace; "" means every one.
	Namespace string
	// WorkloadKind is the canonical kind (Deployment, StatefulSet, DaemonSet,
	// ReplicaSet, Job, CronJob, Pod), or "" when the model passed a bare name.
	WorkloadKind string
	// Workload narrows to one workload's pods; "" means every workload.
	Workload string
	// WindowMinutes is the rate window, already clamped to 1..60.
	WindowMinutes int
}

// ExternalConnectionsResult is the tool's answer. Every field is derived from
// addresses, hostnames and resource names — never from a credential or a
// configuration value.
type ExternalConnectionsResult struct {
	// SourcePresent is false when there is no flow data for this cluster at
	// all (Hubble not installed or not shipping): NOT "no connections".
	SourcePresent bool `json:"sourcePresent"`
	// ClusterUIDKnown is false while KubeBolt has not read the cluster's
	// identity yet; flow queries cannot be scoped until it has, so the flow
	// answer is empty for that reason and not because there is no traffic.
	ClusterUIDKnown bool   `json:"clusterUIDKnown"`
	WindowMinutes   int    `json:"windowMinutes"`
	Namespace       string `json:"namespace,omitempty"`
	// Workload echoes the resolved workload as Kind/name.
	Workload string `json:"workload,omitempty"`
	// Destinations are the external endpoints seen in the flows, busiest
	// first, cluster plumbing (infrastructure) last.
	Destinations []ExternalDestination `json:"destinations"`
	// OmittedDestinations counts destinations left out to keep the answer
	// small.
	OmittedDestinations int `json:"omittedDestinations,omitempty"`
	// DependencyHosts is the fallback when the flows cannot answer for a
	// workload: the hosts its configuration names, and those the ExternalName
	// and selectorless Services it names lead to, in any namespace. An entry
	// with no host is a lead with no address: a Secret (never read), a
	// ConfigMap or a namespace's Services that could not be read, or a
	// Service chain that ends without an address; its source says which.
	DependencyHosts        []ExternalDependencyHost `json:"dependencyHosts,omitempty"`
	OmittedDependencyHosts int                      `json:"omittedDependencyHosts,omitempty"`
	// Entry is how requests from outside the cluster reach the workload (or
	// the namespace): the Ingresses and LoadBalancer Services in front of it.
	Entry        []ExternalEntry `json:"entry,omitempty"`
	OmittedEntry int             `json:"omittedEntry,omitempty"`
	// FleetSearch is present when the addresses above were looked up among
	// what the organization's other clusters expose (see InFleet).
	FleetSearch *FleetSearch `json:"fleetSearch,omitempty"`
	// ClusterNetwork names where this cluster's own network lives in its
	// cloud, when a row above is a resource of that cloud.
	ClusterNetwork *ClusterNetwork `json:"clusterNetwork,omitempty"`
	Notes          []string        `json:"notes,omitempty"`
}

// ExternalEntry is an Ingress or a Service of type LoadBalancer in front of
// the workload: the address users reach, classified like any other, and on
// EKS the target groups the AWS Load Balancer Controller keeps its pods in.
type ExternalEntry struct {
	// Via is "Ingress <namespace>/<name>" or "Service <namespace>/<name>".
	Via     string `json:"via"`
	Address string `json:"address,omitempty"`
	// TargetGroups are target group ARNs, for elbv2 DescribeTargetHealth.
	TargetGroups []string `json:"targetGroups,omitempty"`
	*ExternalClassification
}

// ClusterNetwork is where the cluster's own network lives in its cloud
// account: on AKS, the resource group of the network security group, load
// balancer, NAT gateway and public IPs the pods' traffic crosses, in and out;
// on EKS, the node instances whose security groups, subnets' network ACLs and
// route tables it crosses. Read from the nodes, never from the cloud. Without
// it Kobi listed the whole account to find that group, spent its connector
// calls on the wrong ones and never read the security group whose outbound
// rule cut the cluster off its database (nsg-egress-deny-pg, 2026-10-12).
type ClusterNetwork struct {
	Provider       string   `json:"provider"`
	ResourceGroups []string `json:"resourceGroups,omitempty"`
	// Instances are the EC2 instance ids of the nodes, in Region.
	Instances []string `json:"instances,omitempty"`
	Region    string   `json:"region,omitempty"`
	// From is the node field or label the rest was read from.
	From string `json:"from"`
	Note string `json:"note"`
}

// FleetSearch counts the clusters an address lookup covered, among those the
// caller may see: searched, or skipped because KubeBolt has no live
// connection to them right now (they are never connected just to answer). A
// metrics-only cluster has no resources to read and is not counted.
type FleetSearch struct {
	ClustersSearched int `json:"clustersSearched"`
	ClustersSkipped  int `json:"clustersSkipped"`
}

// FleetMatch says an address is exposed by a Service (its LoadBalancer
// ingress), an Ingress or a Gateway of a cluster of the caller's organization:
// a workload there, not a cloud dependency.
type FleetMatch struct {
	// Cluster is the name the operator knows it by (its display name, else its
	// context), sanitized to one short line: the display name is written by
	// whoever installed the agent.
	Cluster string `json:"cluster"`
	// ClusterContext identifies the cluster uniquely, where two display names
	// can repeat: what offer_cluster_switch takes.
	ClusterContext string   `json:"clusterContext"`
	Kind           string   `json:"kind"`
	Namespace      string   `json:"namespace"`
	Name           string   `json:"name"`
	Ports          []string `json:"ports,omitempty"`
	// SameCluster marks an address of the cluster being investigated, reached
	// through its own external address (a hairpin).
	SameCluster bool `json:"sameCluster"`
	// SharedBy counts the other objects of that cluster exposing the same
	// address (an ingress controller's LoadBalancer and the Ingresses it
	// serves).
	SharedBy int `json:"sharedBy,omitempty"`
	// OtherClusters counts other clusters exposing the same address: a private
	// address can repeat across networks.
	OtherClusters int `json:"otherClusters,omitempty"`
}

// ExternalClassification names what an endpoint is (see package extclassify)
// and whether the org can inspect it.
type ExternalClassification struct {
	// Provider is the connector id (aws, gcp, azure), "" when unattributed.
	Provider     string `json:"provider,omitempty"`
	Service      string `json:"service,omitempty"`
	Kind         string `json:"kind"`
	ResourceHint string `json:"resourceHint,omitempty"`
	Region       string `json:"region,omitempty"`
	// Infrastructure marks cluster plumbing: never the dependency to chase.
	Infrastructure bool   `json:"infrastructure,omitempty"`
	Confidence     string `json:"confidence"`
	// FirstTools are the connector's LOCAL tool names to start with, among
	// those it serves the caller (Kobi and Autopilot are served different
	// sets); the callable name adds the connector prefix shown in the tool
	// list. Present only when ConnectorEnabled, and never for a caller
	// served no connector tools (WithoutConnectorTools).
	FirstTools []string `json:"firstTools,omitempty"`
	// ConnectorEnabled says the org has a working connector for Provider.
	ConnectorEnabled bool   `json:"connectorEnabled"`
	Note             string `json:"note,omitempty"`
}

// ExternalDestination is one external endpoint and who calls it.
type ExternalDestination struct {
	FQDN string `json:"fqdn,omitempty"`
	IP   string `json:"ip,omitempty"`
	// IPCount is the number of distinct addresses seen for FQDN when more
	// than one (a managed endpoint behind several addresses).
	IPCount int `json:"ipCount,omitempty"`
	ExternalClassification
	RatePerSec float64 `json:"ratePerSec"`
	// Verdict is the flow verdict ("forwarded"; comma-joined when mixed).
	Verdict     string           `json:"verdict,omitempty"`
	Callers     []ExternalCaller `json:"callers"`
	MoreCallers int              `json:"moreCallers,omitempty"`
	// InFleet names the workload behind the address when a cluster of the
	// organization exposes it.
	InFleet *FleetMatch `json:"inFleet,omitempty"`
}

// ExternalCaller is one workload (or, when its owner is unknown, one pod)
// sending traffic to a destination.
type ExternalCaller struct {
	Namespace string `json:"namespace"`
	// Workload is Kind/name, e.g. Deployment/orders.
	Workload   string  `json:"workload,omitempty"`
	Pod        string  `json:"pod,omitempty"`
	RatePerSec float64 `json:"ratePerSec"`
}

// ExternalDependencyHost is one endpoint a workload's configuration names,
// directly or through a Service. A nil classification means the entry has no
// host to classify: a Secret (not read), a ConfigMap or Services that could
// not be read, or a Service chain that ends without an address.
type ExternalDependencyHost struct {
	Source string `json:"source"`
	Scheme string `json:"scheme,omitempty"`
	Host   string `json:"host,omitempty"`
	Port   string `json:"port,omitempty"`
	*ExternalClassification
	// InFleet names the workload behind the host when a cluster of the
	// organization exposes it.
	InFleet *FleetMatch `json:"inFleet,omitempty"`
}

// execExternalConnections runs get_external_connections. The boolean is the
// IsError flag.
func (e *Executor) execExternalConnections(ctx context.Context, args map[string]interface{}) (string, bool) {
	if e.external == nil {
		return `{"error":"cannot see external connections from this server: the flow data and workload configuration behind this tool are not available here. This is NOT evidence that the workload has no external dependencies — say you cannot see them from here."}`, true
	}
	q := ExternalConnectionsQuery{
		Namespace:     strings.TrimSpace(stringArg(args, "namespace")),
		WindowMinutes: intArg(args, "windowMinutes", defaultExternalWindowMinutes),
	}
	if q.Namespace != "" && !namespaceNameRE.MatchString(q.Namespace) {
		// Refused, not searched: a namespace that cannot exist matches no
		// flow, and the answer would read "no traffic left the cluster".
		return jsonString(map[string]string{"error": fmt.Sprintf("namespace %q is not a valid Kubernetes namespace name", q.Namespace)}), true
	}
	if q.WindowMinutes < 1 {
		q.WindowMinutes = defaultExternalWindowMinutes
	}
	if q.WindowMinutes > maxExternalWindowMinutes {
		q.WindowMinutes = maxExternalWindowMinutes
	}
	if w := strings.TrimSpace(stringArg(args, "workload")); w != "" {
		kind, name, err := ParseWorkloadRef(w)
		if err != nil {
			return errJSON(err), true
		}
		q.WorkloadKind, q.Workload = kind, name
	}
	out, err := e.external.ExternalConnections(ctx, q)
	if err != nil {
		return jsonString(map[string]string{"error": "could not read external connections: " + err.Error()}), true
	}
	if out.WindowMinutes == 0 {
		out.WindowMinutes = q.WindowMinutes
	}
	FitExternalConnections(&out, externalConnectionsBudget)
	out.Notes = append(out.Notes, ExternalConnectionsSkillNote)
	return jsonString(out), false
}

// ExternalConnectionsSkillNote points from the tool's answer to the
// procedure that reads it, so loading it does not depend on the model
// remembering the index.
const ExternalConnectionsSkillNote = "To read these (classify each address, another cluster of the organization, a managed cloud service behind it), load_skill external-dependencies if you have not."

// AnnotateExternalConnections adds the skill note to an answer recorded
// before the tool wrote it, so a replay reads what the tool writes today.
func AnnotateExternalConnections(text string) string {
	var out map[string]interface{}
	if json.Unmarshal([]byte(text), &out) != nil || out["error"] != nil {
		return text
	}
	notes, _ := out["notes"].([]interface{})
	for _, n := range notes {
		if n == ExternalConnectionsSkillNote {
			return text
		}
	}
	out["notes"] = append(notes, ExternalConnectionsSkillNote)
	return jsonString(out)
}

// workloadKinds maps the kind spellings the model uses to the canonical one.
var workloadKinds = map[string]string{
	"deployment": "Deployment", "deployments": "Deployment", "deploy": "Deployment",
	"statefulset": "StatefulSet", "statefulsets": "StatefulSet", "sts": "StatefulSet",
	"daemonset": "DaemonSet", "daemonsets": "DaemonSet", "ds": "DaemonSet",
	"replicaset": "ReplicaSet", "replicasets": "ReplicaSet", "rs": "ReplicaSet",
	"job": "Job", "jobs": "Job",
	"cronjob": "CronJob", "cronjobs": "CronJob", "cj": "CronJob",
	"pod": "Pod", "pods": "Pod", "po": "Pod",
}

// namespaceNameRE is an RFC 1123 label, the shape of every namespace name.
var namespaceNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// workloadNameRE is an RFC 1123 name: it goes into a regular expression and a
// lister key, so anything else is refused rather than escaped into a pattern
// that could never match.
var workloadNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// ParseWorkloadRef splits "Kind/name" (Deployment/orders, deploy/orders) or a
// bare name into the canonical kind ("" for a bare name) and the name.
func ParseWorkloadRef(s string) (kind, name string, err error) {
	s = strings.TrimSpace(s)
	name = s
	if k, n, found := strings.Cut(s, "/"); found {
		kind = workloadKinds[strings.ToLower(strings.TrimSpace(k))]
		if kind == "" {
			return "", "", fmt.Errorf("workload must be Kind/name (Deployment, StatefulSet, DaemonSet, Job, CronJob or Pod) or a bare name, not %q", s)
		}
		name = strings.TrimSpace(n)
	}
	if !workloadNameRE.MatchString(name) {
		return "", "", fmt.Errorf("workload name %q is not a valid Kubernetes name", name)
	}
	return kind, name, nil
}

// PodNamePattern returns an unanchored regular expression matching the names
// of the pods a workload creates, by the controllers' naming conventions (see
// promBuilder.podNamePattern). It matches the pods of earlier rollouts too,
// which a list of live pods would not: flow data reaches back over a window
// in which pods came and went. An empty kind matches the shapes of every
// controller (and a bare pod of that name), which can also match a sibling
// workload whose name extends this one; callers resolve the kind when they
// can.
func PodNamePattern(kind, name string) string {
	switch kind {
	case "Pod":
		return regexp.QuoteMeta(name)
	case "ReplicaSet":
		return regexp.QuoteMeta(name) + "-[a-z0-9]+"
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
		return (&promBuilder{kind: kind, name: name}).podNamePattern()
	}
	return regexp.QuoteMeta(name) + "(?:-[a-z0-9]+){0,2}"
}

// FitExternalConnections shrinks r until its JSON is at most budget bytes:
// first the long tail of callers, then the quietest destinations, then the
// last configuration hosts. Each cut is counted in the result, so the model
// knows the list is partial rather than concluding the rest does not exist.
func FitExternalConnections(r *ExternalConnectionsResult, budget int) {
	if r.Destinations == nil {
		r.Destinations = []ExternalDestination{}
	}
	for i := range r.Destinations {
		if r.Destinations[i].Callers == nil {
			r.Destinations[i].Callers = []ExternalCaller{}
		}
	}
	size := func() int {
		b, err := json.Marshal(r)
		if err != nil {
			return 0
		}
		return len(b)
	}
	if size() <= budget {
		return
	}
	r.Notes = append(r.Notes, "The answer was trimmed to fit; narrow it with namespace or workload to see everything.")
	for i := range r.Destinations {
		d := &r.Destinations[i]
		if len(d.Callers) > callersKeptWhenTrimming {
			d.MoreCallers += len(d.Callers) - callersKeptWhenTrimming
			d.Callers = d.Callers[:callersKeptWhenTrimming]
		}
	}
	for size() > budget && len(r.Destinations) > 0 {
		r.Destinations = r.Destinations[:len(r.Destinations)-1]
		r.OmittedDestinations++
	}
	for size() > budget && len(r.DependencyHosts) > 0 {
		r.DependencyHosts = r.DependencyHosts[:len(r.DependencyHosts)-1]
		r.OmittedDependencyHosts++
	}
}
