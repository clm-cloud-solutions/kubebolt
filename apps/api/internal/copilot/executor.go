package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Limits applied to tool results to prevent context window blow-ups.
// Pod logs and full topology dumps can be enormous and quickly exhaust the
// LLM's context if multiple calls are made in sequence.
const (
	// Max bytes a generic tool result can occupy. ~32KB ≈ 8K tokens.
	maxToolResultBytes = 32 * 1024
	// Max lines we'll fetch from a pod log regardless of what the LLM asks.
	maxLogTailLines = 500
	// Max bytes we'll keep from pod logs after fetch+filter. Slightly larger
	// than the generic cap because log investigation is a primary use case
	// and lines are cheap to tokenize. ~48KB ≈ 12K tokens worst case.
	maxLogBytes = 48 * 1024
	// Default tail when the LLM doesn't specify one.
	defaultLogTailLines = 200
	// Burst window for get_operational_episodes. The default matches the
	// /insights/operational-episodes endpoint so Kobi and the UI describe the
	// same day. The cap is the insights retention floor — a wider window
	// recomputes over rows the retention pass has already pruned, which costs
	// the query and returns nothing.
	defaultBurstWindowHours = 24
	maxBurstWindowHours     = 168
	// Cap on bursts per answer. At ~280 bytes each this keeps the result near
	// 1.5k tokens even on the worst morning — the tool must not become the
	// thing that blows the context it exists to save.
	maxBurstsReturned = 20
	// Episode history. The default window matches GET /insights/episodes so
	// Kobi and the History tab describe the same day.
	defaultEpisodeWindowHours = 24
	maxEpisodeWindowHours     = 720
	defaultEpisodesReturned   = 25
	maxEpisodesReturned       = 50
	recurrenceReturned        = 8
	// Security findings. 1,224 active rows in one production org: a list is
	// never the default answer, a posture is.
	defaultFindingRows = 25
	maxFindingRows     = 50
	topImagesReturned  = 5
	// Workload rows, ranked worst first. The Security page shows 25 a page; a
	// shortlist is what an answer needs, the rest is "and N more".
	defaultFindingWorkloads = 15
	maxFindingWorkloads     = 50
	// One finding's drill-down. A CVE can sit in many packages of one image,
	// and a CIS control can fail on hundreds of resources: enough to act on,
	// with the total stated.
	detailPackagesReturned = 20
	detailFailingReturned  = 25
	// Recent deploys: "what changed" is asked about the last hours, rarely
	// beyond a week; the ReplicaSets older than the history limit are gone
	// from the cluster anyway.
	defaultDeployWindowHours = 24
	maxDeployWindowHours     = 168
	defaultDeploysReturned   = 25
	maxDeploysReturned       = 100
	// Runtime events (Falco). A noisy rule fires hundreds of times an hour:
	// the counts say that, the rows are a sample, newest first.
	defaultRuntimeEventHours     = 24
	defaultRuntimeEventsReturned = 10
	maxRuntimeEventsReturned     = 25
	maxRuntimeEventsScanned      = 500
)

// EpisodeSource is the slice of insights.EpisodeReader the history tools need,
// with the ORG already bound by the caller. An interface rather than a pair of
// funcs because the three reads belong together and a fourth would otherwise
// change the setter's signature; org-bound rather than org-taking for the same
// reason metricsRetention is a func — this package must not learn the tenancy
// plumbing.
type EpisodeSource interface {
	// Window is the history list: the overlap query behind GET /insights/episodes.
	Window(ctx context.Context, q insights.EpisodeQuery) ([]insights.Episode, error)
	// Episode is one episode with its append-only transitions.
	Episode(ctx context.Context, id string) (insights.Episode, []insights.Transition, error)
	// Recurrence is the same fingerprint over time — "has this happened before".
	Recurrence(ctx context.Context, fingerprint string, limit int32) ([]insights.Episode, error)
}

// FindingSource is the security pillar's read side, with the caller's ORG and
// ENTITLEMENT already applied. Scope matters more here than anywhere else in
// the executor: findings are per-cluster rows, and cluster_scope.go records
// that forgetting to narrow them shipped three times.
type FindingSource interface {
	// List returns the records the caller may read. The implementation applies
	// the org and the team narrowing; the cluster is q.ClusterID, or every
	// cluster when allClusters.
	List(ctx context.Context, q findings.Query, allClusters bool) ([]findings.Record, error)
	// Detail is one finding plus a live re-read of its scanner — the same
	// drill-down the Security page opens. found=false covers both "no such
	// finding" and "not yours to read", which the caller must not tell apart.
	Detail(ctx context.Context, clusterID, fingerprint string) (detail *findings.Detail, found bool, err error)
}

// RuntimeEventSource is the Falco side of the security pillar, with the
// caller's ORG and ENTITLEMENT applied by the implementation, as with
// FindingSource. A runtime event carries the command line that ran and the
// user it ran as — the most revealing payload in the pillar.
type RuntimeEventSource interface {
	// List returns events newest first. The cluster is q.ClusterID, or every
	// cluster when allClusters.
	List(ctx context.Context, q findings.EventQuery, allClusters bool) ([]findings.EventRecord, error)
}

// FleetSource answers "which cluster is worst" across everything the caller
// may see. Org and entitlement are applied by the implementation, as with
// FindingSource.
type FleetSource interface {
	// ActiveInsights returns every ACTIVE insight across the caller's clusters.
	// Resolved ones are history: counting them would paint a cluster amber
	// after it was fixed.
	ActiveInsights(ctx context.Context) ([]insights.InsightRecord, error)
	// ClusterName resolves a cluster UID to its display name. An episode — and
	// a fleet row — outlives its cluster, and a uid means nothing to a reader.
	ClusterName(ctx context.Context, clusterID string) string
}

// ClusterListSource answers which clusters this call's caller may see: what
// list_clusters lists and the only clusters offer_cluster_switch may offer.
// The org and team walls are applied by the implementation, from ctx — the
// same rule GET /clusters applies to a person, and the investigated cluster's
// audience for a service principal. Manager.ListClusters is not that list:
// it walks every context of the process, every org's agent:<uid> and the
// operator's own clusters among them.
type ClusterListSource interface {
	// CallerClusters returns the caller's clusters; false when they could not
	// be decided (no identity, or the reads did not finish in time), and then
	// nothing may be listed or offered.
	CallerClusters(ctx context.Context) ([]cluster.ClusterInfo, bool)
}

// Executor runs tool calls server-side using the active Connector and Engine.
// Each tool maps to existing connector/engine methods. Tool execution is
// internal to the backend — no HTTP round-trip from the chat handler to
// other endpoints.
type Executor struct {
	manager *cluster.Manager
	// metricsRetention answers how far back this request's org keeps metrics.
	// nil, or a non-positive result, means no cap — self-hosted and OSS, where
	// retention is whatever the operator configured on their own storage.
	//
	// A function rather than a value because the answer is per-ORG and resolved
	// from the request context, and an interface rather than importing settings
	// because this package must not depend on the EE plan machinery.
	metricsRetention func(ctx context.Context) time.Duration
	// operationalEpisodes answers "what bursts happened in this window" for
	// the request's org. A function for the same reason metricsRetention is
	// one: the org is resolved from the request context, and binding it here
	// keeps this package clear of the tenancy plumbing. nil means the install
	// has no operational reader (the episode store is a type assertion away
	// from it), and the tool says so rather than pretending there were none —
	// "no bursts" and "I cannot see bursts" are different answers.
	operationalEpisodes func(ctx context.Context, from, to time.Time) ([]insights.OperationalEpisode, error)
	// episodes answers "has this happened before" and "what happened on the
	// cluster that is down". nil means the install has no episode store, and
	// the tools say so rather than reporting an empty history.
	episodes EpisodeSource
	// findings is the security pillar. nil means persistence is off and the
	// tool says so rather than reporting a clean cluster.
	findings FindingSource
	// runtimeEvents is Falco's feed. nil means no event store, and the tool
	// says so rather than reporting a quiet cluster.
	runtimeEvents RuntimeEventSource
	// coverage is get_coverage's metric probes and agent registry.
	coverage CoverageSource
	// metricsQuery runs query_metrics. nil means no metrics store.
	metricsQuery MetricsQuerySource
	// rightSizing is GET /right-sizing's computation behind get_right_sizing.
	rightSizing RightSizingSource
	// fleet answers across clusters. nil means no persisted insight store, and
	// the tool says so rather than reporting a healthy fleet.
	fleet FleetSource
	// clusterList answers list_clusters and offer_cluster_switch with the
	// caller's clusters. nil means none was wired: single-tenant keeps the
	// manager's list (one org, no teams); multi-tenant lists and offers
	// nothing, since the manager's list is every org's.
	clusterList ClusterListSource
}

// NewExecutor creates a new tool executor bound to a cluster manager.
func NewExecutor(manager *cluster.Manager) *Executor {
	return &Executor{manager: manager}
}

// WithMetricsRetention wires the per-org metrics-retention lookup, so a range
// wider than the org actually keeps is narrowed to what exists instead of being
// served half-empty. Chainable; nil is accepted and means "no cap".
func (e *Executor) WithMetricsRetention(fn func(ctx context.Context) time.Duration) *Executor {
	e.metricsRetention = fn
	return e
}

// WithOperationalEpisodes wires the per-org burst lookup behind
// get_operational_episodes. Chainable; nil is accepted and means the install
// cannot answer the question.
func (e *Executor) WithOperationalEpisodes(fn func(ctx context.Context, from, to time.Time) ([]insights.OperationalEpisode, error)) *Executor {
	e.operationalEpisodes = fn
	return e
}

// WithEpisodes wires the org's insight history behind get_insight_episodes and
// get_insight_episode. Chainable; nil is accepted and means the install cannot
// answer the question.
func (e *Executor) WithEpisodes(src EpisodeSource) *Executor {
	e.episodes = src
	return e
}

// WithFindings wires the org's security findings behind get_findings.
// Chainable; nil is accepted and means the install cannot answer.
func (e *Executor) WithFindings(src FindingSource) *Executor {
	e.findings = src
	return e
}

// WithRuntimeEvents wires Falco's runtime events behind get_runtime_events.
// Chainable; nil is accepted and means the install cannot answer.
func (e *Executor) WithRuntimeEvents(src RuntimeEventSource) *Executor {
	e.runtimeEvents = src
	return e
}

// WithFleet wires the cross-cluster view behind get_fleet_summary.
// Chainable; nil is accepted and means the install cannot answer.
func (e *Executor) WithFleet(src FleetSource) *Executor {
	e.fleet = src
	return e
}

// WithClusterList wires the caller's clusters behind list_clusters and
// offer_cluster_switch. Chainable; nil is accepted (see Executor.clusterList).
func (e *Executor) WithClusterList(src ClusterListSource) *Executor {
	e.clusterList = src
	return e
}

// callerClusters is the clusters this call's caller may see; false when they
// cannot be told, and then nothing is listed or offered.
func (e *Executor) callerClusters(ctx context.Context) ([]cluster.ClusterInfo, bool) {
	if e.clusterList != nil {
		return e.clusterList.CallerClusters(ctx)
	}
	if auth.MultiTenantEnabled || e.manager == nil {
		return nil, false
	}
	return e.manager.ListClusters(ctx), true
}

// Execute runs a single tool call against the default-tenant + active-cluster
// runtime. It is a thin shim over ExecuteCtx for callers that have no request
// context (the OSS chat loop). New callers that DO carry a request context
// — most notably the MCP server, where the context holds the resolved
// (tenant, cluster) RuntimeKey — must call ExecuteCtx so per-tenant routing
// works. See internal/kubebolt-w2-connector-pool-design.md.
func (e *Executor) Execute(call ToolCall) ToolResult {
	return e.ExecuteCtx(context.Background(), call)
}

// ExecuteCtx runs a single tool call and returns its result as a JSON string,
// resolving the connector/engine for the (tenant, cluster) carried by ctx via
// cluster.RuntimeKeyFromContext. When ctx has no RuntimeKey (the zero value),
// it resolves to default-tenant + active-cluster — preserving the original
// single-tenant OSS behavior. Errors during execution are returned as
// ToolResult with IsError=true so the caller (LLM or MCP host) can react
// gracefully.
func (e *Executor) ExecuteCtx(ctx context.Context, call ToolCall) ToolResult {
	return redactToolResult(call.Name, e.executeCtx(ctx, call))
}

// redactedJSONTools return Kubernetes objects whose env, args, annotations or
// messages may carry a credential: their JSON goes through cluster.RedactObject
// on the way out. Free-text tools (logs, describe, YAML) are redacted where the
// text is produced, because their payload is one string field.
var redactedJSONTools = map[string]bool{
	"get_resource_detail":  true,
	"get_events":           true,
	"get_workload_pods":    true,
	"get_cronjob_jobs":     true,
	"get_workload_history": true,
}

// redactToolResult is the last step of every tool call: a result reaches the
// model provider, Autopilot's run_events and the incident timeline, so a
// credential in it would leak three times over. See cluster/redact.go.
func redactToolResult(name string, res ToolResult) ToolResult {
	if res.IsError || !redactedJSONTools[name] || res.Content == "" {
		return res
	}
	var v interface{}
	if err := json.Unmarshal([]byte(res.Content), &v); err != nil {
		res.Content = cluster.RedactText(res.Content)
		return res
	}
	res.Content = jsonString(cluster.RedactObject(v))
	return res
}

