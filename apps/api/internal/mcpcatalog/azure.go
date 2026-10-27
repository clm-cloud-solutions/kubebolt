package mcpcatalog

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The Microsoft Azure connector reads Azure through the Azure MCP Server
// (@azure/mcp) running next to KubeBolt as a sidecar. The sidecar holds the
// Azure identity; KubeBolt holds only which subscription to read, so the
// connector has no secret fields. This file is the reviewed half — the
// allowlist, the families an admin approves and the setup manual, generated
// from the same tables so they cannot drift. azure_binding.go talks to the
// sidecar.

const (
	azureConnectorID = "azure"
	azureModeSidecar = "sidecar"

	// DefaultAzmcpURL is where the Azure MCP Server listens when it runs with
	// incoming auth disabled: loopback only, MCP endpoint at the root path.
	// KUBEBOLT_MCP_AZMCP_URL overrides it.
	DefaultAzmcpURL = "http://127.0.0.1:5001/"

	// azmcpVersion is the Azure MCP Server release the allowlist was
	// reviewed against. Mirrors AZMCP_VERSION in Makefile.ee and the image
	// tag in deploy/helm/kubebolt/values.yaml (a test compares them): the betas
	// rename commands and parameters every few days, so a bump means running
	// the live test (KUBEBOLT_MCP_LIVE_AZURE=1) against the new version.
	azmcpVersion = "3.0.0-beta.46"

	azureGUIDPattern = `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
)

var azureGUIDRE = regexp.MustCompile(azureGUIDPattern)

// azureCommand is one router command KubeBolt lets through. Kobi gets every
// listed command; Autopilot only those marked, because it only needs what
// changes a pipeline decision.
type azureCommand struct {
	name string
	// summary replaces the server's own description in the tool text: it is
	// reviewed with the allowlist, stays put across betas, and carries none
	// of the "always use this tool" steering the upstream text does.
	summary   string
	autopilot bool
	// autopilotNote narrows the command for Autopilot; check enforces it.
	autopilotNote string
	// strip lists parameters the command declares but KubeBolt never
	// forwards: database logins and key-based auth.
	strip []string
	// idFilters lists resource-id parameters the server matches as a
	// substring of the results it already read, not as an id it reads: they
	// also take the part of an id after /subscriptions/<id>/.
	idFilters []string
	// lists marks a command that lists resources. Some answer with names
	// alone (postgres_list: {"Servers":["pg1"]}), while the commands that
	// read one of them need its resource group: such an answer carries
	// azureNamesOnlyNote.
	lists bool
	// check vets the prepared parameters and returns a message for the model
	// when the call must not go out.
	check func(c Consumer, subscription string, params map[string]json.RawMessage) string
}

// azureNamespace is one router of the sidecar (--mode namespace).
type azureNamespace struct {
	name     string
	title    string
	purpose  string
	family   string
	commands []azureCommand
}

// azureDirectTool is one of the tools the sidecar exposes flat, outside any
// router, in namespace mode.
type azureDirectTool struct {
	name      string
	namespace string // the --namespace that enables it
	family    string
	summary   string
	autopilot bool
}

const (
	azureFamilyAKS       = "Kubernetes (AKS)"
	azureFamilyMonitor   = "Metrics and logs"
	azureFamilyHealth    = "Health and diagnostics"
	azureFamilyData      = "Databases and caches"
	azureFamilyMessaging = "Messaging"
	azureFamilyRegistry  = "Registry and access"
	azureFamilyInventory = "Inventory and quotas"
	azureFamilyKeyVault  = "Key Vault metadata"
	azureFamilyStorage   = "Storage"
	azureFamilyCost      = "Cost and recommendations"
)

// Parameters that would let a command log in to a database. The flexible
// server list only goes below server level with a login, so its server and
// database filters go too: the connector stays on the control plane.
var (
	azureDBLogin      = []string{"auth-type", "password", "user"}
	azurePostgresList = []string{"auth-type", "database", "password", "schema", "server", "user"}
	azureMySQLList    = []string{"auth-type", "database", "password", "server", "user"}
)

// azureNamespaces is the allowlist: the reviewed (namespace, command) pairs,
// with the command names measured on the live server. Anything
// not listed here does not exist for the model.
var azureNamespaces = []azureNamespace{
	{
		name: "aks", title: "Azure Kubernetes Service", family: azureFamilyAKS,
		purpose: "Cluster and node pool state: a pool that cannot scale or a cluster mid-upgrade explains NotReady nodes and a maxed-out HPA.",
		commands: []azureCommand{
			{name: "aks_cluster_get", autopilot: true, lists: true, summary: "List the AKS clusters, or show one: Kubernetes version, power and provisioning state, node count, networking, identity."},
			{name: "aks_nodepool_get", autopilot: true, summary: "List a cluster's node pools, or show one: VM size, node count, autoscaling bounds, mode and provisioning state."},
		},
	},
	{
		name: "monitor", title: "Azure Monitor", family: azureFamilyMonitor,
		purpose: "Platform metrics for any Azure resource, KQL over Log Analytics, and the activity log of what changed. Fired alerts are only visible through a workspace's Alert table.",
		commands: []azureCommand{
			{name: "monitor_activitylog_list", autopilot: true, summary: "Activity log of one resource, found by its name alone (no resource group needed), over the last hours: stop and start, scaling, failovers, firewall and configuration changes, Resource Health events. KubeBolt merges each operation into one row (who, when, final status, what was written, the changed sub-resource as \"on\") under the resource id and its group, so top counts operations: it defaults to 10, set it to 20 when looking for what changed, and about 50 fit in one answer. hours defaults to 24; the log keeps 90 days (2160). match keeps the operations whose name or changed sub-resource contains it (a parameter's configurations/<name>, a firewall or security rule's name)."},
			{name: "monitor_metrics_batchquery", autopilot: true, summary: "The same metrics for up to 50 resources of one type and region in one request."},
			{name: "monitor_metrics_definitions", autopilot: true, summary: "The metrics a resource exposes (names, namespaces, units, aggregations); call it before metrics_query."},
			{name: "monitor_metrics_query", autopilot: true, summary: "Time series of a resource's platform metrics: CPU, DTU, storage, connections and failed connections, server load, dead-lettered messages."},
			{name: "monitor_resource_log_query", autopilot: true, summary: "KQL over the logs of one resource (resource-id), e.g. AzureDiagnostics."},
			{name: "monitor_table_list", summary: "The tables of a Log Analytics workspace, to write a KQL query."},
			{name: "monitor_workspace_list", lists: true, summary: "The Log Analytics workspaces of the subscription."},
			{name: "monitor_workspace_log_query", autopilot: true, summary: "KQL over a whole Log Analytics workspace: ContainerLogV2, KubeEvents, AzureActivity, AzureDiagnostics, Alert."},
		},
	},
	{
		name: "resourcehealth", title: "Azure Resource Health", family: azureFamilyHealth,
		purpose: "Whether Azure itself reports a resource degraded: the first question when a managed database or cache stops answering. A 409 that says the provider is not registered means the subscription has not registered Microsoft.ResourceHealth, or registered it minutes ago (it can take about ten minutes to settle); say so to the user, and read the resource's health events in monitor_activitylog_list, which carries them either way.",
		commands: []azureCommand{
			{name: "resourcehealth_availability-status_get", autopilot: true, summary: "Availability (Available, Degraded, Unavailable, Unknown) and its reason for one resource (resourceId, camelCase) or every resource of a resource group."},
			{name: "resourcehealth_health-events_list", autopilot: true, summary: "Service health events of the subscription: incidents, planned maintenance, advisories."},
		},
	},
	{
		name: "applens", title: "Azure AppLens", family: azureFamilyHealth,
		purpose: "Guided diagnostics for one resource (AKS, SQL, App Service).",
		commands: []azureCommand{
			{name: "applens_resource_diagnose", autopilot: true, summary: "Ask AppLens a question about one resource (performance, failures, availability) and get its diagnosis."},
		},
	},
	{
		name: "sql", title: "Azure SQL Database", family: azureFamilyData,
		purpose: "Server and database state; the firewall rules explain a refused connection from AKS.",
		commands: []azureCommand{
			{name: "sql_db_get", autopilot: true, summary: "Show one database or list a server's databases: status, tier, size, zone redundancy."},
			{name: "sql_elastic-pool_list", summary: "A server's elastic pools: SKU, capacity, state, per-database limits."},
			{name: "sql_server_entra-admin_list", summary: "A server's Microsoft Entra administrators."},
			{name: "sql_server_firewall-rule_list", autopilot: true, summary: "A server's firewall rules (IP ranges)."},
			{name: "sql_server_get", autopilot: true, summary: "Show one server or list the servers of a resource group: state, version, public network access."},
		},
	},
	{
		name: "postgres", title: "Azure Database for PostgreSQL", family: azureFamilyData,
		purpose: "Flexible server inventory, configuration and parameters. " + azureServerDown("is_db_alive or active_connections") +
			" Server level only: KubeBolt never logs in to a database and removes user names and passwords from every request.",
		commands: []azureCommand{
			{name: "postgres_list", autopilot: true, lists: true, strip: azurePostgresList, summary: "List the PostgreSQL flexible servers of the subscription or of one resource group, by name only: no resource group, id or state."},
			{name: "postgres_server_config_get", autopilot: true, strip: azureDBLogin, summary: "A server's configuration: version, SKU, location (the server's region), storage, backup retention, geo-redundancy. Not whether it is running, nor its network or firewall rules."},
			{name: "postgres_server_param_get", autopilot: true, strip: azureDBLogin, summary: "The value of one server parameter (max_connections, ...)."},
		},
	},
	{
		name: "mysql", title: "Azure Database for MySQL", family: azureFamilyData,
		purpose: "Flexible server inventory, configuration and parameters. " + azureServerDown("Uptime or active_connections") +
			" Server level only: KubeBolt never logs in to a database and removes user names and passwords from every request.",
		commands: []azureCommand{
			{name: "mysql_list", autopilot: true, lists: true, strip: azureMySQLList, summary: "List the MySQL flexible servers of the subscription or of one resource group."},
			{name: "mysql_server_config_get", autopilot: true, strip: azureDBLogin, summary: "A server's configuration: version, SKU, storage, backup retention, geo-redundancy. Not whether it is running, nor its network or firewall rules."},
			{name: "mysql_server_param_get", autopilot: true, strip: azureDBLogin, summary: "The value of one server parameter (max_connections, wait_timeout, ...)."},
		},
	},
	{
		name: "cosmos", title: "Azure Cosmos DB", family: azureFamilyData,
		purpose: "Inventory only; throttling (429) and RU consumption are metrics: use monitor metrics_query on the account.",
		commands: []azureCommand{
			{name: "cosmos_list", autopilot: true, strip: []string{"auth-method"}, summary: "List the Cosmos DB accounts, an account's databases, or a database's containers. Never items."},
		},
	},
	{
		name: "redis", title: "Azure Cache for Redis", family: azureFamilyData,
		purpose: "Inventory only; server load, errors and connected clients are metrics: use monitor metrics_query.",
		commands: []azureCommand{
			{name: "redis_list", autopilot: true, summary: "List the Azure Cache for Redis and Azure Managed Redis resources of the subscription."},
		},
	},
	{
		name: "servicebus", title: "Azure Service Bus", family: azureFamilyMessaging,
		purpose: "Backlog and dead-letter counts: the classic cause of a healthy consumer that processes nothing. namespace is the host name <name>.servicebus.windows.net.",
		commands: []azureCommand{
			{name: "servicebus_queue_details", autopilot: true, check: azureCheckServiceBusHost, summary: "A queue's runtime state: active, dead-lettered and scheduled message counts, size, status."},
			{name: "servicebus_topic_details", autopilot: true, check: azureCheckServiceBusHost, summary: "A topic's runtime state: subscriptions, scheduled messages, size, status."},
			{name: "servicebus_topic_subscription_details", autopilot: true, check: azureCheckServiceBusHost, summary: "A topic subscription's runtime state: active and dead-lettered message counts, delivery settings."},
		},
	},
	{
		name: "eventhubs", title: "Azure Event Hubs", family: azureFamilyMessaging,
		purpose: "Namespace, hub and consumer group state.",
		commands: []azureCommand{
			{name: "eventhubs_eventhub_consumergroup_get", autopilot: true, summary: "List a hub's consumer groups, or show one."},
			{name: "eventhubs_eventhub_get", autopilot: true, summary: "List a namespace's event hubs, or show one: partitions, retention, status."},
			{name: "eventhubs_namespace_get", autopilot: true, lists: true, summary: "List the Event Hubs namespaces, or show one: SKU, capacity, status."},
		},
	},
	{
		name: "acr", title: "Azure Container Registry", family: azureFamilyRegistry,
		purpose: "Whether the registry and the repository an image pull names exist.",
		commands: []azureCommand{
			{name: "acr_registry_list", autopilot: true, lists: true, summary: "List the container registries: login server, SKU, location."},
			{name: "acr_registry_repository_list", autopilot: true, summary: "List the repositories of the registries, or of one registry."},
		},
	},
	{
		name: "role", title: "Azure role assignments", family: azureFamilyRegistry,
		purpose: "Whether an identity holds a role on a resource, e.g. AcrPull for the kubelet identity on a registry. Returns principal and role definition ids, not names.",
		commands: []azureCommand{
			{
				name: "role_assignment_list", autopilot: true, check: azureCheckRoleScope,
				summary:       "Role assignments at a scope (a resource id inside the subscription) and below it.",
				autopilotNote: "scope must be a container registry resource id",
			},
		},
	},
	{
		name: "quota", title: "Azure quotas", family: azureFamilyInventory,
		purpose: "A node pool that cannot scale for lack of vCPU quota in its region.",
		commands: []azureCommand{
			{name: "quota_region_availability_list", autopilot: true, summary: "The regions where the given resource types are available."},
			{name: "quota_usage_check", autopilot: true, summary: "Quota and current usage of the given resource types in one region."},
		},
	},
	{
		name: "keyvault", title: "Azure Key Vault", family: azureFamilyKeyVault,
		purpose: "Key and certificate metadata and expiry (CSI Secrets Store, expiring certificates). Secret values are never readable through KubeBolt.",
		commands: []azureCommand{
			{name: "keyvault_certificate_get", summary: "List a vault's certificates, or show one: thumbprint, expiry, policy."},
			{name: "keyvault_key_get", summary: "List a vault's keys, or show one: type, enabled, expiry."},
		},
	},
	{
		name: "storage", title: "Azure Storage", family: azureFamilyStorage,
		purpose: "Account settings and container properties; never blob or table contents.",
		commands: []azureCommand{
			{name: "storage_account_get", lists: true, summary: "List the storage accounts, or show one: SKU, kind, HTTPS-only, public access, hierarchical namespace."},
			{name: "storage_blob_container_get", summary: "List an account's blob containers, or show one: public access, lease, last modified."},
		},
	},
	{
		name: "advisor", title: "Azure Advisor", family: azureFamilyCost,
		purpose: "Recommendations on cost, reliability, performance and security.",
		commands: []azureCommand{
			{name: "advisor_recommendation_list", idFilters: []string{"resource"}, summary: "Recommendation records, filterable by category, impact, resource type or resource."},
			{name: "advisor_recommendation_summary", idFilters: []string{"resource"}, summary: "Counts and breakdowns of the recommendations (by category, impact, type)."},
		},
	},
	{
		name: "optimization", title: "Azure cost optimization", family: azureFamilyCost,
		purpose: "Cost-saving recommendations ranked by annual savings.",
		commands: []azureCommand{
			{name: "optimization_recommendation_explain", summary: "Explain one right-size recommendation for a VM or scale set (resource-id), with its utilization."},
			{name: "optimization_recommendation_list", summary: "Cost-saving recommendations ranked by impact and annual savings."},
		},
	},
}

// azureServerDown is how a flexible server router answers "is the server
// down?", which its own commands cannot: in the reviewed version they read
// the configuration (version, SKU, storage, backup), never the state or the
// firewall, and the list gives names alone. metrics names the server's
// liveness and connection metrics. The two calls it names need arguments a
// model does not guess: the activity log's default top of 10 cuts the
// entries before a stop, and a metrics query over the default 24 h window
// hides the stop at an hourly interval and is refused at a 5-minute one,
// which makes 288 buckets where max-buckets defaults to 50.
func azureServerDown(metrics string) string {
	return "Whether the server is running is not here. For \"is it down?\", call monitor_activitylog_list with just the server name and top 20 " +
		"(stop and start, failover, firewall and network changes, who made them and when; no resource group needed; the default top of 10 cuts older entries), " +
		"then monitor_metrics_query " + metrics + " at interval PT5M with start-time one or two hours back, " +
		"or over a longer window with max-buckets raised (it defaults to 50; 24 h at PT5M is 288): a stopped server reads 0 or has no data. " +
		"Then resourcehealth availability once the subscription has registered " +
		"Microsoft.ResourceHealth (409 until then; its events also appear in the activity log)."
}

// azureDirectTools are the inventory tools the sidecar exposes flat. They
// anchor an external dependency (name, resource group, type) before asking
// about it.
var azureDirectTools = []azureDirectTool{
	{name: "group_list", namespace: "group", family: azureFamilyInventory, autopilot: true, summary: "List the resource groups of the connected subscription (name, id, location)."},
	{name: "group_resource_list", namespace: "group", family: azureFamilyInventory, autopilot: true, summary: "List the resources of one resource group (name, id, type, location): the way to find the database, cache or queue a workload depends on."},
	{name: "subscription_list", namespace: "subscription", family: azureFamilyInventory, summary: "List the subscriptions the connector's identity can see (id, name, state, tenant). Every other tool reads only the subscription KubeBolt is set to."},
}

// azureFamilies is what the admin approves, in display order. The Kobi and
// Autopilot flags are derived from the tables above, not written by hand.
var azureFamilies = []struct{ label, detail string }{
	{azureFamilyAKS, "Cluster and node pool state: version, networking, identity, pool size, autoscaling and provisioning state."},
	{azureFamilyMonitor, "Azure Monitor metrics for any resource, KQL over a Log Analytics workspace or one resource's logs, and the activity log of what changed. Kobi also lists workspaces and tables."},
	{azureFamilyHealth, "Resource Health availability, service health events, and AppLens diagnosis of a resource."},
	{azureFamilyData, "Azure SQL servers, databases and firewall rules (Kobi also elastic pools and Entra admins); PostgreSQL and MySQL flexible server configuration and parameters; the Cosmos DB and Redis inventory. Server level only."},
	{azureFamilyMessaging, "Service Bus queue, topic and subscription runtime state (backlog, dead-letter); Event Hubs namespaces, hubs and consumer groups."},
	{azureFamilyRegistry, "Container registries and their repositories; role assignments in the subscription, such as who may pull from a registry."},
	{azureFamilyInventory, "Resource groups and their resources (Kobi also the subscription list); quota usage and region availability."},
	{azureFamilyKeyVault, "Key and certificate properties and expiry. Never a secret value."},
	{azureFamilyStorage, "Storage account settings and blob container properties. Never blob or table contents."},
	{azureFamilyCost, "Azure Advisor recommendations and cost-optimization explanations."},
}

// azureDeniedCommands are refused even if a future edit put them in the
// allowlist. --read-only on the server does not remove them: they are
// annotated read-only but return secret values, data-plane rows or
// infrastructure rewrites.
var azureDeniedCommands = map[string]bool{
	"keyvault_secret_get":                           true,
	"keyvault_admin_settings_get":                   true,
	"storage_blob_get":                              true,
	"storage_table_list":                            true,
	"postgres_table_schema_get":                     true,
	"mysql_table_schema_get":                        true,
	"postgres_database_query":                       true,
	"mysql_database_query":                          true,
	"advisor_recommendation_apply":                  true,
	"advisor_remediation_get":                       true,
	"monitor_instrumentation_get-learning-resource": true,
}

// azureDeniedFragments catch whole families of data-plane reads by name, for
// the day the server adds one: Cosmos items and schema sampling, secrets,
// table schemas behind a database login.
var azureDeniedFragments = []string{"secret", "_item_", "_database_container_", "table_schema", "database_query"}

// azureWriteVerbs end the name of every mutating command the server has.
var azureWriteVerbs = []string{"create", "delete", "update", "set", "apply", "import", "upload", "rename", "start", "next", "send", "purge", "restore", "reset"}

// azureCommandDenied is the fixed deny list, applied before the allowlist.
func azureCommandDenied(cmd string) bool {
	if azureDeniedCommands[cmd] {
		return true
	}
	for _, f := range azureDeniedFragments {
		if strings.Contains(cmd, f) {
			return true
		}
	}
	verb := cmd
	if i := strings.LastIndexByte(cmd, '_'); i >= 0 {
		verb = cmd[i+1:]
	}
	for _, v := range azureWriteVerbs {
		if strings.Contains(verb, v) {
			return true
		}
	}
	return false
}

// azureIndex resolves a command name to its table entry.
var azureIndex = sync.OnceValue(func() map[string]*azureCommand {
	idx := make(map[string]*azureCommand)
	for i := range azureNamespaces {
		ns := &azureNamespaces[i]
		for j := range ns.commands {
			idx[ns.commands[j].name] = &ns.commands[j]
		}
	}
	return idx
})

// azureSidecarFlags are the options the sidecar is started with, before its
// --namespace list. Read-only, with the proxy tools (which would run any
// command by name) off, and response caching off: during an incident "has
// it recovered?" must not read a cached answer. Incoming auth is off, which
// the server only allows on loopback. Mirrors the dev-azmcp recipe in
// Makefile.ee, the chart's azure-mcp container and the compose file's
// azure-mcp service; tests compare them.
var azureSidecarFlags = []string{
	"--transport", "http", "--mode", "namespace",
	"--read-only", "--disable-proxy-tools", "--disable-caching", "--dangerously-disable-http-incoming-auth",
}

// azureSidecarNamespaces is the --namespace list the sidecar must be started
// with: every router in the allowlist plus the namespaces of the direct
// tools. Mirrors AZMCP_NAMESPACES in Makefile.ee.
func azureSidecarNamespaces() []string {
	seen := map[string]bool{}
	var out []string
	for _, ns := range azureNamespaces {
		if !seen[ns.name] {
			seen[ns.name] = true
			out = append(out, ns.name)
		}
	}
	for _, d := range azureDirectTools {
		if !seen[d.namespace] {
			seen[d.namespace] = true
			out = append(out, d.namespace)
		}
	}
	sort.Strings(out)
	return out
}

// The Enterprise build lists Azure as coming soon (azure_offer_ee.go): its
// only mode is the sidecar, and one sidecar holds one identity per instance.
func init() {
	if azureOffered {
		register(newAzureDef)
	} else {
		registerPreview(newAzureDef)
	}
}

func newAzureDef() *ConnectorDef {
	return &ConnectorDef{
		ID:       azureConnectorID,
		Name:     "Microsoft Azure",
		Category: CategoryCloud,
		Description: "Lets Kobi read the Azure resources your workloads depend on: AKS, Azure Monitor metrics and logs, " +
			"Resource Health, SQL, PostgreSQL and MySQL servers, Service Bus, Event Hubs and Container Registry. " +
			"KubeBolt talks to the Azure MCP Server that runs next to it; the Azure identity, a read-only service principal, " +
			"lives in that server and never in KubeBolt. Because one server holds one identity, this connector is only offered " +
			"on single-tenant installs. KubeBolt lets through only a reviewed list of read-only commands and pins every call " +
			"to the subscription you set here.",
		Tags:    []string{"AKS", "Azure Monitor", "Resource Health", "Azure SQL", "PostgreSQL", "Service Bus"},
		DocsURL: "https://learn.microsoft.com/azure/developer/azure-mcp-server/overview",
		MinPlan: "team",
		AuthModes: []AuthMode{{
			ID:    azureModeSidecar,
			Label: "Azure MCP Server sidecar",
			Description: "The Azure MCP Server runs next to KubeBolt with a read-only service principal. " +
				"KubeBolt only needs the subscription to read; there is nothing secret to store here.",
			SelfHostedOnly: true,
			Fields: []FieldSchema{
				{
					Name: "subscriptionId", Label: "Subscription ID", Required: true,
					Help:        "The subscription KubeBolt reads. Every call is pinned to it.",
					Placeholder: "00000000-0000-0000-0000-000000000000",
					Pattern:     azureGUIDPattern, MaxLen: 36,
				},
				{
					Name: "tenantId", Label: "Tenant ID", Advanced: true,
					Help:        "Only when the service principal can see more than one Microsoft Entra directory. Leave empty to use its home tenant.",
					Placeholder: "00000000-0000-0000-0000-000000000000",
					Pattern:     azureGUIDPattern, MaxLen: 36,
				},
			},
		}},
		Families: azureFamilyList(),
		Never: []string{
			"Read Key Vault secret values or vault settings",
			"Read database rows, Cosmos DB items, blobs, table contents or table schemas",
			"Log in to a database: user names, passwords and key-based auth are removed from every request",
			"Create, change or delete anything in Azure: the server runs read-only and KubeBolt lets through only the commands listed here",
			"Apply Advisor recommendations or fetch remediation scripts",
			"Read a subscription other than the one set here",
		},
		Guide:  azureGuide,
		Binder: azureBinder{},
	}
}

// azureFamilyList derives each family's consumer flags from the allowlist,
// so the drawer can never promise Autopilot a family it cannot read.
func azureFamilyList() []Family {
	kobi := map[string]bool{}
	auto := map[string]bool{}
	for _, ns := range azureNamespaces {
		for _, c := range ns.commands {
			kobi[ns.family] = true
			auto[ns.family] = auto[ns.family] || c.autopilot
		}
	}
	for _, d := range azureDirectTools {
		kobi[d.family] = true
		auto[d.family] = auto[d.family] || d.autopilot
	}
	out := make([]Family, 0, len(azureFamilies))
	for _, f := range azureFamilies {
		if !kobi[f.label] {
			continue
		}
		out = append(out, Family{Label: f.label, Detail: f.detail, Kobi: true, Autopilot: auto[f.label]})
	}
	return out
}

// azureGuide renders the setup manual. It prints the org's subscription when
// one is stored and the sidecar address this install dials.
func azureGuide(env Env, fields map[string]string) SetupGuide {
	sub := strings.TrimSpace(fields["subscriptionId"])
	if !azureGUIDRE.MatchString(sub) {
		sub = "<subscription-id>"
	}
	listen := "http://127.0.0.1:5001"
	endpoint := env.AzmcpURL
	if endpoint == "" {
		endpoint = DefaultAzmcpURL
	}
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		listen = u.Scheme + "://" + u.Host
	}

	var ns strings.Builder
	for _, n := range azureSidecarNamespaces() {
		ns.WriteString(" \\\n  --namespace " + n)
	}

	// Every value is written once, on a declaration line at the top of the
	// block that uses it (SUB=..., export AZURE_...=...), and the commands
	// read the variable: the blocks run in different shells (the owner who
	// registers Resource Health, the host that runs the sidecar), so each one
	// declares what it needs.
	return SetupGuide{Steps: []SetupStep{
		{
			Title: "Create a read-only service principal",
			Body: "Create a service principal with Reader, Monitoring Reader and Log Analytics Reader on the subscription. " +
				"Keep the appId, password and tenant it prints: they go into the Azure MCP Server's environment, never into KubeBolt.",
			Language: "shell",
			Code: "SUB=" + sub + "\n" +
				"az ad sp create-for-rbac --name kubebolt-azure-reader --role Reader --scopes /subscriptions/$SUB\n" +
				"APP_ID=<appId from the output>\n" +
				"az role assignment create --assignee $APP_ID --role \"Monitoring Reader\" --scope /subscriptions/$SUB\n" +
				"az role assignment create --assignee $APP_ID --role \"Log Analytics Reader\" --scope /subscriptions/$SUB",
		},
		{
			Title: "Optional: messaging and Key Vault metadata",
			Body: "Service Bus queue and topic details are data-plane reads: add Azure Service Bus Data Receiver on the namespaces Kobi should inspect. " +
				"For key and certificate expiry add Key Vault Reader. Never grant Key Vault Secrets User: KubeBolt does not read secret values and the identity should not be able to either. " +
				"Run it in the shell of the first step: it uses its `APP_ID`.",
			Language: "shell",
			Code: "SERVICE_BUS_NAMESPACE_ID=<service-bus-namespace-resource-id>\n" +
				"KEY_VAULT_ID=<key-vault-resource-id>\n" +
				"az role assignment create --assignee $APP_ID --role \"Azure Service Bus Data Receiver\" --scope $SERVICE_BUS_NAMESPACE_ID\n" +
				"az role assignment create --assignee $APP_ID --role \"Key Vault Reader\" --scope $KEY_VAULT_ID",
		},
		{
			Title: "Recommended: register the Resource Health provider",
			Body: "Resource Health says whether Azure itself reports a resource available or degraded. It answers only in a subscription that has registered its resource provider. " +
				"Registering turns the provider's API on in the subscription. It creates no resource and gives none of your identities new permissions: their role assignments are unchanged. " +
				"Microsoft adds the provider's app to your Microsoft Entra tenant, and advises registering a provider only when you use it. Resource Health is free. " +
				"It needs the `Microsoft.ResourceHealth/register/action` permission, which Owner and Contributor include and Reader does not, so the read-only service principal cannot do it: run this once as an owner or contributor of the subscription. " +
				"The second command prints the registration state. It can take about ten minutes to settle: until then Resource Health may still answer that the provider is not registered, " +
				"and Kobi still reads a resource's health events from its activity log.",
			Language: "shell",
			Code: "SUB=" + sub + "\n" +
				"az provider register --namespace Microsoft.ResourceHealth --subscription $SUB\n" +
				"az provider show --namespace Microsoft.ResourceHealth --subscription $SUB --query registrationState -o tsv",
		},
		{
			Title: "Run the Azure MCP Server next to KubeBolt",
			Body: "The server holds the service principal. On Kubernetes the KubeBolt Helm chart runs it as a second container of the API pod: " +
				"it reads the service principal from a Secret only that container is given, listens on the pod's loopback with its own sign-in turned off, " +
				"and runs read-only with response caching off, so an answer during an incident is never a stale one. " +
				"Run this where kubectl and helm reach the cluster KubeBolt runs in, with the release name and namespace of your install.",
			Language: "shell",
			Code: "RELEASE=kubebolt\n" +
				"NAMESPACE=kubebolt\n" +
				"read -rs AZURE_CLIENT_SECRET   # paste the password from the first step; it stays out of your shell history\n" +
				"kubectl create secret generic kubebolt-azure-reader -n $NAMESPACE \\\n" +
				"  --from-literal=tenant-id=<tenant from the first step> \\\n" +
				"  --from-literal=client-id=<appId from the first step> \\\n" +
				"  --from-literal=client-secret=\"$AZURE_CLIENT_SECRET\"\n" +
				"helm upgrade $RELEASE oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt -n $NAMESPACE --reuse-values \\\n" +
				"  --set mcpConnectors.azure.sidecar.enabled=true \\\n" +
				"  --set mcpConnectors.azure.sidecar.existingSecret=kubebolt-azure-reader",
		},
		{
			Title: "Or run it yourself",
			Body: "Without the Helm chart, run the server where the KubeBolt API runs: on the same host, or in a container that shares the API's network " +
				"(with Docker Compose, `docker compose --profile azure up -d` and the three `AZURE_` values in `deploy/.env`). " +
				"It listens on loopback only, with its own sign-in turned off, so nothing outside can reach it. " +
				"KubeBolt reaches it at " + endpoint + "; set `KUBEBOLT_MCP_AZMCP_URL` on the API if it listens elsewhere.",
			Language: "shell",
			Code: "export AZURE_TENANT_ID=<tenant from the first step>\n" +
				"export AZURE_CLIENT_ID=<appId from the first step>\n" +
				"read -rs AZURE_CLIENT_SECRET && export AZURE_CLIENT_SECRET   # paste the password from the first step; it stays out of your shell history\n" +
				"export AZURE_SUBSCRIPTION_ID=" + sub + "\n" +
				"export AZURE_TOKEN_CREDENTIALS=EnvironmentCredential   # use the service principal, never a developer's az login\n" +
				"export AZURE_MCP_COLLECT_TELEMETRY=false\n" +
				"export ASPNETCORE_URLS=" + listen + "\n" +
				"export AZURE_MCP_DANGEROUSLY_DISABLE_HTTPS_REDIRECTION=true\n" +
				"npx -y @azure/mcp@" + azmcpVersion + " server start " + strings.Join(azureSidecarFlags[:4], " ") + " \\\n" +
				"  " + strings.Join(azureSidecarFlags[4:], " ") + ns.String(),
		},
		{
			Title: "Set the subscription here and test",
			Body: "Enter the subscription ID (and the tenant ID only if the service principal sees several directories), then run Test. " +
				"The test lists the subscriptions the server's identity can see and checks that this one is among them.",
		},
	}}
}

// azureCheckRoleScope keeps role_assignment_list inside the subscription and,
// for Autopilot, on a container registry: the only question it needs RBAC
// for is whether the kubelet identity may pull.
func azureCheckRoleScope(c Consumer, subscription string, params map[string]json.RawMessage) string {
	raw, ok := params["scope"]
	if !ok {
		if c == ConsumerAutopilot {
			return "role_assignment_list needs scope set to a container registry resource id (/subscriptions/<id>/resourceGroups/<rg>/providers/Microsoft.ContainerRegistry/registries/<name>)."
		}
		return ""
	}
	var scope string
	if json.Unmarshal(raw, &scope) != nil {
		return "scope must be a string."
	}
	scope = azureTrimSlash(strings.TrimSpace(scope))
	if !azureIDInSubscription(scope, subscription) {
		if c == ConsumerAutopilot {
			return "For Autopilot, role_assignment_list only accepts a container registry resource id in the connected subscription as scope."
		}
		return "scope must be the connected subscription or a resource inside it (/subscriptions/<id>/...), with no empty, \".\" or \"..\" segments."
	}
	if c == ConsumerAutopilot && !azureRegistryScopeRE.MatchString(scope) {
		return "For Autopilot, role_assignment_list only accepts a container registry resource id in the connected subscription as scope."
	}
	return ""
}

var azureRegistryScopeRE = regexp.MustCompile(`(?i)^/subscriptions/[^/]+/resourcegroups/[^/]+/providers/microsoft\.containerregistry/registries/[^/]+$`)

// azureServiceBusSuffixes are the host names of Service Bus namespaces in
// the public and sovereign clouds.
var azureServiceBusSuffixes = []string{".servicebus.windows.net", ".servicebus.usgovcloudapi.net", ".servicebus.chinacloudapi.cn"}

// azureServiceBusNameRE is a namespace name: 6 to 50 letters, digits and
// hyphens, starting with a letter and ending with a letter or digit.
var azureServiceBusNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{4,48}[a-z0-9]$`)

// azureCheckServiceBusHost keeps the Service Bus commands on Service Bus.
// Their namespace is a host name the sidecar connects to directly, outside
// ARM, and it presents its Azure token there: any other host would receive
// a token that reads the queues the identity can read.
func azureCheckServiceBusHost(_ Consumer, _ string, params map[string]json.RawMessage) string {
	raw, ok := params["namespace"]
	if !ok {
		return ""
	}
	var ns string
	if json.Unmarshal(raw, &ns) != nil {
		return "namespace must be a string."
	}
	host := strings.ToLower(strings.TrimSpace(ns))
	for _, suffix := range azureServiceBusSuffixes {
		if name, ok := strings.CutSuffix(host, suffix); ok && azureServiceBusNameRE.MatchString(name) {
			return ""
		}
	}
	return "namespace must be a Service Bus host name such as <name>.servicebus.windows.net: the server connects to it with its Azure identity."
}
