package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	appslisters "k8s.io/client-go/listers/apps/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
)

func refStrings(refs []hostRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.scheme+"|"+r.host+"|"+r.port)
	}
	return out
}

func TestParseDependencyValue_Shapes(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		hostKey bool
		want    []string
	}{
		{"postgres url with credentials", "postgres://orders:S3cr3t@mydb.c9x.us-east-1.rds.amazonaws.com:5432/orders?sslmode=require", false,
			[]string{"postgres|mydb.c9x.us-east-1.rds.amazonaws.com|5432"}},
		{"postgresql scheme uppercase host", "postgresql://u:p@PG-Orders.Postgres.Database.Azure.com/app", false,
			[]string{"postgresql|pg-orders.postgres.database.azure.com|"}},
		{"mysql url", "mysql://root:pw@10.20.0.7:3306/shop", false, []string{"mysql|10.20.0.7|3306"}},
		{"rediss url", "rediss://:token@sessions.redis.cache.windows.net:6380/0", false,
			[]string{"rediss|sessions.redis.cache.windows.net|6380"}},
		{"amqps url", "amqps://user:pw@b-1234.mq.us-east-1.amazonaws.com:5671", false,
			[]string{"amqps|b-1234.mq.us-east-1.amazonaws.com|5671"}},
		{"mongodb multi-host", "mongodb://u:p@db1.example.com:27017,db2.example.com:27017/app?replicaSet=rs0", false,
			[]string{"mongodb|db1.example.com|27017", "mongodb|db2.example.com|27017"}},
		{"url list", "http://a.example.com,postgres://u:p@db.example.com/app", false,
			[]string{"http|a.example.com|", "postgres|db.example.com|"}},
		{"mongodb+srv", "mongodb+srv://u:p@cluster0.ab1cd.mongodb.net/app", false, []string{"mongodb+srv|cluster0.ab1cd.mongodb.net|"}},
		{"https url", "https://api.stripe.com/v1/charges?key=abc", false, []string{"https|api.stripe.com|"}},
		{"jdbc postgresql", "jdbc:postgresql://pg.example.com:5432/app", false, []string{"postgresql|pg.example.com|5432"}},
		{"jdbc postgresql azure user@server in params", "jdbc:postgresql://pg1.postgres.database.azure.com:5432/app?user=admin@pg1&password=x", false,
			[]string{"postgresql|pg1.postgres.database.azure.com|5432"}},
		{"jdbc sqlserver", "jdbc:sqlserver://srv.database.windows.net:1433;database=app;user=u@srv;password=p;encrypt=true", false,
			[]string{"sqlserver|srv.database.windows.net|1433"}},
		{"jdbc sqlserver serverName property", "jdbc:sqlserver://;serverName=srv2.database.windows.net;port=1433;databaseName=app", false,
			[]string{"sqlserver|srv2.database.windows.net|1433"}},
		{"ado.net azure sql", "Server=tcp:orders-sql.database.windows.net,1433;Initial Catalog=orders;User ID=app;Password=S3cr3t;Encrypt=True;", false,
			[]string{"sqlserver|orders-sql.database.windows.net|1433"}},
		{"ado.net data source named instance", "Data Source=sql01.corp.example.com\\PROD;Initial Catalog=x;Integrated Security=true", false,
			[]string{"|sql01.corp.example.com|"}},
		{"npgsql host and port", "Host=pg.example.com;Port=5433;Database=app;Username=u;Password=p", false,
			[]string{"|pg.example.com|5433"}},
		{"npgsql host list", "Host=pg1.example.com,pg2.example.com;Port=5432;Password=p", false,
			[]string{"|pg1.example.com|5432", "|pg2.example.com|5432"}},
		{"mysql connector server", "Server=my-orders.mysql.database.azure.com;Port=3306;Uid=u;Pwd=p", false,
			[]string{"|my-orders.mysql.database.azure.com|3306"}},
		{"libpq keyword dsn", "host=pg.example.com port=5432 dbname=app user=u password=p", false,
			[]string{"|pg.example.com|5432"}},
		{"stackexchange redis", "sessions.redis.cache.windows.net:6380,password=S3cr3t,ssl=True,abortConnect=False", false,
			[]string{"|sessions.redis.cache.windows.net|6380"}},
		{"service bus connection string", "Endpoint=sb://orders-bus.servicebus.windows.net/;SharedAccessKeyName=app;SharedAccessKey=S3cr3t=", false,
			[]string{"sb|orders-bus.servicebus.windows.net|"}},
		{"storage connection string", "DefaultEndpointsProtocol=https;AccountName=stmedia;AccountKey=S3cr3t==;EndpointSuffix=core.windows.net", false,
			[]string{"https|stmedia.blob.core.windows.net|"}},
		{"cosmos connection string", "AccountEndpoint=https://catalog.documents.azure.com:443/;AccountKey=S3cr3t==;", false,
			[]string{"https|catalog.documents.azure.com|443"}},
		{"bare host:port", "mydb.example.com:5432", false, []string{"|mydb.example.com|5432"}},
		{"kafka broker list", "b-1.events.x.c2.kafka.us-east-1.amazonaws.com:9092,b-2.events.x.c2.kafka.us-east-1.amazonaws.com:9092", false,
			[]string{"|b-1.events.x.c2.kafka.us-east-1.amazonaws.com|9092", "|b-2.events.x.c2.kafka.us-east-1.amazonaws.com|9092"}},
		{"bare host needs a host key", "mydb.example.com", false, nil},
		{"bare host with host key", "mydb.example.com", true, []string{"|mydb.example.com|"}},
		{"bare ip with host key", "10.20.0.5", true, []string{"|10.20.0.5|"}},
		{"bracketed ipv6 with port", "[fd12::5]:5432", false, []string{"|fd12::5|5432"}},
		{"cloud sql connection name", "shop-prod:us-central1:orders-db", false, []string{"cloudsql|shop-prod:us-central1:orders-db|"}},
		{"cloud sql proxy v1 instances", "shop-prod:us-central1:orders=tcp:5432,shop-prod:us-central1:carts=tcp:5433", false,
			[]string{"cloudsql|shop-prod:us-central1:orders|", "cloudsql|shop-prod:us-central1:carts|"}},
		{"cloud sql proxy v2 with port query", "shop-prod:europe-west1:orders?port=5432", false,
			[]string{"cloudsql|shop-prod:europe-west1:orders|"}},
		{"s3 url names a bucket, not a host", "s3://media-bucket/path/to/key", false, []string{"s3|media-bucket|"}},
		{"other object storage urls are not hosts", "gs://media-bucket/path/to/key", false, nil},
		{"file url", "file:///etc/app/config.yaml", false, nil},
		{"version string", "1.2.3", true, nil},
		{"image reference", "nginx:1.25", false, nil},
		{"registry path", "registry.example.com:5000/team/app", false, nil},
		{"listen address", "0.0.0.0:8080", false, nil},
		{"loopback", "127.0.0.1:5432", false, nil},
		{"localhost url", "http://localhost:8080/healthz", false, nil},
		{"port out of range", "db.example.com:70000", false, nil},
		{"plain word", "info", true, []string{"|info|"}}, // single label: dropped later as in-cluster
		{"boolean", "true", false, nil},
		{"empty", "  ", true, nil},
		{"quoted value", `"postgres://u:p@db.example.com:5432/x"`, false, []string{"postgres|db.example.com|5432"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := refStrings(parseDependencyValue(tc.value, tc.hostKey))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseDependencyValue(%q, %v)\n got  %v\n want %v", tc.value, tc.hostKey, got, tc.want)
			}
		})
	}
}

