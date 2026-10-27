package extclassify

import (
	"regexp"
	"strings"
)

// azureRegionRE matches an Azure region name as it appears in endpoints
// ("eastus", "eastus2", "westeurope", "usgovvirginia").
var azureRegionRE = regexp.MustCompile(`^[a-z]{4,}[0-9]?$`)

func azureRegion(label string) string {
	switch label {
	case "hcp", "privatelink", "portal", "data", "public":
		return ""
	}
	if azureRegionRE.MatchString(label) {
		return label
	}
	return ""
}

// Azure first tools are the connector's router names (one per azmcp
// namespace) plus its direct inventory tools.
var (
	azureToolsSQL     = []string{"monitor", "sql", "resourcehealth", "applens"}
	azureToolsSQLMI   = []string{"monitor", "resourcehealth", "group_resource_list"}
	azureToolsPG      = []string{"monitor", "postgres", "resourcehealth"}
	azureToolsMySQL   = []string{"monitor", "mysql", "resourcehealth"}
	azureToolsRedis   = []string{"redis", "monitor", "resourcehealth"}
	azureToolsBus     = []string{"servicebus", "eventhubs", "monitor"}
	azureToolsCosmos  = []string{"cosmos", "monitor", "resourcehealth"}
	azureToolsStorage = []string{"storage", "monitor", "resourcehealth"}
	azureToolsACR     = []string{"acr", "role"}
	azureToolsVault   = []string{"keyvault", "monitor"}
	azureToolsApp     = []string{"applens", "monitor", "resourcehealth"}
	azureToolsHealth  = []string{"monitor", "resourcehealth"}
	azureToolsInv     = []string{"group_resource_list", "resourcehealth"}
)

// azureDatabaseDown is the path to "is this database down?" for an Azure
// database server. The hostname gives the server's name and not its resource
// group, which the sql, postgres and mysql commands require; the activity log
// takes the name alone and each entry carries the resource id, group
// included. Its default top of 10 cuts the entries before a stop, so the
// note asks for more. then is what to read once the group is known.
func azureDatabaseDown(then string) string {
	return "Is it down? Start with monitor_activitylog_list on the server name and top 20 (the default top of 10 cuts older entries): " +
		"stop and start, failover, firewall and network changes, who made them and when. " +
		"It needs no resource group, and its entries carry the resource id, with the group the other commands need. " +
		then + " Never guess a resource group."
}

// azureLivenessWindow is how to read a server's liveness metric so that it
// shows a stop: an hourly interval hides it, and the default 24 h window at
// 5 minutes is 288 buckets, which the query refuses unless max-buckets
// (default 50) is raised.
const azureLivenessWindow = " at interval PT5M with start-time one or two hours back, " +
	"or over a longer window with max-buckets raised (it defaults to 50; 24 h at PT5M is 288): a stopped server reads 0 or has no data."

// azureSuffix is one Azure endpoint domain. match receives the labels before
// the suffix (at least one) and returns the classification. The resource name
// is always the first label: a Private Link alias ("<name>.privatelink.<zone>")
// keeps it in front.
type azureSuffix struct {
	suffix string
	match  func(l []string) Classification
}

func hasLabel(l []string, want string) bool {
	for _, lb := range l {
		if lb == want {
			return true
		}
	}
	return false
}

func azureSQL(l []string) Classification {
	rest := l[1:]
	if len(rest) > 0 && !(len(rest) == 1 && rest[0] == "privatelink") {
		// <mi>.<dns-zone>.database.windows.net (and "<mi>.public.<zone>…").
		return fqdnClass(ProviderAzure, "azure-sql", KindDatabase, l[0], "", azureToolsSQLMI,
			"Looks like an Azure SQL Managed Instance (the hint is the instance name); the sql tools cover logical servers, so start from its metrics and health.")
	}
	return fqdnClass(ProviderAzure, "azure-sql", KindDatabase, l[0], "", azureToolsSQL,
		"Azure SQL logical server (the hint is the server name). "+azureDatabaseDown(
			"Then sql_db_get for the database status and sql_server_firewall-rule_list for the firewall rules."))
}