func (e *Executor) executeCtx(ctx context.Context, call ToolCall) ToolResult {
	res := ToolResult{ToolCallID: call.ID}

	// A nil manager is a legitimate Executor — one wired for the tools that
	// read no cluster state at all. Resolving through it unconditionally would
	// panic before the per-tool gate below could decide anything.
	var conn *cluster.Connector
	if e.manager != nil {
		conn = e.manager.Connector(ctx)
	}
	// Metrics-only / disconnected: most tools read live cluster objects through the
	// connector — resource reads, per-workload metrics (which resolve a workload→pods
	// via the API + the cluster UID), and the propose_* actions. Gate them per-tool
	// with an actionable message instead of failing opaquely, so Kobi stays usable for
	// docs + guidance on a monitored-only cluster. (Full metrics RCA needs the
	// connector's workload→pod resolution — that's the Phase-2 KSM-resource path.)
	if conn == nil {
		switch call.Name {
		case "get_kubebolt_docs":
			// knowledge-base lookup — no connector needed; fall through.
		case "offer_cluster_switch", "list_clusters":
			// Reads the cluster LIST, not any cluster. Gating it on a live
			// connector would withhold the switch exactly when the selected
			// cluster is the broken one — and list_clusters is how an MCP
			// client with no cluster selected finds out which ones exist; it
			// used to answer "enable the agent-proxy" to that question.
		case "get_fleet_summary":
			// Cross-cluster and read from the persisted store. Requiring a
			// live connector would make "which cluster is worst" unanswerable
			// precisely when one of them is down.
		case "get_coverage":
			// "What can KubeBolt see" is asked precisely when the cluster is
			// not reachable; it reports that instead of failing on it.
		case "query_metrics":
			// Metrics live in the metrics store, and a metrics-only cluster
			// has series but no connector.
		case "get_findings", "get_finding_workloads", "get_finding_detail", "get_runtime_events":
			// Security findings are persisted rows, org-level, swept in the
			// background — GET /findings is outside requireConnector for that
			// reason. A cluster that is down is still carrying whatever CVEs it
			// had, and refusing to say so would report a connectivity problem
			// as a security answer. The detail re-reads the scanner through the
			// FINDING's cluster, not the request's, and degrades to the stored
			// row when that one is down too.
		case "get_insight_episodes", "get_insight_episode":
			// Insight history is org-level and lives in the persisted episode
			// store. Answering WITHOUT a connector is the whole point: the
			// question "what happened on the cluster that died last night" is
			// asked about clusters that are down, and GET /insights/episodes
			// sits outside requireConnector for exactly this.
		case "get_operational_episodes":
			// Burst history is org-level and lives in the persisted episode
			// store, not in the cluster runtime. It MUST answer without a
			// connector: the question "why did everything break at 03:10" is
			// asked about clusters that are down, and it is exactly the answer
			// that stops Kobi from blaming the workload that died with them.
			// GET /insights/operational-episodes is mounted outside
			// requireConnector for the same reason — keep the two in step.
		default:
			res.Content = `{"error":"This needs live cluster access. Enable the KubeBolt agent-proxy (rbac.mode=reader or operator) or connect the cluster's API directly — the metrics dashboards still work.","needsProxy":true}`
			res.IsError = true
			return res
		}
	}

	args := parseArgs(call.Input)

	switch call.Name {
	case "get_cluster_overview":
		res.Content = jsonString(conn.GetOverview())

	case "list_resources":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		search := stringArg(args, "search")
		status := stringArg(args, "status")
		sort := stringArg(args, "sort")
		order := stringArg(args, "order")
		page := intArg(args, "page", 1)
		limit := intArg(args, "limit", 50)
		if t == "" {
			res.Content = `{"error":"type parameter is required"}`
			res.IsError = true
			return res
		}
		node := stringArg(args, "node")
		list := conn.GetResources(t, ns, search, status, node, sort, order, page, limit)
		if list.Forbidden {
			// An optional CRD that is simply not installed reads as CanList=false
			// too. Saying "forbidden" there sent the operator after an RBAC grant
			// for a CRD that does not exist (2026-09-27, certificates on a
			// cluster without cert-manager). The permission probe already knows
			// the difference.
			if p, ok := conn.Permissions()[t]; ok && p != nil && p.Absent {
				res.Content = jsonString(map[string]interface{}{
					"type":         t,
					"notInstalled": true,
					"message":      fmt.Sprintf("%s is not installed on this cluster: the %s API (%s) is not registered. There is nothing to list — this is not a permission problem.", t, t, p.Group),
				})
				return res
			}
			res.Content = fmt.Sprintf(`{"error":"forbidden: insufficient permissions to access %s","forbidden":true}`, t)
			res.IsError = true
			return res
		}
		res.Content = jsonString(list)

	case "get_resource_detail":
		t, ns, name := nsResourceArgs(args)
		if t == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		detail, err := conn.GetResourceDetail(t, ns, name)
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		res.Content = jsonString(detail)

	case "get_resource_yaml":
		t, ns, name := nsResourceArgs(args)
		if t == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		yamlBytes, err := conn.GetResourceYAML(t, ns, name)
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		res.Content = jsonString(map[string]string{"yaml": string(cluster.RedactYAML(yamlBytes))})

	case "get_resource_describe":
		t, ns, name := nsResourceArgs(args)
		if t == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		describeOutput, err := describeResource(conn, t, ns, name)
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		res.Content = jsonString(map[string]string{"describe": cluster.RedactText(describeOutput)})

	case "get_pod_logs":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		container := stringArg(args, "container")
		grep := stringArg(args, "grep")
		since := stringArg(args, "since")
		sinceTimeStr := stringArg(args, "sinceTime")
		endTimeStr := stringArg(args, "endTime")
		previous := boolArg(args, "previous")

		tailLines := int64(intArg(args, "tailLines", defaultLogTailLines))
		if tailLines <= 0 {
			tailLines = defaultLogTailLines
		}
		if tailLines > maxLogTailLines {
			tailLines = maxLogTailLines
		}

		q := cluster.LogQuery{
			Container: container,
			TailLines: tailLines,
			Previous:  previous,
		}
		if since != "" {
			d, err := time.ParseDuration(since)
			if err != nil {
				res.Content = jsonString(map[string]string{"error": fmt.Sprintf("invalid since value %q: expected duration like '15m', '1h'", since)})
				res.IsError = true
				return res
			}
			if d > 0 {
				q.SinceSeconds = int64(d.Seconds())
			}
		}
		if sinceTimeStr != "" {
			t, err := time.Parse(time.RFC3339, sinceTimeStr)
			if err != nil {
				res.Content = jsonString(map[string]string{"error": fmt.Sprintf("invalid sinceTime %q: expected RFC3339 like '2026-05-10T14:00:00Z'", sinceTimeStr)})
				res.IsError = true
				return res
			}
			q.SinceTime = t
		}
		if endTimeStr != "" {
			t, err := time.Parse(time.RFC3339, endTimeStr)
			if err != nil {
				res.Content = jsonString(map[string]string{"error": fmt.Sprintf("invalid endTime %q: expected RFC3339 like '2026-05-10T16:00:00Z'", endTimeStr)})
				res.IsError = true
				return res
			}
			q.EndTime = t
		}

		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}

		// Resolve the container name BEFORE calling the apiserver.
		// Without this, multi-container pods (gitlab-webservice has 5
		// containers, every istio-injected pod has 2+) fail with a
		// human-readable error the LLM has to parse to retry.
		//
		// Two-step flow for multi-container pods, to keep token cost
		// in check: instead of auto-picking the first container and
		// shipping its 20-50KB of logs (which the LLM might then
		// supersede with a second call to a different container),
		// we return ONLY the container list on the first call and
		// let the LLM pick using its world-knowledge of common
		// container-naming conventions. The LLM then re-calls with
		// `container=<name>` and gets the logs in a single round.
		// Net savings: ~25-50% on multi-container pods compared to
		// the auto-fetch approach. Single-container pods are
		// transparently auto-resolved (no extra round-trip there
		// because there's no choice to make).
		extraMeta := map[string]any{}
		if detail, dErr := conn.GetResourceDetail("pods", ns, name); dErr == nil {
			containerNames := extractPodContainerNames(detail)

			if container == "" {
				switch len(containerNames) {
				case 0:
					// No container info available (pod detail empty
					// or unusual shape). Fall through to GetPodLogs
					// and let the apiserver's error surface.
				case 1:
					// Single-container pod: auto-resolve. No choice
					// to make, so no round-trip needed.
					q.Container = containerNames[0]
					extraMeta["containerSelected"] = containerNames[0]
				default:
					// Multi-container, no container specified.
					// Return the list + nudge the LLM to pick using
					// the container names + the symptom in the
					// user's question. Common heuristics the LLM
					// applies automatically: skip init-style names
					// (`certificates`, `configure`, `dependencies`,
					// `wait-for-x`), prefer app-named containers
					// (`webservice`, `api`, the deployment's name),
					// recognize sidecar patterns (`istio-proxy`,
					// `linkerd-proxy`, `vault-agent`, `fluentbit`)
					// and pick the app container unless the user's
					// question is specifically about the sidecar.
					res.Content = jsonString(map[string]any{
						"availableContainers": containerNames,
						"podContainerCount":   len(containerNames),
						"hint": fmt.Sprintf(
							"pod %s/%s has %d containers — no logs returned on this call. Pick the container whose logs match the symptom you're investigating (e.g., for HTTP errors prefer the app container, not init helpers like 'configure' / 'certificates' / 'dependencies'; for traffic policy issues, prefer 'istio-proxy' or similar sidecars). Re-call get_pod_logs with container=<name> from availableContainers to actually read the logs.",
							ns, name, len(containerNames)),
					})
					return res
				}
			} else {
				// User-supplied container — validate against the pod
				// so we fail fast with a useful error instead of
				// surfacing the apiserver's "not found" 404.
				valid := false
				for _, n := range containerNames {
					if n == container {
						valid = true
						break
					}
				}
				if !valid && len(containerNames) > 0 {
					res.Content = jsonString(map[string]any{
						"error":               fmt.Sprintf("container %q not found in pod %s/%s", container, ns, name),
						"availableContainers": containerNames,
					})
					res.IsError = true
					return res
				}
				extraMeta["containerSelected"] = container
			}
		}
		// If the pod fetch failed (network blip, pod just deleted),
		// fall through with whatever container the caller passed.
		// GetPodLogs will surface the underlying error if any.

		logs, err := conn.GetPodLogs(ns, name, q)
		if err != nil {
			// Attach the meta we collected so the LLM still knows
			// what containers are available even when the read
			// failed for some other reason.
			payload := map[string]any{"error": err.Error()}
			for k, v := range extraMeta {
				payload[k] = v
			}
			res.Content = jsonString(payload)
			res.IsError = true
			return res
		}
		res.Content = formatPodLogs(cluster.RedactText(logs), grep, extraMeta)

	case "get_workload_pods":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		var pods []map[string]interface{}
		switch t {
		case "deployments":
			pods = conn.GetDeploymentPods(ns, name)
		case "statefulsets":
			pods = conn.GetStatefulSetPods(ns, name)
		case "daemonsets":
			pods = conn.GetDaemonSetPods(ns, name)
		case "jobs":
			pods = conn.GetJobPods(ns, name)
		default:
			res.Content = fmt.Sprintf(`{"error":"unsupported workload type: %s"}`, t)
			res.IsError = true
			return res
		}
		res.Content = jsonString(map[string]interface{}{"pods": pods})

	case "get_workload_history":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		var history []map[string]interface{}
		if t == "deployments" {
			history = conn.GetDeploymentHistory(ns, name)
		} else {
			history = conn.GetWorkloadHistory(t, ns, name)
		}
		res.Content = jsonString(map[string]interface{}{"history": history})

	case "get_cronjob_jobs":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		jobs := conn.GetCronJobJobs(ns, name)
		res.Content = jsonString(map[string]interface{}{"jobs": jobs})

	case "get_topology":
		res.Content = jsonString(conn.GetTopology())

	case "get_insights":
		eng := e.manager.Engine(ctx) // resolved for ctx's (tenant, cluster); see ExecuteCtx
		if eng == nil {
			res.Content = `{"error":"insights engine not available"}`
			res.IsError = true
			return res
		}
		severity := stringArg(args, "severity")
		resolved := boolArg(args, "resolved")
		items := eng.GetInsights(severity, resolved)
		res.Content = jsonString(map[string]interface{}{"items": items, "total": len(items)})

	case "offer_cluster_switch":
		// NOT named propose_* on purpose. GovernedToolDefinitions withholds
		// every propose_ tool when actions are disabled — correct, they mutate
		// the cluster. This one changes the operator's VIEW, mutates nothing,
		// and is most needed exactly by the read-only operator who cannot do
		// anything else from the chat. The propose_ prefix decides GOVERNANCE;
		// kind:"action_proposal" decides RENDERING. They coincide everywhere
		// else, which is why this had to be separated deliberately.
		//
		// The target is matched among the CALLER's clusters only: a cluster of
		// another org, or of a team the caller is not in, is not found, and the
		// refusal lists none of it.
		clusters, ok := e.callerClusters(ctx)
		if !ok {
			res.Content = `{"error":"the clusters you may switch to could not be decided right now, so no switch can be offered. Say so; do not offer the switch."}`
			res.IsError = true
			return res
		}
		content, failed := buildSwitchOffer(clusters, args)
		res.Content, res.IsError = content, failed

	case "get_fleet_summary":
		if e.fleet == nil {
			res.Content = `{"error":"the fleet view needs persisted insights, which are not enabled on this install. This is NOT a healthy fleet — say you cannot see across clusters here."}`
			res.IsError = true
			return res
		}
		recs, err := e.fleet.ActiveInsights(ctx)
		if err != nil {
			res.Content = jsonString(map[string]interface{}{"error": "could not summarize the fleet: " + err.Error()})
			res.IsError = true
			return res
		}
		res.Content = jsonString(summarizeFleet(ctx, recs, e.fleet.ClusterName))

	case "get_findings":
		if e.findings == nil {
			res.Content = `{"error":"security findings are not available on this install (persistence disabled). This is NOT a clean cluster — say you cannot see findings here."}`
			res.IsError = true
			return res
		}
		q := findings.Query{
			Source:   stringArg(args, "source"),
			Kind:     stringArg(args, "kind"),
			Severity: stringArg(args, "severity"),
			Status:   stringArg(args, "status"),
		}
		if q.Status == "" {
			// Same default as the Security page: what needs attention, not the
			// archive. A resolved finding is history, and asking for "our CVEs"
			// meaning the ones already fixed is nobody's question.
			q.Status = findings.StatusActive
		}
		allClusters := stringArg(args, "cluster") == "all"
		if !allClusters {
			q.ClusterID = e.currentClusterID(ctx)
		}
		recs, err := e.findings.List(ctx, q, allClusters)
		if err != nil {
			res.Content = jsonString(map[string]interface{}{"error": "could not read findings: " + err.Error()})
			res.IsError = true
			return res
		}
		// In-memory facets the store's Query has no column for.
		image := stringArg(args, "image")
		resource := stringArg(args, "resource")
		// Across the org a row must say where it lives: the same workload name
		// exists in every cluster, and get_finding_detail needs the cluster to
		// find the row again.
		var clusterName func(string) string
		if allClusters {
			clusterName = e.clusterDisplayName(ctx)
		}
		res.Content = jsonString(summarizeFindings(recs, image, resource,
			q.Severity != "" || q.Kind != "" || q.Source != "" || image != "" || resource != "",
			intArg(args, "limit", defaultFindingRows), clusterName))

	case "get_finding_workloads":
		if e.findings == nil {
			res.Content = `{"error":"security findings are not available on this install (persistence disabled). This is NOT a clean cluster — say you cannot see findings here."}`
			res.IsError = true
			return res
		}
		q := findings.Query{Status: stringArg(args, "status")}
		if q.Status == "" {
			q.Status = findings.StatusActive
		}
		allClusters := stringArg(args, "cluster") == "all"
		if !allClusters {
			q.ClusterID = e.currentClusterID(ctx)
		}
		recs, err := e.findings.List(ctx, q, allClusters)
		if err != nil {
			res.Content = jsonString(map[string]interface{}{"error": "could not read findings: " + err.Error()})
			res.IsError = true
			return res
		}
		// The lens and facets narrow the RECORDS, exactly as the Security page
		// does: under kind=exposed_secret a workload with thirty CVEs and one
		// leaked key must count the key, not the CVEs.
		group, severity, kind := stringArg(args, "group"), stringArg(args, "severity"), stringArg(args, "kind")
		view := findings.AggregateWorkloads(recs, func(rec *findings.Record) bool {
			return (group == "" || findings.SecurityGroup(rec) == group) &&
				(severity == "" || string(rec.Severity) == severity) &&
				(kind == "" || string(rec.Kind) == kind)
		})
		limit := intArg(args, "limit", defaultFindingWorkloads)
		if limit < 1 || limit > maxFindingWorkloads {
			limit = maxFindingWorkloads
		}
		var clusterName func(string) string
		if allClusters {
			clusterName = e.clusterDisplayName(ctx)
		}
		res.Content = jsonString(summarizeFindingWorkloads(view, limit, clusterName))

	case "get_finding_detail":
		if e.findings == nil {
			res.Content = `{"error":"security findings are not available on this install (persistence disabled). This is NOT a clean cluster — say you cannot see findings here."}`
			res.IsError = true
			return res
		}
		fingerprint := stringArg(args, "fingerprint")
		if fingerprint == "" {
			res.Content = `{"error":"fingerprint is required — take it from a get_findings row"}`
			res.IsError = true
			return res
		}
		clusterID := stringArg(args, "clusterId")
		if clusterID == "" {
			clusterID = e.currentClusterID(ctx)
		}
		detail, found, err := e.findings.Detail(ctx, clusterID, fingerprint)
		if err != nil {
			res.Content = jsonString(map[string]interface{}{"error": "could not read the finding: " + err.Error()})
			res.IsError = true
			return res
		}
		if !found {
			res.Content = `{"error":"no finding with that fingerprint in this cluster. If the row came from get_findings with cluster=\"all\", pass its clusterId."}`
			res.IsError = true
			return res
		}
		res.Content = jsonString(summarizeFindingDetail(detail, e.clusterDisplayName(ctx)))

	case "get_coverage":
		res.Content = jsonString(e.coverageReport(ctx, conn))

	case "query_metrics":
		if e.metricsQuery == nil {
			res.Content = `{"error":"there is no metrics store to query on this install. This is NOT a zero value — say you cannot query metrics here."}`
			res.IsError = true
			return res
		}
		out, err := e.runQueryMetrics(ctx, args)
		if err != nil {
			res.Content = jsonString(map[string]string{"error": err.Error()})
			res.IsError = true
			return res
		}
		res.Content = jsonString(out)

	case "get_right_sizing":
		if e.rightSizing == nil {
			res.Content = `{"error":"right-sizing is not available on this install"}`
			res.IsError = true
			return res
		}
		rs, err := e.rightSizing.RightSizing(ctx)
		if err != nil {
			res.Content = jsonString(map[string]string{"error": "could not compute right-sizing: " + err.Error()})
			res.IsError = true
			return res
		}
		limit := intArg(args, "limit", defaultRightSizingRows)
		if limit < 1 || limit > maxRightSizingRows {
			limit = maxRightSizingRows
		}
		res.Content = jsonString(summarizeRightSizing(rs, stringArg(args, "namespace"), stringArg(args, "severity"), limit))

	case "get_recent_deploys":
		hours := intArg(args, "sinceHours", defaultDeployWindowHours)
		if hours < 1 {
			hours = 1
		}
		if hours > maxDeployWindowHours {
			hours = maxDeployWindowHours
		}
		limit := intArg(args, "limit", defaultDeploysReturned)
		if limit < 1 || limit > maxDeploysReturned {
			limit = maxDeploysReturned
		}
		since := time.Now().Add(-time.Duration(hours) * time.Hour)
		res.Content = jsonString(summarizeDeploys(conn.GetDeploys(since), hours,
			stringArg(args, "namespace"), stringArg(args, "name"), limit))

	case "get_runtime_events":
		if e.runtimeEvents == nil {
			res.Content = `{"error":"runtime events are not available on this install (no Falco feed, or persistence disabled). This is NOT evidence that nothing suspicious ran — say you cannot see runtime events here."}`
			res.IsError = true
			return res
		}
		hours := intArg(args, "sinceHours", defaultRuntimeEventHours)
		if hours < 1 {
			hours = 1
		}
		if hours > maxEpisodeWindowHours {
			hours = maxEpisodeWindowHours
		}
		limit := intArg(args, "limit", defaultRuntimeEventsReturned)
		if limit < 1 || limit > maxRuntimeEventsReturned {
			limit = maxRuntimeEventsReturned
		}
		q := findings.EventQuery{
			Source:   stringArg(args, "source"),
			Priority: stringArg(args, "priority"),
			Since:    time.Now().UTC().Add(-time.Duration(hours) * time.Hour),
			// Namespace and pod narrow in memory, so the store reads a wider
			// page than it returns.
			Limit: maxRuntimeEventsScanned,
		}
		allClusters := stringArg(args, "cluster") == "all"
		if !allClusters {
			q.ClusterID = e.currentClusterID(ctx)
		}
		evs, err := e.runtimeEvents.List(ctx, q, allClusters)
		if err != nil {
			res.Content = jsonString(map[string]interface{}{"error": "could not read runtime events: " + err.Error()})
			res.IsError = true
			return res
		}
		var clusterName func(string) string
		if allClusters {
			clusterName = e.clusterDisplayName(ctx)
		}
		res.Content = jsonString(summarizeRuntimeEvents(evs, hours,
			stringArg(args, "namespace"), stringArg(args, "pod"), limit, clusterName))

	case "get_insight_episodes":
		if e.episodes == nil {
			res.Content = `{"error":"insight history is not enabled on this install. This is NOT evidence that nothing happened — say you cannot see history here."}`
			res.IsError = true
			return res
		}
		hours := intArg(args, "sinceHours", defaultEpisodeWindowHours)
		if hours < 1 {
			hours = 1
		}
		if hours > maxEpisodeWindowHours {
			hours = maxEpisodeWindowHours
		}
		until := time.Now().UTC()
		q := insights.EpisodeQuery{
			Status:   stringArg(args, "status"),
			Severity: stringArg(args, "severity"),
			RuleID:   stringArg(args, "rule"),
			Since:    until.Add(-time.Duration(hours) * time.Hour),
			Until:    until,
			Limit:    int32(intArg(args, "limit", defaultEpisodesReturned)),
		}
		if q.Limit < 1 || q.Limit > maxEpisodesReturned {
			q.Limit = maxEpisodesReturned
		}
		// Scope: this request's cluster by default, canonicalised the way the
		// HTTP handler does (the header carries a context NAME for direct
		// clusters; episodes key on the kube-system UID). "all" widens to the
		// org — which is the only way to reach episodes of a cluster that has
		// since been removed from the selector.
		switch c := stringArg(args, "cluster"); {
		case c == "all":
			q.ClusterID = ""
		case c != "":
			q.ClusterID = e.canonicalCluster(ctx, c)
		default:
			q.ClusterID = e.canonicalCluster(ctx, cluster.RuntimeKeyFromContext(ctx).Cluster)
		}

		eps, err := e.episodes.Window(ctx, q)
		if err != nil {
			res.Content = jsonString(map[string]interface{}{"error": "could not read insight history: " + err.Error()})
			res.IsError = true
			return res
		}
		rows := make([]map[string]interface{}, 0, len(eps))
		for _, ep := range eps {
			rows = append(rows, episodeRow(ep))
		}
		res.Content = jsonString(map[string]interface{}{
			"episodes":   rows,
			"total":      len(rows),
			"windowFrom": q.Since.Format(time.RFC3339),
			"windowTo":   q.Until.Format(time.RFC3339),
		})

	case "get_insight_episode":
		if e.episodes == nil {
			res.Content = `{"error":"insight history is not enabled on this install. This is NOT evidence that nothing happened — say you cannot see history here."}`
			res.IsError = true
			return res
		}
		id := stringArg(args, "id")
		if id == "" {
			res.Content = `{"error":"id is required — get it from get_insight_episodes or from the insight the user opened"}`
			res.IsError = true
			return res
		}
		ep, transitions, err := e.episodes.Episode(ctx, id)
		if err != nil {
			res.Content = jsonString(map[string]interface{}{"error": "could not read episode " + id + ": " + err.Error()})
			res.IsError = true
			return res
		}
		detail := episodeRow(ep)
		// The DETAIL is where evidence lives: the list would pay for it on
		// every row, and this tool is called once, on purpose, about one thing.
		if len(ep.Evidence) > 0 {
			detail["evidence"] = ep.Evidence
		}
		// The timeline, oldest first: it is a narrative, and the transitions
		// carry WHO — system, the rule, the watchdog, or a user who silenced it.
		tl := make([]map[string]interface{}, 0, len(transitions))
		for _, t := range transitions {
			row := map[string]interface{}{
				"at": t.At.UTC().Format(time.RFC3339), "to": t.ToState, "actor": t.Actor,
			}
			if t.FromState != "" {
				row["from"] = t.FromState
			}
			if t.Reason != "" {
				row["reason"] = t.Reason
			}
			tl = append(tl, row)
		}
		detail["timeline"] = tl
		// Recurrence answers "has this happened before" without a second call,
		// which is the question the episode is usually opened to settle.
		if rec, err := e.episodes.Recurrence(ctx, ep.Fingerprint, recurrenceReturned); err == nil && len(rec) > 0 {
			prior := make([]map[string]interface{}, 0, len(rec))
			for _, r := range rec {
				if r.ID == ep.ID {
					continue
				}
				prior = append(prior, episodeRow(r))
			}
			detail["recurrence"] = prior
			detail["recurrenceCount"] = len(prior)
		}
		res.Content = jsonString(detail)

	case "get_operational_episodes":
		if e.operationalEpisodes == nil {
			res.Content = `{"error":"operational episodes are not available on this install — insight persistence is off or its store predates them. This is NOT evidence that no burst happened."}`
			res.IsError = true
			return res
		}
		hours := intArg(args, "sinceHours", defaultBurstWindowHours)
		if hours < 1 {
			hours = 1
		}
		if hours > maxBurstWindowHours {
			hours = maxBurstWindowHours
		}
		until := time.Now().UTC()
		from := until.Add(-time.Duration(hours) * time.Hour)
		eps, err := e.operationalEpisodes(ctx, from, until)
		if err != nil {
			res.Content = jsonString(map[string]interface{}{"error": "could not compute operational episodes: " + err.Error()})
			res.IsError = true
			return res
		}
		// The member and seed ids are uuids the model cannot do anything with
		// — there is no tool that takes one — so they ship as counts. A burst
		// with 40 members would otherwise spend ~1.5KB of context saying
		// nothing. The burst's own id stays: it is what a human types into the
		// UI to see the same thing.
		//
		// Bursts arrive oldest-first. A noisy week can produce dozens, and this
		// tool gets called precisely on the noisy days, so the tail — the most
		// recent, and a contiguous timeline — is what survives the cap. The
		// response says it was cut rather than quietly shrinking, because a
		// silently truncated history is how a model concludes the morning was
		// calm.
		total := len(eps)
		truncated := false
		if total > maxBurstsReturned {
			eps = eps[total-maxBurstsReturned:]
			truncated = true
		}
		out := make([]map[string]interface{}, 0, len(eps))
		for _, ep := range eps {
			out = append(out, map[string]interface{}{
				"id":       ep.ID,
				"kind":     ep.Kind,
				"clusters": ep.Clusters,
				// onsetFrom..onsetTo is when things BROKE — usually minutes.
				// lastSeen is how long the burst was still visible, which a
				// chronic member stretches to weeks. The precedence rule in the
				// system prompt compares clocks, so it needs the onset, not the
				// tail: a burst that started at 03:05 explains a 03:10 failure
				// even if it was last seen in November.
				"onsetFrom":     ep.WindowFrom.UTC().Format(time.RFC3339),
				"onsetTo":       ep.OnsetTo.UTC().Format(time.RFC3339),
				"lastSeen":      ep.WindowTo.UTC().Format(time.RFC3339),
				"members":       len(ep.MemberIDs),
				"seeds":         len(ep.SeedIDs),
				"affected":      ep.Blast.Affected,
				"autoRecovered": ep.Blast.AutoRecovered,
				"remediated":    ep.Blast.Remediated,
				"stillFiring":   ep.Blast.StillFiring,
				"expired":       ep.Blast.Expired,
				"worstResource": ep.Blast.WorstResource,
				"worstSeconds":  ep.Blast.WorstSeconds,
			})
		}
		payload := map[string]interface{}{
			"episodes":   out,
			"total":      total,
			"returned":   len(out),
			"windowFrom": from.Format(time.RFC3339),
			"windowTo":   until.Format(time.RFC3339),
		}
		if truncated {
			payload["truncated"] = true
			payload["note"] = fmt.Sprintf(
				"showing the %d most recent of %d bursts; narrow sinceHours to see the earlier ones",
				len(out), total)
		}
		res.Content = jsonString(payload)

	case "get_events":
		eventType := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		involvedKind := stringArg(args, "involvedKind")
		involvedName := stringArg(args, "involvedName")
		limit := intArg(args, "limit", 100)
		events := conn.GetEvents(eventType, ns, involvedKind, involvedName, limit)
		res.Content = jsonString(events)

	case "search_resources":
		query := strings.ToLower(strings.TrimSpace(stringArg(args, "q")))
		if query == "" {
			res.Content = `{"results":[]}`
			return res
		}
		results := searchAllResources(conn, query, 50)
		res.Content = jsonString(map[string]interface{}{"results": results})

	case "get_permissions":
		perms := conn.Permissions()
		res.Content = jsonString(perms)

	case "list_clusters":
		// The caller's clusters, as GET /clusters shows them — not the
		// manager's list, which is every org's (see ClusterListSource).
		clusters, ok := e.callerClusters(ctx)
		if !ok {
			res.Content = `{"error":"the clusters you may see could not be decided right now, so none are listed. This is NOT an empty fleet — say you cannot list the clusters right now."}`
			res.IsError = true
			return res
		}
		res.Content = jsonString(clusters)

	case "propose_restart_workload":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		switch t {
		case "deployments", "statefulsets", "daemonsets":
		default:
			res.Content = fmt.Sprintf(`{"error":"cannot restart %s — only deployments, statefulsets, daemonsets"}`, t)
			res.IsError = true
			return res
		}
		// Verify the target exists so we don't propose ghost actions. This is
		// a read against the local informer cache — cheap.
		if _, err := conn.GetResourceDetail(t, ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		p := newProposal("restart_workload")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Summary = fmt.Sprintf("Restart %s %s/%s", strings.TrimSuffix(t, "s"), ns, name)
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "low")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_debug_pod":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		image := stringArg(args, "image")
		if image == "" {
			image = "busybox"
		}
		targetContainer := stringArg(args, "targetContainer")
		command := stringArg(args, "command")
		rationale := stringArg(args, "rationale")
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		if _, err := conn.GetResourceDetail("pods", ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target pod %s/%s not found: %w", ns, name, err))
			res.IsError = true
			return res
		}
		p := newProposal("debug_pod")
		p.Target = ProposalTarget{Type: "pods", Namespace: ns, Name: name}
		p.Params["image"] = image
		if targetContainer != "" {
			p.Params["targetContainer"] = targetContainer
		}
		if command != "" {
			// Wrap as sh -c so the LLM can pass a single shell line; the
			// ephemeral container runs it, exits, and the output lands in the
			// container logs for get_pod_logs to read back. The execution path
			// (debugPodRequest.Command) honors this verbatim.
			p.Params["command"] = []string{"sh", "-c", command}
		}
		p.Summary = fmt.Sprintf("Attach debug container (%s) to pod %s/%s", image, ns, name)
		if command != "" {
			// Surface the exact command in the card — it runs under the
			// operator's RBAC, so they should see it before clicking Execute.
			p.Summary += fmt.Sprintf(" — runs: %s", command)
		}
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		// Ephemeral containers can't be removed without recreating the pod.
		p.Reversible = false
		res.Content = jsonString(p)

	case "propose_scale_workload":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		replicas := intArg(args, "replicas", -1)
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		if replicas < 0 {
			res.Content = `{"error":"replicas must be >= 0"}`
			res.IsError = true
			return res
		}
		switch t {
		case "deployments", "statefulsets":
		default:
			res.Content = fmt.Sprintf(`{"error":"cannot scale %s — only deployments and statefulsets"}`, t)
			res.IsError = true
			return res
		}
		if _, err := conn.GetResourceDetail(t, ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		p := newProposal("scale_workload")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["replicas"] = replicas
		p.Summary = fmt.Sprintf("Scale %s %s/%s to %d replica(s)", strings.TrimSuffix(t, "s"), ns, name, replicas)
		p.Rationale = rationale
		// Default: scale-to-zero is medium (pauses the workload); other
		// scales are low. The LLM can override via the `risk` arg when
		// situational context warrants a different level.
		defaultRisk := "low"
		if replicas == 0 {
			defaultRisk = "medium"
		}
		p.Risk = resolveRisk(stringArg(args, "risk"), defaultRisk)
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_rollback_deployment":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		toRevision := intArg(args, "toRevision", 0)
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		// Verify the deployment exists.
		dep, err := conn.GetResourceDetail("deployments", ns, name)
		if err != nil {
			res.Content = errJSON(fmt.Errorf("target deployments/%s/%s not found: %w", ns, name, err))
			res.IsError = true
			return res
		}
		// Verify there is rollback history (>= 2 revisions). Without this
		// the action is impossible and the LLM should fall back to a
		// different remediation.
		history := conn.GetDeploymentHistory(ns, name)
		if len(history) < 2 {
			res.Content = jsonString(map[string]interface{}{
				"error":          fmt.Sprintf("deployment %s/%s has no rollback history (need >= 2 revisions, found %d)", ns, name, len(history)),
				"revisionsFound": len(history),
				"hint":           "the deployment was never updated after creation; suggest a different remediation (restart, edit yaml, etc.)",
			})
			res.IsError = true
			return res
		}
		// Resolve current revision and the target.
		fromRev := 0
		if a, ok := dep["annotations"].(map[string]string); ok {
			if v := a["deployment.kubernetes.io/revision"]; v != "" {
				fromRev, _ = strconv.Atoi(v)
			}
		}
		resolvedTo := toRevision
		if resolvedTo == 0 {
			// Default: most recent revision != current.
			for _, h := range history {
				rstr, _ := h["revision"].(string)
				r, _ := strconv.Atoi(rstr)
				if r != fromRev && r > 0 {
					resolvedTo = r
					break
				}
			}
		} else {
			// Confirm specified revision exists in history.
			found := false
			for _, h := range history {
				rstr, _ := h["revision"].(string)
				r, _ := strconv.Atoi(rstr)
				if r == toRevision {
					found = true
					break
				}
			}
			if !found {
				res.Content = jsonString(map[string]interface{}{
					"error": fmt.Sprintf("revision %d not found in history of deployments/%s/%s", toRevision, ns, name),
				})
				res.IsError = true
				return res
			}
		}
		if resolvedTo == 0 || resolvedTo == fromRev {
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("could not resolve a target revision distinct from the current (%d)", fromRev),
			})
			res.IsError = true
			return res
		}
		p := newProposal("rollback_deployment")
		p.Target = ProposalTarget{Type: "deployments", Namespace: ns, Name: name}
		p.Params["toRevision"] = resolvedTo
		if fromRev > 0 {
			p.Params["fromRevision"] = fromRev
		}
		if fromRev > 0 {
			p.Summary = fmt.Sprintf("Roll back deployment %s/%s from revision %d to revision %d", ns, name, fromRev, resolvedTo)
		} else {
			p.Summary = fmt.Sprintf("Roll back deployment %s/%s to revision %d", ns, name, resolvedTo)
		}
		p.Rationale = rationale
		// Default: medium (deployment-wide template change), but the LLM
		// can downgrade to "low" when the target revision is well-tested
		// and the change is small, or upgrade to "high" when rolling back
		// across many revisions or affecting critical production paths.
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_set_resources":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		switch t {
		case "deployments", "statefulsets", "daemonsets":
		default:
			res.Content = fmt.Sprintf(`{"error":"cannot set-resources on %s — only deployments, statefulsets, daemonsets"}`, t)
			res.IsError = true
			return res
		}
		containers, err := parseSetResourcesContainers(args["containers"])
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		if len(containers) == 0 {
			res.Content = `{"error":"containers is required and must be non-empty"}`
			res.IsError = true
			return res
		}
		detail, err := conn.GetResourceDetail(t, ns, name)
		if err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		// Validate every requested container name exists on the pod
		// template. Reject early with the valid list so the LLM can
		// retry with a correct name on the next turn instead of seeing
		// a 400 after the user clicks Execute.
		validNames := extractContainerNames(detail)
		for _, c := range containers {
			if !validNames[c["container"].(string)] {
				res.Content = jsonString(map[string]interface{}{
					"error":           fmt.Sprintf("container %q not found on %s/%s/%s", c["container"], t, ns, name),
					"validContainers": namesAsSlice(validNames),
				})
				res.IsError = true
				return res
			}
		}
		p := newProposal("set_resources")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["containers"] = containers
		p.Summary = fmt.Sprintf("Set resources on %s %s/%s (%d container%s)", strings.TrimSuffix(t, "s"), ns, name, len(containers), pluralS(len(containers)))
		p.Rationale = rationale
		// Default medium — spec-mutating + triggers a rolling update,
		// so heavier than restart. LLM can override per riskProp.
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_set_image":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		switch t {
		case "deployments", "statefulsets", "daemonsets":
		default:
			res.Content = fmt.Sprintf(`{"error":"cannot set-image on %s — only deployments, statefulsets, daemonsets"}`, t)
			res.IsError = true
			return res
		}
		images, err := parseSetImageEntries(args["images"])
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		if len(images) == 0 {
			res.Content = `{"error":"images is required and must be non-empty"}`
			res.IsError = true
			return res
		}
		detail, err := conn.GetResourceDetail(t, ns, name)
		if err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		// Build name → current image map so we can both validate the
		// container exists AND short-circuit if every requested image
		// already matches. Without the short-circuit Kobi would render a
		// useless card whose Execute is a no-op.
		currentImages := extractContainerImages(detail)
		if len(currentImages) == 0 {
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("could not read current container images for %s/%s/%s", t, ns, name),
			})
			res.IsError = true
			return res
		}
		allUnchanged := true
		for _, img := range images {
			containerName := img["container"].(string)
			requestedImage := img["image"].(string)
			currentImage, ok := currentImages[containerName]
			if !ok {
				res.Content = jsonString(map[string]interface{}{
					"error":           fmt.Sprintf("container %q not found on %s/%s/%s", containerName, t, ns, name),
					"validContainers": mapKeys(currentImages),
				})
				res.IsError = true
				return res
			}
			if requestedImage != currentImage {
				allUnchanged = false
			}
		}
		if allUnchanged {
			res.Content = jsonString(map[string]interface{}{
				"error": "no image change requested — every container already runs the requested image",
				"hint":  "if the workload is failing despite identical images, the cause is elsewhere (probes, env, resources)",
			})
			res.IsError = true
			return res
		}
		p := newProposal("set_image")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["images"] = images
		p.Summary = fmt.Sprintf("Set image on %s %s/%s (%d container%s)", strings.TrimSuffix(t, "s"), ns, name, len(images), pluralS(len(images)))
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_set_env":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		if t == "" || ns == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		switch t {
		case "deployments", "statefulsets", "daemonsets":
		default:
			res.Content = fmt.Sprintf(`{"error":"cannot set-env on %s — only deployments, statefulsets, daemonsets"}`, t)
			res.IsError = true
			return res
		}
		envContainers, err := parseSetEnvContainers(args["containers"])
		if err != nil {
			res.Content = errJSON(err)
			res.IsError = true
			return res
		}
		if len(envContainers) == 0 {
			res.Content = `{"error":"containers is required and must be non-empty"}`
			res.IsError = true
			return res
		}
		detail, err := conn.GetResourceDetail(t, ns, name)
		if err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}
		validNames := extractContainerNames(detail)
		nSet, nRemove := 0, 0
		for _, c := range envContainers {
			containerName := c["container"].(string)
			if !validNames[containerName] {
				res.Content = jsonString(map[string]interface{}{
					"error":           fmt.Sprintf("container %q not found on %s/%s/%s", containerName, t, ns, name),
					"validContainers": namesAsSlice(validNames),
				})
				res.IsError = true
				return res
			}
			envList, _ := c["env"].([]map[string]interface{})
			for _, e := range envList {
				envName, _ := e["name"].(string)
				action, _ := e["action"].(string)
				if envName == "" {
					res.Content = jsonString(map[string]interface{}{
						"error": fmt.Sprintf("env entry on container %q has empty name", containerName),
					})
					res.IsError = true
					return res
				}
				switch action {
				case "set":
					nSet++
					// Credential guardrail: refuse a literal value for any
					// env var whose NAME looks credential-shaped. The LLM
					// cannot be trusted to handle credentials by direct
					// value — the right path is Secret/CM refs in the
					// YAML editor.
					if credentialNameRE.MatchString(envName) {
						if v, ok := e["value"].(string); ok && v != "" {
							res.Content = jsonString(map[string]interface{}{
								"error":   fmt.Sprintf("refusing to set literal value on credential-shaped env var %q on container %q", envName, containerName),
								"hint":    "use the YAML editor to bind this env var to a Secret/ConfigMap (valueFrom.secretKeyRef / configMapKeyRef)",
								"pattern": "names matching password|secret|token|key|credential are blocked at the Copilot layer",
							})
							res.IsError = true
							return res
						}
					}
				case "remove":
					nRemove++
				default:
					res.Content = jsonString(map[string]interface{}{
						"error": fmt.Sprintf("env entry %q on container %q has invalid action %q — must be \"set\" or \"remove\"", envName, containerName, action),
					})
					res.IsError = true
					return res
				}
			}
		}
		p := newProposal("set_env")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["containers"] = envContainers
		// triggerRollout=true so existing pods pick up literal-value
		// changes immediately. The set-env endpoint applies the
		// rollout-restart annotation when this is set.
		p.Params["triggerRollout"] = true
		p.Summary = fmt.Sprintf("Set env on %s %s/%s (%d set, %d remove)", strings.TrimSuffix(t, "s"), ns, name, nSet, nRemove)
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_patch_hpa":
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		if ns == "" || name == "" {
			res.Content = `{"error":"namespace and name are required"}`
			res.IsError = true
			return res
		}
		// At least one bound must be present. We accept the keys whether
		// they arrive as JSON number (float64) or as an absent value.
		_, hasMin := args["minReplicas"]
		_, hasMax := args["maxReplicas"]
		if !hasMin && !hasMax {
			res.Content = `{"error":"at least one of minReplicas or maxReplicas is required"}`
			res.IsError = true
			return res
		}
		minR := intArg(args, "minReplicas", -1)
		maxR := intArg(args, "maxReplicas", -1)
		// Validate the side the caller actually set.
		if hasMin && minR < 0 {
			res.Content = `{"error":"minReplicas must be >= 0"}`
			res.IsError = true
			return res
		}
		if hasMax && maxR < 1 {
			res.Content = `{"error":"maxReplicas must be >= 1"}`
			res.IsError = true
			return res
		}
		if hasMax && maxR > hpaMaxReplicasCap {
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("maxReplicas must be <= %d (safety cap)", hpaMaxReplicasCap),
				"hint":  "if you genuinely need more than that, scale via the YAML editor and add cluster-ops review",
			})
			res.IsError = true
			return res
		}
		if hasMin && hasMax && maxR < minR {
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("maxReplicas (%d) must be >= minReplicas (%d)", maxR, minR),
			})
			res.IsError = true
			return res
		}
		// Verify the HPA exists (informer cache lookup).
		if _, err := conn.GetResourceDetail("hpas", ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target hpas/%s/%s not found: %w", ns, name, err))
			res.IsError = true
			return res
		}
		p := newProposal("patch_hpa")
		p.Target = ProposalTarget{Type: "hpas", Namespace: ns, Name: name}
		if hasMin {
			p.Params["minReplicas"] = minR
		}
		if hasMax {
			p.Params["maxReplicas"] = maxR
		}
		// Summary line: prefer "max X → Y" when the caller is bumping
		// maxReplicas (the common case), fall back to a generic line.
		switch {
		case hasMax && hasMin:
			p.Summary = fmt.Sprintf("Patch HPA %s/%s (min=%d, max=%d)", ns, name, minR, maxR)
		case hasMax:
			p.Summary = fmt.Sprintf("Patch HPA %s/%s (max=%d)", ns, name, maxR)
		case hasMin:
			p.Summary = fmt.Sprintf("Patch HPA %s/%s (min=%d)", ns, name, minR)
		}
		p.Rationale = rationale
		p.Risk = resolveRisk(stringArg(args, "risk"), "medium")
		p.Reversible = true
		res.Content = jsonString(p)

	case "propose_delete_resource":
		t := stringArg(args, "type")
		ns := stringArg(args, "namespace")
		name := stringArg(args, "name")
		rationale := stringArg(args, "rationale")
		force := boolArg(args, "force")
		orphan := boolArg(args, "orphan")
		if t == "" || name == "" {
			res.Content = `{"error":"type, namespace, and name are required"}`
			res.IsError = true
			return res
		}
		// Whitelist enforcement at the executor level. Even if the LLM
		// somehow constructs a tool_call with a blocked type, we refuse
		// to materialize the proposal. Prompt injection defense: no
		// payload shape change can route around this switch.
		switch t {
		case "deployments", "statefulsets", "daemonsets",
			"services", "configmaps", "secrets",
			"jobs", "cronjobs", "pods", "ingresses",
			"hpas", "horizontalpodautoscalers":
			// allowed
		default:
			res.Content = jsonString(map[string]interface{}{
				"error": fmt.Sprintf("resource type %q cannot be deleted via Copilot proposal — recommend kubectl directly", t),
				"hint":  "Allowed types: deployments, statefulsets, daemonsets, services, configmaps, secrets, jobs, cronjobs, pods, ingresses, hpas. Namespaces, nodes, PVs, PVCs, RBAC resources are blocked by design.",
			})
			res.IsError = true
			return res
		}
		// Verify the target exists.
		if _, err := conn.GetResourceDetail(t, ns, name); err != nil {
			res.Content = errJSON(fmt.Errorf("target %s/%s/%s not found: %w", t, ns, name, err))
			res.IsError = true
			return res
		}

		// Compute blast radius from the informer cache. Read-only; safe
		// to call from any tool. The LLM should read this and reflect
		// the consequences in its text response.
		blast := conn.ComputeDeleteBlastRadius(t, ns, name)

		p := newProposal("delete_resource")
		p.Target = ProposalTarget{Type: t, Namespace: ns, Name: name}
		p.Params["force"] = force
		p.Params["orphan"] = orphan
		p.Params["blastRadius"] = blast
		p.Summary = fmt.Sprintf("Delete %s %s/%s (irreversible)", strings.TrimSuffix(t, "s"), ns, name)
		p.Rationale = rationale
		// Default high — the LLM can technically downgrade per riskProp's
		// guidance, but for delete the tool description tells it to keep
		// high. Reversible is always false: deleting a resource is
		// irreversible (only "recoverable" if the user has the YAML
		// stored elsewhere, which we cannot verify).
		p.Risk = resolveRisk(stringArg(args, "risk"), "high")
		p.Reversible = false
		res.Content = jsonString(p)

	case "get_workload_metrics":
		out, err := e.execGetWorkloadMetrics(ctx, call, args, conn)
		if err != nil {
			res.Content = err.Error()
			res.IsError = true
			return res
		}
		res.Content = out

	case "get_kubebolt_docs":
		topic := stringArg(args, "topic")
		res.Content = jsonString(map[string]string{
			"topic":   topic,
			"content": KubebolDocsGet(topic),
		})

	default:
		res.Content = fmt.Sprintf(`{"error":"unknown tool: %s"}`, call.Name)
		res.IsError = true
	}

	// Truncate oversized results to prevent context window blow-up.
	// Some tools (topology, describe, yaml) can return huge payloads that
	// quickly exhaust the LLM's context if multiple are made in sequence.
	// get_pod_logs handles its own smart truncation via formatPodLogs.
	if call.Name != "get_pod_logs" {
		res.Content = truncateToolResult(res.Content, call.Name)
	}

	return res
}

