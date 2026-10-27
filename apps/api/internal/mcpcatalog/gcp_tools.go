package mcpcatalog

import (
	"sort"
	"strings"
)

// The Google Cloud allowlist: which of Google's per-service MCP endpoints
// KubeBolt talks to, which of their tools each consumer gets, and how every
// argument that names a project is pinned. Names were verified against the
// live, public tools/list on 2026-09-24; the families the
// admin approves and the setup manual are derived from this table.
//
// The Kobi set is deliberately lean — about 30 tools, ~40 KB of definitions —
// because every tool is paid for in every prompt. Autopilot gets a stricter
// subset: only what changes a pipeline decision for an incident.

const (
	gcpFamilyGKE        = "Kubernetes Engine (GKE)"
	gcpFamilyMonitoring = "Monitoring and alerts"
	gcpFamilyLogs       = "Logs and errors"
	gcpFamilyDatabases  = "Databases"
	gcpFamilyCaches     = "Caches and messaging"
	gcpFamilyCompute    = "Compute"
	gcpFamilyInventory  = "Inventory, recommendations and quotas"
)

// gcpFamilies is the plain-language list, in the order the drawer shows it.
var gcpFamilies = []struct{ label, detail string }{
	{gcpFamilyGKE, "Clusters, node pools, versions and autoscaling, and the upgrade or repair operations in progress."},
	{gcpFamilyMonitoring, "Cloud Monitoring time series and PromQL queries, firing alerts and alert policies."},
	{gcpFamilyLogs, "Cloud Logging entries, including the audit logs of what changed, and Error Reporting groups."},
	{gcpFamilyDatabases, "Cloud SQL and AlloyDB instance status, and Database Insights query and system metrics — without connecting to the database."},
	{gcpFamilyCaches, "Memorystore for Redis and Valkey instances, and Pub/Sub subscriptions with their delivery and dead-letter settings."},
	{gcpFamilyCompute, "VM instances, the managed instance groups behind node pools, and persistent disks."},
	{gcpFamilyInventory, "Cloud Asset inventory (firewalls, load balancers, DNS and other resources without a tool of their own), Recommender and quota limits."},
}

// gcpPage bounds a list call: the default fills an omitted page size, the
// maximum clamps a larger one. Monitoring and Logging default to 100,000
// points and 50 entries, far past what fits in a tool result.
type gcpPage struct {
	field    string
	def, max int
}

// gcpToolRule is how one Google tool is exposed. Arguments are classified
// explicitly: a field that could name a project must appear in paths,
// pathLists, projectIDs, fixed, free or drop, or the tool is not bound — a
// new such field in Google's schema is reviewed before it is trusted.
type gcpToolRule struct {
	kobi, autopilot bool

	// paths are string fields holding a full resource name; the value must
	// be projects/<projectId> or start with projects/<projectId>/.
	paths []string
	// pathLists are arrays of such names.
	pathLists []string
	// fill are the paths/pathLists that default to projects/<projectId>
	// when omitted; they stop being required.
	fill []string
	// projectIDs are plain project-id fields. They are removed from the
	// schema the model sees and always sent with the configured project.
	projectIDs []string
	// fixed are fields with one valid value (query_range's location); like
	// projectIDs they are hidden and filled.
	fixed map[string]string
	// free are fields whose name looks like a resource name but is not one
	// (a VM or MIG name, scoped by the project field next to it).
	free []string
	// drop are fields removed from the schema and refused if sent:
	// deprecated, unsupported, or bulk the model does not need.
	drop []string

	page *gcpPage
	// loc marks tools that take a region or zone, where the configured
	// default location is worth saying.
	loc bool
	// desc replaces Google's description. {project} and {location} are
	// substituted at bind time. Hand-written text carries its own scope
	// sentence; Google's gets one appended.
	desc string
	// note is appended to Google's description.
	note string
}

