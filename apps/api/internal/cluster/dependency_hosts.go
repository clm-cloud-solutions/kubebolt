package cluster

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// DependencyHost is one endpoint a workload's configuration points at: where
// it was found and the scheme/host/port parsed out of it — nothing else.
//
// Why this exists: without Hubble there is no flow data, and the next best
// answer to "what does this workload depend on outside the cluster?" is in
// its env vars, ConfigMaps and args. Those values are exactly where
// credentials live (DATABASE_URL=postgres://user:pass@host/db), and the YAML
// and detail tools hand them to the model verbatim. This walks the same
// sources and returns only the parsed endpoint, so the connection string
// itself never leaves the process.
//
// A Secret reference is reported with its Source and an empty Host: the
// Secret is never read, but knowing a DSN comes from `secret db/url` is still
// a lead. So is a ConfigMap or a namespace's Services that cannot be read,
// and a Service chain that ends without an address. Host is lower-case; for
// Scheme "cloudsql" it is a Cloud SQL instance connection name
// ("project:region:instance"), not a hostname; for "s3" an S3 bucket name
// (from s3:// or an S3 ARN); for "bucket" a bucket name read from a variable
// named like one (FEATURED_BUCKET), whose cloud the value does not say.
type DependencyHost struct {
	// Source says where the value was found, e.g. "env DATABASE_URL
	// (container app)", "configmap app-cfg/DB_HOST via env DB_HOST (container
	// app)", "service externalName db-alias", or the Services a configured
	// in-cluster name led through: "env DB_HOST (container app) → service
	// shop-data/shop-db (ExternalName)".
	Source string `json:"source"`
	Scheme string `json:"scheme,omitempty"`
	Host   string `json:"host,omitempty"`
	Port   string `json:"port,omitempty"`
}

// Bounds: a ConfigMap can embed whole config files and a namespace can hold
// hundreds of Services. The answer is a short list for a model to read.
const (
	maxDependencyHosts    = 100
	maxDependencyRefs     = 50
	maxDependencyValueLen = 8 << 10
	maxConfigMapKeys      = 500
	// maxServiceHops bounds how many Services an ExternalName chain is
	// followed through: a real alias chain is one or two Services deep.
	maxServiceHops = 3
)

// hostRef is one parsed endpoint.
type hostRef struct{ scheme, host, port string }

// namesResource reports whether host is a resource name rather than a DNS
// name: never in-cluster, and never given a companion port.
func (r hostRef) namesResource() bool {
	return r.scheme == "cloudsql" || r.scheme == "s3" || r.scheme == "bucket"
}

// Buckets name storage, not hosts, so the URL and host passes skipped them:
// catalog-api read its bucket from FEATURED_BUCKET, get_external_connections
// listed only its database, and with the cluster's agent cut off Kobi chased
// the database while the outage was the bucket's route out of the VPC
// (nat-route-removed, 2026-10-22). An S3 bucket is named by s3:// (and
// Hadoop's s3a://, s3n://) or an S3 ARN; a variable named like a bucket
// holding a bare bucket name names one in whatever cloud the cluster runs in.
// Its virtual-hosted and path-style https URLs were already hosts.
var (
	bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3ARNRE      = regexp.MustCompile(`arn:aws[a-z-]*:s3:::([a-z0-9][a-z0-9.-]{1,61}[a-z0-9])`)
	s3Schemes    = map[string]bool{"s3": true, "s3a": true, "s3n": true}
)

// validBucket reports whether s follows the S3 bucket naming rules: 3 to 63
// lower-case letters, digits, dots and hyphens, no "..", not an address.
func validBucket(s string) bool {
	if !bucketNameRE.MatchString(s) || strings.Contains(s, "..") {
		return false
	}
	_, err := netip.ParseAddr(s)
	return err != nil
}

// bucketVar returns the bucket a variable holds when its name marks it as
// one (FEATURED_BUCKET, BUCKET_NAME, LOGS_BUCKET_NAME) and its value is a
// bare bucket name.
func bucketVar(name, value string) (string, bool) {
	n := normalizeVarName(name)
	if n != "BUCKET" && !strings.HasSuffix(n, "_BUCKET") && !strings.HasSuffix(n, "BUCKET_NAME") {
		return "", false
	}
	v := strings.Trim(strings.TrimSpace(value), `"'`)
	return v, validBucket(v)
}

// networkSchemes are URL schemes whose authority is a network host. Anything
// else (s3://bucket, gs://bucket, file://) names something that is not a host.
var networkSchemes = map[string]bool{
	"postgres": true, "postgresql": true, "mysql": true, "mariadb": true,
	"sqlserver": true, "mssql": true, "oracle": true, "cockroachdb": true,
	"redis": true, "rediss": true, "memcached": true,
	"amqp": true, "amqps": true, "kafka": true, "nats": true,
	"mongodb": true, "mongodb+srv": true, "cassandra": true, "clickhouse": true,
	"http": true, "https": true, "ws": true, "wss": true,
	"grpc": true, "grpcs": true, "tcp": true, "tls": true,
	"ldap": true, "ldaps": true, "smtp": true, "smtps": true, "ftp": true, "sftp": true,
	"sb": true, // Azure Service Bus / Event Hubs "Endpoint=sb://<ns>.servicebus.windows.net/"
}