// truncateToolResult caps the size of tool result content. If truncated, it
// appends a clear notice so the LLM knows the data was cut.
func truncateToolResult(content, toolName string) string {
	if len(content) <= maxToolResultBytes {
		return content
	}
	truncated := content[:maxToolResultBytes]
	notice := fmt.Sprintf(
		`... [TRUNCATED: %s result was %d bytes, capped at %d bytes (~%dKB) to preserve context window. Request a smaller subset (fewer lines, narrower namespace, specific resource) for more detail.]`,
		toolName, len(content), maxToolResultBytes, maxToolResultBytes/1024,
	)
	// Wrap as a JSON-safe response so the LLM still gets a valid payload
	return jsonString(map[string]string{
		"truncated_result": truncated,
		"notice":           notice,
	})
}

// formatPodLogs applies optional grep filtering and a byte cap that preserves
// the NEWEST log lines (truncates from the head, not the tail) aligned on line
// boundaries. The response always carries metadata so the LLM can decide
// whether to request a narrower window or a specific filter.
//
// `extra` carries caller-supplied metadata that gets merged into the
// response payload — used by the get_pod_logs executor case to surface
// containerSelected / availableContainers / containerAutoSelected /
// hint so the LLM can re-query a different container without parsing
// human-readable error strings.
func formatPodLogs(raw, grep string, extra map[string]any) string {
	// Count original lines before any filtering
	originalLines := 0
	if raw != "" {
		originalLines = strings.Count(raw, "\n")
		if !strings.HasSuffix(raw, "\n") {
			originalLines++
		}
	}

	body := raw
	filterApplied := ""
	filteredOutLines := 0
	if grep != "" {
		re, err := regexp.Compile("(?i)" + grep)
		if err != nil {
			return jsonString(map[string]any{
				"error": fmt.Sprintf("invalid grep pattern %q: %s", grep, err.Error()),
			})
		}
		kept := make([]string, 0, 128)
		for _, line := range strings.Split(raw, "\n") {
			if line == "" {
				continue
			}
			if re.MatchString(line) {
				kept = append(kept, line)
			}
		}
		filterApplied = grep
		filteredOutLines = originalLines - len(kept)
		body = strings.Join(kept, "\n")
	}

	// Byte cap: preserve the TAIL (newest lines), not the head.
	truncated := false
	bytesDropped := 0
	if len(body) > maxLogBytes {
		head := len(body) - maxLogBytes
		// Advance to the next newline so we don't start mid-line.
		if nl := strings.IndexByte(body[head:], '\n'); nl >= 0 {
			head += nl + 1
		}
		bytesDropped = head
		body = body[head:]
		truncated = true
	}

	returnedLines := 0
	if body != "" {
		returnedLines = strings.Count(body, "\n")
		if !strings.HasSuffix(body, "\n") {
			returnedLines++
		}
	}

	payload := map[string]any{
		"logs":          body,
		"originalLines": originalLines,
		"returnedLines": returnedLines,
	}
	if filterApplied != "" {
		payload["grep"] = filterApplied
		payload["filteredOutLines"] = filteredOutLines
	}
	if truncated {
		payload["truncated"] = true
		payload["bytesDropped"] = bytesDropped
		payload["hint"] = "logs truncated to preserve context; use 'since' for a narrower window or 'grep' to filter"
	}
	// Merge caller meta last. If the caller also set "hint" (e.g.
	// container auto-selected note), the truncation hint takes
	// precedence because it's more actionable — operators care more
	// about "your data is incomplete" than "I picked a container".
	for k, v := range extra {
		if _, present := payload[k]; !present {
			payload[k] = v
		}
	}
	return jsonString(payload)
}