var azureSuffixes = []azureSuffix{
	{"database.windows.net", azureSQL},
	{"database.usgovcloudapi.net", azureSQL},
	{"database.chinacloudapi.cn", azureSQL},
	{"postgres.database.azure.com", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "azure-postgres", KindDatabase, l[0], "", azureToolsPG,
			"Azure Database for PostgreSQL server (the hint is the server name). "+azureDatabaseDown(
				"Then monitor_metrics_query is_db_alive"+azureLivenessWindow+" postgres_server_config_get gives SKU, version and location (the server's region), not whether the server runs."))
	}},
	{"mysql.database.azure.com", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "azure-mysql", KindDatabase, l[0], "", azureToolsMySQL,
			"Azure Database for MySQL server (the hint is the server name). "+azureDatabaseDown(
				"Then monitor_metrics_query Uptime"+azureLivenessWindow+" mysql_server_config_get gives the server's configuration, not whether the server runs."))
	}},
	{"redis.cache.windows.net", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "azure-redis", KindCache, l[0], "", azureToolsRedis,
			"Azure Cache for Redis (the hint is the cache name).")
	}},
	// Azure Managed Redis / Redis Enterprise: <name>.<region>.redis.azure.net
	{"redis.azure.net", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "azure-redis", KindCache, l[0], azureRegion(l[len(l)-1]), azureToolsRedis,
			"Azure Managed Redis (the hint is the cache name).")
	}},
	{"redisenterprise.cache.azure.net", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "azure-redis", KindCache, l[0], azureRegion(l[len(l)-1]), azureToolsRedis,
			"Azure Cache for Redis Enterprise (the hint is the cache name).")
	}},
	{"servicebus.windows.net", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "servicebus", KindQueue, l[0], "", azureToolsBus,
			"Service Bus or Event Hubs namespace (they share this domain; the hint is the namespace): if servicebus finds nothing, try eventhubs.")
	}},
	{"documents.azure.com", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "cosmos", KindDatabase, l[0], "", azureToolsCosmos,
			"Cosmos DB account (the hint is the account name): throttling (429) shows in its metrics.")
	}},
	{"cosmos.azure.com", func(l []string) Classification {
		if hasLabel(l, "postgres") {
			return fqdnClass(ProviderAzure, "azure-postgres", KindDatabase, strings.TrimPrefix(l[0], "c-"), "", azureToolsHealth,
				"Azure Cosmos DB for PostgreSQL cluster (the hint is the cluster name).")
		}
		return fqdnClass(ProviderAzure, "cosmos", KindDatabase, l[0], "", azureToolsCosmos,
			"Cosmos DB account (the hint is the account name): throttling (429) shows in its metrics.")
	}},
	{"core.windows.net", azureStorage},
	{"core.usgovcloudapi.net", azureStorage},
	{"core.chinacloudapi.cn", azureStorage},
	{"azurecr.io", func(l []string) Classification {
		region := ""
		if len(l) == 3 && l[2] == "data" {
			region = azureRegion(l[1])
		}
		return fqdnClass(ProviderAzure, "acr", KindRegistry, l[0], region, azureToolsACR,
			"Azure Container Registry (the hint is the registry name): check the repository exists and the kubelet identity has AcrPull.")
	}},
	{"vault.azure.net", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "keyvault", KindPlatform, l[0], "", azureToolsVault,
			"Key Vault (the hint is the vault name): only key and certificate metadata can be read, never secrets.")
	}},
	{"managedhsm.azure.net", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "keyvault", KindPlatform, l[0], "", azureToolsHealth, "Key Vault Managed HSM.")
	}},
	{"azmk8s.io", func(l []string) Classification {
		return plumbing(ProviderAzure, "aks-control-plane", azureRegion(l[len(l)-1]),
			"The AKS API server (control plane): cluster plumbing, not a dependency.")
	}},
	// Public IP DNS label: <label>.<region>.cloudapp.azure.com
	{"cloudapp.azure.com", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "public-ip", KindLoadBalancer, l[0], azureRegion(l[len(l)-1]), azureToolsInv,
			"DNS label of an Azure public IP (a load balancer, application gateway or VM): find which resource holds it.")
	}},
	{"internal.cloudapp.net", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "vm", KindUnknown, l[0], "", azureToolsInv,
			"An Azure VM's internal DNS name (the hint is the VM name): find the VM and what runs on it.")
	}},
	{"azurewebsites.net", func(l []string) Classification {
		return fqdnClass(ProviderAzure, "app-service", KindPlatform, l[0], "", azureToolsApp,
			"Azure App Service app (the hint is the app name).")
	}},
}

func azureStorage(l []string) Classification {
	sub := l[len(l)-1]
	kind := KindStorage
	switch sub {
	case "queue":
		kind = KindQueue
	case "table":
		kind = KindDatabase
	}
	return fqdnClass(ProviderAzure, "storage", kind, l[0], "", azureToolsStorage,
		"Azure Storage account (the hint is the account name; this is its "+sub+" endpoint).")
}

// azurePlumbing are the identity and management endpoints every AKS cluster
// talks to (workload identity token exchange, ARM calls from add-ons).
var azurePlumbing = map[string]string{
	"login.microsoftonline.com": "entra-id",
	"login.windows.net":         "entra-id",
	"management.azure.com":      "arm",
}

func classifyAzure(host string) (Classification, bool) {
	if svc, ok := azurePlumbing[host]; ok {
		return plumbing(ProviderAzure, svc, "",
			"Azure identity or management API, typically called by a cluster add-on or for workload identity: plumbing, not a dependency."), true
	}
	for _, s := range azureSuffixes {
		if l, ok := trimSuffixLabels(host, s.suffix); ok && len(l) > 0 {
			return s.match(l), true
		}
	}
	for _, apex := range []string{"windows.net", "azure.com", "azure.net", "azurecr.io", "azmk8s.io"} {
		if _, ok := trimSuffixLabels(host, apex); ok {
			return fqdnClass(ProviderAzure, "", KindUnknown, "", "", nil, "An Azure endpoint whose service KubeBolt does not recognise."), true
		}
	}
	return Classification{}, false
}
