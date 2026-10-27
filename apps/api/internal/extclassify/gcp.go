package extclassify

import (
	"regexp"
	"strings"
)

// gcpRegionRE matches a Google Cloud region (us-central1, europe-west4,
// northamerica-northeast1); gcpZoneRE a zone in one (us-central1-a).
var (
	gcpRegionRE = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)
	gcpZoneRE   = regexp.MustCompile(`^([a-z]+-[a-z]+[0-9]+)-[a-z]$`)
	// "<region>-<service>" regional API hosts (us-central1-aiplatform).
	gcpRegionPrefixRE = regexp.MustCompile(`^([a-z]+-[a-z]+[0-9]+)-([a-z0-9-]+)$`)
	// Cloud SQL instance connection name, "project:region:instance"; the
	// project may be domain-scoped ("example.com:my-project").
	cloudSQLConnNameRE = regexp.MustCompile(`^((?:[a-z0-9.-]+:)?[a-z][a-z0-9-]{4,29}):([a-z]+-[a-z]+[0-9]+):([a-z0-9][a-z0-9-]{0,97})$`)
)

func gcpRegion(label string) string {
	if gcpRegionRE.MatchString(label) {
		return label
	}
	if m := gcpZoneRE.FindStringSubmatch(label); m != nil {
		return m[1]
	}
	return ""
}

// GCP first tools are "<service>_<tool>" local names, where service is the
// googleapis host label of the MCP endpoint.
var (
	gcpToolsCloudSQL    = []string{"sqladmin_list_instances", "sqladmin_get_instance", "monitoring_list_timeseries"}
	gcpToolsCloudSQLKey = []string{"sqladmin_get_instance", "monitoring_list_timeseries", "databaseinsights_get_query_metrics"}
	gcpToolsAlloyDB     = []string{"alloydb_list_instances", "alloydb_get_instance", "monitoring_list_timeseries"}
	gcpToolsMemorystore = []string{"redis_list_instances", "memorystore_list_instances", "monitoring_list_timeseries"}
	gcpToolsPubSub      = []string{"pubsub_list_subscriptions", "pubsub_get_subscription", "monitoring_list_timeseries"}
	gcpToolsTelemetry   = []string{"monitoring_list_timeseries", "logging_list_log_entries"}
	gcpToolsLogs        = []string{"logging_list_log_entries", "monitoring_list_timeseries"}
	gcpToolsVM          = []string{"compute_list_instances", "compute_get_instance_basic_info"}
	gcpToolsInventory   = []string{"cloudasset_list_assets"}
)

// gcpPlumbing are Google API services cluster plumbing talks to: token
// exchange for Workload Identity, the GKE and Compute control planes used by
// add-ons (CSI, autoscaler, ingress).
var gcpPlumbing = map[string]string{
	"oauth2":               "identity",
	"sts":                  "identity",
	"iamcredentials":       "identity",
	"accounts":             "identity",
	"www":                  "identity",
	"container":            "gke-control-plane",
	"compute":              "compute",
	"cloudresourcemanager": "resourcemanager",
}

// gcpDatabases maps data-plane API hosts to a service name. KubeBolt never
// reads their data; their health is in Monitoring and Logging.
var gcpDatabases = map[string]string{
	"spanner": "spanner", "bigtable": "bigtable", "bigtableadmin": "bigtable",
	"bigquery": "bigquery", "bigquerystorage": "bigquery",
	"firestore": "firestore", "datastore": "firestore",
}

// gcpTelemetry are export endpoints for logs, metrics and traces.
var gcpTelemetry = map[string]bool{
	"logging": true, "monitoring": true, "cloudtrace": true,
	"clouderrorreporting": true, "cloudprofiler": true, "telemetry": true,
}

// classifyCloudSQLConnName recognises a Cloud SQL instance connection name
// ("project:region:instance"), which is what Cloud SQL connectors and the
// Auth Proxy are configured with instead of a hostname. It names the instance
// exactly, so it is the best anchor a GKE workload's config can give.
func classifyCloudSQLConnName(host string) (Classification, bool) {
	m := cloudSQLConnNameRE.FindStringSubmatch(host)
	if m == nil {
		return Classification{}, false
	}
	return fqdnClass(ProviderGCP, "cloudsql", KindDatabase, m[3], m[2], gcpToolsCloudSQLKey,
		"Cloud SQL instance connection name (the hint is the instance, in project "+m[1]+")."), true
}