// The property the whole helper exists for: whatever a connection string
// looks like — well-formed, or with a password full of URL metacharacters —
// no credential, path or query survives into the result.
func TestParseDependencyValue_NeverLeaksCredentials(t *testing.T) {
	const user, pass = "dbadmin.ops", "Pw.9f"
	values := []string{
		"postgres://" + user + ":" + pass + "@db.example.com:5432/app",
		"postgres://" + user + ":" + pass + "/x@db.example.com/app",
		"postgres://" + user + ":5/" + pass + "@db.example.com/app",
		"postgres://" + user + ":" + pass + "?a=b@db.example.com/app",
		"postgres://" + user + ":" + pass + ";host=evil.example.com@db.example.com/app",
		"mysql://" + user + ":" + pass + "@@db.example.com/app",
		"mongodb://" + user + ":" + pass + ",x.example.com:1@db.example.com/app",
		"redis://:" + pass + "@cache.example.com:6379",
		"Server=tcp:db.example.com,1433;User ID=" + user + ";Password=" + pass + ";",
		"Server=db.example.com;Password=\"" + pass + ";host=evil." + pass + "\";",
		"host=db.example.com user=" + user + " password=" + pass,
		"cache.example.com:6380,password=" + pass + ",ssl=True",
		"jdbc:postgresql://db.example.com/app?user=" + user + "&password=" + pass,
		"https://" + user + ":" + pass + "@api.example.com/v1?token=" + pass,
		// RFC 3986 allows ' ; = in userinfo; a space or a quote must not end
		// the URL inside the password, and the key=value pass must not read it.
		"postgres://" + user + ":12'" + pass + "@db.example.com/app",
		"postgres://" + user + ":12 " + pass + "@db.example.com/app",
		"postgres://" + user + ":12;x=" + pass + "@db.example.com/app",
		"postgres://" + user + ":12;host=evil." + pass + ";q@db.example.com/app",
		"postgres://" + user + ":1@evil.x'" + pass + "@db.example.com/app",
		"redis://" + user + ":12;a=b@cache.example.com:6379",
		`{"db":"postgres://` + user + `:12'x@db.example.com/app"}`,
	}
	for _, v := range values {
		for _, hk := range []bool{false, true} {
			assertNoLeak(t, v, hk, user, pass)
		}
	}
}

func assertNoLeak(t *testing.T, v string, hk bool, user, pass string) {
	t.Helper()
	for _, r := range parseDependencyValue(v, hk) {
		joined := r.scheme + " " + r.host + " " + r.port
		for _, secret := range []string{user, pass, strings.ToLower(pass), "pw", "evil", "token", "/app", "?"} {
			if strings.Contains(joined, secret) {
				t.Errorf("value %q leaked %q in %+v", v, secret, r)
			}
		}
		if r.port == "12" || r.port == "1234" || r.port == "12345" {
			t.Errorf("value %q leaked a password prefix as the port in %+v", v, r)
		}
	}
}

// A value longer than the parse bound is cut; wherever the cut lands inside
// a URL's userinfo, nothing before the '@' may come back as host:port.
func TestParseDependencyValue_TruncationNeverLeaks(t *testing.T) {
	const want = "postgres|db.example.com|5432"
	for _, url := range []string{
		"postgres://dbadmin.ops:12345@db.example.com:5432/app",
		"postgres://dbadmin.ops:12 345@db.example.com:5432/app",
		"postgres://dbadmin.ops:12'345@db.example.com:5432/app",
		"postgres://dbadmin.ops:1@evil.x.example@db.example.com:5432/app",
	} {
		// Every cut point from just before the URL to its end.
		for pad := maxDependencyValueLen - len(url) - 2; pad <= maxDependencyValueLen; pad++ {
			v := strings.Repeat("x", pad) + " " + url
			for _, got := range refStrings(parseDependencyValue(v, false)) {
				if got != want {
					t.Errorf("cut %d bytes into %q: got %q", maxDependencyValueLen-pad-1, url, got)
				}
			}
		}
	}
	// A URL that ends before the cut, followed by more config, is still found.
	v := "url: postgres://u:p@db.example.com:5432/app\n" + strings.Repeat("key: value\n", maxDependencyValueLen/10)
	if got := refStrings(parseDependencyValue(v, false)); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("URL before the cut: got %v", got)
	}
}