// extractPodContainerNames returns the names of the regular containers
// (not init/ephemeral) of a pod as exposed by the connector's
// GetResourceDetail. Mirrors what the apiserver's "a container name
// must be specified, choose one of: [...]" error lists — the
// auto-selection logic in get_pod_logs uses this to pre-empt that
// error class on multi-container pods.
func extractPodContainerNames(detail map[string]interface{}) []string {
	cs, ok := detail["containers"].([]map[string]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if n, ok := c["name"].(string); ok && n != "" {
			out = append(out, n)
		}
	}
	return out
}

// ----- helpers -----

func parseArgs(input json.RawMessage) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	var args map[string]interface{}
	if err := json.Unmarshal(input, &args); err != nil {
		return map[string]interface{}{}
	}
	return args
}

func stringArg(args map[string]interface{}, key string) string {
	v, _ := args[key].(string)
	return v
}

func intArg(args map[string]interface{}, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func boolArg(args map[string]interface{}, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func nsResourceArgs(args map[string]interface{}) (string, string, string) {
	// Accept `pod`, `Pod`, `deployment`… — see CanonicalResourceType.
	t := CanonicalResourceType(stringArg(args, "type"))
	ns := stringArg(args, "namespace")
	name := stringArg(args, "name")
	if ns == "_" {
		ns = ""
	}
	return t, ns, name
}

func jsonString(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal failed: %s"}`, err.Error())
	}
	return string(b)
}

func errJSON(err error) string {
	return jsonString(map[string]string{"error": err.Error()})
}

// searchAllResources reuses the same approach as the /search handler.
// We duplicate the resource type list here to avoid coupling the copilot
// package to the api package.
func searchAllResources(conn *cluster.Connector, query string, limit int) []map[string]interface{} {
	types := []string{
		"pods", "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs",
		"services", "ingresses", "configmaps", "secrets", "nodes", "namespaces",
		"pvcs", "pvs", "hpas", "storageclasses",
	}
	results := make([]map[string]interface{}, 0)
	for _, rt := range types {
		if len(results) >= limit {
			break
		}
		list := conn.GetResources(rt, "", query, "", "", "", "", 1, limit)
		for _, item := range list.Items {
			if len(results) >= limit {
				break
			}
			name, _ := item["name"].(string)
			ns, _ := item["namespace"].(string)
			status, _ := item["status"].(string)
			results = append(results, map[string]interface{}{
				"name":         name,
				"namespace":    ns,
				"kind":         normalizeKind(rt),
				"status":       status,
				"resourceType": rt,
			})
		}
	}
	return results
}

func normalizeKind(rt string) string {
	switch rt {
	case "pods":
		return "Pod"
	case "deployments":
		return "Deployment"
	case "statefulsets":
		return "StatefulSet"
	case "daemonsets":
		return "DaemonSet"
	case "jobs":
		return "Job"
	case "cronjobs":
		return "CronJob"
	case "services":
		return "Service"
	case "ingresses":
		return "Ingress"
	case "configmaps":
		return "ConfigMap"
	case "secrets":
		return "Secret"
	case "nodes":
		return "Node"
	case "namespaces":
		return "Namespace"
	case "pvcs":
		return "PersistentVolumeClaim"
	case "pvs":
		return "PersistentVolume"
	case "hpas":
		return "HorizontalPodAutoscaler"
	case "storageclasses":
		return "StorageClass"
	}
	return rt
}

// hpaMaxReplicasCap is the safety ceiling Kobi enforces on
// propose_patch_hpa. Mirrors maxReplicasSafetyCap in the api package
// — they MUST stay in sync. If the api-layer cap changes, this
// constant has to move with it; the load-bearing test in
// actions_hpa_test.go (TestMaxReplicasSafetyCapDefined) pins the
// canonical value to 1000 and is the source of truth.
const hpaMaxReplicasCap = 1000

// credentialNameRE matches env var NAMES that look credential-shaped.
// We refuse a literal `value` on these (the operator must bind a
// Secret/ConfigMap reference via the YAML editor). The pattern is
// intentionally conservative — only the most obvious "this is a
// secret" naming conventions, to avoid false positives that would
// frustrate operators tweaking legit non-secret env vars.
var credentialNameRE = regexp.MustCompile(`(?i)(password|secret|token|key|credential)`)

// parseSetResourcesContainers normalizes the LLM-provided containers
// array into a clean []map[string]interface{} where each row is
// shape-checked. We rebuild instead of passing through so the
// downstream JSON (in the ActionProposal params) is stable regardless
// of what the LLM padded the call with.
func parseSetResourcesContainers(raw interface{}) ([]map[string]interface{}, error) {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("containers must be an array")
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("containers[%d] must be an object", i)
		}
		containerName, _ := m["container"].(string)
		if containerName == "" {
			return nil, fmt.Errorf("containers[%d].container is required", i)
		}
		row := map[string]interface{}{"container": containerName}
		if v, ok := m["initContainer"].(bool); ok && v {
			row["initContainer"] = true
		}
		if req, ok := m["requests"].(map[string]interface{}); ok {
			cleaned := cleanQuantityMap(req)
			if len(cleaned) > 0 {
				row["requests"] = cleaned
			}
		}
		if lim, ok := m["limits"].(map[string]interface{}); ok {
			cleaned := cleanQuantityMap(lim)
			if len(cleaned) > 0 {
				row["limits"] = cleaned
			}
		}
		if row["requests"] == nil && row["limits"] == nil {
			return nil, fmt.Errorf("containers[%d] must set at least one of requests/limits", i)
		}
		out = append(out, row)
	}
	return out, nil
}