// gcpService is one MCP endpoint: https://<id>.googleapis.com<path>.
type gcpService struct {
	id     string // the googleapis host label; local tool names are <id>_<tool>
	path   string // "" means /mcp (Cloud Storage would need /storage/mcp)
	family string
	tools  map[string]gcpToolRule
}

const gcpComputeZoneNote = "zone is required: a Kubernetes node's topology.kubernetes.io/zone label gives it."

// gcpServices is the allowlist, ordered by id.
var gcpServices = []gcpService{
	{
		id: "alloydb", family: gcpFamilyDatabases,
		tools: map[string]gcpToolRule{
			"list_instances": {kobi: true, autopilot: true, paths: []string{"parent"}, loc: true,
				page: &gcpPage{"pageSize", 50, 200},
				note: "Use parent projects/{project}/locations/-/clusters/- to list every instance."},
			"get_instance": {kobi: true, autopilot: true, paths: []string{"name"}, loc: true},
		},
	},
	{
		id: "cloudasset", family: gcpFamilyInventory,
		tools: map[string]gcpToolRule{
			"list_assets": {kobi: true, autopilot: true, paths: []string{"parent"}, fill: []string{"parent"},
				drop: []string{"relationshipTypes"}, page: &gcpPage{"pageSize", 50, 200},
				desc: "Lists what exists in the project from Cloud Asset Inventory — the way to see resources that have no tool of their own: " +
					"firewall rules, networks and subnets, forwarding rules and backend services, Cloud NAT routers, Cloud DNS zones, " +
					"Secret Manager secrets (names only, never values), Artifact Registry repositories. parent defaults to projects/{project}. " +
					"Pass assetTypes (regular expressions allowed), e.g. [\"compute.googleapis.com/Firewall\"], " +
					"[\"compute.googleapis.com/ForwardingRule\",\"compute.googleapis.com/BackendService\"], [\"dns.googleapis.com/ManagedZone\"], " +
					"and contentType \"RESOURCE\" to get each resource's configuration (without it only names come back). " +
					"There is no search by IP address: list the type and look for the address in the result. pageSize defaults to 50."},
		},
	},
	{
		id: "clouderrorreporting", family: gcpFamilyLogs,
		tools: map[string]gcpToolRule{
			"list_group_stats": {kobi: true, autopilot: true, paths: []string{"projectName"}, fill: []string{"projectName"},
				drop: []string{"alignment", "alignmentTime", "timedCountDuration"}, page: &gcpPage{"pageSize", 20, 100},
				desc: "Recurring application errors grouped by Cloud Error Reporting: each group's stack trace, count, first and last seen, " +
					"and affected users. projectName defaults to projects/{project}. Narrow with timeRange {\"period\": \"PERIOD_1_HOUR\" | " +
					"\"PERIOD_6_HOURS\" | \"PERIOD_1_DAY\" | \"PERIOD_1_WEEK\"} and serviceFilter {\"service\": \"<service name>\"}; " +
					"order is COUNT_DESC (default) or LAST_SEEN_DESC. pageSize defaults to 20. For individual log lines use logging_list_log_entries."},
		},
	},
	{
		id: "cloudquotas", family: gcpFamilyInventory,
		tools: map[string]gcpToolRule{
			"list_quota_infos": {kobi: true, autopilot: true, paths: []string{"parent"}},
		},
	},
	{
		id: "compute", family: gcpFamilyCompute,
		tools: map[string]gcpToolRule{
			"list_instances": {kobi: true, autopilot: true, projectIDs: []string{"project"}, loc: true,
				page: &gcpPage{"pageSize", 100, 500}, note: gcpComputeZoneNote},
			"get_instance_basic_info": {kobi: true, autopilot: true, projectIDs: []string{"project"}, free: []string{"name"}, loc: true,
				note: gcpComputeZoneNote},
			"list_instance_group_managers": {kobi: true, autopilot: true, projectIDs: []string{"project"}, loc: true,
				page: &gcpPage{"pageSize", 100, 500}, note: gcpComputeZoneNote},
			"list_managed_instances": {kobi: true, autopilot: true, projectIDs: []string{"project"}, free: []string{"name"}, loc: true,
				page: &gcpPage{"pageSize", 100, 500}, note: gcpComputeZoneNote},
			"list_disks": {kobi: true, projectIDs: []string{"project"}, loc: true,
				page: &gcpPage{"pageSize", 100, 500}, note: gcpComputeZoneNote},
		},
	},
	{
		id: "container", family: gcpFamilyGKE,
		tools: map[string]gcpToolRule{
			"list_clusters":   {kobi: true, autopilot: true, paths: []string{"parent"}, loc: true},
			"get_cluster":     {kobi: true, autopilot: true, paths: []string{"name"}, loc: true},
			"list_node_pools": {kobi: true, autopilot: true, paths: []string{"parent"}, loc: true},
			"list_operations": {kobi: true, autopilot: true, paths: []string{"parent"}, loc: true},
		},
	},
	{
		id: "databaseinsights", family: gcpFamilyDatabases,
		tools: map[string]gcpToolRule{
			"get_query_metrics": {kobi: true, autopilot: true, paths: []string{"parent"}, projectIDs: []string{"resource"}, loc: true,
				desc: "Query-level metrics from Cloud SQL (PostgreSQL, MySQL) and AlloyDB query insights — which queries spend execution, lock " +
					"and IO time — without connecting to the database. parent: projects/{project}/locations/<the instance's region>. " +
					"promqlQuery: PromQL over quoted metric names with the labels project_id and resource_id=\"{project}:<instance>\", e.g. " +
					"topk(10, sum by (query_hash)(avg_over_time({\"__name__\"=\"cloudsql.googleapis.com/database/postgresql/insights/perquery/execution_time\"," +
					"\"monitored_resource\"=\"cloudsql_instance_database\",\"project_id\"=\"{project}\",\"resource_id\"=\"{project}:<instance>\"}[5m]))). " +
					"Metrics: PostgreSQL cloudsql.googleapis.com/database/postgresql/insights/{aggregate,perquery}/{execution_time,lock_time,io_time,row_count}; " +
					"MySQL dbinsights.googleapis.com/{aggregate,perquery}/{execution_time,execution_count,lock_time,io_time}; " +
					"AlloyDB alloydb.googleapis.com/database/postgresql/insights/... . Use aggregate metrics unless a query_hash is known and " +
					"never group by querystring unless asked. Optional startTime/endTime in RFC3339 UTC. The project ID (resource) is filled in."},
			"get_system_metrics": {kobi: true, autopilot: true, paths: []string{"parent"}, projectIDs: []string{"resource"}, loc: true,
				desc: "System metrics of Cloud SQL, AlloyDB, Spanner and Bigtable instances (CPU, memory, disk, connections, locks) through PromQL. " +
					"parent: projects/{project}/locations/<the instance's region>. promqlQuery example: " +
					"max_over_time({\"__name__\"=\"cloudsql.googleapis.com/database/cpu/utilization\",\"monitored_resource\"=\"cloudsql_database\"," +
					"\"project_id\"=\"{project}\",\"database_id\"=\"{project}:<instance>\"}[5m]); omit database_id to cover every instance. " +
					"Cloud SQL metrics (cloudsql.googleapis.com/database/...): cpu/utilization, memory/usage, memory/quota, disk/bytes_used, " +
					"disk/quota, network/connections, postgresql/num_backends, postgresql/backends_in_wait, postgresql/deadlock_count, " +
					"postgresql/transaction_id_utilization, mysql/queries. Use [5m] windows and aggregations (max, avg, topk). " +
					"Optional startTime/endTime in RFC3339 UTC. The project ID (resource) is filled in."},
		},
	},
	{
		id: "logging", family: gcpFamilyLogs,
		tools: map[string]gcpToolRule{
			"list_log_entries": {kobi: true, autopilot: true, pathLists: []string{"resourceNames"}, fill: []string{"resourceNames"},
				drop: []string{"projectIds"}, page: &gcpPage{"pageSize", 20, 100},
				desc: "Reads Cloud Logging entries. resourceNames defaults to [\"projects/{project}\"]. Always filter by time and source, e.g. " +
					"timestamp>=\"<RFC3339 start>\" AND resource.type=\"cloudsql_database\" AND severity>=WARNING. Useful sources: " +
					"resource.type=\"k8s_cluster\" (GKE control plane), \"gce_instance\", \"cloudsql_database\", \"redis_instance\", " +
					"\"http_load_balancer\"; the audit log of what changed: logName=\"projects/{project}/logs/cloudaudit.googleapis.com%2Factivity\". " +
					"Use orderBy \"timestamp desc\" for the newest first. pageSize defaults to 20 (max 100); follow nextPageToken only when needed."},
		},
	},
	{
		id: "memorystore", family: gcpFamilyCaches,
		tools: map[string]gcpToolRule{
			"list_instances": {kobi: true, autopilot: true, paths: []string{"parent"}, loc: true, page: &gcpPage{"pageSize", 50, 200}},
			"get_instance":   {kobi: true, paths: []string{"name"}, loc: true},
		},
	},
	{
		id: "monitoring", family: gcpFamilyMonitoring,
		tools: map[string]gcpToolRule{
			"list_timeseries": {kobi: true, autopilot: true, paths: []string{"name"}, fill: []string{"name"},
				drop: []string{"orderBy", "secondaryAggregation"}, page: &gcpPage{"pageSize", 500, 2000},
				desc: "Reads Cloud Monitoring time series for one metric type. name defaults to projects/{project}. filter names exactly one " +
					"metric type and may narrow by labels, e.g. metric.type=\"cloudsql.googleapis.com/database/cpu/utilization\" AND " +
					"resource.labels.database_id=\"{project}:<instance>\". interval {\"startTime\", \"endTime\"} in RFC3339: keep it short " +
					"(the last hour unless asked). view \"FULL\" returns points, \"HEADERS\" only which series exist. Aggregate so the answer " +
					"stays small: aggregation {\"alignmentPeriod\": \"300s\", \"perSeriesAligner\": \"ALIGN_MEAN\" (ALIGN_RATE for counters), " +
					"\"crossSeriesReducer\": \"REDUCE_MAX\", \"groupByFields\": [\"resource.label.database_id\"]}. pageSize defaults to 500 points. " +
					"Useful types: cloudsql.googleapis.com/database/{cpu/utilization,memory/utilization,disk/utilization,network/connections,up}, " +
					"redis.googleapis.com/stats/memory/usage_ratio, redis.googleapis.com/clients/connected, " +
					"pubsub.googleapis.com/subscription/{num_undelivered_messages,oldest_unacked_message_age}, " +
					"loadbalancing.googleapis.com/https/{request_count,backend_latencies}. For PromQL use monitoring_query_range."},
			"query_range": {kobi: true, autopilot: true, paths: []string{"name"}, fill: []string{"name"},
				fixed: map[string]string{"location": "global"},
				desc: "Runs a PromQL range query on Cloud Monitoring (Google Cloud metrics and Managed Service for Prometheus). " +
					"name defaults to projects/{project}. Pass query, start and end (RFC3339 or UNIX seconds; keep the range short, " +
					"e.g. the last hour) and step (e.g. \"60s\"). A Google Cloud metric maps to PromQL by replacing the first \"/\" with \":\" " +
					"and every other \".\" or \"/\" with \"_\", e.g. cloudsql_googleapis_com:database_cpu_utilization{database_id=\"{project}:<instance>\"}; " +
					"the quoted form {\"__name__\"=\"cloudsql.googleapis.com/database/cpu/utilization\"} also works. " +
					"Aggregate (avg by, max by, topk) so the answer stays small."},
			"list_alerts": {kobi: true, autopilot: true, paths: []string{"parent"}, fill: []string{"parent"},
				page: &gcpPage{"pageSize", 50, 200}},
			"list_alert_policies": {kobi: true, paths: []string{"name"}, fill: []string{"name"},
				page: &gcpPage{"pageSize", 20, 100}},
		},
	},
	{
		id: "pubsub", family: gcpFamilyCaches,
		tools: map[string]gcpToolRule{
			"list_subscriptions": {kobi: true, autopilot: true, projectIDs: []string{"projectId"}, page: &gcpPage{"pageSize", 100, 300}},
			"get_subscription":   {kobi: true, autopilot: true, projectIDs: []string{"projectId"}},
		},
	},
	{
		id: "recommender", family: gcpFamilyInventory,
		tools: map[string]gcpToolRule{
			"list_recommendations": {kobi: true, autopilot: true, paths: []string{"parent"}, loc: true,
				page: &gcpPage{"pageSize", 20, 100},
				desc: "Lists Recommender recommendations. parent: projects/{project}/locations/<location>/recommenders/<recommender id>, " +
					"e.g. google.container.DiagnosisRecommender (GKE problems; the cluster's location), " +
					"google.compute.instance.MachineTypeRecommender (a zone), google.cloudsql.instance.OutOfDiskRecommender, " +
					"google.cloudsql.instance.PerformanceRecommender or google.cloudsql.instance.UnderprovisionedRecommender (a region). " +
					"Filter the active ones with stateInfo.state = ACTIVE. pageSize defaults to 20."},
		},
	},
	{
		id: "redis", family: gcpFamilyCaches,
		tools: map[string]gcpToolRule{
			"list_instances": {kobi: true, autopilot: true, paths: []string{"parent"}, loc: true, page: &gcpPage{"pageSize", 50, 200},
				note: "Use parent projects/{project}/locations/- to list every region."},
			"get_instance": {kobi: true, paths: []string{"name"}, loc: true},
		},
	},
	{
		id: "sqladmin", family: gcpFamilyDatabases,
		tools: map[string]gcpToolRule{
			"list_instances": {kobi: true, autopilot: true, projectIDs: []string{"project"}, loc: true,
				page: &gcpPage{"maxResults", 50, 200}},
			"get_instance": {kobi: true, autopilot: true, projectIDs: []string{"project"}, loc: true},
		},
	},
}