// The userinfo fixes must not cost the shapes they sit next to.
func TestParseDependencyValue_UserinfoShapes(t *testing.T) {
	cases := []struct {
		name, value string
		want        []string
	}{
		{"semicolon in a redis password", "redis://:p;w=1@cache.example.com:6379", []string{"redis|cache.example.com|6379"}},
		{"url inside json next to an email", `{"db":"postgres://u:p@db.example.com/app","owner":"ops@corp.example.com"}`,
			[]string{"postgres|db.example.com|"}},
		{"url in yaml next to an email", "url: postgres://db.example.com:5432/app\nowner: ops@corp.example.com",
			[]string{"postgres|db.example.com|5432"}},
		{"jdbc sqlserver with @ in the password property",
			"jdbc:sqlserver://srv.database.windows.net:1433;user=u@srv;password=p@ss.word.example.com",
			[]string{"sqlserver|srv.database.windows.net|1433"}},
		// Ambiguous on purpose: the '@' after the space may close this URL's
		// userinfo, so the host is given up rather than risk a password.
		{"url then a bare email", "postgres://db.example.com/app ops@corp.example.com", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := refStrings(parseDependencyValue(tc.value, false))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseDependencyValue(%q)\n got  %v\n want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestIsCredentialName(t *testing.T) {
	cases := map[string]bool{
		"DB_PASSWORD": true, "PGPASSWORD": true, "db.password": true, "--password": true,
		"--api-token": true, "JWT_SECRET": true, "AZURE_STORAGE_KEY": true, "REDIS_PASS": true,
		"AWS_SECRET_ACCESS_KEY": true, "SERVICE_CREDENTIALS": true,
		"TOKEN_URL": false, "OAUTH_TOKEN_ENDPOINT": false, "KEY_VAULT_URI": false, "SECRETS_MANAGER_ENDPOINT": false,
		"DATABASE_URL": false, "DB_HOST": false, "KEYCLOAK_URL": false, "BYPASS_CACHE": false, "--db-host": false,
	}
	for name, want := range cases {
		if got := isCredentialName(name); got != want {
			t.Errorf("isCredentialName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A credential shaped like host:port stays unread whether it sits in an env
// var or behind a flag.
func TestWorkloadDependencyHosts_CredentialNamesNeverParsed(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-0"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "api",
			Env: []corev1.EnvVar{
				{Name: "DB_PASSWORD", Value: "hunter.two:1234"},
				{Name: "API_TOKEN", Value: "https://tok.example.com"},
				{Name: "DB_HOST", Value: "db.example.com"},
			},
			Args: []string{"--password=hunter.two:1234", "--api-token", "tok.example.com:443", "--db-port=5432"},
		}}},
	}
	c := &Connector{podLister: corelisters.NewPodLister(indexer(t, pod))}
	got, err := c.WorkloadDependencyHosts("Pod", "shop", "api-0")
	if err != nil {
		t.Fatal(err)
	}
	want := []DependencyHost{{Source: "env DB_HOST (container api)", Host: "db.example.com"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestIsInClusterHost(t *testing.T) {
	nsExists := func(ns string) bool { return ns == "data" || ns == "default" }
	cases := map[string]bool{
		"redis":                                true,
		"redis.data":                           true, // <svc>.<namespace>
		"redis.data.svc":                       true,
		"redis.data.svc.cluster.local":         true,
		"redis.data.svc.corp.local":            true, // custom cluster domain
		"example.com":                          false,
		"db.example.com":                       false,
		"10.0.0.1":                             false,
		"mydb.c9x.us-east-1.rds.amazonaws.com": false,
	}
	for host, want := range cases {
		if got := isInClusterHost(host, nsExists); got != want {
			t.Errorf("isInClusterHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestHostVarPrefix(t *testing.T) {
	cases := []struct {
		name, prefix string
		ok           bool
	}{
		{"DB_HOST", "DB_", true}, {"PGHOST", "PG", true}, {"REDIS_ADDR", "REDIS_", true},
		{"--db-host", "DB_", true}, {"kafka.bootstrap.servers", "KAFKA_BOOTSTRAP_", true},
		{"DATABASE_URL", "", false}, {"LOG_LEVEL", "", false},
	}
	for _, tc := range cases {
		p, ok := hostVarPrefix(tc.name)
		if p != tc.prefix || ok != tc.ok {
			t.Errorf("hostVarPrefix(%q) = %q, %v; want %q, %v", tc.name, p, ok, tc.prefix, tc.ok)
		}
	}
}

// ---- connector-level tests over real listers on hand-built indexers ----

func indexer(t *testing.T, objs ...interface{}) cache.Indexer {
	t.Helper()
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, o := range objs {
		if err := idx.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	return idx
}

func boolPtr(b bool) *bool    { return &b }
func int32Ptr(i int32) *int32 { return &i }

// depFixture is a namespace "shop" with one Deployment exercising every
// source, next to a "data" namespace that makes "redis.data" in-cluster.
func depFixture(t *testing.T) *Connector {
	t.Helper()
	appCfg := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "app-cfg"},
		Data: map[string]string{
			"DB_HOST":   "orders.cluster-c9x.us-east-1.rds.amazonaws.com",
			"DB_PORT":   "5432",
			"LOG_LEVEL": "info",
			"CACHE_URL": "redis://:S3cr3t@sessions.abc123.use1.cache.amazonaws.com:6379",
		},
	}
	extra := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "extra"},
		Data:       map[string]string{"QUEUE_ENDPOINT": "https://sqs.us-east-1.amazonaws.com/123456789012/orders"},
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "orders"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{
				Name:    "migrate",
				Command: []string{"sh", "-c", "migrate -database postgres://admin:Hunter2@legacy-db.corp.example.com:5432/orders up"},
			}},
			Containers: []corev1.Container{
				{
					Name:    "app",
					EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-cfg"}}}},
					Env: []corev1.EnvVar{
						{Name: "DATABASE_URL", Value: "postgres://orders:Hunter2@$(DB_HOST):$(DB_PORT)/orders"},
						{Name: "SEARCH_URL", Value: "http://search.data.svc.cluster.local:9200"},
						{Name: "RATES_API", Value: "https://api.exchangerate.example.com/v1"},
						{Name: "SESSION_STORE", Value: "redis.data:6379"},
						{Name: "QUEUE_ENDPOINT", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "extra"}, Key: "QUEUE_ENDPOINT"}}},
						{Name: "OPTIONAL_THING", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "missing"}, Key: "X", Optional: boolPtr(true)}}},
						{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "db-creds"}, Key: "password"}}},
					},
					Args: []string{"--redis-addr", "carts.abc123.use1.cache.amazonaws.com:6379", "--listen=0.0.0.0:8080", "--log-level=debug"},
				},
				{
					Name:    "cloudsql-proxy",
					Args:    []string{"--structured-logs", "shop-prod:us-central1:orders-db?port=5432"},
					EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "proxy-creds"}}}},
				},
			},
		}}},
	}
	svcs := []interface{}{
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db-alias"},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: "Orders.Cluster-C9X.us-east-1.rds.amazonaws.com.",
				Ports: []corev1.ServicePort{{Port: 5432}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "inventory-alias"},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: "inventory.data.svc.cluster.local"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "legacy-erp"},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "web"}}},
	}
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "legacy-erp-abc",
			Labels: map[string]string{discoveryv1.LabelServiceName: "legacy-erp"}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.20.0.5"}}},
		Ports:     []discoveryv1.EndpointPort{{Port: int32Ptr(8443)}},
	}
	return &Connector{
		deploymentLister:    appslisters.NewDeploymentLister(indexer(t, deploy)),
		configMapLister:     corelisters.NewConfigMapLister(indexer(t, appCfg, extra)),
		serviceLister:       corelisters.NewServiceLister(indexer(t, svcs...)),
		endpointSliceLister: discoverylisters.NewEndpointSliceLister(indexer(t, slice)),
		namespaceLister: corelisters.NewNamespaceLister(indexer(t,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "data"}})),
	}
}

