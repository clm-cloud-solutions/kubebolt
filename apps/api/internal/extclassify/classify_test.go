package extclassify

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

type want struct {
	provider, service, kind, hint, region string
	infra                                 bool
	confidence                            string
	firstTool                             string // "" = not checked
}

type classifyCase struct {
	name                 string
	fqdn, ip             string
	clusterProv, cluster string
	want                 want
}

func runCases(t *testing.T, cases []classifyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.fqdn, tc.ip, tc.clusterProv, tc.cluster)
			w := tc.want
			if got.Provider != w.provider || got.Service != w.service || got.Kind != w.kind ||
				got.ResourceHint != w.hint || got.Region != w.region ||
				got.Infrastructure != w.infra || got.Confidence != w.confidence {
				t.Errorf("Classify(%q, %q, %q, %q)\n got  provider=%q service=%q kind=%q hint=%q region=%q infra=%v confidence=%q\n want provider=%q service=%q kind=%q hint=%q region=%q infra=%v confidence=%q",
					tc.fqdn, tc.ip, tc.clusterProv, tc.cluster,
					got.Provider, got.Service, got.Kind, got.ResourceHint, got.Region, got.Infrastructure, got.Confidence,
					w.provider, w.service, w.kind, w.hint, w.region, w.infra, w.confidence)
			}
			if w.firstTool != "" && (len(got.FirstTools) == 0 || got.FirstTools[0] != w.firstTool) {
				t.Errorf("FirstTools = %v, want first %q", got.FirstTools, w.firstTool)
			}
		})
	}
}