var (
	// urlRE finds "scheme://" (optionally JDBC-prefixed) anywhere in a value;
	// the authority is cut out by hand so userinfo never reaches a regexp
	// capture that could be logged or returned.
	urlRE = regexp.MustCompile(`(?i)(?:jdbc:)?([a-z][a-z0-9+.\-]*)://`)
	// hostnameRE: dotted DNS name whose last label has a letter (so "1.2.3"
	// version strings are not hosts). Single-label names are handled apart.
	hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9-]*[a-z][a-z0-9-]*$`)
	portRE     = regexp.MustCompile(`^[0-9]{1,5}$`)
	// Cloud SQL instance connection name, optionally followed by the proxy's
	// "=tcp:5432" (v1 -instances) or "?port=5432" (v2) listener suffix.
	cloudSQLRE = regexp.MustCompile(`^((?:[a-z0-9.-]+:)?[a-z][a-z0-9-]{4,29}:[a-z]+-[a-z]+[0-9]+:[a-z0-9][a-z0-9-]{0,97})(?:$|[=?])`)
)

// hostKeys are key=value keys (connection-string keywords, flag names) whose
// value is a host.
var hostKeys = map[string]bool{
	"server": true, "data source": true, "datasource": true, "addr": true, "address": true,
	"network address": true, "host": true, "hostname": true, "hostaddr": true,
	"servername": true, "endpoint": true,
}

// hostNameSuffixes mark an env var or flag name as holding host(s), which
// lets a bare hostname without a port count (DB_HOST=mydb.example.com).
var hostNameSuffixes = []string{"HOSTNAME", "HOSTS", "HOST", "SERVERS", "SERVER", "ADDRESS", "ADDR", "ENDPOINT", "BROKERS"}

// normalizeVarName upper-cases an env var, ConfigMap key or flag name and
// maps '-' and '.' to '_' ("--db-host" → "DB_HOST", "db.password" →
// "DB_PASSWORD").
func normalizeVarName(name string) string {
	n := strings.ToUpper(strings.TrimLeft(strings.TrimSpace(name), "-"))
	return strings.ReplaceAll(strings.ReplaceAll(n, "-", "_"), ".", "_")
}

// credentialParts mark a variable or flag whose value is a credential,
// anywhere in the name (PGPASSWORD, JWT_SECRET); credentialWords only as a
// whole '_'-separated word, being too short to match inside other words
// (BYPASS, KEYCLOAK).
var (
	credentialParts = []string{"PASSWORD", "PASSWD", "PASSPHRASE", "SECRET", "TOKEN", "CREDENTIAL", "APIKEY"}
	credentialWords = map[string]bool{"PASS": true, "PWD": true, "KEY": true}
	// endpointSuffixes end a name that holds an address even when it also
	// carries a credential word (TOKEN_URL, KEY_VAULT_URI, SECRETS_ENDPOINT).
	endpointSuffixes = []string{"URL", "URI", "DSN", "HOST", "HOSTNAME", "HOSTS", "ENDPOINT", "ADDR", "ADDRESS", "SERVER", "SERVERS", "BROKERS"}
)

// isCredentialName reports whether a variable or flag holds a credential
// (DB_PASSWORD, --api-token, AZURE_STORAGE_KEY). Such values are never
// parsed: a password shaped like "a.b:1234" would otherwise come back as
// host:port.
func isCredentialName(name string) bool {
	n := normalizeVarName(name)
	for _, s := range endpointSuffixes {
		if strings.HasSuffix(n, s) {
			return false
		}
	}
	for _, p := range credentialParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	for _, w := range strings.Split(n, "_") {
		if credentialWords[w] {
			return true
		}
	}
	return false
}

// hostVarPrefix reports whether name (env var or flag) names a host and
// returns the prefix its companion port variable would share: DB_HOST → DB_
// (DB_PORT), PGHOST → PG (PGPORT).
func hostVarPrefix(name string) (string, bool) {
	n := normalizeVarName(name)
	for _, s := range hostNameSuffixes {
		if strings.HasSuffix(n, s) {
			return strings.TrimSuffix(n, s), true
		}
	}
	return "", false
}

// parseDependencyValue extracts endpoints from one configuration value.
// hostKey says the value's name marks it as a host, so a bare hostname
// without a port is accepted; otherwise only unambiguous shapes count
// (URLs, host:port, connection-string host keywords, Cloud SQL names).
//
// It never returns userinfo, paths, query strings or the raw value: every
// result is rebuilt from a validated host and a numeric port.
func parseDependencyValue(v string, hostKey bool) []hostRef {
	v = strings.TrimSpace(v)
	truncated := len(v) > maxDependencyValueLen
	if truncated {
		v = v[:maxDependencyValueLen]
	}
	v = strings.Trim(v, `"'`)
	if v == "" {
		return nil
	}
	var out []hostRef

	// 1. URLs and JDBC URLs, possibly several in one value. Every URL's
	// userinfo (and all of an ambiguous URL) is blanked out of kvText, so the
	// key=value pass below never reads a password: "u:p;host=x.example.com;q@db"
	// is a valid userinfo, not a Host keyword.
	urls := urlRE.FindAllStringSubmatchIndex(v, -1)
	kvText := v
	if len(urls) > 0 {
		masked := []byte(v)
		for i, m := range urls {
			// Each URL ends where the next one starts, so a list of URLs cannot
			// lend one URL's '@' to another's authority.
			end := len(v)
			if i+1 < len(urls) {
				end = urls[i+1][0]
			}
			// A URL running into the truncation point may have lost the '@'
			// that ends its userinfo: unless it visibly ends before the cut,
			// it is skipped and hidden whole.
			var auth string
			hide := end - m[1]
			if !(truncated && i == len(urls)-1) || urlClosed(v[m[1]:end]) {
				jdbc := strings.HasPrefix(strings.ToLower(v[m[0]:m[1]]), "jdbc:")
				auth, hide = urlAuthority(v[m[1]:end], jdbc)
			}
			for j := m[1]; j < m[1]+hide; j++ {
				masked[j] = ' '
			}
			scheme := strings.ToLower(v[m[2]:m[3]])
			if networkSchemes[scheme] {
				out = append(out, parseAuthority(scheme, auth)...)
			} else if s3Schemes[scheme] && validBucket(auth) {
				out = append(out, hostRef{scheme: "s3", host: auth})
			}
		}
		kvText = string(masked)
	}
	for _, m := range s3ARNRE.FindAllStringSubmatch(v, -1) {
		if validBucket(m[1]) {
			out = append(out, hostRef{scheme: "s3", host: m[1]})
		}
	}

	// 2. key=value pairs (ADO.NET, libpq keyword DSNs, StackExchange.Redis,
	// JDBC properties) and bare tokens. A ';' means ADO.NET-style, where ','
	// belongs to the value (Server=tcp:host,1433); otherwise ',' and
	// whitespace separate tokens.
	// Quoted segments (Password="a;host=b") stay whole, so a secret value can
	// never be split into something that reads as a host keyword.
	adoStyle := strings.Contains(kvText, ";")
	parts := splitOutsideQuotes(kvText, func(r rune) bool {
		if adoStyle {
			return r == ';'
		}
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	var kvHosts []string
	kvPort, account, suffix := "", "", ""
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if ref, ok := parseCloudSQLName(p); ok {
			out = append(out, ref)
			continue
		}
		if k, val, ok := strings.Cut(p, "="); ok && !strings.Contains(k, "://") {
			key := strings.ToLower(strings.TrimSpace(k))
			switch {
			case hostKeys[key]:
				kvHosts = append(kvHosts, strings.TrimSpace(val))
			case key == "port":
				kvPort = strings.TrimSpace(val)
			case key == "accountname":
				account = strings.TrimSpace(val)
			case key == "endpointsuffix":
				suffix = strings.TrimSpace(val)
			}
			continue
		}
		if len(urls) > 0 {
			continue // the URL pass already covered this value
		}
		if ref, ok := parseHostPort("", p, hostKey); ok {
			out = append(out, ref)
		}
	}
	if !portRE.MatchString(kvPort) {
		kvPort = ""
	}
	for _, h := range kvHosts {
		out = append(out, parseConnStringHost(h, kvPort, adoStyle)...)
	}
	// Azure Storage connection strings name the account and the cloud's
	// suffix instead of a host: AccountName=acct;EndpointSuffix=core.windows.net.
	if account != "" && suffix != "" {
		if ref, ok := parseHostPort("https", account+".blob."+suffix, true); ok {
			out = append(out, ref)
		}
	}
	return out
}