func TestWorkloadDependencyHosts_Deployment(t *testing.T) {
	c := depFixture(t)
	got, err := c.WorkloadDependencyHosts("Deployment", "shop", "orders")
	if err != nil {
		t.Fatal(err)
	}
	want := []DependencyHost{
		{Source: "command (init container migrate)", Scheme: "postgres", Host: "legacy-db.corp.example.com", Port: "5432"},
		{Source: "configmap app-cfg/CACHE_URL via envFrom (container app)", Scheme: "redis", Host: "sessions.abc123.use1.cache.amazonaws.com", Port: "6379"},
		{Source: "configmap app-cfg/DB_HOST via envFrom (container app)", Host: "orders.cluster-c9x.us-east-1.rds.amazonaws.com", Port: "5432"},
		{Source: "env DATABASE_URL (container app)", Scheme: "postgres", Host: "orders.cluster-c9x.us-east-1.rds.amazonaws.com", Port: "5432"},
		{Source: "env RATES_API (container app)", Scheme: "https", Host: "api.exchangerate.example.com"},
		{Source: "configmap extra/QUEUE_ENDPOINT via env QUEUE_ENDPOINT (container app)", Scheme: "https", Host: "sqs.us-east-1.amazonaws.com"},
		{Source: "args (container app)", Host: "carts.abc123.use1.cache.amazonaws.com", Port: "6379"},
		{Source: "args (container cloudsql-proxy)", Scheme: "cloudsql", Host: "shop-prod:us-central1:orders-db"},
		{Source: "service externalName db-alias", Host: "orders.cluster-c9x.us-east-1.rds.amazonaws.com", Port: "5432"},
		{Source: "service externalName inventory-alias", Host: "inventory.data.svc.cluster.local"},
		{Source: "service legacy-erp (selectorless endpoints)", Host: "10.20.0.5", Port: "8443"},
		{Source: "secret db-creds/password via env DB_PASSWORD (container app): not read"},
		{Source: "secret proxy-creds via envFrom (container cloudsql-proxy): not read"},
	}
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", "  ")
		t.Errorf("WorkloadDependencyHosts mismatch; got:\n%s", gb)
	}

	// Nothing credential-shaped, no path and no in-cluster name leaks through.
	raw, _ := json.Marshal(got)
	for _, bad := range []string{"S3cr3t", "Hunter2", "admin", "/orders", "/v1", "123456789012", "search.data", "redis.data", "0.0.0.0", "info", "debug"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("result contains %q: %s", bad, raw)
		}
	}
}