// cleanQuantityMap keeps only the cpu/memory keys with non-empty
// string values. Drops everything else so the proposal payload is
// not contaminated by stray fields the LLM may have added.
func cleanQuantityMap(m map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	if v, ok := m["cpu"].(string); ok && v != "" {
		out["cpu"] = v
	}
	if v, ok := m["memory"].(string); ok && v != "" {
		out["memory"] = v
	}
	return out
}

// parseSetImageEntries normalizes the LLM-provided images array.
func parseSetImageEntries(raw interface{}) ([]map[string]interface{}, error) {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("images must be an array")
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("images[%d] must be an object", i)
		}
		containerName, _ := m["container"].(string)
		image, _ := m["image"].(string)
		if containerName == "" {
			return nil, fmt.Errorf("images[%d].container is required", i)
		}
		if image == "" {
			return nil, fmt.Errorf("images[%d].image is required", i)
		}
		out = append(out, map[string]interface{}{
			"container": containerName,
			"image":     image,
		})
	}
	return out, nil
}

// parseSetEnvContainers normalizes the LLM-provided env containers
// array. The env entry list inside each container is kept as
// []map[string]interface{} so the executor case can iterate without
// having to reshape again. valueFrom is not exposed here — only
// literal value or remove — per the file-level comment on the env
// tool description (literal values + Secret refs split between Kobi
// and the YAML editor).
func parseSetEnvContainers(raw interface{}) ([]map[string]interface{}, error) {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("containers must be an array")
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("containers[%d] must be an object", i)
		}
		containerName, _ := m["container"].(string)
		if containerName == "" {
			return nil, fmt.Errorf("containers[%d].container is required", i)
		}
		envRaw, ok := m["env"].([]interface{})
		if !ok || len(envRaw) == 0 {
			return nil, fmt.Errorf("containers[%d].env is required and must be non-empty", i)
		}
		envOut := make([]map[string]interface{}, 0, len(envRaw))
		for j, e := range envRaw {
			em, ok := e.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("containers[%d].env[%d] must be an object", i, j)
			}
			row := map[string]interface{}{}
			if v, ok := em["name"].(string); ok {
				row["name"] = v
			}
			if v, ok := em["action"].(string); ok {
				row["action"] = v
			}
			if v, ok := em["value"].(string); ok {
				row["value"] = v
			}
			envOut = append(envOut, row)
		}
		row := map[string]interface{}{
			"container": containerName,
			"env":       envOut,
		}
		if v, ok := m["initContainer"].(bool); ok && v {
			row["initContainer"] = true
		}
		out = append(out, row)
	}
	return out, nil
}