// splitOutsideQuotes splits s at separator runes that are not inside single
// or double quotes, dropping empty fields.
func splitOutsideQuotes(s string, isSep func(rune) bool) []string {
	var out []string
	var quote rune
	start := 0
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case isSep(r):
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// urlEnd are the characters that end a URL inside a larger value (a config
// file, a shell line, a JSON document).
const urlEnd = " \t\r\n\"'<>"

// urlClosed reports whether the URL at the start of rest ends, and so does
// the text right after it that urlAuthority inspects: nothing past that
// point can change how the URL is read.
func urlClosed(rest string) bool {
	i := strings.IndexAny(rest, urlEnd)
	return i >= 0 && strings.ContainsAny(rest[i+1:], urlEnd)
}

// urlAuthority returns the host part of what follows "scheme://", and how
// many leading bytes of rest are userinfo (or, when the URL is ambiguous,
// all of rest) so the caller can keep them away from any other parser.
//
// The default cut is at the LAST '@' of the URL, then at the first '/', '?',
// '#' (or ';' in a JDBC URL). That order guarantees userinfo never survives
// even when a password holds unescaped '/', '?', ';' or '@' characters: the
// worst case is a missed host, never a leaked credential.
//
// The URL itself ends at whitespace or a quote. A password may hold either
// (RFC 3986 allows "'" in userinfo), so when the text right after that end
// still carries an '@', the '@' may be this URL's: nothing is returned.
//
// One shape needs the standard order instead: an '@' inside the PARAMETERS
// with no userinfo in the authority — Azure's "user=name@server" in
// jdbc:sqlserver://srv:1433;user=u@srv and jdbc:postgresql://h/db?user=u@srv.
// The standard authority is taken only when it has no '@' of its own and the
// last '@' sits after a parameter introducer ('?', or ';' in a JDBC URL)
// inside a key=value segment, with nothing but a path in between. ';' and '='
// are legal in userinfo, so outside JDBC a ';' never ends the authority.
func urlAuthority(rest string, jdbc bool) (string, int) {
	if i := strings.IndexAny(rest, urlEnd); i >= 0 {
		next := rest[i+1:]
		if j := strings.IndexAny(next, urlEnd); j >= 0 {
			next = next[:j]
		}
		if strings.Contains(next, "@") {
			return "", len(rest)
		}
		rest = rest[:i]
	}
	stops, params := "/?#", "?"
	if jdbc {
		stops, params = "/?#;", "?;"
	}
	at := strings.LastIndexByte(rest, '@')
	std := rest
	if i := strings.IndexAny(std, stops); i >= 0 {
		std = std[:i]
	}
	if at < 0 {
		return std, 0
	}
	if !strings.Contains(std, "@") && at > len(std) {
		tail := rest[len(std):at]
		if p := strings.IndexAny(tail, params); p >= 0 &&
			!strings.ContainsAny(tail[:p], "@=") &&
			strings.Contains(tail[p:], "=") {
			return std, 0
		}
	}
	safe := rest[at+1:]
	if i := strings.IndexAny(safe, "/?#;"); i >= 0 {
		safe = safe[:i]
	}
	return safe, at + 1
}

// parseAuthority splits a URL authority into hosts (MongoDB and JDBC MySQL
// accept "h1:p1,h2:p2").
func parseAuthority(scheme, auth string) []hostRef {
	var out []hostRef
	for _, hp := range strings.Split(auth, ",") {
		if ref, ok := parseHostPort(scheme, hp, true); ok {
			out = append(out, ref)
		}
	}
	return out
}

// parseConnStringHost handles a connection-string host keyword value:
// SQL Server's "tcp:host,1433" and "host\INSTANCE", Npgsql's "h1,h2" host
// lists, and "host:port".
func parseConnStringHost(val, port string, adoStyle bool) []hostRef {
	scheme := ""
	low := strings.ToLower(strings.TrimSpace(val))
	if strings.HasPrefix(low, "tcp:") {
		scheme = "sqlserver"
		val = strings.TrimSpace(val[len("tcp:"):])
	}
	hosts := []string{val}
	if adoStyle && strings.Contains(val, ",") {
		h, p, _ := strings.Cut(val, ",")
		if portRE.MatchString(strings.TrimSpace(p)) {
			hosts, port = []string{h}, strings.TrimSpace(p)
		} else {
			hosts = strings.Split(val, ",")
		}
	}
	if port == "1433" {
		scheme = "sqlserver"
	}
	var out []hostRef
	for _, h := range hosts {
		if i := strings.IndexByte(h, '\\'); i >= 0 {
			h = h[:i] // SQL Server named instance
		}
		ref, ok := parseHostPort(scheme, h, true)
		if !ok {
			continue
		}
		if ref.port == "" {
			ref.port = port
		}
		out = append(out, ref)
	}
	return out
}

// parseHostPort validates "host", "host:port", "[v6]:port" or a bare IP.
// A missing port is accepted only when bareOK (the name says it is a host).
func parseHostPort(scheme, s string, bareOK bool) (hostRef, bool) {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), `"'`))
	if s == "" {
		return hostRef{}, false
	}
	host, port := s, ""
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return hostRef{}, false
		}
		host, port = s[1:end], strings.TrimPrefix(s[end+1:], ":")
		if s[end+1:] != "" && !strings.HasPrefix(s[end+1:], ":") {
			return hostRef{}, false
		}
	case strings.Count(s, ":") == 1:
		host, port, _ = strings.Cut(s, ":")
	case strings.Count(s, ":") > 1:
		// Unbracketed IPv6 literal (no port possible).
		if _, err := netip.ParseAddr(s); err != nil {
			return hostRef{}, false
		}
	}
	host = strings.TrimSuffix(host, ".")
	if port != "" && !portRE.MatchString(port) {
		return hostRef{}, false
	}
	if n, err := strconv.Atoi(port); port != "" && (err != nil || n == 0 || n > 65535) {
		return hostRef{}, false
	}
	if port == "" && !bareOK {
		return hostRef{}, false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		if a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() {
			return hostRef{}, false // bind/listen addresses, not dependencies
		}
		return hostRef{scheme: scheme, host: a.WithZone("").String(), port: port}, true
	}
	if host == "localhost" || !(hostnameRE.MatchString(host) || isSingleLabel(host)) {
		return hostRef{}, false
	}
	return hostRef{scheme: scheme, host: host, port: port}, true
}

var singleLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// isSingleLabel accepts a bare Service-style name. It is parsed so that it
// can be recognised as in-cluster and dropped (unless an ExternalName points
// at it), rather than failing validation silently.
func isSingleLabel(h string) bool {
	return singleLabelRE.MatchString(h) && strings.ContainsAny(h, "abcdefghijklmnopqrstuvwxyz")
}

// parseCloudSQLName matches a Cloud SQL instance connection name, as given to
// the Auth Proxy (v1 "-instances=p:r:i=tcp:5432", v2 "p:r:i?port=5432") or
// to the connectors (INSTANCE_CONNECTION_NAME=p:r:i).
func parseCloudSQLName(s string) (hostRef, bool) {
	m := cloudSQLRE.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return hostRef{}, false
	}
	return hostRef{scheme: "cloudsql", host: m[1]}, true
}

// isInClusterHost reports whether host resolves inside the cluster: Service
// DNS names, bare single labels (search-path names), and "<svc>.<namespace>"
// when the second label is a namespace of this cluster.
func isInClusterHost(host string, isNamespace func(string) bool) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	if !strings.Contains(host, ".") {
		return true
	}
	if strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".cluster.local") || strings.Contains(host, ".svc.") {
		return true
	}
	if parts := strings.Split(host, "."); len(parts) == 2 && isNamespace != nil && isNamespace(parts[1]) {
		return true
	}
	return false
}

// normalizeWorkloadKind accepts the shapes callers use ("Deployment",
// "deployments", "deploy").
func normalizeWorkloadKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "deployments", "deploy":
		return "Deployment"
	case "statefulset", "statefulsets", "sts":
		return "StatefulSet"
	case "daemonset", "daemonsets", "ds":
		return "DaemonSet"
	case "replicaset", "replicasets", "rs":
		return "ReplicaSet"
	case "job", "jobs":
		return "Job"
	case "cronjob", "cronjobs", "cj":
		return "CronJob"
	case "pod", "pods", "po":
		return "Pod"
	}
	return ""
}