func TestWorkloadDependencyHosts_KindsAndErrors(t *testing.T) {
	c := depFixture(t)

	if _, err := c.WorkloadDependencyHosts("deployments", "shop", "orders"); err != nil {
		t.Errorf("plural kind: %v", err)
	}
	if _, err := c.WorkloadDependencyHosts("Deployment", "shop", "nope"); !apierrors.IsNotFound(err) {
		t.Errorf("missing workload: err = %v, want NotFound", err)
	}
	if _, err := c.WorkloadDependencyHosts("Gizmo", "shop", "orders"); err == nil {
		t.Error("unsupported kind: want an error")
	}
	var pd *PermissionDeniedError
	if _, err := c.WorkloadDependencyHosts("StatefulSet", "shop", "orders"); !errors.As(err, &pd) {
		t.Errorf("nil StatefulSet lister: err = %v, want PermissionDeniedError", err)
	}
	var nilConn *Connector
	if _, err := nilConn.WorkloadDependencyHosts("Deployment", "shop", "orders"); err == nil {
		t.Error("nil connector: want an error")
	}
}

// Partial RBAC: without the ConfigMap informer the references are reported
// as unreadable leads instead of silently disappearing; without the Service
// informers the Service pass is skipped, not failed.
func TestWorkloadDependencyHosts_PartialRBAC(t *testing.T) {
	c := depFixture(t)
	c.configMapLister = nil
	c.serviceLister = nil
	c.endpointSliceLister = nil
	c.namespaceLister = nil
	got, err := c.WorkloadDependencyHosts("Deployment", "shop", "orders")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]DependencyHost{}
	for _, h := range got {
		sources[h.Source] = h
	}
	for _, s := range []string{
		"configmap app-cfg via envFrom (container app): not readable",
		"configmap extra/QUEUE_ENDPOINT via env QUEUE_ENDPOINT (container app): not readable",
		"configmap missing/X via env OPTIONAL_THING (container app): not readable",
	} {
		h, ok := sources[s]
		if !ok || h.Host != "" {
			t.Errorf("missing unreadable-configmap lead %q in %+v", s, got)
		}
	}
	// $(DB_HOST) cannot be expanded any more, so DATABASE_URL yields nothing,
	// and without the namespace lister "redis.data" can no longer be told
	// apart from an external two-label name.
	for _, h := range got {
		if strings.Contains(h.Host, "$(") || strings.HasPrefix(h.Source, "service ") {
			t.Errorf("unexpected entry %+v", h)
		}
	}
	if _, ok := sources["env SESSION_STORE (container app)"]; !ok {
		t.Errorf("without a namespace lister, redis.data:6379 should be kept; got %+v", got)
	}
}

func TestWorkloadDependencyHosts_PodAndCronJob(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "worker-0"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "worker",
			Env: []corev1.EnvVar{
				{Name: "PGHOST", Value: "pg-orders.postgres.database.azure.com"},
				{Name: "PGPORT", Value: "5432"},
				{Name: "SQL_CONN", Value: "Server=tcp:orders-sql.database.windows.net,1433;Password=Hunter2;"},
			},
		}}},
	}
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "nightly"},
		Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "export",
				Args: []string{"--bucket-endpoint=https://media.s3.us-east-1.amazonaws.com"},
			}}},
		}}}},
	}
	c := &Connector{
		podLister:     corelisters.NewPodLister(indexer(t, pod)),
		cronJobLister: batchlisters.NewCronJobLister(indexer(t, cj)),
	}

	got, err := c.WorkloadDependencyHosts("Pod", "shop", "worker-0")
	if err != nil {
		t.Fatal(err)
	}
	want := []DependencyHost{
		{Source: "env PGHOST (container worker)", Host: "pg-orders.postgres.database.azure.com", Port: "5432"},
		{Source: "env SQL_CONN (container worker)", Scheme: "sqlserver", Host: "orders-sql.database.windows.net", Port: "1433"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pod: got %+v, want %+v", got, want)
	}

	got, err = c.WorkloadDependencyHosts("cronjob", "shop", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	want = []DependencyHost{{Source: "args (container export)", Scheme: "https", Host: "media.s3.us-east-1.amazonaws.com"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cronjob: got %+v, want %+v", got, want)
	}
}

// ---- Services the configuration names in another namespace ----

// crossNSConnector puts Deployment shop-backend/orders-api, one container
// "api" with env, next to the namespaces shop-backend, shop-data and
// shop-edge, and the given Services and EndpointSlices.
func crossNSConnector(t *testing.T, env []corev1.EnvVar, objs ...interface{}) *Connector {
	t.Helper()
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop-backend", Name: "orders-api"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "api", Env: env}},
		}}},
	}
	var svcs, slices []interface{}
	for _, o := range objs {
		if _, ok := o.(*discoveryv1.EndpointSlice); ok {
			slices = append(slices, o)
		} else {
			svcs = append(svcs, o)
		}
	}
	return &Connector{
		deploymentLister:    appslisters.NewDeploymentLister(indexer(t, deploy)),
		serviceLister:       corelisters.NewServiceLister(indexer(t, svcs...)),
		endpointSliceLister: discoverylisters.NewEndpointSliceLister(indexer(t, slices...)),
		namespaceLister: corelisters.NewNamespaceLister(indexer(t,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop-backend"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop-data"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop-edge"}})),
	}
}

func externalNameService(namespace, name, target string, ports ...int32) *corev1.Service {
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: target},
	}
	for _, p := range ports {
		s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{Port: p})
	}
	return s
}

func dbHostEnv(host string) []corev1.EnvVar {
	return []corev1.EnvVar{{Name: "DB_HOST", Value: host}, {Name: "DB_PORT", Value: "5432"}}
}