// gcpScopedArgNames are argument names that can point a call at a resource.
// Every one a bound tool declares must be classified by its rule.
var gcpScopedArgNames = map[string]bool{
	"parent": true, "name": true, "names": true, "resource": true, "resourceNames": true,
	"projectName": true, "project": true, "projectId": true, "projectIds": true, "projectNumber": true,
	"full_resource_name": true, "fullResourceName": true, "scope": true,
}

// gcpDeniedServices are endpoints KubeBolt never talks to, whatever a table
// says: cloudcli runs arbitrary gcloud/bq commands, Gemini Cloud Assist
// creates investigations, and the other three read table rows, documents or
// query results.
var gcpDeniedServices = map[string]string{
	"cloudcli":          "runs arbitrary gcloud and bq commands",
	"geminicloudassist": "creates Gemini Cloud Assist investigations",
	"spanner":           "serves database contents",
	"bigquery":          "serves query results and table contents",
	"firestore":         "serves document contents",
}

// gcpDeniedTools are tools Google annotates readOnlyHint=true that read data
// or secrets. The platform deny policy on tool.isReadOnly does not stop
// them, so KubeBolt refuses them by name wherever they appear.
var gcpDeniedTools = map[string]string{
	"execute_sql":              "runs SQL",
	"execute_sql_readonly":     "runs SQL against the database",
	"execute_sql_read_only":    "runs SQL against the database",
	"get_instance_auth_string": "returns the Redis AUTH secret",
	"read_object":              "returns Cloud Storage object contents",
	"read_text":                "returns Cloud Storage object contents",
	"get_query_results":        "returns query results",
	"get_query_plan":           "runs a query to plan it",
	"create_session":           "opens a database session",
	"get_document":             "returns document contents",
	"list_documents":           "returns document contents",
	"list_collections":         "enumerates document data",
	"run_gcloud_command":       "runs arbitrary gcloud commands",
	"run_bq_command":           "runs arbitrary bq commands",
}