// extractContainerNames returns the set of container names from a
// resource detail map. Used to validate that propose_set_resources /
// propose_set_env target a real container before emitting a card.
// Covers both normal and init containers (init lives under the
// `initContainers` key in pod-level details; for workloads we don't
// expose initContainers separately yet, so init-name validation is
// best-effort — the backend handler will reject unknown init
// containers cleanly at Execute time).
func extractContainerNames(detail map[string]interface{}) map[string]bool {
	out := map[string]bool{}
	if cs, ok := detail["containers"].([]map[string]interface{}); ok {
		for _, c := range cs {
			if n, ok := c["name"].(string); ok && n != "" {
				out[n] = true
			}
		}
	}
	return out
}

// extractContainerImages returns container-name → image map from a
// resource detail. Used by propose_set_image for the no-op short-
// circuit + container existence check.
func extractContainerImages(detail map[string]interface{}) map[string]string {
	out := map[string]string{}
	if cs, ok := detail["containers"].([]map[string]interface{}); ok {
		for _, c := range cs {
			n, _ := c["name"].(string)
			img, _ := c["image"].(string)
			if n != "" {
				out[n] = img
			}
		}
	}
	return out
}

func namesAsSlice(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ensure unused models import survives compile
var _ = models.ResourceList{}

// episodeRow is the shape both history tools ship. Deliberately NOT the raw
// Episode: the fingerprint is 64 hex characters the model cannot act on (the
// detail tool returns recurrence itself, so nobody has to carry one around),
// and the tenant id is the same value on every row of a response.
func episodeRow(ep insights.Episode) map[string]interface{} {
	row := map[string]interface{}{
		"id":          ep.ID,
		"rule":        ep.RuleID,
		"resource":    ep.Resource,
		"status":      ep.Status,
		"severity":    ep.Severity,
		"maxSeverity": ep.MaxSeverity,
		"firstSeen":   ep.FirstSeen.UTC().Format(time.RFC3339),
		"lastSeen":    ep.LastSeen.UTC().Format(time.RFC3339),
	}
	if ep.Namespace != "" {
		row["namespace"] = ep.Namespace
	}
	if ep.Title != "" {
		row["title"] = ep.Title
	}
	// The cluster's NAME, not its uid: an episode survives its cluster, and a
	// uid means nothing to the person reading the answer.
	if ep.ClusterName != "" {
		row["cluster"] = ep.ClusterName
	} else if ep.ClusterID != "" {
		row["cluster"] = ep.ClusterID
	}
	if ep.ResolvedAt != nil {
		row["resolvedAt"] = ep.ResolvedAt.UTC().Format(time.RFC3339)
	}
	if ep.ResolutionKind != "" {
		row["resolutionKind"] = ep.ResolutionKind
	}
	if ep.FlapCount > 0 {
		row["flapCount"] = ep.FlapCount
	}
	if ep.PrevEpisodeID != "" {
		row["prevEpisodeId"] = ep.PrevEpisodeID
	}
	return row
}

// canonicalCluster maps a context name to the kube-system UID episodes key on.
// Falls through unchanged when there is no manager (tests) or no match.
func (e *Executor) canonicalCluster(ctx context.Context, id string) string {
	if id == "" || e.manager == nil {
		return id
	}
	return e.manager.CanonicalClusterID(ctx, id)
}

// summarizeFindings turns a scope of records into either a POSTURE or a set of
// ROWS, and the default is the posture. One production org carries 1,224 active
// findings; listing them is 245KB against a 32KB tool cap, and "1,224 findings"
// answers nothing anyway. 1,224 across 18 workloads, all fixable, is a
// morning's work — that is the sentence the summary is built to let the model
// say.
//
// Rollup handling mirrors api/findings.go, and it is not cosmetic: a compliance
// control marked Rollup aggregates findings stored individually, so counting
// both inflates every number by the same problem twice. The rows still ship —
// it is the counting that stops.
//
// clusterName, when set, stamps each row with its cluster — for an org-wide
// scope, where a workload name alone does not say which cluster to go to.
func summarizeFindings(recs []findings.Record, image, resource string, faceted bool, limit int, clusterName func(string) string) map[string]interface{} {
	if limit < 1 || limit > maxFindingRows {
		limit = maxFindingRows
	}
	bySeverity, bySource, byKind := map[string]int{}, map[string]int{}, map[string]int{}
	perWorkload, perImage := map[string]int{}, map[string]int{}
	imageCritical := map[string]int{}
	rollups, counted := 0, 0

	var rows []findings.Record
	for _, rec := range recs {
		if image != "" && !sameImage(rec.Image, image) {
			continue
		}
		if resource != "" && rec.ResourceName != resource {
			continue
		}
		rows = append(rows, rec)
		if rec.Rollup {
			rollups++
			continue
		}
		counted++
		bySeverity[string(rec.Severity)]++
		bySource[rec.Source]++
		byKind[string(rec.Kind)]++
		if rec.ResourceName != "" {
			// Keyed by cluster too: the same workload name exists in every
			// cluster, and collapsing them under-reports the blast radius.
			perWorkload[rec.ClusterID+"|"+rec.ResourceNamespace+"/"+rec.ResourceName]++
		}
		// The image is what someone actually rebuilds. Two workloads sharing an
		// image are ONE fix, and a list of affected workloads never says so.
		if rec.Image != "" {
			perImage[rec.Image]++
			if rec.Severity == "critical" {
				imageCritical[rec.Image]++
			}
		}
	}

	out := map[string]interface{}{
		"total":             counted,
		"bySeverity":        bySeverity,
		"bySource":          bySource,
		"byKind":            byKind,
		"affectedWorkloads": len(perWorkload),
	}
	if rollups > 0 {
		out["rollupsExcluded"] = rollups
		out["rollupNote"] = "compliance controls that aggregate findings counted individually; shown in rows, excluded from the counts so the same problem is not counted twice"
	}
	if name, n := topKey(perWorkload); n > 0 {
		// Strip the cluster prefix the key carries for uniqueness.
		if i := strings.Index(name, "|"); i >= 0 {
			name = name[i+1:]
		}
		out["worstWorkload"] = map[string]interface{}{"resource": name, "findings": n}
	}
	if imgs := topImages(perImage, imageCritical); len(imgs) > 0 {
		out["topImages"] = imgs
		out["imageNote"] = "rebuilding one image fixes every workload running it"
	}

	// Rows only when the caller narrowed. An unfaceted request gets the shape
	// of the problem; a faceted one is already asking about something specific.
	if faceted {
		if len(rows) > limit {
			out["truncated"] = true
			out["returned"] = limit
			rows = rows[:limit]
		}
		out["findings"] = findingRows(rows, clusterName)
	} else if len(rows) > 0 {
		out["hint"] = "narrow with severity, kind, source, image or resource to get the rows"
	}
	return out
}

// sameImage matches an image facet against a stored finding. Trivy stores the
// reference fully qualified (index.docker.io/library/nginx:1.27-alpine) while
// a pod spec and get_workload_history carry the short form (nginx:1.27-alpine).
// An exact comparison found nothing for the short form, and "nothing" read as
// "this image is clean" — measured: 2 critical CVEs on nginx:1.27-alpine
// reported as none. A query without a tag or digest matches every tag of the
// repository.
func sameImage(stored, query string) bool {
	s, q := normalizeImageRef(stored), normalizeImageRef(query)
	if s == q {
		return true
	}
	if !hasTagOrDigest(q) {
		return imageRepository(s) == q
	}
	return false
}

// normalizeImageRef drops what Docker Hub implies: the registry host and the
// library/ namespace of official images.
func normalizeImageRef(ref string) string {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for _, host := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		if strings.HasPrefix(ref, host) {
			ref = strings.TrimPrefix(ref, host)
			break
		}
	}
	return strings.TrimPrefix(ref, "library/")
}