func orderAPIHosts(t *testing.T, c *Connector) []DependencyHost {
	t.Helper()
	got, err := c.WorkloadDependencyHosts("Deployment", "shop-backend", "orders-api")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

const azurePG = "pg-orders.postgres.database.azure.com"

// The 2026-09-24 miss: DB_HOST names a Service of ANOTHER namespace, and that
// Service is an ExternalName to the managed database. The in-cluster name was
// dropped and only the workload's own namespace was scanned, so the answer
// was empty and Kobi said it could not find the external host.
func TestWorkloadDependencyHosts_ExternalNameInAnotherNamespace(t *testing.T) {
	c := crossNSConnector(t, dbHostEnv("shop-db.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "shop-db", azurePG))
	want := []DependencyHost{{
		Source: "env DB_HOST (container api) → service shop-data/shop-db (ExternalName)",
		Host:   azurePG, Port: "5432",
	}}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// Every name a pod can reach a Service by leads to it; a bare name resolves in
// the workload's own namespace.
func TestWorkloadDependencyHosts_ServiceNameForms(t *testing.T) {
	for _, tc := range []struct {
		host, svc string
		objs      []interface{}
	}{
		{"shop-db.shop-data.svc.cluster.local", "shop-data/shop-db", []interface{}{externalNameService("shop-data", "shop-db", azurePG)}},
		{"shop-db.shop-data.svc", "shop-data/shop-db", []interface{}{externalNameService("shop-data", "shop-db", azurePG)}},
		{"shop-db.shop-data", "shop-data/shop-db", []interface{}{externalNameService("shop-data", "shop-db", azurePG)}},
		{"shop-db.shop-data.svc.corp.local", "shop-data/shop-db", []interface{}{externalNameService("shop-data", "shop-db", azurePG)}},
		{"shop-db", "shop-backend/shop-db", []interface{}{externalNameService("shop-backend", "shop-db", azurePG)}},
	} {
		t.Run(tc.host, func(t *testing.T) {
			got := orderAPIHosts(t, crossNSConnector(t, dbHostEnv(tc.host), tc.objs...))
			want := []DependencyHost{{
				Source: "env DB_HOST (container api) → service " + tc.svc + " (ExternalName)",
				Host:   azurePG, Port: "5432",
			}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v\nwant %+v", got, want)
			}
		})
	}
}

// A bare name in the workload's own namespace is also what the namespace scan
// finds: the target is listed once, by the chain that says which variable
// points at it.
func TestWorkloadDependencyHosts_SameNamespaceServiceListedOnce(t *testing.T) {
	c := crossNSConnector(t, []corev1.EnvVar{{Name: "DB_HOST", Value: "shop-db"}},
		externalNameService("shop-backend", "shop-db", azurePG, 5432))
	want := []DependencyHost{{
		Source: "env DB_HOST (container api) → service shop-backend/shop-db (ExternalName)",
		Host:   azurePG, Port: "5432",
	}}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestWorkloadDependencyHosts_ExternalNameChains(t *testing.T) {
	t.Run("two hops", func(t *testing.T) {
		c := crossNSConnector(t, dbHostEnv("db.shop-edge"),
			externalNameService("shop-edge", "db", "shop-db.shop-data.svc.cluster.local"),
			externalNameService("shop-data", "shop-db", azurePG))
		want := []DependencyHost{{
			Source: "env DB_HOST (container api) → service shop-edge/db (ExternalName) → service shop-data/shop-db (ExternalName)",
			Host:   azurePG, Port: "5432",
		}}
		if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v\nwant %+v", got, want)
		}
	})
	t.Run("loop", func(t *testing.T) {
		c := crossNSConnector(t, dbHostEnv("a.shop-data.svc.cluster.local"),
			externalNameService("shop-data", "a", "b.shop-data.svc.cluster.local"),
			externalNameService("shop-data", "b", "a.shop-data.svc.cluster.local"))
		want := []DependencyHost{{
			Source: "env DB_HOST (container api) → service shop-data/a (ExternalName) → service shop-data/b (ExternalName): loops back to service shop-data/a",
		}}
		if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v\nwant %+v", got, want)
		}
	})
	t.Run("longer than three hops", func(t *testing.T) {
		c := crossNSConnector(t, dbHostEnv("h1.shop-data"),
			externalNameService("shop-data", "h1", "h2.shop-data.svc.cluster.local"),
			externalNameService("shop-data", "h2", "h3.shop-data.svc.cluster.local"),
			externalNameService("shop-data", "h3", "h4.shop-data.svc.cluster.local"),
			externalNameService("shop-data", "h4", azurePG))
		want := []DependencyHost{{
			Source: "env DB_HOST (container api) → service shop-data/h1 (ExternalName) → service shop-data/h2 (ExternalName) → service shop-data/h3 (ExternalName) → service shop-data/h4: not followed, more than 3 ExternalName hops",
		}}
		if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v\nwant %+v", got, want)
		}
	})
}

// A selectorless Service in another namespace leads to the addresses behind
// it, with the configured scheme.
func TestWorkloadDependencyHosts_SelectorlessInAnotherNamespace(t *testing.T) {
	env := []corev1.EnvVar{{Name: "ERP_URL", Value: "https://legacy-erp.shop-data.svc.cluster.local/api"}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop-data", Name: "legacy-erp"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 443}}}}
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop-data", Name: "legacy-erp-abc",
			Labels: map[string]string{discoveryv1.LabelServiceName: "legacy-erp"}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.20.0.5"}}},
		Ports:     []discoveryv1.EndpointPort{{Port: int32Ptr(8443)}},
	}
	c := crossNSConnector(t, env, svc, slice)
	want := []DependencyHost{{
		Source: "env ERP_URL (container api) → service shop-data/legacy-erp (selectorless endpoints)",
		Scheme: "https", Host: "10.20.0.5", Port: "8443",
	}}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}

	c.endpointSliceLister = nil
	want = []DependencyHost{{
		Source: "env ERP_URL (container api) → service shop-data/legacy-erp (selectorless endpoints): endpoints not readable",
	}}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("without EndpointSlices: got %+v\nwant %+v", got, want)
	}

	// No endpoints at all: the Service the configuration names leads nowhere,
	// which is worth saying.
	c = crossNSConnector(t, env, svc)
	want = []DependencyHost{{
		Source: "env ERP_URL (container api) → service shop-data/legacy-erp (selectorless endpoints): no endpoints",
	}}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("with no endpoints: got %+v\nwant %+v", got, want)
	}
}