// gcpWriteFragments mark tool names that change something. Google annotates
// all of them readOnlyHint=false today; matching the names too means a
// mis-annotated write is still refused.
var (
	gcpWriteContains = []string{"create", "update", "delete", "upgrade"}
	gcpWritePrefixes = []string{
		"apply_", "backup_", "cancel_", "clone", "commit", "deploy", "design_", "export", "failover", "import",
		"invoke_", "investigate", "mark_", "move_", "optimize_", "patch_", "publish", "reschedule", "reset_",
		"restore", "set_", "start_", "stop_", "undelete", "ask_", "write_",
	}
)

// gcpDenied reports why KubeBolt refuses a Google tool whatever the
// allowlist says, or "" when nothing refuses it. The GKE *_k8s_* tools are
// refused because they read Kubernetes objects and logs (Secrets included)
// outside KubeBolt's own access and redaction.
func gcpDenied(service, tool string) string {
	if why, ok := gcpDeniedServices[service]; ok {
		return why
	}
	if why, ok := gcpDeniedTools[tool]; ok {
		return why
	}
	if strings.HasPrefix(tool, "execute_sql") {
		return "runs SQL"
	}
	if strings.Contains(tool, "k8s") {
		return "reads Kubernetes objects and logs outside KubeBolt's own access"
	}
	for _, f := range gcpWriteContains {
		if strings.Contains(tool, f) {
			return "changes resources"
		}
	}
	for _, p := range gcpWritePrefixes {
		if strings.HasPrefix(tool, p) {
			return "changes resources"
		}
	}
	return ""
}