func hasTagOrDigest(ref string) bool {
	if strings.Contains(ref, "@") {
		return true
	}
	last := ref[strings.LastIndex(ref, "/")+1:]
	return strings.Contains(last, ":")
}

// imageRepository strips the tag or digest. The tag colon is searched only in
// the last path segment: a registry port (host:5000/app) is not a tag.
func imageRepository(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref[slash+1:], ":"); i >= 0 {
		ref = ref[:slash+1+i]
	}
	return ref
}

func findingRows(recs []findings.Record, clusterName func(string) string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(recs))
	for _, rec := range recs {
		row := map[string]interface{}{
			"fingerprint": rec.Fingerprint,
			"severity":    string(rec.Severity),
			"title":       rec.Title,
			"source":      rec.Source,
			"kind":        string(rec.Kind),
			"firstSeen":   rec.FirstSeen.UTC().Format(time.RFC3339),
		}
		if clusterName != nil {
			// The id is what get_finding_detail takes back; the name is what
			// the reader recognizes. A fingerprint is NOT unique across
			// clusters — the same CVE on the same Deployment in two clusters
			// shares one.
			row["clusterId"] = rec.ClusterID
			if n := clusterName(rec.ClusterID); n != "" {
				row["cluster"] = n
			}
		}
		if rec.ResourceName != "" {
			row["resource"] = strings.TrimPrefix(rec.ResourceNamespace+"/"+rec.ResourceName, "/")
		}
		if rec.Image != "" {
			row["image"] = rec.Image
		}
		if rec.Remediation != "" {
			row["remediation"] = rec.Remediation
		}
		if rec.CISControl != "" {
			row["cisControl"] = rec.CISControl
		}
		if rec.Benchmark != "" {
			row["benchmark"] = rec.Benchmark
		}
		if rec.Rollup {
			row["rollup"] = true
		}
		out = append(out, row)
	}
	return out
}

// summarizeFindingWorkloads shapes the workload-first view for the model. The
// ranking is the Security page's (findings.AggregateWorkloads): exposed secrets
// first, then severity before volume — so "what do I touch first" gets the
// same answer in the chat as on the screen.
func summarizeFindingWorkloads(view findings.WorkloadView, limit int, clusterName func(string) string) map[string]interface{} {
	out := map[string]interface{}{
		"totalWorkloads": len(view.Workloads),
		"findings":       view.Findings,
	}
	if view.Unassigned > 0 {
		out["unassigned"] = view.Unassigned
		out["unassignedNote"] = "findings with no workload — compliance controls, broken down under benchmarks"
	}
	rows := view.Workloads
	if len(rows) > limit {
		out["truncated"] = true
		out["returned"] = limit
		rows = rows[:limit]
	}
	list := make([]map[string]interface{}, 0, len(rows))
	for _, w := range rows {
		row := map[string]interface{}{
			"resource": findings.QualifiedName(w.Namespace, w.Name),
			"kind":     w.Kind,
			"total":    w.Total,
			"fixable":  w.Fixable,
		}
		// Zero bands are omitted: on fifty rows they are most of the payload
		// and none of the information.
		for band, n := range map[string]int{"critical": w.Critical, "high": w.High, "medium": w.Medium, "low": w.Low} {
			if n > 0 {
				row[band] = n
			}
		}
		if len(w.Kinds) > 0 {
			row["kinds"] = w.Kinds
		}
		if w.Secrets > 0 {
			row["exposedSecrets"] = w.Secrets
		}
		if w.Image != "" {
			row["image"] = w.Image
			if w.Images > 1 {
				row["images"] = w.Images
			}
		}
		if !w.OldestSeen.IsZero() {
			row["openSince"] = w.OldestSeen.UTC().Format(time.RFC3339)
		}
		if clusterName != nil {
			row["clusterId"] = w.ClusterID
			if n := clusterName(w.ClusterID); n != "" {
				row["cluster"] = n
			}
		}
		list = append(list, row)
	}
	out["workloads"] = list

	names := func(ids []string) []string {
		if clusterName == nil {
			return nil
		}
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			if n := clusterName(id); n != "" {
				out = append(out, n)
			} else {
				out = append(out, id)
			}
		}
		return out
	}
	if len(view.TopImages) > 0 {
		imgs := make([]map[string]interface{}, 0, len(view.TopImages))
		for _, im := range view.TopImages {
			e := map[string]interface{}{
				"image": im.Image, "workloads": im.Workloads, "findings": im.Findings,
				"runsIn": im.WorkloadNames,
			}
			if im.Critical > 0 {
				e["critical"] = im.Critical
			}
			if im.High > 0 {
				e["high"] = im.High
			}
			if c := names(im.Clusters); len(c) > 0 {
				e["clusters"] = c
			}
			imgs = append(imgs, e)
		}
		out["topImages"] = imgs
		out["imageNote"] = "rebuilding one image clears every workload running it"
	}
	if len(view.TopChecks) > 0 {
		chks := make([]map[string]interface{}, 0, len(view.TopChecks))
		for _, c := range view.TopChecks {
			e := map[string]interface{}{
				"check": c.Title, "workloads": c.Workloads, "findings": c.Findings,
				"failsOn": c.WorkloadNames,
			}
			if cl := names(c.Clusters); len(cl) > 0 {
				e["clusters"] = cl
			}
			chks = append(chks, e)
		}
		out["topChecks"] = chks
		out["checkNote"] = "one manifest pattern failing in many places — fixing the pattern clears them all"
	}
	if len(view.Benchmarks) > 0 {
		bms := make([]map[string]interface{}, 0, len(view.Benchmarks))
		for _, b := range view.Benchmarks {
			e := map[string]interface{}{"benchmark": b.Name, "failingControls": b.Failing}
			if b.Critical > 0 {
				e["critical"] = b.Critical
			}
			if b.High > 0 {
				e["high"] = b.High
			}
			if b.Rollups > 0 {
				e["rollups"] = b.Rollups
			}
			bms = append(bms, e)
		}
		out["benchmarks"] = bms
	}
	return out
}

// summarizeFindingDetail shapes one finding's drill-down: what the stored row
// says, plus what the scanner says NOW — every package carrying the CVE (the
// stored row keeps one arbitrary remediation of possibly seventeen), or which
// resources fail a CIS control (the stored row keeps only the count).
func summarizeFindingDetail(d *findings.Detail, clusterName func(string) string) map[string]interface{} {
	rec := d.Record
	out := map[string]interface{}{
		"fingerprint": rec.Fingerprint,
		"severity":    string(rec.Severity),
		"title":       rec.Title,
		"kind":        string(rec.Kind),
		"source":      rec.Source,
		"status":      rec.Status,
		"firstSeen":   rec.FirstSeen.UTC().Format(time.RFC3339),
		"lastSeen":    rec.LastSeen.UTC().Format(time.RFC3339),
		"live":        d.Live,
	}
	if n := clusterName(rec.ClusterID); n != "" {
		out["cluster"] = n
	}
	if rec.ResourceName != "" {
		out["resource"] = strings.TrimSpace(rec.ResourceKind + " " + findings.QualifiedName(rec.ResourceNamespace, rec.ResourceName))
	}
	if rec.Image != "" {
		out["image"] = rec.Image
	}
	if rec.Remediation != "" {
		out["remediation"] = rec.Remediation
	}
	if rec.CISControl != "" {
		out["cisControl"] = rec.CISControl
	}
	if rec.Benchmark != "" {
		out["benchmark"] = rec.Benchmark
	}
	if rec.ResolvedAt != nil {
		out["resolvedAt"] = rec.ResolvedAt.UTC().Format(time.RFC3339)
	}
	if d.LiveError != "" {
		out["liveError"] = d.LiveError
		out["liveNote"] = "the scanner could not be re-read, so this is the stored finding only — NOT a sign that it is fixed"
	}

	if len(d.Images) > 0 {
		imgs := make([]map[string]interface{}, 0, len(d.Images))
		for _, im := range d.Images {
			e := map[string]interface{}{"image": im.Image, "containers": im.Containers}
			if im.OS != "" {
				e["os"] = im.OS
			}
			// -1 is "no Pod informer", which must not read as zero pods.
			if im.Pods >= 0 {
				e["pods"] = im.Pods
			}
			pkgs := im.Packages
			if len(pkgs) > detailPackagesReturned {
				e["packagesTotal"] = len(pkgs)
				pkgs = pkgs[:detailPackagesReturned]
			}
			fixable := 0
			list := make([]map[string]interface{}, 0, len(pkgs))
			for _, p := range pkgs {
				row := map[string]interface{}{"package": p.Name, "installed": p.InstalledVersion}
				if p.FixedVersion != "" {
					row["fixedIn"] = p.FixedVersion
					fixable++
				} else {
					row["fixedIn"] = "no upstream fix yet"
				}
				if p.Severity != "" {
					row["severity"] = strings.ToLower(p.Severity)
				}
				if p.Score > 0 {
					row["score"] = p.Score
				}
				if p.Link != "" {
					row["advisory"] = p.Link
				}
				list = append(list, row)
			}
			e["packages"] = list
			e["fixablePackages"] = fixable
			imgs = append(imgs, e)
		}
		out["images"] = imgs
		out["imageNote"] = "packages are sorted fixable first; one rebuild of the image fixes every container listed"
	} else if d.Live && rec.Kind == integrations.FindingCVE {
		out["note"] = "the scanner's current report no longer lists this CVE for this workload — it may have been fixed since the last sweep (every 10 minutes)"
	}

	if c := d.Compliance; c != nil {
		e := map[string]interface{}{"failingTotal": c.FailingTotal}
		if c.Benchmark != "" {
			e["benchmark"] = c.Benchmark
		}
		if c.Description != "" {
			e["requires"] = c.Description
		}
		// The benchmark's own rating can disagree with the finding's (the
		// normalizer defaults compliance to medium). Both ship; neither wins.
		if c.Severity != "" {
			e["benchmarkSeverity"] = c.Severity
		}
		failing := c.FailingResources
		if len(failing) > detailFailingReturned {
			failing = failing[:detailFailingReturned]
		}
		rows := make([]map[string]interface{}, 0, len(failing))
		for _, f := range failing {
			row := map[string]interface{}{"resource": strings.TrimSpace(f.Kind + " " + findings.QualifiedName(f.Namespace, f.Name))}
			if f.Message != "" {
				row["message"] = f.Message
			}
			rows = append(rows, row)
		}
		e["failing"] = rows
		out["compliance"] = e
	}
	return out
}

// currentClusterID is the request's cluster as the persisted stores key it —
// the kube-system UID. It resolves the way the request's connector does
// (ActiveContextFor: an explicit X-KubeBolt-Cluster, else the org's active
// context), NOT from ?cluster=: neither the chat nor /mcp sends that, and
// relying on it made "this cluster" read the whole org.
func (e *Executor) currentClusterID(ctx context.Context) string {
	if e.manager == nil {
		return ""
	}
	return e.manager.CanonicalClusterID(ctx, e.manager.ActiveContextFor(ctx))
}

// clusterDisplayName resolves a cluster UID to the name a reader recognizes.
// Empty when there is no manager (tests) or no name on record.
func (e *Executor) clusterDisplayName(ctx context.Context) func(string) string {
	if e.manager == nil {
		return func(string) string { return "" }
	}
	return func(id string) string { return e.manager.DisplayNameForCluster(ctx, id) }
}