func classifyGCP(host string) (Classification, bool) {
	if l, ok := trimSuffixLabels(host, "googleapis.com"); ok {
		return gcpGoogleAPIs(l), true
	}
	if l, ok := trimSuffixLabels(host, "sql.goog"); ok && len(l) > 0 {
		return fqdnClass(ProviderGCP, "cloudsql", KindDatabase, "", gcpRegion(l[len(l)-1]), gcpToolsCloudSQL,
			"Cloud SQL instance DNS name (Private Service Connect / private DNS); it does not carry the instance name: list the instances and match their dnsName."), true
	}
	if l, ok := trimSuffixLabels(host, "gke.goog"); ok && len(l) > 0 {
		return plumbing(ProviderGCP, "gke-control-plane", gcpRegion(l[len(l)-1]),
			"The GKE API server (DNS-based control-plane endpoint): cluster plumbing, not a dependency."), true
	}
	if l, ok := trimSuffixLabels(host, "pkg.dev"); ok && len(l) == 1 {
		region := ""
		if i := strings.LastIndexByte(l[0], '-'); i > 0 {
			region = l[0][:i]
		}
		return fqdnClass(ProviderGCP, "artifact-registry", KindRegistry, "", region, gcpToolsInventory,
			"Artifact Registry: the repository is in the image path, not the hostname."), true
	}
	if host == "gcr.io" || strings.HasSuffix(host, ".gcr.io") {
		return fqdnClass(ProviderGCP, "artifact-registry", KindRegistry, "", "", gcpToolsInventory,
			"Container Registry (gcr.io, served by Artifact Registry): the project is in the image path."), true
	}
	if l, ok := trimSuffixLabels(host, "run.app"); ok && len(l) > 0 {
		region := ""
		if len(l) >= 2 {
			region = gcpRegion(l[1])
		}
		return fqdnClass(ProviderGCP, "cloudrun", KindPlatform, l[0], region, gcpToolsLogs,
			"Cloud Run service URL (the hint is the URL's first label, which starts with the service name)."), true
	}
	if l, ok := trimSuffixLabels(host, "cloudfunctions.net"); ok && len(l) == 1 {
		region := ""
		if m := gcpRegionPrefixRE.FindStringSubmatch(l[0]); m != nil {
			region = m[1]
		}
		return fqdnClass(ProviderGCP, "cloudfunctions", KindPlatform, "", region, gcpToolsLogs,
			"Cloud Functions endpoint: the function name is in the path."), true
	}
	// GCE internal DNS: <vm>.<zone>.c.<project>.internal or <vm>.c.<project>.internal
	if l, ok := trimSuffixLabels(host, "internal"); ok && len(l) >= 3 && l[len(l)-2] == "c" {
		region := ""
		if len(l) == 4 {
			region = gcpRegion(l[1])
		}
		return fqdnClass(ProviderGCP, "compute", KindUnknown, l[0], region, gcpToolsVM,
			"A Compute Engine VM's internal DNS name (the hint is the VM name): find the VM and what runs on it."), true
	}
	return Classification{}, false
}

// gcpGoogleAPIs classifies *.googleapis.com: global (<svc>), regional
// (<svc>.<region>.rep, <region>-<svc>), bucket-hosted storage
// (<bucket>.storage), Private Service Connect aliases (<svc>-<endpoint>.p)
// and Cloud SQL PSC (*.psc).
func gcpGoogleAPIs(l []string) Classification {
	n := len(l)
	if n == 0 {
		return plumbing(ProviderGCP, "googleapis", "", "The Google APIs apex domain.")
	}
	svc, region, hint := l[0], "", ""
	switch {
	case l[n-1] == "psc":
		return fqdnClass(ProviderGCP, "cloudsql", KindDatabase, "", "", gcpToolsCloudSQL,
			"Cloud SQL through Private Service Connect: list the instances and match the endpoint.")
	case n == 2 && l[1] == "p":
		// <svc>-<endpoint>.p.googleapis.com
		if i := strings.IndexByte(l[0], '-'); i > 0 {
			svc = l[0][:i]
		}
	case n == 2 && l[1] == "storage":
		svc, hint = "storage", l[0]
	case n == 3 && l[2] == "rep":
		region = gcpRegion(l[1])
	case n == 1:
		if m := gcpRegionPrefixRE.FindStringSubmatch(svc); m != nil {
			region, svc = m[1], m[2]
		}
	default:
		return fqdnClass(ProviderGCP, "googleapis", KindPlatform, "", "", gcpToolsInventory,
			"A Google API endpoint whose service KubeBolt does not recognise.")
	}

	switch svc {
	case "sqladmin":
		return fqdnClass(ProviderGCP, "cloudsql", KindDatabase, "", region, gcpToolsCloudSQL,
			"Cloud SQL Admin API, used by the Cloud SQL connectors and Auth Proxy: the instance itself is reached by IP on port 3307, and its name (project:region:instance) is in the workload's configuration.")
	case "alloydb":
		return fqdnClass(ProviderGCP, "alloydb", KindDatabase, "", region, gcpToolsAlloyDB,
			"AlloyDB API, used by the AlloyDB connectors and Auth Proxy: the instance is in the workload's configuration.")
	case "redis", "memorystore":
		return fqdnClass(ProviderGCP, "memorystore", KindCache, "", region, gcpToolsMemorystore,
			"Memorystore API: the instance itself is reached by private IP.")
	case "pubsub":
		return fqdnClass(ProviderGCP, "pubsub", KindQueue, "", region, gcpToolsPubSub,
			"Pub/Sub API: the topic and subscription are in the workload's configuration.")
	case "storage":
		note := "Cloud Storage: the bucket is in the request path, not the hostname."
		if hint != "" {
			note = "Cloud Storage bucket (the hint is the bucket name)."
		}
		return fqdnClass(ProviderGCP, "storage", KindStorage, hint, region, gcpToolsTelemetry, note)
	}
	if name, ok := gcpDatabases[svc]; ok {
		return fqdnClass(ProviderGCP, name, KindDatabase, "", region, gcpToolsTelemetry,
			"Google "+name+" API: the instance is not in the hostname; its health is in Monitoring and Logging.")
	}
	if name, ok := gcpPlumbing[svc]; ok {
		return plumbing(ProviderGCP, name, region,
			"Google identity or control-plane API, typically called for Workload Identity or by a cluster add-on: plumbing, not a dependency.")
	}
	if gcpTelemetry[svc] {
		return fqdnClass(ProviderGCP, svc, KindPlatform, "", region, nil, "Google Cloud telemetry API (logs, metrics or traces export).")
	}
	return fqdnClass(ProviderGCP, "googleapis", KindPlatform, svc, region, gcpToolsInventory,
		"Google "+svc+" API (the hint is the API name): the resource is not in the hostname; list the project's assets to find it.")
}