// gcpServiceByID returns one allowlisted service.
func gcpServiceByID(id string) (*gcpService, bool) {
	for i := range gcpServices {
		if gcpServices[i].id == id {
			return &gcpServices[i], true
		}
	}
	return nil, false
}

// allows reports whether the static table gives consumer c this tool.
func (r gcpToolRule) allows(c Consumer) bool {
	switch c {
	case ConsumerKobi:
		return r.kobi
	case ConsumerAutopilot:
		return r.autopilot
	}
	return false
}

// gcpLocalName is the binding-local tool name: the service keeps names that
// collide across endpoints (list_instances exists on five) apart.
func gcpLocalName(service, tool string) string { return service + "_" + tool }

// gcpToolNames lists the local names the table gives a consumer, sorted.
func gcpToolNames(c Consumer) []string {
	var out []string
	for _, s := range gcpServices {
		for name, r := range s.tools {
			if r.allows(c) {
				out = append(out, gcpLocalName(s.id, name))
			}
		}
	}
	sort.Strings(out)
	return out
}

// gcpFamilyList derives each family's consumer flags from the table, so the
// drawer can never promise a consumer a family it cannot read.
func gcpFamilyList() []Family {
	kobi := map[string]bool{}
	auto := map[string]bool{}
	for _, s := range gcpServices {
		for _, r := range s.tools {
			kobi[s.family] = kobi[s.family] || r.kobi
			auto[s.family] = auto[s.family] || r.autopilot
		}
	}
	out := make([]Family, 0, len(gcpFamilies))
	for _, f := range gcpFamilies {
		if !kobi[f.label] && !auto[f.label] {
			continue
		}
		out = append(out, Family{Label: f.label, Detail: f.detail, Kobi: kobi[f.label], Autopilot: auto[f.label]})
	}
	return out
}