// A name that is no Service, and an ordinary Service with a selector, are
// in-cluster and say nothing about external dependencies.
func TestWorkloadDependencyHosts_OrdinaryInClusterNamesStayOut(t *testing.T) {
	ordinary := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop-data", Name: "catalog"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "catalog"},
			Ports: []corev1.ServicePort{{Port: 8080}}}}
	for _, host := range []string{"nope.shop-data.svc.cluster.local", "catalog.shop-data.svc.cluster.local", "catalog.shop-data"} {
		c := crossNSConnector(t, dbHostEnv(host), ordinary)
		if got := orderAPIHosts(t, c); len(got) != 0 {
			t.Errorf("DB_HOST=%s: want nothing, got %+v", host, got)
		}
	}
}

// Where the Services of a namespace cannot be read, the answer says so
// instead of going quiet: no Service informer at all, or a namespace-scoped
// ServiceAccount whose informers do not cover that namespace.
func TestWorkloadDependencyHosts_ServiceNamespaceNotReadable(t *testing.T) {
	want := []DependencyHost{{Source: "services not readable in shop-data"}}

	c := crossNSConnector(t, dbHostEnv("shop-db.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "shop-db", azurePG))
	c.serviceLister = nil
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("no Service lister: got %+v\nwant %+v", got, want)
	}

	c = crossNSConnector(t, dbHostEnv("shop-db.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "shop-db", azurePG))
	c.permissions = ResourcePermissions{"services": {Resource: "services", CanList: true, CanWatch: true,
		NamespaceScoped: true, Namespaces: []string{"shop-backend"}}}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("namespace outside the ServiceAccount's scope: got %+v\nwant %+v", got, want)
	}
}

// A Service that aliases a host is evidence of its own next to a variable
// that names the same host directly: the chain's entry must not be taken for
// a duplicate of the variable's, nor the Service dropped from the scan.
func TestWorkloadDependencyHosts_AliasBesideTheSameHost(t *testing.T) {
	env := []corev1.EnvVar{
		{Name: "PGHOST", Value: azurePG}, {Name: "PGPORT", Value: "5432"},
		{Name: "DB_HOST", Value: "shop-db"}, {Name: "DB_PORT", Value: "5432"},
	}
	c := crossNSConnector(t, env, externalNameService("shop-backend", "shop-db", azurePG, 5432))
	want := []DependencyHost{
		{Source: "env PGHOST (container api)", Host: azurePG, Port: "5432"},
		{Source: "env DB_HOST (container api) → service shop-backend/shop-db (ExternalName)", Host: azurePG, Port: "5432"},
	}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// An alias the configuration names that leads nowhere outside the cluster is
// still listed, with the reason: a dangling alias is a cause of "cannot
// connect", and naming it in the configuration must not hide it.
func TestWorkloadDependencyHosts_AliasDeadEnds(t *testing.T) {
	env := []corev1.EnvVar{{Name: "CACHE_HOST", Value: "cache-alias"}, {Name: "CACHE_PORT", Value: "6379"}}
	catalog := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop-data", Name: "catalog"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "catalog"}}}
	const alias = "env CACHE_HOST (container api) → service shop-backend/cache-alias (ExternalName)"
	for _, tc := range []struct{ target, want string }{
		{"missing.shop-data.svc.cluster.local", alias + " → service shop-data/missing: no such Service"},
		{"redis-0.redis.shop-data.svc.cluster.local", alias + " → redis-0.redis.shop-data.svc.cluster.local: inside the cluster"},
		{"catalog.shop-data.svc.cluster.local", alias + " → service shop-data/catalog: inside the cluster"},
		{"kubernetes.default.svc.cluster.local", alias + " → service default/kubernetes: inside the cluster"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			c := crossNSConnector(t, env, externalNameService("shop-backend", "cache-alias", tc.target), catalog)
			want := []DependencyHost{{Source: tc.want}}
			if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v\nwant %+v", got, want)
			}
		})
	}
}

// An ExternalName's target is a CNAME, resolved as an absolute name with no
// search path: only "<svc>.<ns>.svc.<cluster domain>" (trailing dot allowed)
// names a Service. A shorter form resolves to nothing, whatever Service a pod
// would reach by the same name. (The workload's own pg is listed by the scan
// of its namespace, not by the chain.)
func TestWorkloadDependencyHosts_ExternalNameTargetsAreAbsolute(t *testing.T) {
	const via = "env DB_HOST (container api) → service shop-data/shop-db (ExternalName)"
	for _, target := range []string{"pg", "pg.shop-edge", "pg.shop-edge.svc"} {
		t.Run(target, func(t *testing.T) {
			c := crossNSConnector(t, dbHostEnv("shop-db.shop-data.svc.cluster.local"),
				externalNameService("shop-data", "shop-db", target),
				externalNameService("shop-backend", "pg", "own.example.com"),
				externalNameService("shop-data", "pg", "wrong.example.com"),
				externalNameService("shop-edge", "pg", "wrong.example.com"))
			want := []DependencyHost{
				{Source: "service externalName pg", Host: "own.example.com"},
				{Source: via + " → " + target + ": not followed, not a full Service name (CNAME targets get no search path)"},
			}
			if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v\nwant %+v", got, want)
			}
		})
	}
	c := crossNSConnector(t, dbHostEnv("shop-db.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "shop-db", "pg.shop-edge.svc.cluster.local."),
		externalNameService("shop-edge", "pg", azurePG))
	want := []DependencyHost{{Source: via + " → service shop-edge/pg (ExternalName)", Host: azurePG, Port: "5432"}}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("full name with a trailing dot: got %+v\nwant %+v", got, want)
	}
}