// WorkloadDependencyHosts lists the endpoints a workload's configuration
// points at, from the informer caches (no apiserver call): env literals,
// ConfigMap-backed env (valueFrom and envFrom), command and args of every
// container and init container, plus the ExternalName and selectorless
// Services of its namespace — the two shapes an external dependency takes
// when it is modelled in Kubernetes (finding #49).
//
// kind is Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob or Pod
// (plural and short forms accepted). A configured host that resolves inside
// the cluster is not a dependency by itself, but the Service it names may be
// one in any namespace: an ExternalName is followed to its target (through at
// most maxServiceHops Services) and a selectorless Service to its endpoints;
// any other in-cluster name is dropped, and a Service chain that leads
// nowhere outside the cluster is listed with an empty Host and the reason.
// Secret references are listed with an empty Host too; Secrets are never
// read, and neither are the values of variables and flags named as
// credentials (DB_PASSWORD, --api-token). A missing informer (partial RBAC)
// yields PermissionDeniedError for the workload itself, a "not readable"
// entry for a ConfigMap it references, and one "services not readable in
// <ns>" entry for a namespace whose Services it names cannot be read.
func (c *Connector) WorkloadDependencyHosts(kind, namespace, name string) ([]DependencyHost, error) {
	if c == nil {
		return nil, fmt.Errorf("no cluster connection")
	}
	spec, err := c.workloadPodSpec(kind, namespace, name)
	if err != nil {
		return nil, err
	}
	b := &depCollector{
		seen:        map[depKey]bool{},
		seenRef:     map[string]bool{},
		followed:    map[string]bool{},
		isNamespace: c.namespaceExists,
		configMap:   c.configMapReader(namespace),
	}
	b.inCluster = func(source string, r hostRef) { c.followService(b, namespace, source, r) }
	for i := range spec.InitContainers {
		b.container(&spec.InitContainers[i], "init container "+spec.InitContainers[i].Name)
	}
	for i := range spec.Containers {
		b.container(&spec.Containers[i], "container "+spec.Containers[i].Name)
	}
	c.collectServiceHosts(namespace, b)
	return append(b.hosts, b.refs...), nil
}