func TestClassify_AWS(t *testing.T) {
	runCases(t, []classifyCase{
		{name: "rds instance", fqdn: "mydb.c9akciq32.us-east-1.rds.amazonaws.com",
			want: want{"aws", "rds", KindDatabase, "mydb", "us-east-1", false, ConfidenceFQDN, "call rds DescribeDBInstances"}},
		{name: "rds uppercase with root dot", fqdn: "  MyDB.C9AKCIQ32.US-EAST-1.RDS.AMAZONAWS.COM.  ",
			want: want{"aws", "rds", KindDatabase, "mydb", "us-east-1", false, ConfidenceFQDN, "call rds DescribeDBInstances"}},
		{name: "aurora writer", fqdn: "orders.cluster-c9akciq32.eu-west-1.rds.amazonaws.com",
			want: want{"aws", "rds", KindDatabase, "orders", "eu-west-1", false, ConfidenceFQDN, "call rds DescribeDBClusters"}},
		{name: "aurora reader", fqdn: "orders.cluster-ro-c9akciq32.eu-west-1.rds.amazonaws.com",
			want: want{"aws", "rds", KindDatabase, "orders", "eu-west-1", false, ConfidenceFQDN, "call rds DescribeDBClusters"}},
		{name: "aurora custom endpoint", fqdn: "analytics.cluster-custom-c9akciq32.eu-west-1.rds.amazonaws.com",
			want: want{"aws", "rds", KindDatabase, "analytics", "eu-west-1", false, ConfidenceFQDN, "call rds DescribeDBClusterEndpoints"}},
		{name: "rds proxy", fqdn: "orders-proxy.proxy-c9akciq32.us-west-2.rds.amazonaws.com",
			want: want{"aws", "rds", KindDatabase, "orders-proxy", "us-west-2", false, ConfidenceFQDN, "call rds DescribeDBProxies"}},
		{name: "rds proxy endpoint", fqdn: "ro-ep.endpoint.proxy-c9akciq32.us-west-2.rds.amazonaws.com",
			want: want{"aws", "rds", KindDatabase, "ro-ep", "us-west-2", false, ConfidenceFQDN, "call rds DescribeDBProxyEndpoints"}},
		{name: "rds with a stray port", fqdn: "mydb.c9akciq32.us-east-1.rds.amazonaws.com:5432",
			want: want{"aws", "rds", KindDatabase, "mydb", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "rds china partition", fqdn: "mydb.abc123.cn-north-1.rds.amazonaws.com.cn",
			want: want{"aws", "rds", KindDatabase, "mydb", "cn-north-1", false, ConfidenceFQDN, "call rds DescribeDBInstances"}},
		{name: "rds govcloud", fqdn: "mydb.abc123.us-gov-west-1.rds.amazonaws.com",
			want: want{"aws", "rds", KindDatabase, "mydb", "us-gov-west-1", false, ConfidenceFQDN, ""}},
		{name: "docdb cluster", fqdn: "docs.cluster-c9akciq32.us-east-1.docdb.amazonaws.com",
			want: want{"aws", "docdb", KindDatabase, "docs", "us-east-1", false, ConfidenceFQDN, "call rds DescribeDBClusters"}},
		{name: "neptune cluster", fqdn: "graph.cluster-c9akciq32.us-east-1.neptune.amazonaws.com",
			want: want{"aws", "neptune", KindDatabase, "graph", "us-east-1", false, ConfidenceFQDN, "call rds DescribeDBClusters"}},
		{name: "elasticache primary", fqdn: "master.sessions.abc123.use1.cache.amazonaws.com",
			want: want{"aws", "elasticache", KindCache, "sessions", "us-east-1", false, ConfidenceFQDN, "call elasticache DescribeReplicationGroups"}},
		{name: "elasticache node group", fqdn: "sessions.abc123.ng.0001.apse2.cache.amazonaws.com",
			want: want{"aws", "elasticache", KindCache, "sessions", "ap-southeast-2", false, ConfidenceFQDN, ""}},
		{name: "elasticache config endpoint", fqdn: "sessions.abc123.clustercfg.euc1.cache.amazonaws.com",
			want: want{"aws", "elasticache", KindCache, "sessions", "eu-central-1", false, ConfidenceFQDN, ""}},
		{name: "elasticache serverless", fqdn: "carts-x1y2z3.serverless.usw2.cache.amazonaws.com",
			want: want{"aws", "elasticache", KindCache, "carts", "us-west-2", false, ConfidenceFQDN, "call elasticache DescribeServerlessCaches"}},
		{name: "memorydb", fqdn: "clustercfg.rank.abc123.memorydb.us-east-1.amazonaws.com",
			want: want{"aws", "memorydb", KindCache, "rank", "us-east-1", false, ConfidenceFQDN, "call memorydb DescribeClusters"}},
		{name: "alb", fqdn: "k8s-shop-ingress-1234567890.us-east-1.elb.amazonaws.com",
			want: want{"aws", "elb", KindLoadBalancer, "k8s-shop-ingress", "us-east-1", false, ConfidenceFQDN, "call elbv2 DescribeLoadBalancers"}},
		{name: "internal dualstack alb", fqdn: "dualstack.internal-api-lb-987654321.eu-west-1.elb.amazonaws.com",
			want: want{"aws", "elb", KindLoadBalancer, "api-lb", "eu-west-1", false, ConfidenceFQDN, ""}},
		{name: "nlb", fqdn: "edge-nlb-0123456789abcdef.elb.us-east-2.amazonaws.com",
			want: want{"aws", "elb", KindLoadBalancer, "edge-nlb", "us-east-2", false, ConfidenceFQDN, ""}},
		{name: "eks control plane", fqdn: "0123456789abcdef0123456789abcdef.gr7.us-east-1.eks.amazonaws.com",
			want: want{"aws", "eks-control-plane", KindPlatform, "", "us-east-1", true, ConfidenceFQDN, ""}},
		{name: "eks oidc", fqdn: "oidc.eks.us-east-1.amazonaws.com",
			want: want{"aws", "eks-control-plane", KindPlatform, "", "us-east-1", true, ConfidenceFQDN, ""}},
		{name: "sqs regional", fqdn: "sqs.us-east-1.amazonaws.com",
			want: want{"aws", "sqs", KindQueue, "", "us-east-1", false, ConfidenceFQDN, "call sqs ListQueues"}},
		{name: "sqs legacy", fqdn: "eu-west-1.queue.amazonaws.com",
			want: want{"aws", "sqs", KindQueue, "", "eu-west-1", false, ConfidenceFQDN, ""}},
		{name: "sqs dualstack api.aws", fqdn: "sqs.us-east-1.api.aws",
			want: want{"aws", "sqs", KindQueue, "", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "sns", fqdn: "sns.us-east-1.amazonaws.com",
			want: want{"aws", "sns", KindQueue, "", "us-east-1", false, ConfidenceFQDN, "call sns GetTopicAttributes"}},
		{name: "dynamodb", fqdn: "dynamodb.ap-northeast-1.amazonaws.com",
			want: want{"aws", "dynamodb", KindDatabase, "", "ap-northeast-1", false, ConfidenceFQDN, "call dynamodb ListTables"}},
		{name: "s3 virtual hosted regional", fqdn: "media-assets.s3.us-east-1.amazonaws.com",
			want: want{"aws", "s3", KindStorage, "media-assets", "us-east-1", false, ConfidenceFQDN, "call s3 GetBucketLocation"}},
		{name: "s3 dotted bucket global", fqdn: "logs.example.com.s3.amazonaws.com",
			want: want{"aws", "s3", KindStorage, "logs.example.com", "", false, ConfidenceFQDN, ""}},
		{name: "s3 legacy dash region", fqdn: "backups.s3-us-west-2.amazonaws.com",
			want: want{"aws", "s3", KindStorage, "backups", "us-west-2", false, ConfidenceFQDN, ""}},
		{name: "s3 dualstack", fqdn: "backups.s3.dualstack.eu-west-1.amazonaws.com",
			want: want{"aws", "s3", KindStorage, "backups", "eu-west-1", false, ConfidenceFQDN, ""}},
		{name: "s3 path style", fqdn: "s3.amazonaws.com",
			want: want{"aws", "s3", KindStorage, "", "", false, ConfidenceFQDN, ""}},
		{name: "s3 website", fqdn: "site.s3-website-us-east-1.amazonaws.com",
			want: want{"aws", "s3", KindStorage, "site", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "vpc endpoint sqs", fqdn: "vpce-0a1b2c3d4e5f-abcdefgh.sqs.us-east-1.vpce.amazonaws.com",
			want: want{"aws", "sqs", KindQueue, "", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "ecr", fqdn: "123456789012.dkr.ecr.us-east-1.amazonaws.com",
			want: want{"aws", "ecr", KindRegistry, "123456789012", "us-east-1", false, ConfidenceFQDN, "call ecr DescribeRepositories"}},
		{name: "efs", fqdn: "fs-0123abcd.efs.us-east-1.amazonaws.com",
			want: want{"aws", "efs", KindStorage, "fs-0123abcd", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "msk broker", fqdn: "b-1.events.a1b2c3.c2.kafka.us-east-1.amazonaws.com",
			want: want{"aws", "kafka", KindQueue, "events", "us-east-1", false, ConfidenceFQDN, "call kafka ListClustersV2"}},
		{name: "opensearch", fqdn: "vpc-search-logs-abcdef1234567890.us-east-1.es.amazonaws.com",
			want: want{"aws", "opensearch", KindDatabase, "search-logs", "us-east-1", false, ConfidenceFQDN, "call cloudwatch GetMetricData"}},
		{name: "redshift", fqdn: "warehouse.abc123xyz.us-east-1.redshift.amazonaws.com",
			want: want{"aws", "redshift", KindDatabase, "warehouse", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "sts is plumbing", fqdn: "sts.us-east-1.amazonaws.com",
			want: want{"aws", "sts", KindPlatform, "", "us-east-1", true, ConfidenceFQDN, ""}},
		{name: "ec2 api is plumbing", fqdn: "ec2.eu-west-1.amazonaws.com",
			want: want{"aws", "ec2", KindPlatform, "", "eu-west-1", true, ConfidenceFQDN, ""}},
		{name: "secretsmanager api", fqdn: "secretsmanager.us-east-1.amazonaws.com",
			want: want{"aws", "secretsmanager", KindPlatform, "", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "ec2 private dns", fqdn: "ip-10-0-1-2.us-west-2.compute.internal",
			want: want{"aws", "ec2", KindUnknown, "ip-10-0-1-2", "us-west-2", false, ConfidenceFQDN, "call ec2 DescribeInstances"}},
		{name: "ec2 public dns us-east-1", fqdn: "ec2-54-1-2-3.compute-1.amazonaws.com",
			want: want{"aws", "ec2", KindUnknown, "ec2-54-1-2-3", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "unrecognised deep aws host", fqdn: "a.b.c.d.us-east-1.amazonaws.com",
			want: want{"aws", "", KindUnknown, "", "us-east-1", false, ConfidenceFQDN, ""}},
		{name: "cloudfront", fqdn: "d111111abcdef8.cloudfront.net",
			want: want{"aws", "cloudfront", KindLoadBalancer, "d111111abcdef8", "", false, ConfidenceFQDN, ""}},
	})
}

func TestClassify_Azure(t *testing.T) {
	runCases(t, []classifyCase{
		{name: "azure sql", fqdn: "orders-sql.database.windows.net",
			want: want{"azure", "azure-sql", KindDatabase, "orders-sql", "", false, ConfidenceFQDN, "monitor"}},
		{name: "azure sql private link", fqdn: "orders-sql.privatelink.database.windows.net",
			want: want{"azure", "azure-sql", KindDatabase, "orders-sql", "", false, ConfidenceFQDN, "monitor"}},
		{name: "azure sql managed instance", fqdn: "mi-prod.3f2a1b4c5d6e.database.windows.net",
			want: want{"azure", "azure-sql", KindDatabase, "mi-prod", "", false, ConfidenceFQDN, "monitor"}},
		{name: "postgres flexible", fqdn: "pg-orders.postgres.database.azure.com",
			want: want{"azure", "azure-postgres", KindDatabase, "pg-orders", "", false, ConfidenceFQDN, "monitor"}},
		{name: "postgres private link", fqdn: "pg-orders.privatelink.postgres.database.azure.com",
			want: want{"azure", "azure-postgres", KindDatabase, "pg-orders", "", false, ConfidenceFQDN, "monitor"}},
		{name: "mysql flexible", fqdn: "my-orders.mysql.database.azure.com",
			want: want{"azure", "azure-mysql", KindDatabase, "my-orders", "", false, ConfidenceFQDN, "monitor"}},
		{name: "redis", fqdn: "sessions.redis.cache.windows.net",
			want: want{"azure", "azure-redis", KindCache, "sessions", "", false, ConfidenceFQDN, "redis"}},
		{name: "managed redis", fqdn: "sessions.westeurope.redis.azure.net",
			want: want{"azure", "azure-redis", KindCache, "sessions", "westeurope", false, ConfidenceFQDN, "redis"}},
		{name: "service bus", fqdn: "orders-bus.servicebus.windows.net",
			want: want{"azure", "servicebus", KindQueue, "orders-bus", "", false, ConfidenceFQDN, "servicebus"}},
		{name: "cosmos sql api", fqdn: "catalog.documents.azure.com",
			want: want{"azure", "cosmos", KindDatabase, "catalog", "", false, ConfidenceFQDN, "cosmos"}},
		{name: "cosmos mongo api", fqdn: "catalog.mongo.cosmos.azure.com",
			want: want{"azure", "cosmos", KindDatabase, "catalog", "", false, ConfidenceFQDN, "cosmos"}},
		{name: "blob storage", fqdn: "stmedia.blob.core.windows.net",
			want: want{"azure", "storage", KindStorage, "stmedia", "", false, ConfidenceFQDN, "storage"}},
		{name: "queue storage private link", fqdn: "stjobs.privatelink.queue.core.windows.net",
			want: want{"azure", "storage", KindQueue, "stjobs", "", false, ConfidenceFQDN, "storage"}},
		{name: "acr", fqdn: "contosoprod.azurecr.io",
			want: want{"azure", "acr", KindRegistry, "contosoprod", "", false, ConfidenceFQDN, "acr"}},
		{name: "acr data endpoint", fqdn: "contosoprod.eastus2.data.azurecr.io",
			want: want{"azure", "acr", KindRegistry, "contosoprod", "eastus2", false, ConfidenceFQDN, "acr"}},
		{name: "key vault", fqdn: "kv-prod.vault.azure.net",
			want: want{"azure", "keyvault", KindPlatform, "kv-prod", "", false, ConfidenceFQDN, "keyvault"}},
		{name: "aks control plane", fqdn: "aks-prod-dns-1a2b3c4d.hcp.westeurope.azmk8s.io",
			want: want{"azure", "aks-control-plane", KindPlatform, "", "westeurope", true, ConfidenceFQDN, ""}},
		{name: "aks private control plane", fqdn: "0f1e2d3c-aaaa-bbbb.privatelink.eastus2.azmk8s.io",
			want: want{"azure", "aks-control-plane", KindPlatform, "", "eastus2", true, ConfidenceFQDN, ""}},
		{name: "entra id", fqdn: "login.microsoftonline.com",
			want: want{"azure", "entra-id", KindPlatform, "", "", true, ConfidenceFQDN, ""}},
		{name: "public ip dns label", fqdn: "shop-ingress.eastus.cloudapp.azure.com",
			want: want{"azure", "public-ip", KindLoadBalancer, "shop-ingress", "eastus", false, ConfidenceFQDN, ""}},
	})
}

// An Azure database hostname gives the server's name and not its resource
// group, which the server commands require, and the postgres and mysql
// commands read configuration, not whether the server runs. The note sends
// "is it down?" to the activity log by name first, and never to a guessed
// group; the server's own tool still follows it. The calls it names carry
// the arguments that make them show a stop: more activity log entries than
// the default 10, and a liveness metric at 5 minutes, over the last hours or
// over a longer window with max-buckets raised (it defaults to 50, and 24 h
// at 5 minutes is 288).
func TestClassify_AzureDatabaseNotes(t *testing.T) {
	window := []string{
		"at interval PT5M with start-time one or two hours back",
		"or over a longer window with max-buckets raised (it defaults to 50; 24 h at PT5M is 288)",
		"a stopped server reads 0 or has no data",
	}
	for _, tc := range []struct {
		fqdn, tool string
		then       []string
	}{
		{"orders-sql.database.windows.net", "sql", []string{"sql_db_get for the database status", "sql_server_firewall-rule_list"}},
		{"pg-orders.postgres.database.azure.com", "postgres", append([]string{"monitor_metrics_query is_db_alive at interval PT5M", "postgres_server_config_get gives SKU, version and location (the server's region), not whether the server runs"}, window...)},
		{"my-orders.mysql.database.azure.com", "mysql", append([]string{"monitor_metrics_query Uptime at interval PT5M", "mysql_server_config_get gives the server's configuration, not whether the server runs"}, window...)},
	} {
		got := Classify(tc.fqdn, "", "Azure", "")
		for _, want := range append([]string{
			"Start with monitor_activitylog_list on the server name and top 20",
			"the default top of 10 cuts older entries",
			"It needs no resource group",
			"its entries carry the resource id",
			"Never guess a resource group.",
		}, tc.then...) {
			if !strings.Contains(got.Note, want) {
				t.Errorf("%s: note lacks %q:\n%s", tc.fqdn, want, got.Note)
			}
		}
		// The 50 is the query's max-buckets default, not a limit of the window.
		if strings.Contains(got.Note, "allows at most 50 buckets") {
			t.Errorf("%s: note calls 50 buckets a limit of the window:\n%s", tc.fqdn, got.Note)
		}
		if len(got.FirstTools) < 2 || got.FirstTools[0] != "monitor" || got.FirstTools[1] != tc.tool {
			t.Errorf("%s: FirstTools = %v, want monitor then %s", tc.fqdn, got.FirstTools, tc.tool)
		}
	}
}

func TestClassify_GCP(t *testing.T) {
	runCases(t, []classifyCase{
		{name: "sqladmin api", fqdn: "sqladmin.googleapis.com",
			want: want{"gcp", "cloudsql", KindDatabase, "", "", false, ConfidenceFQDN, "sqladmin_list_instances"}},
		{name: "cloud sql psc dns", fqdn: "1a2b3c4d5e6f.1a2b3c4d5e6f.us-central1.sql.goog",
			want: want{"gcp", "cloudsql", KindDatabase, "", "us-central1", false, ConfidenceFQDN, "sqladmin_list_instances"}},
		{name: "cloud sql psc googleapis", fqdn: "orders.psc.googleapis.com",
			want: want{"gcp", "cloudsql", KindDatabase, "", "", false, ConfidenceFQDN, ""}},
		{name: "pubsub", fqdn: "pubsub.googleapis.com",
			want: want{"gcp", "pubsub", KindQueue, "", "", false, ConfidenceFQDN, "pubsub_list_subscriptions"}},
		{name: "storage bucket host", fqdn: "media-bucket.storage.googleapis.com",
			want: want{"gcp", "storage", KindStorage, "media-bucket", "", false, ConfidenceFQDN, ""}},
		{name: "memorystore api", fqdn: "redis.googleapis.com",
			want: want{"gcp", "memorystore", KindCache, "", "", false, ConfidenceFQDN, "redis_list_instances"}},
		{name: "alloydb regional", fqdn: "alloydb.us-central1.rep.googleapis.com",
			want: want{"gcp", "alloydb", KindDatabase, "", "us-central1", false, ConfidenceFQDN, "alloydb_list_instances"}},
		{name: "regional prefix api", fqdn: "us-central1-aiplatform.googleapis.com",
			want: want{"gcp", "googleapis", KindPlatform, "aiplatform", "us-central1", false, ConfidenceFQDN, "cloudasset_list_assets"}},
		{name: "psc alias for google api", fqdn: "pubsub-myendpoint.p.googleapis.com",
			want: want{"gcp", "pubsub", KindQueue, "", "", false, ConfidenceFQDN, ""}},
		{name: "spanner", fqdn: "spanner.googleapis.com",
			want: want{"gcp", "spanner", KindDatabase, "", "", false, ConfidenceFQDN, "monitoring_list_timeseries"}},
		{name: "gke management api", fqdn: "container.googleapis.com",
			want: want{"gcp", "gke-control-plane", KindPlatform, "", "", true, ConfidenceFQDN, ""}},
		{name: "gke dns endpoint", fqdn: "gke-0123456789abcdef-123456789012.us-central1.gke.goog",
			want: want{"gcp", "gke-control-plane", KindPlatform, "", "us-central1", true, ConfidenceFQDN, ""}},
		{name: "workload identity token exchange", fqdn: "sts.googleapis.com",
			want: want{"gcp", "identity", KindPlatform, "", "", true, ConfidenceFQDN, ""}},
		{name: "metadata server name", fqdn: "metadata.google.internal",
			want: want{"gcp", "imds", KindPlatform, "", "", true, ConfidenceFQDN, ""}},
		{name: "gce internal dns", fqdn: "db-vm-1.us-central1-a.c.my-project.internal",
			want: want{"gcp", "compute", KindUnknown, "db-vm-1", "us-central1", false, ConfidenceFQDN, "compute_list_instances"}},
		{name: "artifact registry", fqdn: "europe-west4-docker.pkg.dev",
			want: want{"gcp", "artifact-registry", KindRegistry, "", "europe-west4", false, ConfidenceFQDN, ""}},
		{name: "cloud run", fqdn: "checkout-abc123xyz-uc.a.run.app",
			want: want{"gcp", "cloudrun", KindPlatform, "checkout-abc123xyz-uc", "", false, ConfidenceFQDN, ""}},
		{name: "cloud sql connection name", fqdn: "Shop-Prod:us-central1:orders-db",
			want: want{"gcp", "cloudsql", KindDatabase, "orders-db", "us-central1", false, ConfidenceFQDN, "sqladmin_get_instance"}},
		{name: "cloud sql connection name domain-scoped project", fqdn: "example.com:shop-prod:europe-west1:orders",
			want: want{"gcp", "cloudsql", KindDatabase, "orders", "europe-west1", false, ConfidenceFQDN, "sqladmin_get_instance"}},
		{name: "unknown google api", fqdn: "translate.googleapis.com",
			want: want{"gcp", "googleapis", KindPlatform, "translate", "", false, ConfidenceFQDN, "cloudasset_list_assets"}},
	})
}

func TestClassify_AddressesAndFallbacks(t *testing.T) {
	runCases(t, []classifyCase{
		{name: "imds on aws cluster", ip: "169.254.169.254", clusterProv: "AWS", cluster: "us-east-1",
			want: want{"aws", "imds", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "imds wins over any hostname", fqdn: "mydb.abc.us-east-1.rds.amazonaws.com", ip: "169.254.169.254", clusterProv: "Azure",
			want: want{"azure", "imds", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "imds with unknown cluster cloud", ip: "169.254.169.254",
			want: want{"", "imds", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "aws imds ipv6", ip: "fd00:ec2::254",
			want: want{"aws", "imds", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "aws imds ipv6 bracketed", ip: "[FD00:EC2::254]",
			want: want{"aws", "imds", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "amazon dns", ip: "169.254.169.253",
			want: want{"aws", "cluster-dns", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "eks pod identity agent", ip: "169.254.170.23",
			want: want{"aws", "pod-identity", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "azure wireserver", ip: "168.63.129.16", clusterProv: "Azure",
			want: want{"azure", "cluster-dns", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "nodelocal dns", ip: "169.254.20.10",
			want: want{"", "cluster-dns", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "other link-local", ip: "169.254.1.1",
			want: want{"", "link-local", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "ipv6 link-local with zone", ip: "fe80::1%eth0",
			want: want{"", "link-local", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "loopback", ip: "127.0.0.1",
			want: want{"", "loopback", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "private ip on aws cluster", ip: "10.0.3.17", clusterProv: "AWS", cluster: "US-EAST-1",
			want: want{"aws", "", KindUnknown, "", "", false, ConfidenceIPInProvider, "call ec2 DescribeNetworkInterfaces"}},
		{name: "private ip on azure cluster", ip: "172.16.4.5", clusterProv: "Azure", cluster: "westeurope",
			want: want{"azure", "", KindUnknown, "", "", false, ConfidenceIPInProvider, "group_list"}},
		{name: "private ip on gcp cluster", ip: "192.168.10.3", clusterProv: "GCP", cluster: "us-central1",
			want: want{"gcp", "", KindUnknown, "", "", false, ConfidenceIPInProvider, "sqladmin_list_instances"}},
		{name: "cgnat ip counts as private", ip: "100.64.2.3", clusterProv: "gcp",
			want: want{"gcp", "", KindUnknown, "", "", false, ConfidenceIPInProvider, ""}},
		{name: "ipv6 ula private", ip: "fd12:3456::10", clusterProv: "AWS", cluster: "eu-west-1",
			want: want{"aws", "", KindUnknown, "", "", false, ConfidenceIPInProvider, ""}},
		{name: "ipv4-mapped ipv6 private", ip: "::ffff:10.1.2.3", clusterProv: "AWS",
			want: want{"aws", "", KindUnknown, "", "", false, ConfidenceIPInProvider, ""}},
		{name: "private ip on kind cluster", ip: "10.0.3.17", clusterProv: "kind (local)",
			want: want{"", "", KindUnknown, "", "", false, ConfidenceUnknown, ""}},
		{name: "private ip on-prem", ip: "10.0.3.17", clusterProv: "on-prem",
			want: want{"", "", KindUnknown, "", "", false, ConfidenceUnknown, ""}},
		{name: "public ip no fqdn", ip: "52.1.2.3", clusterProv: "AWS", cluster: "us-east-1",
			want: want{"", "", KindUnknown, "", "", false, ConfidenceUnknown, ""}},
		{name: "public ipv6 no fqdn", ip: "2600:1f18::1", clusterProv: "AWS",
			want: want{"", "", KindUnknown, "", "", false, ConfidenceUnknown, ""}},
		{name: "custom domain alone", fqdn: "db.corp.example.com", clusterProv: "AWS",
			want: want{"", "", KindUnknown, "", "", false, ConfidenceUnknown, ""}},
		{name: "custom domain with private ip falls back to provider", fqdn: "db.corp.example.com", ip: "10.0.3.17", clusterProv: "AWS", cluster: "us-east-1",
			want: want{"aws", "", KindUnknown, "", "", false, ConfidenceIPInProvider, "call ec2 DescribeNetworkInterfaces"}},
		{name: "custom domain with public ip", fqdn: "api.stripe.com", ip: "54.187.1.1", clusterProv: "AWS",
			want: want{"", "", KindUnknown, "", "", false, ConfidenceUnknown, ""}},
		{name: "ip literal in fqdn slot", fqdn: "10.0.3.17", clusterProv: "Azure",
			want: want{"azure", "", KindUnknown, "", "", false, ConfidenceIPInProvider, ""}},
		{name: "bracketed ipv6 with port in fqdn slot", fqdn: "[fd00:ec2::254]:80",
			want: want{"aws", "imds", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "ipv4 with port in ip slot", ip: "169.254.169.254:80",
			want: want{"", "imds", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "bracketed ipv6 literal in fqdn slot", fqdn: "[fd00:ec2::254]",
			want: want{"aws", "imds", KindInfrastructure, "", "", true, ConfidenceWellKnownIP, ""}},
		{name: "fqdn wins over provider of cluster", fqdn: "orders-sql.database.windows.net", ip: "10.1.1.1", clusterProv: "AWS",
			want: want{"azure", "azure-sql", KindDatabase, "orders-sql", "", false, ConfidenceFQDN, ""}},
		{name: "in-cluster service name", fqdn: "postgres.data.svc.cluster.local",
			want: want{"", "in-cluster", KindUnknown, "", "", false, ConfidenceFQDN, ""}},
		{name: "in-cluster short svc", fqdn: "redis.cache.svc",
			want: want{"", "in-cluster", KindUnknown, "", "", false, ConfidenceFQDN, ""}},
		{name: "bare single label", fqdn: "postgres",
			want: want{"", "in-cluster", KindUnknown, "", "", false, ConfidenceFQDN, ""}},
		{name: "kubernetes api alias", fqdn: "kubernetes.default.svc.cluster.local",
			want: want{"", "kube-apiserver", KindPlatform, "", "", true, ConfidenceFQDN, ""}},
		{name: "empty input", want: want{"", "", KindUnknown, "", "", false, ConfidenceUnknown, ""}},
		{name: "garbage input", fqdn: "a..b", ip: "not-an-ip",
			want: want{"", "", KindUnknown, "", "", false, ConfidenceUnknown, ""}},
	})
}

func TestNormalizeProvider(t *testing.T) {
	cases := map[string]string{
		"AWS": "aws", "aws": "aws", " Amazon Web Services ": "aws",
		"GCP": "gcp", "gce": "gcp", "Google Cloud": "gcp",
		"Azure": "azure", "AZURE": "azure", "aks": "azure",
		"kind (local)": "", "on-prem": "", "cloud (unrecognised)": "", "Oracle Cloud": "", "": "",
	}
	for in, want := range cases {
		if got := NormalizeProvider(in); got != want {
			t.Errorf("NormalizeProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAWSShortRegion(t *testing.T) {
	cases := map[string]string{
		"use1": "us-east-1", "usw2": "us-west-2", "euc1": "eu-central-1", "apse1": "ap-southeast-1",
		"apne3": "ap-northeast-3", "cac1": "ca-central-1", "sae1": "sa-east-1", "eu-west-1": "eu-west-1",
		"cache": "", "": "",
	}
	for in, want := range cases {
		if got := awsShortRegion(in); got != want {
			t.Errorf("awsShortRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripHashSuffix(t *testing.T) {
	cases := map[string]string{
		"my-alb-1234567890": "my-alb",
		"carts-x1y2z3":      "carts",
		"api-gateway":       "api-gateway", // no id: word segment kept
		"web-backend":       "web-backend", // 7 letters, no digit: kept
		"noid":              "noid",
		"db-12345":          "db-12345", // too short to be a generated id
	}
	for in, want := range cases {
		if got := stripHashSuffix(in); got != want {
			t.Errorf("stripHashSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

// The contract every result keeps: a Kind and a Confidence from the known
// sets, first tools that are valid local names for the provider's connector
// (so the model never receives a tool name that cannot exist), infrastructure
// never carrying first tools, and JSON that round-trips.
func TestClassify_Invariants(t *testing.T) {
	kinds := map[string]bool{KindDatabase: true, KindCache: true, KindQueue: true, KindLoadBalancer: true,
		KindStorage: true, KindRegistry: true, KindPlatform: true, KindInfrastructure: true, KindUnknown: true}
	confidences := map[string]bool{ConfidenceFQDN: true, ConfidenceWellKnownIP: true,
		ConfidenceIPInProvider: true, ConfidencePrivateName: true, ConfidenceUnknown: true}
	awsTool := regexp.MustCompile(`^call [a-z0-9-]+ [A-Z][A-Za-z0-9]+$`)
	localTool := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	inputs := [][2]string{
		{"mydb.x.us-east-1.rds.amazonaws.com", ""}, {"sessions.redis.cache.windows.net", ""},
		{"sqladmin.googleapis.com", ""}, {"", "10.0.0.1"}, {"", "169.254.169.254"},
		{"aks-1.hcp.eastus.azmk8s.io", ""}, {"x.cloudfront.net", ""}, {"stmedia.blob.core.windows.net", ""},
		{"media.storage.googleapis.com", ""}, {"orders.cluster-x.eu-west-1.docdb.amazonaws.com", ""},
		{"unknown.example.org", ""}, {"", "8.8.8.8"}, {"b-1.c.x.c2.kafka.us-east-1.amazonaws.com", ""},
		{"db.kubeshop.internal", ""},
	}
	for _, prov := range []string{"AWS", "Azure", "GCP", ""} {
		for _, in := range inputs {
			c := Classify(in[0], in[1], prov, "region-1")
			if !kinds[c.Kind] || !confidences[c.Confidence] {
				t.Errorf("%v on %q: kind %q / confidence %q outside the known sets", in, prov, c.Kind, c.Confidence)
			}
			if c.Infrastructure && len(c.FirstTools) > 0 {
				t.Errorf("%v on %q: infrastructure with first tools %v", in, prov, c.FirstTools)
			}
			for _, tool := range c.FirstTools {
				switch c.Provider {
				case ProviderAWS:
					if !awsTool.MatchString(tool) {
						t.Errorf("%v: aws first tool %q is not \"call <service> <Operation>\"", in, tool)
					}
				case ProviderAzure, ProviderGCP:
					if !localTool.MatchString(tool) || strings.Contains(tool, "__") {
						t.Errorf("%v: %s first tool %q is not a local tool name", in, c.Provider, tool)
					}
				default:
					t.Errorf("%v: first tools %v without a provider", in, c.FirstTools)
				}
			}
			b, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var back Classification
			if err := json.Unmarshal(b, &back); err != nil || back.Kind != c.Kind || back.Provider != c.Provider {
				t.Errorf("%v: JSON round-trip lost data: %s", in, b)
			}
		}
	}
}

// An address alone never carries a region. A private endpoint can front a
// resource in another region and a Google VPC spans regions, so the cluster's
// region given as the destination's read as the resource's: the model named
// eastus2 for a server in centralus. Where to look is said in the note, as
// where to look.
func TestPrivateAddressCarriesNoRegion(t *testing.T) {
	for _, tc := range []struct {
		provider, region, note string
	}{
		{"AWS", "us-east-1", "addresses.private-ip-address in us-east-1 (the cluster's region, where its network is)"},
		{"Azure", "westeurope", "take the region from the resource you find, not from the cluster"},
		{"GCP", "us-central1", "A Google VPC spans regions: take the region from the resource you find"},
	} {
		got := Classify("", "10.20.30.40", tc.provider, tc.region)
		if got.Region != "" {
			t.Errorf("%s: a private address carries region %q; it must carry none", tc.provider, got.Region)
		}
		if !strings.Contains(got.Note, tc.note) {
			t.Errorf("%s: note %q lacks %q", tc.provider, got.Note, tc.note)
		}
	}
	// With no cluster region, the AWS note does not invent one.
	if got := Classify("", "10.20.30.40", "AWS", ""); strings.Contains(got.Note, " in  (") || strings.Contains(got.Note, "cluster's region, where") {
		t.Errorf("AWS note without a cluster region: %q", got.Note)
	}
}

// A name in a private domain on an EKS cluster is a Route 53 private hosted
// zone record to read, not an unknown host left to the operator
// (db.kubeshop.internal in the AWS lab). Elsewhere it stays unknown.
func TestClassify_PrivateNameOnAWS(t *testing.T) {
	c := Classify("db.kubeshop.internal", "", "AWS", "us-east-1")
	if c.Provider != ProviderAWS || c.Service != "route53" || c.Confidence != ConfidencePrivateName ||
		c.ResourceHint != "db.kubeshop.internal" || c.Region != "" || c.Infrastructure {
		t.Fatalf("private name on aws: %+v", c)
	}
	if len(c.FirstTools) != 3 || c.FirstTools[0] != "call route53 ListHostedZonesByVPC" || !strings.Contains(c.Note, "private hosted zone") || !strings.Contains(c.Note, "VPCId") {
		t.Fatalf("tools/note: %+v", c)
	}
	for _, h := range []string{"db.corp", "ldap.office.lan", "API.Shop.Internal."} {
		if c := Classify(h, "", "AWS", ""); c.Service != "route53" {
			t.Errorf("%s: %+v", h, c)
		}
	}
	unknown := []struct{ host, prov string }{
		{"db.kubeshop.internal", "Azure"},
		{"db.kubeshop.internal", "GCP"},
		{"db.kubeshop.internal", ""},
		{"internal", "AWS"},                      // one label: an in-cluster name
		{"db.example.com", "AWS"},                // a public zone: may be split-horizon, not assumed
		{"ip-10-0-1-2.ec2.internal", "AWS"},      // an instance, matched before
		{"orders.shop.svc.cluster.local", "AWS"}, // a Service
	}
	for _, u := range unknown {
		if c := Classify(u.host, "", u.prov, ""); c.Confidence == ConfidencePrivateName {
			t.Errorf("%s on %q read as a private zone record: %+v", u.host, u.prov, c)
		}
	}
}