// The hop limit is named only when there is a fourth Service to follow; a
// fourth name that is no Service is a dangling alias, and says so.
func TestWorkloadDependencyHosts_HopLimitNeedsAFourthService(t *testing.T) {
	c := crossNSConnector(t, dbHostEnv("h1.shop-data"),
		externalNameService("shop-data", "h1", "h2.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "h2", "h3.shop-data.svc.cluster.local"),
		externalNameService("shop-data", "h3", "ghost.shop-data.svc.cluster.local"))
	want := []DependencyHost{{
		Source: "env DB_HOST (container api) → service shop-data/h1 (ExternalName) → service shop-data/h2 (ExternalName) → service shop-data/h3 (ExternalName) → service shop-data/ghost: no such Service",
	}}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// Services that cannot be read are reported once per namespace, however many
// names the configuration has there: the references share one cap, and a
// Secret's must not be crowded out of it.
func TestWorkloadDependencyHosts_UnreadableServicesOncePerNamespace(t *testing.T) {
	var initEnv []corev1.EnvVar
	for i := 0; i < maxDependencyRefs+10; i++ {
		initEnv = append(initEnv, corev1.EnvVar{Name: fmt.Sprintf("SVC%d_HOST", i), Value: fmt.Sprintf("svc%d.shop-data.svc.cluster.local", i)})
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop-backend", Name: "orders-api"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "wait", Env: initEnv}},
			Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{
				{Name: "DATABASE_URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "db-url"}, Key: "url"}}},
			}}},
		}}},
	}
	c := &Connector{deploymentLister: appslisters.NewDeploymentLister(indexer(t, deploy))}
	want := []DependencyHost{
		{Source: "services not readable in shop-data"},
		{Source: "secret db-url/url via env DATABASE_URL (container api): not read"},
	}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// An alias of the workload's namespace pointing into a namespace whose
// Services cannot be read: the unreadable namespace is named, and the alias
// is still listed by the scan of the workload's namespace.
func TestWorkloadDependencyHosts_AliasIntoUnreadableNamespace(t *testing.T) {
	c := crossNSConnector(t, dbHostEnv("shop-db"),
		externalNameService("shop-backend", "shop-db", "pg.shop-data.svc.cluster.local"))
	c.permissions = ResourcePermissions{"services": {Resource: "services", CanList: true, CanWatch: true,
		NamespaceScoped: true, Namespaces: []string{"shop-backend"}}}
	want := []DependencyHost{
		{Source: "service externalName shop-db", Host: "pg.shop-data.svc.cluster.local"},
		{Source: "services not readable in shop-data"},
	}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// A bucket names storage, not a host. nat-route-removed (2026-10-22):
// catalog-api read its bucket from FEATURED_BUCKET, and with only its
// database listed Kobi chased the database.
func TestParseDependencyValue_Buckets(t *testing.T) {
	cases := []struct {
		value string
		want  []string
	}{
		{"s3://tienda-featured/featured.json", []string{"s3|tienda-featured|"}},
		{"s3a://data.lake.raw/events/", []string{"s3|data.lake.raw|"}},
		{`{"Resource":"arn:aws:s3:::tienda-featured/*"}`, []string{"s3|tienda-featured|"}},
		{"arn:aws-us-gov:s3:::gov-bucket", []string{"s3|gov-bucket|"}},
		// Its https endpoints were already hosts, and stay so.
		{"https://tienda-featured.s3.us-east-2.amazonaws.com/featured.json", []string{"https|tienda-featured.s3.us-east-2.amazonaws.com|"}},
		// Not a bucket: upper case, an address, too short.
		{"s3://Not_A_Bucket/x", nil},
		{"s3://10.0.0.1/x", nil},
		{"s3://ab", nil},
	}
	for _, c := range cases {
		if got := refStrings(parseDependencyValue(c.value, false)); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%q: got %v, want %v", c.value, got, c.want)
		}
	}
}

func TestBucketVar(t *testing.T) {
	for _, c := range []struct {
		name, value, want string
		ok                bool
	}{
		{"FEATURED_BUCKET", "tienda-featured-111122223333", "tienda-featured-111122223333", true},
		{"BUCKET_NAME", "'logs.archive'", "logs.archive", true},
		{"bucket", "uploads", "uploads", true},
		{"LOGS_BUCKET", "My_Bucket", "", false},
		{"FEATURED_BUCKET", "10.0.0.1", "", false},
		{"BUCKETS", "a-b-c", "", false},
		{"DB_HOST", "tienda-featured", "", false},
	} {
		got, ok := bucketVar(c.name, c.value)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s=%q: got %q %v, want %q %v", c.name, c.value, got, ok, c.want, c.ok)
		}
	}
}

// A bucket is never an in-cluster name, even when it has no dot.
func TestWorkloadDependencyHosts_BucketsAreListed(t *testing.T) {
	c := crossNSConnector(t, []corev1.EnvVar{
		{Name: "FEATURED_BUCKET", Value: "tienda-featured-111122223333"},
		{Name: "EXPORT_URL", Value: "s3://tienda-exports/daily/"},
		{Name: "MODE", Value: "catalog"},
	})
	want := []DependencyHost{
		{Source: "env FEATURED_BUCKET (container api)", Scheme: "bucket", Host: "tienda-featured-111122223333"},
		{Source: "env EXPORT_URL (container api)", Scheme: "s3", Host: "tienda-exports"},
	}
	if got := orderAPIHosts(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