// workloadPodSpec returns the pod template of the workload. The object comes
// from the shared informer cache: read it, never modify it.
func (c *Connector) workloadPodSpec(kind, namespace, name string) (*corev1.PodSpec, error) {
	switch normalizeWorkloadKind(kind) {
	case "Deployment":
		if c.deploymentLister == nil {
			return nil, &PermissionDeniedError{Resource: "deployments"}
		}
		o, err := c.deploymentLister.Deployments(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return &o.Spec.Template.Spec, nil
	case "StatefulSet":
		if c.statefulSetLister == nil {
			return nil, &PermissionDeniedError{Resource: "statefulsets"}
		}
		o, err := c.statefulSetLister.StatefulSets(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return &o.Spec.Template.Spec, nil
	case "DaemonSet":
		if c.daemonSetLister == nil {
			return nil, &PermissionDeniedError{Resource: "daemonsets"}
		}
		o, err := c.daemonSetLister.DaemonSets(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return &o.Spec.Template.Spec, nil
	case "ReplicaSet":
		if c.replicaSetLister == nil {
			return nil, &PermissionDeniedError{Resource: "replicasets"}
		}
		o, err := c.replicaSetLister.ReplicaSets(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return &o.Spec.Template.Spec, nil
	case "Job":
		if c.jobLister == nil {
			return nil, &PermissionDeniedError{Resource: "jobs"}
		}
		o, err := c.jobLister.Jobs(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return &o.Spec.Template.Spec, nil
	case "CronJob":
		if c.cronJobLister == nil {
			return nil, &PermissionDeniedError{Resource: "cronjobs"}
		}
		o, err := c.cronJobLister.CronJobs(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return &o.Spec.JobTemplate.Spec.Template.Spec, nil
	case "Pod":
		if c.podLister == nil {
			return nil, &PermissionDeniedError{Resource: "pods"}
		}
		o, err := c.podLister.Pods(namespace).Get(name)
		if err != nil {
			return nil, err
		}
		return &o.Spec, nil
	}
	return nil, fmt.Errorf("unsupported workload kind %q", kind)
}

func (c *Connector) namespaceExists(ns string) bool {
	if c.namespaceLister == nil || ns == "" {
		return false
	}
	_, err := c.namespaceLister.Get(ns)
	return err == nil
}

// configMapReader returns a per-call memoized lookup of a namespace's
// ConfigMap data. status is "" when readable, else why not.
func (c *Connector) configMapReader(namespace string) func(name string) (map[string]string, string) {
	type entry struct {
		data   map[string]string
		status string
	}
	memo := map[string]entry{}
	return func(name string) (map[string]string, string) {
		if e, ok := memo[name]; ok {
			return e.data, e.status
		}
		var e entry
		switch {
		case c.configMapLister == nil:
			e.status = "not readable"
		default:
			cm, err := c.configMapLister.ConfigMaps(namespace).Get(name)
			if err != nil || cm == nil {
				e.status = "not found"
			} else {
				e.data = cm.Data
			}
		}
		memo[name] = e
		return e.data, e.status
	}
}

// collectServiceHosts adds the namespace's ExternalName targets and the
// addresses behind its selectorless Services (hand-written Endpoints to a
// VM or a managed database). The apiserver's own "default/kubernetes" is
// selectorless too and is skipped, and so is a Service the configuration
// already led to: followService listed its target, or why it has none, with
// the variable that names it.
func (c *Connector) collectServiceHosts(namespace string, b *depCollector) {
	if c.serviceLister == nil {
		return
	}
	svcs, err := c.serviceLister.Services(namespace).List(labels.Everything())
	if err != nil {
		return
	}
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
	for _, s := range svcs {
		if b.followed[namespace+"/"+s.Name] {
			continue
		}
		switch {
		case s.Spec.Type == corev1.ServiceTypeExternalName:
			ref, ok := parseHostPort("", s.Spec.ExternalName, true)
			if !ok {
				continue
			}
			ports := servicePortStrings(s)
			if len(ports) == 0 {
				ports = []string{""}
			}
			source := "service externalName " + s.Name
			for _, p := range ports {
				ref.port = p
				b.addHost(source, ref, source)
			}
		case len(s.Spec.Selector) == 0 && !(namespace == "default" && s.Name == "kubernetes"):
			source := "service " + s.Name + " (selectorless endpoints)"
			refs, _ := c.selectorlessEndpoints(namespace, s.Name)
			for _, r := range refs {
				b.addHost(source, r, source)
			}
		}
	}
}

func servicePortStrings(s *corev1.Service) []string {
	var out []string
	for _, p := range s.Spec.Ports {
		if p.Port > 0 {
			out = append(out, strconv.Itoa(int(p.Port)))
		}
	}
	return out
}

// selectorlessEndpoints returns the addresses behind a selectorless Service,
// one per address and port. ok is false when the namespace's EndpointSlices
// cannot be read.
func (c *Connector) selectorlessEndpoints(namespace, service string) (out []hostRef, ok bool) {
	if c.endpointSliceLister == nil || !c.readableIn("endpointslices", namespace) {
		return nil, false
	}
	list, err := c.endpointSliceLister.EndpointSlices(namespace).List(
		labels.SelectorFromSet(labels.Set{discoveryv1.LabelServiceName: service}))
	if err != nil {
		return nil, false
	}
	for _, sl := range list {
		ports := []string{""}
		if len(sl.Ports) > 0 {
			ports = ports[:0]
			for _, p := range sl.Ports {
				if p.Port != nil && *p.Port > 0 {
					ports = append(ports, strconv.Itoa(int(*p.Port)))
				}
			}
			if len(ports) == 0 {
				ports = []string{""}
			}
		}
		for _, ep := range sl.Endpoints {
			for _, addr := range ep.Addresses {
				ref, ok := parseHostPort("", addr, true)
				if !ok {
					continue
				}
				for _, p := range ports {
					ref.port = p
					out = append(out, ref)
				}
			}
		}
	}
	return out, true
}

// readableIn reports whether the informer of resource covers namespace. A
// namespace-scoped ServiceAccount's informers watch only the namespaces it
// was granted, and a Get anywhere else reads as "not found" — which must not
// be taken for "no such object".
func (c *Connector) readableIn(resource, namespace string) bool {
	p, ok := c.permissions[resource]
	if !ok || p == nil || !p.NamespaceScoped {
		return true
	}
	return slices.Contains(p.Namespaces, namespace)
}

// serviceName reads the Service an in-cluster host of the configuration
// names, the way a pod's DNS search path resolves it: "<svc>" in the
// workload's own namespace, "<svc>.<ns>" when <ns> is a namespace of this
// cluster, "<svc>.<ns>.svc" and "<svc>.<ns>.svc.<cluster domain>". Anything
// else (a pod's or a StatefulSet member's DNS name) is not a Service.
func serviceName(host, namespace string, isNamespace func(string) bool) (svcNamespace, svc string, ok bool) {
	parts := strings.Split(host, ".")
	switch {
	case len(parts) == 1:
		return namespace, parts[0], true
	case len(parts) == 2 && isNamespace != nil && isNamespace(parts[1]):
		return parts[1], parts[0], true
	case len(parts) >= 3 && parts[2] == "svc":
		return parts[1], parts[0], true
	}
	return "", "", false
}

// targetServiceName reads the Service an ExternalName's target names. The
// target is a CNAME, resolved as an absolute name with no search path, so
// only the full "<svc>.<ns>.svc.<cluster domain>" names a Service. full is
// false for a shorter form, which resolves to nothing at all; ok is false for
// any other full name in the cluster domain (a pod's DNS name).
func targetServiceName(host string) (svcNamespace, svc string, full, ok bool) {
	parts := strings.Split(host, ".")
	i := slices.Index(parts, "svc")
	switch {
	case i < 0 || i == len(parts)-1:
		return "", "", false, false
	case i == 2:
		return parts[1], parts[0], true, true
	}
	return "", "", i > 2, false
}

// followService looks up the Service an in-cluster host of the configuration
// names, in whatever namespace, and records what it leads to under a Source
// that spells the chain: an ExternalName's target (followed through at most
// maxServiceHops Services while it is itself a Service's name) or the
// addresses behind a selectorless Service. The configured scheme stays; so
// does the configured port, which is what the client dials through an
// ExternalName. A configured name that is no Service, or an ordinary Service,
// is in-cluster and adds nothing. A chain that goes nowhere outside the
// cluster adds a host-less reference that says why (no such Service, no
// endpoints, a loop, too many hops, back inside the cluster), and a namespace
// whose Services cannot be read is reported once. The Services of a chain
// that listed something are marked followed, so the namespace scan does not
// list them a second time.
func (c *Connector) followService(b *depCollector, namespace, source string, r hostRef) {
	visited := map[string]bool{}
	if c.followChain(b, namespace, source, r, visited) {
		for key := range visited {
			b.followed[key] = true
		}
	}
}

// followChain is followService's walk. It records the Services it reads in
// visited and reports whether it listed an entry.
func (c *Connector) followChain(b *depCollector, namespace, source string, r hostRef, visited map[string]bool) bool {
	via := ""
	for hop := 0; ; hop++ {
		var svcNS, name string
		if hop == 0 {
			var ok bool
			if svcNS, name, ok = serviceName(r.host, namespace, b.isNamespace); !ok {
				return false
			}
		} else {
			var full, ok bool
			if svcNS, name, full, ok = targetServiceName(r.host); !ok {
				why := ": inside the cluster"
				if !full {
					why = ": not followed, not a full Service name (CNAME targets get no search path)"
				}
				return b.addRef(source + via + " → " + r.host + why)
			}
		}
		key := svcNS + "/" + name
		switch {
		case svcNS == "default" && name == "kubernetes":
			return hop > 0 && b.addRef(source+via+" → service "+key+": inside the cluster")
		case visited[key]:
			return b.addRef(source + via + ": loops back to service " + key)
		case c.serviceLister == nil || !c.readableIn("services", svcNS):
			// Once per namespace, not per name: under partial RBAC every name
			// there would say the same, and crowd the other references out.
			// Nothing of the chain is marked followed, so the scan of the
			// workload's namespace still lists an alias that led here.
			b.addRef("services not readable in " + svcNS)
			return false
		}
		svc, err := c.serviceLister.Services(svcNS).Get(name)
		switch {
		case err != nil || svc == nil:
			return hop > 0 && b.addRef(source+via+" → service "+key+": no such Service")
		case hop == maxServiceHops:
			return b.addRef(fmt.Sprintf("%s%s → service %s: not followed, more than %d ExternalName hops", source, via, key, maxServiceHops))
		}
		visited[key] = true
		switch {
		case svc.Spec.Type == corev1.ServiceTypeExternalName:
			via += " → service " + key + " (ExternalName)"
			target, ok := parseHostPort("", svc.Spec.ExternalName, true)
			if !ok {
				return false
			}
			if isInClusterHost(target.host, b.isNamespace) {
				r.host = target.host
				continue
			}
			ports := []string{r.port}
			if r.port == "" {
				if ports = servicePortStrings(svc); len(ports) == 0 {
					ports = []string{""}
				}
			}
			listed := false
			for _, p := range ports {
				listed = b.addHost(source+via, hostRef{scheme: r.scheme, host: target.host, port: p}, via) || listed
			}
			return listed
		case len(svc.Spec.Selector) == 0:
			via += " → service " + key + " (selectorless endpoints)"
			refs, ok := c.selectorlessEndpoints(svcNS, name)
			switch {
			case !ok:
				return b.addRef(source + via + ": endpoints not readable")
			case len(refs) == 0:
				return b.addRef(source + via + ": no endpoints")
			}
			listed := false
			for _, e := range refs {
				e.scheme = r.scheme
				listed = b.addHost(source+via, e, via) || listed
			}
			return listed
		}
		return hop > 0 && b.addRef(source+via+" → service "+key+": inside the cluster")
	}
}

// depCollector accumulates results for one WorkloadDependencyHosts call:
// hosts first, then host-less references, each deduplicated and capped.
type depCollector struct {
	hosts       []DependencyHost
	refs        []DependencyHost
	seen        map[depKey]bool
	seenRef     map[string]bool
	isNamespace func(string) bool
	configMap   func(name string) (map[string]string, string)
	// inCluster receives a configured host that resolves inside the cluster
	// (followService); nil drops it.
	inCluster func(source string, r hostRef)
	// followed are the Services ("namespace/name") of the chains inCluster
	// listed something for.
	followed map[string]bool
}

// addConfigured records an endpoint the workload's configuration names. An
// in-cluster name is not a dependency by itself; it goes to inCluster, which
// may find one behind it.
func (b *depCollector) addConfigured(source string, r hostRef) {
	if !r.namesResource() && isInClusterHost(r.host, b.isNamespace) {
		if b.inCluster != nil {
			b.inCluster(source, r)
		}
		return
	}
	b.addHost(source, r, "")
}

// addHost records a parsed endpoint and reports whether it is listed (now or
// before). via is "" for an endpoint the configuration names, which is
// dropped when in-cluster. Otherwise via names the Service, or the chain of
// Services, the endpoint was reached through: such an endpoint is kept even
// when in-cluster, and is deduplicated only among those reached the same way,
// because "a Service aliases this host" is evidence of its own next to the
// env var that names the same host.
func (b *depCollector) addHost(source string, r hostRef, via string) bool {
	if via == "" && !r.namesResource() && isInClusterHost(r.host, b.isNamespace) {
		return false
	}
	key := depKey{ref: r, service: via}
	if b.seen[key] {
		return true
	}
	if len(b.hosts) >= maxDependencyHosts {
		return false
	}
	b.seen[key] = true
	b.hosts = append(b.hosts, DependencyHost{Source: source, Scheme: r.scheme, Host: r.host, Port: r.port})
	return true
}

// addRef records a host-less reference and reports whether it is listed (now
// or before).
func (b *depCollector) addRef(source string) bool {
	if b.seenRef[source] {
		return true
	}
	if len(b.refs) >= maxDependencyRefs {
		return false
	}
	b.seenRef[source] = true
	b.refs = append(b.refs, DependencyHost{Source: source})
	return true
}

type depKey struct {
	ref     hostRef
	service string
}

// envVal is one resolved configuration value awaiting parsing.
type envVal struct{ name, value, source string }

// varRefRE matches the kubelet's $(VAR) expansion in env values and args.
var varRefRE = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_.-]*)\)`)

// container walks one container's env, envFrom, command and args. Values are
// resolved the way the kubelet would (envFrom first, env overriding, $(VAR)
// expanded from what is known) so a URL assembled from DB_HOST and DB_PORT is
// still found; Secret-backed variables stay unexpanded because they are never
// read.
func (b *depCollector) container(ctr *corev1.Container, where string) {
	vars := map[string]string{}
	var vals []envVal
	for _, ef := range ctr.EnvFrom {
		switch {
		case ef.ConfigMapRef != nil:
			cmName := ef.ConfigMapRef.Name
			data, status := b.configMap(cmName)
			if status != "" {
				if status == "not found" && ef.ConfigMapRef.Optional != nil && *ef.ConfigMapRef.Optional {
					continue
				}
				b.addRef(fmt.Sprintf("configmap %s via envFrom (%s): %s", cmName, where, status))
				continue
			}
			keys := make([]string, 0, len(data))
			for k := range data {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if len(keys) > maxConfigMapKeys {
				keys = keys[:maxConfigMapKeys]
			}
			for _, k := range keys {
				n := ef.Prefix + k
				vars[n] = data[k]
				vals = append(vals, envVal{n, data[k], fmt.Sprintf("configmap %s/%s via envFrom (%s)", cmName, k, where)})
			}
		case ef.SecretRef != nil:
			b.addRef(fmt.Sprintf("secret %s via envFrom (%s): not read", ef.SecretRef.Name, where))
		}
	}
	for _, e := range ctr.Env {
		switch {
		case e.ValueFrom == nil:
			vars[e.Name] = e.Value
			vals = append(vals, envVal{e.Name, e.Value, fmt.Sprintf("env %s (%s)", e.Name, where)})
		case e.ValueFrom.ConfigMapKeyRef != nil:
			ref := e.ValueFrom.ConfigMapKeyRef
			data, status := b.configMap(ref.Name)
			if status != "" {
				if !(status == "not found" && ref.Optional != nil && *ref.Optional) {
					b.addRef(fmt.Sprintf("configmap %s/%s via env %s (%s): %s", ref.Name, ref.Key, e.Name, where, status))
				}
				continue
			}
			v, ok := data[ref.Key]
			if !ok {
				continue
			}
			vars[e.Name] = v
			vals = append(vals, envVal{e.Name, v, fmt.Sprintf("configmap %s/%s via env %s (%s)", ref.Name, ref.Key, e.Name, where)})
		case e.ValueFrom.SecretKeyRef != nil:
			ref := e.ValueFrom.SecretKeyRef
			b.addRef(fmt.Sprintf("secret %s/%s via env %s (%s): not read", ref.Name, ref.Key, e.Name, where))
		}
	}
	expand := func(s string) string {
		if !strings.Contains(s, "$(") {
			return s
		}
		return varRefRE.ReplaceAllStringFunc(s, func(m string) string {
			if v, ok := vars[m[2:len(m)-1]]; ok {
				return v
			}
			return m
		})
	}
	for _, ev := range vals {
		if isCredentialName(ev.name) {
			continue
		}
		prefix, hostKey := hostVarPrefix(ev.name)
		value := expand(ev.value)
		refs := parseDependencyValue(value, hostKey)
		for _, r := range refs {
			if r.port == "" && hostKey && !r.namesResource() {
				r.port = companionPort(vars, prefix)
			}
			b.addConfigured(ev.source, r)
		}
		if len(refs) == 0 {
			if bucket, ok := bucketVar(ev.name, value); ok {
				b.addConfigured(ev.source, hostRef{scheme: "bucket", host: bucket})
			}
		}
	}
	for _, list := range []struct {
		label string
		items []string
	}{{"command", ctr.Command}, {"args", ctr.Args}} {
		var words []string
		for _, it := range list.items {
			words = append(words, strings.Fields(expand(it))...)
		}
		source := fmt.Sprintf("%s (%s)", list.label, where)
		for i, w := range words {
			hostKey := false
			// "--db-host=h" or "--db-host h": the flag name marks a host
			// ("--password=x" / "--password x": it marks a value never parsed).
			flag := ""
			if k, val, ok := strings.Cut(w, "="); ok && strings.HasPrefix(k, "-") {
				flag, w = k, val
			} else if i > 0 && strings.HasPrefix(words[i-1], "-") && !strings.Contains(words[i-1], "=") {
				flag = words[i-1]
			}
			if flag != "" {
				if isCredentialName(flag) {
					continue
				}
				_, hostKey = hostVarPrefix(flag)
			}
			for _, r := range parseDependencyValue(w, hostKey) {
				b.addConfigured(source, r)
			}
		}
	}
}

// companionPort finds the port variable paired with a host variable:
// DB_HOST → DB_PORT, PGHOST → PGPORT.
func companionPort(vars map[string]string, prefix string) string {
	for _, name := range []string{prefix + "PORT", strings.TrimSuffix(prefix, "_") + "_PORT"} {
		for k, v := range vars {
			if strings.EqualFold(k, name) {
				if p := strings.TrimSpace(v); portRE.MatchString(p) {
					return p
				}
			}
		}
	}
	return ""
}