// summarizeDeploys lists Deployment rollouts in the window, newest first.
// A rollout here is a NEW ReplicaSet: a rollback to an existing revision
// reuses its ReplicaSet and does not appear, and StatefulSets and DaemonSets
// are not covered — the note says so, so an empty list is not read as
// "nothing changed".
func summarizeDeploys(deploys []models.DeployEvent, hours int, namespace, name string, limit int) map[string]interface{} {
	rows := make([]models.DeployEvent, 0, len(deploys))
	for _, d := range deploys {
		if namespace != "" && d.Namespace != namespace {
			continue
		}
		if name != "" && d.Name != name {
			continue
		}
		rows = append(rows, d)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].DeployedAt.Equal(rows[j].DeployedAt) {
			return rows[i].DeployedAt.After(rows[j].DeployedAt)
		}
		return rows[i].Namespace+"/"+rows[i].Name < rows[j].Namespace+"/"+rows[j].Name
	})
	out := map[string]interface{}{
		"windowHours": hours,
		"total":       len(rows),
		"note":        "Deployment rollouts only (a new ReplicaSet). A rollback to an existing revision reuses its ReplicaSet and is not listed, and StatefulSets / DaemonSets are not covered — use get_workload_history for a workload's full story. An empty list is not proof that nothing changed.",
	}
	if len(rows) > limit {
		out["truncated"] = true
		out["returned"] = limit
		rows = rows[:limit]
	}
	list := make([]map[string]interface{}, 0, len(rows))
	now := time.Now()
	for _, d := range rows {
		row := map[string]interface{}{
			"deployedAt": d.DeployedAt.UTC().Format(time.RFC3339),
			"ago":        humanAgo(now.Sub(d.DeployedAt)),
			"kind":       d.Kind,
			"resource":   findings.QualifiedName(d.Namespace, d.Name),
		}
		if d.Image != "" {
			row["image"] = d.Image
		}
		list = append(list, row)
	}
	out["deploys"] = list
	return out
}

// A Falco event carries ~40 fields and a one-line output that repeats most of
// them: ~1.7 KB each, so 22 rows passed the 32 KB tool cap and the answer said
// it had been truncated (2026-09-27). The row keeps the fields that say WHAT
// ran, as WHOM, WHERE and against WHAT; the rest is in the Runtime view.
var runtimeEventFieldKeys = []string{
	"proc.name", "proc.cmdline", "proc.pname", "proc.exepath", "user.name",
	"container.name", "container.image.repository", "container.image.tag",
	"evt.type", "fd.name",
}

const (
	runtimeBehaviorMaxRunes = 280
	// runtimeEventsByteBudget keeps the rows well under the 32 KB tool cap,
	// leaving room for the counts and top rules around them.
	runtimeEventsByteBudget = 24 * 1024
	runtimeFieldMaxRunes    = 200
)

func runtimeEventFields(all map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range runtimeEventFieldKeys {
		if v := all[k]; v != "" && v != "<NA>" {
			out[k] = clipRunes(cluster.RedactText(v), runtimeFieldMaxRunes)
		}
	}
	return out
}

// clipRunes cuts at a rune boundary and says so.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func findingsQueryFor(clusterID string) findings.Query {
	return findings.Query{ClusterID: clusterID, Status: findings.StatusActive}
}

func runtimeQueryFor(clusterID string, window time.Duration) findings.EventQuery {
	return findings.EventQuery{ClusterID: clusterID, Since: time.Now().UTC().Add(-window), Limit: maxRuntimeEventsScanned}
}

// humanAgo is a coarse age for a reader; the exact time rides beside it.
func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// summarizeRuntimeEvents is counts first — by priority and by rule, because a
// noisy rule firing 400 times is one fact, not 400 — then a sample of rows,
// newest first. Behaviors and fields carry command lines, so every string goes
// through cluster.RedactText.
func summarizeRuntimeEvents(evs []findings.EventRecord, hours int, namespace, pod string, limit int, clusterName func(string) string) map[string]interface{} {
	rows := make([]findings.EventRecord, 0, len(evs))
	byPriority, byRule := map[string]int{}, map[string]int{}
	for _, ev := range evs {
		if namespace != "" && ev.Namespace != namespace {
			continue
		}
		if pod != "" && !strings.HasPrefix(ev.PodName, pod) {
			continue
		}
		rows = append(rows, ev)
		byPriority[ev.Priority]++
		byRule[ev.RuleName]++
	}
	out := map[string]interface{}{
		"windowHours": hours,
		"total":       len(rows),
		"byPriority":  byPriority,
	}
	if len(evs) >= maxRuntimeEventsScanned {
		out["scanCapped"] = true
		out["scanNote"] = fmt.Sprintf("only the newest %d events were read; narrow by priority or a shorter window for exact counts", maxRuntimeEventsScanned)
	}
	type ruleCount struct {
		rule string
		n    int
	}
	rules := make([]ruleCount, 0, len(byRule))
	for r, n := range byRule {
		rules = append(rules, ruleCount{r, n})
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].n != rules[j].n {
			return rules[i].n > rules[j].n
		}
		return rules[i].rule < rules[j].rule
	})
	if len(rules) > topImagesReturned {
		rules = rules[:topImagesReturned]
	}
	top := make([]map[string]interface{}, 0, len(rules))
	for _, r := range rules {
		top = append(top, map[string]interface{}{"rule": r.rule, "events": r.n})
	}
	out["topRules"] = top
	if len(rows) > limit {
		out["truncated"] = true
		out["returned"] = limit
		rows = rows[:limit]
	}
	list := make([]map[string]interface{}, 0, len(rows))
	budget := runtimeEventsByteBudget
	for i, ev := range rows {
		row := map[string]interface{}{
			"at":       ev.At.UTC().Format(time.RFC3339),
			"priority": ev.Priority,
			"rule":     ev.RuleName,
			"behavior": clipRunes(cluster.RedactText(ev.DetectedBehavior), runtimeBehaviorMaxRunes),
			"source":   ev.Source,
		}
		if ev.PodName != "" || ev.Namespace != "" {
			row["pod"] = findings.QualifiedName(ev.Namespace, ev.PodName)
		}
		if fields := runtimeEventFields(ev.Fields); len(fields) > 0 {
			row["fields"] = fields
		}
		if clusterName != nil {
			row["clusterId"] = ev.ClusterID
			if n := clusterName(ev.ClusterID); n != "" {
				row["cluster"] = n
			}
		}
		// Rows stop at a byte budget, not only at a count: events vary tenfold
		// in size, and a result past the tool cap is cut mid-JSON.
		if b, err := json.Marshal(row); err == nil {
			if len(b) > budget && len(list) > 0 {
				out["truncated"] = true
				out["returned"] = i
				break
			}
			budget -= len(b)
		}
		list = append(list, row)
	}
	out["events"] = list
	return out
}

func topKey(counts map[string]int) (string, int) {
	var bestK string
	var bestN int
	for k, n := range counts {
		// Ties break on the name so the answer does not change between calls
		// on unchanged data — map order in Go is random.
		if n > bestN || (n == bestN && k < bestK) {
			bestK, bestN = k, n
		}
	}
	return bestK, bestN
}

func topImages(counts, critical map[string]int) []map[string]interface{} {
	type row struct {
		image string
		n     int
	}
	all := make([]row, 0, len(counts))
	for k, n := range counts {
		all = append(all, row{k, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].image < all[j].image
	})
	if len(all) > topImagesReturned {
		all = all[:topImagesReturned]
	}
	out := make([]map[string]interface{}, 0, len(all))
	for _, r := range all {
		e := map[string]interface{}{"image": r.image, "findings": r.n}
		if c := critical[r.image]; c > 0 {
			e["critical"] = c
		}
		out = append(out, e)
	}
	return out
}

// summarizeFleet folds active insights into one row per cluster, worst first.
//
// Kobi is otherwise mono-cluster: list_clusters gives names and connectivity,
// and get_insights answers only about the one currently selected. From Home or
// Fleet — where the operator is looking at everything — "which cluster is
// worst?" had no answer at all.
//
// Reads the persisted store, so it answers for clusters that are DOWN. That is
// the case it matters in: a cluster nobody can reach is exactly the one you
// want counted.
func summarizeFleet(ctx context.Context, recs []insights.InsightRecord, name func(context.Context, string) string) map[string]interface{} {
	type row struct {
		crit, warn, info int
	}
	per := map[string]*row{}
	totals := row{}
	for _, rec := range recs {
		r := per[rec.ClusterID]
		if r == nil {
			r = &row{}
			per[rec.ClusterID] = r
		}
		switch rec.Severity {
		case "critical":
			r.crit++
			totals.crit++
		case "warning":
			r.warn++
			totals.warn++
		default:
			r.info++
			totals.info++
		}
	}

	out := make([]map[string]interface{}, 0, len(per))
	for id, r := range per {
		entry := map[string]interface{}{
			"cluster":  id,
			"critical": r.crit,
			"warning":  r.warn,
			"info":     r.info,
			"total":    r.crit + r.warn + r.info,
		}
		if name != nil {
			if n := name(ctx, id); n != "" {
				entry["cluster"] = n
				entry["clusterId"] = id
			}
		}
		out = append(out, entry)
	}
	// Worst first: critical, then warning, then total, then the name — so an
	// unchanged fleet always reports the same order. Map iteration is random
	// and a "worst cluster" that moves between identical calls is not an answer.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		for _, k := range []string{"critical", "warning", "total"} {
			if a[k].(int) != b[k].(int) {
				return a[k].(int) > b[k].(int)
			}
		}
		return fmt.Sprint(a["cluster"]) < fmt.Sprint(b["cluster"])
	})

	resp := map[string]interface{}{
		"clusters": out,
		"totals": map[string]interface{}{
			"clusters": len(out),
			"critical": totals.crit,
			"warning":  totals.warn,
			"info":     totals.info,
		},
	}
	if len(out) > 0 {
		resp["worstCluster"] = out[0]["cluster"]
	}
	return resp
}

// findSwitchTarget matches what the model asked for against the real cluster
// list. Models name clusters however the operator did — display name, context,
// or the uid from a fleet row — so all three are accepted, case-insensitively.
func findSwitchTarget(clusters []cluster.ClusterInfo, want string) (cluster.ClusterInfo, bool) {
	w := strings.ToLower(strings.TrimSpace(want))
	for _, c := range clusters {
		for _, candidate := range []string{c.DisplayName, c.Name, c.Context, c.ClusterID} {
			if candidate != "" && strings.ToLower(candidate) == w {
				return c, true
			}
		}
	}
	return cluster.ClusterInfo{}, false
}

// switchLabel is what a human calls the cluster.
func switchLabel(c cluster.ClusterInfo) string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	if c.Name != "" {
		return c.Name
	}
	return c.Context
}

// switchCandidates lists what the model COULD have meant, so a near-miss is
// one correction rather than a dead end.
func switchCandidates(clusters []cluster.ClusterInfo) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(clusters))
	for _, c := range clusters {
		out = append(out, map[string]interface{}{"cluster": switchLabel(c), "status": c.Status})
	}
	return out
}

// buildSwitchOffer is the whole decision, pure so it can be tested against a
// cluster list rather than a live manager. Returns the tool content and
// whether it is an error.
func buildSwitchOffer(clusters []cluster.ClusterInfo, args map[string]interface{}) (string, bool) {
	target := stringArg(args, "cluster")
	question := stringArg(args, "question")
	if target == "" || question == "" {
		return `{"error":"cluster and question are both required — the question is what the new conversation opens with"}`, true
	}
	match, found := findSwitchTarget(clusters, target)
	if !found {
		return jsonString(map[string]interface{}{
			"error":     fmt.Sprintf("no cluster matches %q", target),
			"available": switchCandidates(clusters),
		}), true
	}
	// Connectivity is checked BEFORE offering, not after clicking. The card is
	// one click — it switches and asks immediately — so offering a switch to a
	// cluster that cannot answer lands the operator on the waiting-for-agent
	// screen with their question in mid-air.
	if match.Status != "connected" {
		detail := match.Status
		if match.Error != "" {
			detail += ": " + match.Error
		}
		return jsonString(map[string]interface{}{
			"error":     fmt.Sprintf("%s is %s, so switching there cannot answer anything right now", switchLabel(match), detail),
			"cluster":   switchLabel(match),
			"status":    match.Status,
			"advice":    "say the cluster is unreachable and report what the fleet view already knows; do not offer the switch",
			"offerable": false,
		}), true
	}
	prop := newProposal("switch_cluster")
	prop.Target = ProposalTarget{Type: "clusters", Name: match.Context}
	// The CONTEXT is what /clusters/switch takes — not the display name the
	// model was given and not the uid.
	prop.Params["cluster"] = match.Context
	prop.Params["clusterLabel"] = switchLabel(match)
	prop.Params["question"] = question
	if established := stringArg(args, "established"); established != "" {
		// Carried so the new conversation does not start cold — and attributed
		// on the other side, because it was learned somewhere else.
		prop.Params["established"] = established
	}
	prop.Summary = "Switch to " + switchLabel(match) + " and ask there"
	prop.Rationale = stringArg(args, "rationale")
	if prop.Rationale == "" {
		prop.Rationale = "This question is about " + switchLabel(match) + ", and every tool except the fleet view reads only the selected cluster."
	}
	// A view change: nothing in any cluster is touched, and switching back is
	// one click.
	prop.Risk = "low"
	prop.Reversible = true
	return jsonString(prop), false
}
