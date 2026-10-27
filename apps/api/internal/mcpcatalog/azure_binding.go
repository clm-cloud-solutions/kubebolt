package mcpcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// azureBinder opens bindings over the Azure MCP Server sidecar.
type azureBinder struct {
	// wrapTransport lets tests see what reaches the sidecar. It wraps the
	// guarded transport, so the dial guard still applies underneath.
	wrapTransport func(http.RoundTripper) http.RoundTripper
}

// azureParam is one parameter of a command as the sidecar declares it.
type azureParam struct {
	typ   string // first non-null JSON type; "" when not declared
	items string // an array's item type; "" when not declared
	desc  string
}

// azureSchema is a command's parameters as the server learned them. It is
// the truth for what a call may carry: the server rejects anything else
// (additionalProperties:false), and subscription/tenant are injected only
// where it is declared.
type azureSchema struct {
	props    map[string]azureParam
	required []string
}

type azureRouter struct {
	ns       *azureNamespace
	commands map[string]azureSchema // learned ∩ allowlist, both consumers
}

type azureTool struct {
	spec      ToolSpec
	namespace string
	router    *azureRouter    // nil for a direct tool
	allowed   map[string]bool // router commands this consumer may run
	direct    *azureDirectTool
	schema    azureSchema // a direct tool's arguments
}

// azureBinding is immutable after Bind; the mcpclient it wraps is safe for
// concurrent use, so the binding is too.
type azureBinding struct {
	client       *mcpclient.Client
	host         string
	subscription string // lower-case GUID
	tenant       string // lower-case GUID, or "" for the identity's home tenant
	tools        map[Consumer]map[string]*azureTool
	order        map[Consumer][]string
	// missing names what the allowlist expects but the sidecar did not
	// offer: a namespace it was not started with, or a command a newer beta
	// renamed. Test surfaces it as a hint.
	missing []string
	// metricDefCache is the one mutable part, with its own lock: the metric
	// definitions callMetrics reads to query each metric the way it allows.
	metricDefCache *azureMetricDefCache
}

// azureLearnArgs asks a router for its command table. It runs locally in the
// server and never calls Azure.
var azureLearnArgs = json.RawMessage(`{"intent":"list commands","learn":true}`)

func (b azureBinder) Bind(ctx context.Context, r Resolved, env Env) (Binding, error) {
	// The sidecar holds one Azure identity per process. On a multi-tenant
	// install that identity would be KubeBolt's, not the org's.
	if !env.AmbientAllowed {
		return nil, errors.New("the Azure connector uses the identity of the Azure MCP Server running next to KubeBolt, so it is only available on single-tenant installs")
	}
	if r.Mode != "" && r.Mode != azureModeSidecar {
		return nil, fmt.Errorf("the Azure connector has no auth mode %q", r.Mode)
	}
	sub := strings.ToLower(strings.TrimSpace(r.Fields["subscriptionId"]))
	if !azureGUIDRE.MatchString(sub) {
		return nil, &FieldError{Field: "subscriptionId", Message: "Subscription ID must be a GUID"}
	}
	tenant := strings.ToLower(strings.TrimSpace(r.Fields["tenantId"]))
	if tenant != "" && !azureGUIDRE.MatchString(tenant) {
		return nil, &FieldError{Field: "tenantId", Message: "Tenant ID must be a GUID"}
	}

	endpoint := env.AzmcpURL
	if endpoint == "" {
		endpoint = DefaultAzmcpURL
	}
	guard, host, err := azureSidecarGuard(endpoint)
	if err != nil {
		return nil, err
	}
	opts := []mcpclient.Option{mcpclient.WithGuard(guard)}
	if env.CallTimeout > 0 {
		opts = append(opts, mcpclient.WithCallTimeout(env.CallTimeout))
	}
	if env.UserAgent != "" {
		opts = append(opts, mcpclient.WithHeaders(map[string]string{"User-Agent": env.UserAgent}))
	}
	if b.wrapTransport != nil {
		hc := guard.HTTPClient()
		hc.Transport = b.wrapTransport(hc.Transport)
		opts = append(opts, mcpclient.WithHTTPClient(hc))
	}
	client, err := mcpclient.New(endpoint, opts...)
	if err != nil {
		return nil, err
	}

	bd := &azureBinding{client: client, host: host, subscription: sub, tenant: tenant, metricDefCache: &azureMetricDefCache{}}
	if err := bd.load(ctx); err != nil {
		_ = client.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return bd, nil
}

// azureSidecarGuard admits exactly the configured sidecar and nothing else.
// The sidecar runs with incoming auth disabled, which the server itself only
// allows on loopback; an address off the machine would be an unauthenticated
// ARM reader on the network, so it is refused here too.
func azureSidecarGuard(endpoint string) (*mcpclient.DialGuard, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, "", errors.New("KUBEBOLT_MCP_AZMCP_URL is not a valid URL")
	}
	if !azureLoopbackHost(u.Hostname()) {
		return nil, "", fmt.Errorf("the Azure MCP Server must listen on loopback, not %s: it runs without sign-in, so it must not be reachable from the network", u.Host)
	}
	hp, err := mcpclient.EndpointHostPort(endpoint)
	if err != nil {
		return nil, "", err
	}
	return mcpclient.NewDialGuard(hp), u.Host, nil
}

func azureLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// load initializes the session, reads the tool list and learns the command
// table of every allowlisted router, then builds each consumer's tools.
func (b *azureBinding) load(ctx context.Context) error {
	// The server's instructions push tools KubeBolt never loads
	// (bestpractices, extension); they are dropped here and never reach a
	// model.
	if _, err := b.client.Initialize(ctx); err != nil {
		return azureReachError(b.host, err)
	}
	listed, err := b.client.ListTools(ctx)
	if err != nil {
		return azureReachError(b.host, err)
	}
	offered := make(map[string]wire.Tool, len(listed))
	for _, t := range listed {
		offered[t.Name] = t
	}

	idx := azureIndex()
	routers := map[string]*azureRouter{}
	for i := range azureNamespaces {
		ns := &azureNamespaces[i]
		if _, ok := offered[ns.name]; !ok {
			b.missing = append(b.missing, ns.name)
			continue
		}
		res, err := b.client.CallTool(ctx, ns.name, azureLearnArgs)
		if err != nil {
			return azureReachError(b.host, err)
		}
		var learned []azureLearned
		if !res.IsError && !res.Truncated {
			learned, err = parseAzureLearn(res.Text())
		}
		if res.IsError || res.Truncated || err != nil {
			slog.Default().Warn("mcp: azure router did not describe its commands; it is left out", "namespace", ns.name)
			b.missing = append(b.missing, ns.name)
			continue
		}
		rt := &azureRouter{ns: ns, commands: map[string]azureSchema{}}
		for _, lc := range learned {
			if c, ok := idx[lc.Command]; ok && azureOwner(c, ns) && !azureCommandDenied(lc.Command) {
				sch := lc.schema()
				if lc.Command == azureActivityLog {
					sch = azureActivitySchema(sch)
				}
				rt.commands[lc.Command] = sch
			}
		}
		for _, c := range ns.commands {
			if _, ok := rt.commands[c.name]; !ok {
				b.missing = append(b.missing, c.name)
			}
		}
		if len(rt.commands) > 0 {
			routers[ns.name] = rt
		}
	}

	directs := map[string]azureSchema{}
	for _, d := range azureDirectTools {
		t, ok := offered[d.name]
		if !ok {
			b.missing = append(b.missing, d.name)
			continue
		}
		directs[d.name] = azureSchemaFromTool(t.InputSchema)
	}
	sort.Strings(b.missing)
	if len(b.missing) > 0 {
		slog.Default().Warn("mcp: the Azure MCP Server does not offer everything KubeBolt allows", "missing", strings.Join(b.missing, ","))
	}

	b.tools = map[Consumer]map[string]*azureTool{}
	b.order = map[Consumer][]string{}
	for _, consumer := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		set := map[string]*azureTool{}
		for i := range azureNamespaces {
			ns := &azureNamespaces[i]
			rt, ok := routers[ns.name]
			if !ok {
				continue
			}
			allowed := map[string]bool{}
			for _, c := range ns.commands {
				if _, learned := rt.commands[c.name]; learned && (consumer == ConsumerKobi || c.autopilot) {
					allowed[c.name] = true
				}
			}
			if len(allowed) == 0 {
				continue
			}
			t := &azureTool{namespace: ns.name, router: rt, allowed: allowed}
			t.spec = ToolSpec{
				Name:        ns.name,
				Description: t.routerDescription(consumer),
				InputSchema: azureRouterInputSchema(azureSortedKeys(allowed)),
			}
			set[ns.name] = t
		}
		for i := range azureDirectTools {
			d := &azureDirectTools[i]
			sch, ok := directs[d.name]
			if !ok || (consumer == ConsumerAutopilot && !d.autopilot) {
				continue
			}
			set[d.name] = &azureTool{
				namespace: d.namespace,
				direct:    d,
				schema:    sch,
				spec: ToolSpec{
					Name:        d.name,
					Description: d.summary + " Subscription and tenant are set by KubeBolt; do not pass them.",
					InputSchema: sch.inputSchema(nil),
				},
			}
		}
		for name := range set {
			if err := ValidateLocalName(azureConnectorID, name); err != nil {
				return err
			}
		}
		b.tools[consumer] = set
		b.order[consumer] = azureSortedKeys(set)
	}
	if len(b.tools[ConsumerKobi]) == 0 {
		return fmt.Errorf("the Azure MCP Server at %s offers none of the tools KubeBolt allows; start it with the namespaces in the setup guide", b.host)
	}
	return nil
}

// azureOwner reports whether c is one of ns's allowlisted commands, so a
// router cannot get another namespace's command learned under its name.
func azureOwner(c *azureCommand, ns *azureNamespace) bool {
	for i := range ns.commands {
		if &ns.commands[i] == c {
			return true
		}
	}
	return false
}

func (b *azureBinding) Tools(consumer Consumer) []ToolSpec {
	names := b.order[consumer]
	out := make([]ToolSpec, 0, len(names))
	for _, n := range names {
		t := b.tools[consumer][n]
		out = append(out, ToolSpec{Name: t.spec.Name, Description: t.spec.Description, InputSchema: azureCloneMap(t.spec.InputSchema)})
	}
	return out
}

func (b *azureBinding) Call(ctx context.Context, consumer Consumer, tool string, args json.RawMessage) (CallResult, error) {
	t, ok := b.tools[consumer][tool]
	if !ok {
		return azureDenial(map[string]any{"command": azureSafeName(tool)},
			fmt.Sprintf("The tool %s is not available through KubeBolt's Azure connector.", azureSafeName(tool))), nil
	}
	in, ok := azureObject(args)
	if !ok {
		return CallResult{IsError: true, Own: true, Text: "The arguments must be a JSON object.", Audit: map[string]any{"namespace": t.namespace}}, nil
	}
	if t.router == nil {
		return b.callDirect(ctx, t, in)
	}
	return b.callRouter(ctx, consumer, t, in)
}

// azureEnvelope is the router call KubeBolt sends. It is built fresh: nothing
// the model put at the top level besides the command and its parameters is
// forwarded, and learn never is.
type azureEnvelope struct {
	Intent     string                     `json:"intent"`
	Command    string                     `json:"command"`
	Parameters map[string]json.RawMessage `json:"parameters"`
}

func (b *azureBinding) callRouter(ctx context.Context, consumer Consumer, t *azureTool, in map[string]json.RawMessage) (CallResult, error) {
	audit := map[string]any{"namespace": t.namespace}

	// The model may ask the router to describe itself, as the server's own
	// description teaches. It is answered here from the filtered table: the
	// server's answer would list the commands KubeBolt withholds.
	if learn, _ := azureBool(in["learn"]); learn {
		audit["command"] = "learn"
		// The header is KubeBolt's (a note); the list carries each command's
		// input schema as the server describes it, so it stays wrapped.
		header, list := t.learnText(consumer)
		return CallResult{Text: list, Notes: []string{header}, Audit: audit}, nil
	}

	cmd, _ := azureString(in["command"])
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		audit["command"] = ""
		return CallResult{IsError: true, Own: true, Audit: audit,
			Text: "Set \"command\" to one of: " + strings.Join(azureSortedKeys(t.allowed), ", ") + "."}, nil
	}
	audit["command"] = azureSafeName(cmd)
	if azureCommandDenied(cmd) || !t.allowed[cmd] {
		return azureDenial(audit, fmt.Sprintf("The command %s is not available through KubeBolt's Azure connector, which only runs a reviewed list of read-only commands. Available for %s: %s.",
			azureSafeName(cmd), t.namespace, strings.Join(azureSortedKeys(t.allowed), ", "))), nil
	}

	params, ok := azureObject(in["parameters"])
	if !ok {
		return CallResult{IsError: true, Own: true, Audit: audit, Text: "\"parameters\" must be a JSON object."}, nil
	}
	spec := azureIndex()[cmd]
	prepared, notes, deny := b.prepare(cmd, spec, t.router.commands[cmd], params)
	if deny == "" && spec.check != nil {
		deny = spec.check(consumer, b.subscription, prepared)
	}
	if deny != "" {
		return azureDenial(audit, deny), nil
	}
	if cmd == azureMetricsQuery {
		return b.callMetrics(ctx, t, prepared, notes, audit)
	}
	if cmd == azureActivityLog {
		return b.callActivityLog(ctx, t, prepared, notes, audit)
	}

	// The model's own intent is not forwarded: without sampling the server
	// does not use it, and it is free text that would otherwise leave the
	// process. The field is required by the router schema, so it is filled.
	raw, err := json.Marshal(azureEnvelope{
		Intent:     "read-only " + cmd + " requested through KubeBolt",
		Command:    cmd,
		Parameters: prepared,
	})
	if err != nil {
		return CallResult{Audit: audit}, err
	}
	res, err := b.client.CallTool(ctx, t.namespace, raw)
	if err != nil {
		return CallResult{Audit: audit}, fmt.Errorf("azure %s: %w", cmd, err)
	}
	notes = append(notes, azureGroupNotes(spec, t.router.commands[cmd], prepared, res)...)
	return azureAnswer(res, notes, audit), nil
}

func (b *azureBinding) callDirect(ctx context.Context, t *azureTool, in map[string]json.RawMessage) (CallResult, error) {
	audit := map[string]any{"namespace": t.namespace, "command": t.direct.name}
	prepared, notes, deny := b.prepare(t.direct.name, nil, t.schema, in)
	if deny != "" {
		return azureDenial(audit, deny), nil
	}
	raw, err := json.Marshal(prepared)
	if err != nil {
		return CallResult{Audit: audit}, err
	}
	res, err := b.client.CallTool(ctx, t.direct.name, raw)
	if err != nil {
		return CallResult{Audit: audit}, fmt.Errorf("azure %s: %w", t.direct.name, err)
	}
	notes = append(notes, azureGroupNotes(nil, t.schema, prepared, res)...)
	return azureAnswer(res, notes, audit), nil
}

// The sidecar splices most parameter values into an ARM URL path
// (/subscriptions/<pinned>/resourceGroups/<resource-group>/...), and ARM
// decodes and normalizes that path: a resource group of
// x/../../../<other>/resourcegroups/rg reads another subscription. So every
// value is checked by what it carries, and a key not named below is a plain
// name: no '/', no character that reshapes a URL, never "." or "..".
var (
	// azureARMKeys carry Azure resource ids (or names). A resource id names
	// its subscription, so it could reach past the pinned one.
	azureARMKeys = map[string]bool{"resource-id": true, "resourceId": true, "resource": true, "resources": true, "scope": true}

	// azurePathKeys legitimately carry '/' without being resource ids:
	// resource types (Microsoft.Sql/servers), metric namespaces, and times a
	// model may write as 01/15/2025. Each segment is checked like a name.
	azurePathKeys = map[string]bool{
		"resource-type": true, "resource-types": true, "metric-namespace": true,
		"start-time": true, "end-time": true, "query-start-time": true, "query-end-time": true,
		"interval": true, "retirement-date": true,
		// KubeBolt's own filter on the activity log (azure_activitylog.go): a
		// sub-resource path, never forwarded.
		azureActivityMatch: true,
	}

	// azureTextKeys carry text, not names: KQL, OData filters, metric names
	// (Windows counters are \Processor(_Total)\% Processor Time), a question
	// for AppLens, search strings. The sidecar sends them in a request body
	// or an encoded query string, never as a path segment.
	azureTextKeys = map[string]bool{
		"query": true, "filter": true, "metric-names": true, "intent": true,
		"question": true, "search": true, "search-string": true,
	}

	// azureKQLKeys end up in a KQL query: the query itself, and the table
	// the predefined queries ("recent", "errors") are run against.
	azureKQLKeys = map[string]bool{"query": true, "table": true}
)

// azureURLChars reshape a URL wherever they sit in a path segment: a
// backslash is read as a slash, '%' starts an escape ARM decodes (%2F is a
// slash to it), '?' and '#' end the path.
const azureURLChars = `\%?#`

// prepare turns the model's parameters into what goes to the server: only
// keys the command declares, each of the type it declares (azureCoerce runs
// first, and what it returns is what is checked and forwarded), never the
// stripped ones nor a null or blank one, subscription and tenant pinned to
// the org's configuration, resource ids inside the pinned subscription, and
// no value that would reshape the request it goes into. spec is nil for a
// direct tool. deny is a message for the model when the call must not go
// out; notes say what was left out.
func (b *azureBinding) prepare(cmd string, spec *azureCommand, sch azureSchema, in map[string]json.RawMessage) (out map[string]json.RawMessage, notes []string, deny string) {
	var strip, idFilters []string
	if spec != nil {
		strip, idFilters = spec.strip, spec.idFilters
	}
	out = map[string]json.RawMessage{}
	var stripped []string
	unknown := 0
	for _, k := range azureSortedKeys(in) {
		v := in[k]
		switch {
		case k == "subscription":
			s, _ := azureString(v)
			s = strings.TrimSpace(s)
			if s != "" && !strings.EqualFold(s, b.subscription) {
				return nil, nil, "This connector reads only subscription " + b.subscription + ". Leave \"subscription\" out: KubeBolt sets it."
			}
			continue
		case k == "tenant":
			s, _ := azureString(v)
			s = strings.TrimSpace(s)
			if b.tenant != "" && s != "" && !strings.EqualFold(s, b.tenant) {
				return nil, nil, "This connector reads only tenant " + b.tenant + ". Leave \"tenant\" out: KubeBolt sets it."
			}
			if b.tenant == "" && s != "" {
				stripped = append(stripped, k)
			}
			continue
		case azureContains(strip, k):
			stripped = append(stripped, k)
			continue
		}
		p, ok := sch.props[k]
		if !ok {
			unknown++
			continue
		}
		if strings.TrimSpace(string(v)) == "null" {
			continue // a parameter left unset; the server never has to render it
		}
		// A blank string parameter is one left unset too. Forwarded, the
		// server does not read it as unset: a resource group of "  " answers
		// 400 ArgumentNullException, where no group lists the subscription.
		if s, isString := azureString(v); isString && p.typ == "string" && strings.TrimSpace(s) == "" {
			continue
		}
		if azureCommaListKeys[k] && p.typ == "string" {
			v = azureJoinCommaList(v)
		}
		v = azureCoerce(p, v)
		if msg := azureCheckType(k, p, v); msg != "" {
			return nil, nil, msg
		}
		if azureValueReparsed(v) {
			return nil, nil, "\"" + k + "\" cannot start with '@' or '-': the Azure MCP Server would read it as a file or an option, not as a value."
		}
		checked, msg := b.checkValue(k, v, azureContains(idFilters, k))
		if msg != "" {
			return nil, nil, msg
		}
		out[k] = checked
	}
	if _, ok := sch.props["subscription"]; ok {
		out["subscription"] = azureJSONString(b.subscription)
	}
	if _, ok := sch.props["tenant"]; ok && b.tenant != "" {
		out["tenant"] = azureJSONString(b.tenant)
	}
	if len(stripped) > 0 {
		notes = append(notes, "Not forwarded: "+strings.Join(stripped, ", ")+" (KubeBolt never passes database logins, key-based auth or a tenant it was not set to).")
	}
	if unknown > 0 {
		notes = append(notes, azureIgnoredNote(cmd, unknown, spec != nil))
	}
	return out, notes, ""
}

// azureIgnoredNote says how many parameters the command does not declare,
// never which: their names are the model's, and a model steered by what a
// provider wrote could spell words in them that would then come back as
// KubeBolt's. A router lists a command's parameters on learn=true; a direct
// tool's are in its own input schema.
func azureIgnoredNote(cmd string, n int, router bool) string {
	what := "1 parameter"
	if n > 1 {
		what = strconv.Itoa(n) + " parameters"
	}
	where := "its input schema lists the ones it takes."
	if router {
		where = "learn=true lists the ones it takes."
	}
	return "Ignored " + what + " that " + cmd + " does not declare: " + where
}

// azureValueReparsed reports whether a parameter value would stop being a
// value inside the sidecar. The server turns every parameter into
// command-line tokens and parses them with System.CommandLine, which reads a
// token starting with '@' as a response file — a file on the sidecar's host,
// whose contents come back in the error text, credentials file included —
// and a token starting with '-' as an option (--subscription=<id>,
// --tenant=<id>, --auth-method=key), which would undo the pinning and the
// stripping above. No Azure name, resource id, KQL query or timestamp starts
// with either, so such values are refused, wherever they sit in the value
// (array items become tokens of their own).
func azureValueReparsed(v json.RawMessage) bool {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return true
	}
	return azureAnyReparsed(x)
}

func azureAnyReparsed(x any) bool {
	switch t := x.(type) {
	case string:
		s := strings.TrimLeftFunc(t, unicode.IsSpace)
		return strings.HasPrefix(s, "@") || strings.HasPrefix(s, "-")
	case []any:
		for _, e := range t {
			if azureAnyReparsed(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range t {
			if azureAnyReparsed(e) {
				return true
			}
		}
	}
	return false
}

// azureCoerce turns a value the server would have read as intended into the
// JSON type it declares, before anything checks it. The router descriptions
// give a type only in words ("top (integer)"), so models write "5" for 5 and
// one id for a list of ids, and the server takes both: it renders every
// value as command-line text. A number or boolean parameter's string becomes
// the number or boolean only when it is exactly one: a whole number for an
// integer (strconv.ParseInt, base 10), a plain decimal for a number, true or
// false, with no space, hex, exponent in an integer, or other spelling. Any
// other string is left as it is, and azureCheckType refuses it. A list
// parameter's single string becomes a one-item list, which then goes through
// every check a list's item does. Objects, lists and nulls are never
// touched. What this returns is what is checked and what is forwarded.
func azureCoerce(p azureParam, v json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(v, &s) != nil {
		return v
	}
	switch p.typ {
	case "array":
		raw, _ := json.Marshal([]string{s})
		return raw
	case "integer":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return json.RawMessage(strconv.FormatInt(n, 10))
		}
	case "number":
		if azureDecimalRE.MatchString(s) {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				raw, _ := json.Marshal(f)
				return raw
			}
		}
	case "boolean":
		if s == "true" || s == "false" {
			return json.RawMessage(s)
		}
	}
	return v
}

// azureDecimalRE is a number written as a JSON number would be, give or take
// a sign or a bare point: ParseFloat alone would also read hex, underscores,
// Inf and NaN, none of which JSON carries.
var azureDecimalRE = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// azureCheckType refuses a value whose JSON type is not the one the server
// declared for the parameter. The server renders whatever it gets as text:
// an object as its raw JSON, keys included, which no check here reads (a
// resource group given as {"x/../../../<other>/resourcegroups/rg": 1} read
// another subscription), and a list given for a text parameter as its items
// joined into one value (a query of ["arg", "(\"\").Resources"] ran arg()).
// So a string parameter takes a string, an array one a list of its item
// type (strings when undeclared), a number or boolean one its own type, and
// a parameter the schema left untyped a single value. No parameter takes an
// object. It runs after azureCoerce, so a string it sees for a number or
// boolean is one that is not exactly the value.
func azureCheckType(key string, p azureParam, v json.RawMessage) string {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return "\"" + key + "\" is not valid JSON."
	}
	want := p.typ
	if p.typ == "array" {
		want = p.items
		if want == "" {
			want = "string"
		}
	}
	words, ok := azureTypeWords[want]
	if !ok {
		return "\"" + key + "\" takes a type KubeBolt does not forward (" + azureSafeName(want) + "): only strings, numbers, booleans and lists of them go to the Azure MCP Server."
	}
	if p.typ != "array" {
		if !azureScalarOf(x, want) {
			if _, str := x.(string); str && azureStringWords[want] != "" {
				return "\"" + key + "\" must be " + words + ": " + azureStringWords[want] + "."
			}
			return "\"" + key + "\" must be " + words + ", not a list or an object."
		}
		return ""
	}
	list, ok := x.([]any)
	for i := 0; ok && i < len(list); i++ {
		ok = azureScalarOf(list[i], want)
	}
	if !ok {
		return "\"" + key + "\" must be a list in which each item is " + words + "."
	}
	return ""
}

// azureTypeWords are the declared types KubeBolt forwards a value of, as a
// refusal names them; "" is a parameter the schema left untyped.
var azureTypeWords = map[string]string{
	"":        "a single string, number or boolean",
	"string":  "a string",
	"integer": "a number",
	"number":  "a number",
	"boolean": "true or false",
}

// azureStringWords say which strings a number or boolean parameter takes
// (azureCoerce), for a refusal of any other string.
var azureStringWords = map[string]string{
	"integer": `a string is taken only when it is exactly a whole number, such as "5"`,
	"number":  `a string is taken only when it is exactly a number, such as "1.5"`,
	"boolean": `a string is taken only when it is exactly "true" or "false"`,
}

// azureScalarOf reports whether a decoded JSON value is a single value of
// the declared type; "" admits any single value.
func azureScalarOf(x any, typ string) bool {
	switch x.(type) {
	case string:
		return typ == "string" || typ == ""
	case float64:
		return typ == "integer" || typ == "number" || typ == ""
	case bool:
		return typ == "boolean" || typ == ""
	}
	return false
}

// checkValue vets one parameter value, every string in it included (array
// items become values of their own in the sidecar), and returns what to
// forward: the value itself, or the resource ids it checked. msg is for the
// model when the value must not go out. idFilter marks a parameter the
// server matches as a substring of its results (idFilters).
func (b *azureBinding) checkValue(key string, v json.RawMessage, idFilter bool) (fwd json.RawMessage, msg string) {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return nil, "\"" + key + "\" is not valid JSON."
	}
	strs := azureStrings(x, nil)
	for _, s := range strs {
		if msg := azureCheckRunes(key, s, azureTextKeys[key]); msg != "" {
			return nil, msg
		}
	}
	if azureARMKeys[key] {
		return b.checkResourceIDs(key, v, idFilter)
	}
	if !azureTextKeys[key] {
		for _, s := range strs {
			if msg := azureCheckName(key, s, azurePathKeys[key]); msg != "" {
				return nil, msg
			}
		}
	}
	if azureKQLKeys[key] {
		// A list reaches Kusto as one query, its items joined: the joins are
		// checked too, or a function name and its parenthesis could arrive
		// in two items that each look harmless.
		kql := strs
		if len(strs) > 1 {
			kql = append(append([]string{}, strs...), strings.Join(strs, " "), strings.Join(strs, ""))
		}
		for _, s := range kql {
			if msg := azureCheckKQL(key, s); msg != "" {
				return nil, msg
			}
		}
	}
	return v, ""
}

// azureStrings collects every string in a decoded JSON value.
func azureStrings(x any, out []string) []string {
	switch t := x.(type) {
	case string:
		out = append(out, t)
	case []any:
		for _, e := range t {
			out = azureStrings(e, out)
		}
	case map[string]any:
		for _, k := range azureSortedKeys(t) {
			out = azureStrings(t[k], out)
		}
	}
	return out
}

// azureSlashLookalikes are characters that read as a slash or a backslash to
// a person and to some URL handling. None belongs in an Azure name, path or
// id, and refusing them keeps the segment checks from depending on how ARM
// and http.sys treat them (today: a 500 or a 400, never a walk).
const azureSlashLookalikes = "\u2044\u2215\u2216\u29F8\u29F9\uFF0F\uFF3C"

// azureCheckRunes refuses control characters in any value, and slash
// look-alikes and line breaks outside free text. Kusto rejects the controls
// today (a NEL or a C0 byte between a function name and its parenthesis is a
// parse error, not a call), but the KQL check should not rest on that.
func azureCheckRunes(key, s string, text bool) string {
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			if text {
				continue
			}
			return "\"" + key + "\" cannot contain a tab or a line break: pass the plain value."
		}
		if unicode.IsControl(r) {
			return "\"" + key + "\" cannot contain control characters: pass the plain value."
		}
		if !text && strings.ContainsRune(azureSlashLookalikes, r) {
			return "\"" + key + "\" cannot contain a character that looks like a slash: use '/' where the value is a path, or pass the plain name."
		}
	}
	return ""
}

// azureCheckName refuses a value that would stop being one name in the URL
// the sidecar builds: a character that reshapes a URL, a '/' where the key
// takes a plain name, and a segment that is empty or only dots, which ARM
// reads as a step (up) the path.
func azureCheckName(key, s string, slashes bool) string {
	if i := strings.IndexAny(s, azureURLChars); i >= 0 {
		return "\"" + key + "\" cannot contain '" + s[i:i+1] + "': pass the plain value, because Azure would read that character as part of the request URL."
	}
	if !strings.Contains(s, "/") {
		if azureDotSegment(s) {
			return "\"" + key + "\" cannot be \".\" or \"..\": Azure would read it as a step up the request path, not as a name."
		}
		return ""
	}
	if !slashes {
		return "\"" + key + "\" cannot contain '/': pass the plain name, because Azure would read a slash as a step in the request path."
	}
	for _, seg := range strings.Split(s, "/") {
		if strings.TrimSpace(seg) == "" || azureDotSegment(seg) {
			return "\"" + key + "\" cannot have an empty, \".\" or \"..\" part between slashes: Azure would read it as a step in the request path."
		}
	}
	return ""
}

// azureDotSegment reports whether a path segment is only dots: "." and ".."
// are normalized away by ARM, and no Azure name is made of dots alone.
func azureDotSegment(seg string) bool {
	seg = strings.TrimSpace(seg)
	return seg != "" && strings.Trim(seg, ".") == ""
}

// checkResourceIDs refuses a resource-id parameter (a string, a
// comma-separated list, or an array of strings) unless each entry is a bare
// name, which the sidecar resolves inside the pinned subscription, or a full
// id in the pinned subscription; an idFilter also takes the tail of an id
// (resourceGroups/rg/...). Any other shape is refused too: a value KubeBolt
// cannot read is a value it cannot vouch for. One trailing slash is dropped
// from an entry before it is checked, and what is forwarded is what was
// checked: the value as the model wrote it, or rebuilt without that slash.
func (b *azureBinding) checkResourceIDs(key string, v json.RawMessage, idFilter bool) (json.RawMessage, string) {
	var vals []string
	var s string
	list := false
	if json.Unmarshal(v, &s) == nil {
		vals = []string{s}
	} else if json.Unmarshal(v, &vals) == nil {
		list = true
	} else {
		return nil, "\"" + key + "\" must be a string."
	}
	trimmed := false
	for n, s := range vals {
		parts := strings.Split(s, ",")
		changed := false
		for j, part := range parts {
			part = strings.TrimSpace(part)
			if i := strings.IndexAny(part, azureURLChars); i >= 0 {
				return nil, "\"" + key + "\" cannot contain '" + part[i:i+1] + "': pass the resource id as it is, unencoded, because Azure would read that character as part of the request URL."
			}
			if id := azureTrimSlash(part); id != part {
				part, changed = id, true
			}
			parts[j] = part
			if !strings.Contains(part, "/") {
				if azureDotSegment(part) {
					return nil, "\"" + key + "\" cannot be \".\" or \"..\": pass a resource name or a full resource id."
				}
				continue // a resource name, resolved inside the pinned subscription
			}
			if azureIDInSubscription(part, b.subscription) || (idFilter && azureIDTail(part)) {
				continue
			}
			tail := ""
			if idFilter {
				tail = ", or the part of one after that prefix (resourceGroups/<group>/...),"
			}
			return nil, "\"" + key + "\" must be a resource name or a full resource id in subscription " + b.subscription +
				" (/subscriptions/" + b.subscription + "/...)" + tail + " with no empty, \".\" or \"..\" segments, the only subscription this connector reads."
		}
		if changed {
			vals[n], trimmed = strings.Join(parts, ","), true
		}
	}
	if !trimmed {
		return v, ""
	}
	if list {
		raw, _ := json.Marshal(vals)
		return raw, ""
	}
	return azureJSONString(vals[0]), ""
}

// azureTrimSlash drops one trailing '/' from an id or scope: models write
// /subscriptions/<id>/ as often as not, and the empty segment it ends in
// would otherwise fail the segment check. A second one still does, and a
// lone "/" stays what it is.
func azureTrimSlash(s string) string {
	if t, ok := strings.CutSuffix(s, "/"); ok && t != "" {
		return t
	}
	return s
}

// azureIDInSubscription reports whether id is an ARM id inside subscription
// sub: /subscriptions/<sub>/..., taken apart on '/', with every segment a
// real one. The segment check is what keeps the pin: the sidecar splices ids
// into ARM URLs and ARM normalizes "." and "..", so
// /subscriptions/<sub>/resourceGroups/../../<other> is a read of <other>, and
// /subscriptions/<sub>/../../providers/... a read at tenant level.
func azureIDInSubscription(id, sub string) bool {
	rest, ok := strings.CutPrefix(id, "/")
	if !ok || strings.ContainsAny(id, azureURLChars) {
		return false
	}
	segs := strings.Split(rest, "/")
	if len(segs) < 2 || !strings.EqualFold(segs[0], "subscriptions") || !strings.EqualFold(segs[1], sub) {
		return false
	}
	for _, seg := range segs {
		if strings.TrimSpace(seg) == "" || azureDotSegment(seg) {
			return false
		}
	}
	return true
}

// azureIDTail reports whether s is the part of a resource id after
// /subscriptions/<id>/, as a substring filter takes it: no leading slash, no
// subscriptions segment up front to name another subscription with, and
// every segment a real one. The server matches it against results it read
// in the pinned subscription.
func azureIDTail(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(strings.ToLower(s), "subscriptions") || strings.ContainsAny(s, azureURLChars) {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if strings.TrimSpace(seg) == "" || azureDotSegment(seg) {
			return false
		}
	}
	return true
}

// azureKQLCrossScopeRE finds a KQL call that reads outside the workspace or
// resource the query runs against: workspace() and app() open another Log
// Analytics workspace or Application Insights app, resource() any resource
// by id, adx(), cluster() and database() an Azure Data Explorer cluster,
// arg() Azure Resource Graph across every subscription the identity can
// read, and externaldata a storage URL. The name may be in any case,
// bracket-quoted (['workspace']), and followed by whitespace or comments
// before its parenthesis. String literals are not skipped: a query that
// only mentions such a call in a string is refused too, which costs the
// model a rephrase, where skipping them would cost a parser that must agree
// with Kusto's on every quote.
var azureKQLCrossScopeRE = func() *regexp.Regexp {
	const names = `(workspace|app|resource|adx|cluster|database|arg|externaldata)`
	const gap = `(?:[\s\v\p{Z}\p{Cf}]|//[^\n]*|/\*.*?\*/)*`
	return regexp.MustCompile(`(?is)(?:(?:^|[^a-z0-9_])` + names + `|\[` + gap + `@?['"]` + names + `['"]` + gap + `\])` + gap + `\(`)
}()

// azureCheckKQL refuses a KQL value that calls a cross-scope function. The
// pin is on the workspace or resource named in the call; these functions
// name their own, in any subscription or tenant the identity can reach.
func azureCheckKQL(key, s string) string {
	m := azureKQLCrossScopeRE.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	fn := strings.ToLower(m[1] + m[2])
	return "The KQL in \"" + key + "\" cannot use " + fn + "(): workspace(), app(), resource(), adx(), cluster(), database(), arg() and externaldata " +
		"read outside the connected subscription, so query the one workspace or resource named in the call."
}

// azureResultText passes the server's text through: success JSON, Azure
// error JSON and plain-text routing errors alike; isError is the signal,
// never the shape of the text. KubeBolt's notes travel apart from it
// (CallResult.Notes), so nothing the server wrote can pass for one.
func azureResultText(res *mcpclient.CallResult) string {
	return res.Text()
}

// azureAnswer is the result of a command the sidecar answered, with its
// digest when it answered without an error.
func azureAnswer(res *mcpclient.CallResult, notes []string, audit map[string]any) CallResult {
	out := CallResult{Text: azureResultText(res), IsError: res.IsError, Notes: append(notes, azureTruncatedNote(res)...), Audit: audit}
	if !res.IsError {
		out.Digest = digestJSON(out.Text)
	}
	return out
}

// azureTruncatedNote is the note on an answer cut at the read limit, or nil
// when it was read whole. A note, not text appended to the answer: appended,
// it sat inside the wrapper, where the prompts tell the model that words like
// these are data.
func azureTruncatedNote(res *mcpclient.CallResult) []string {
	if !res.Truncated {
		return nil
	}
	return []string{fmt.Sprintf("the answer was larger than %d KiB and is cut", res.Bytes>>10)}
}

// azureGroupHow ends a note with where a resource group comes from. The
// commands that read one server, pool or registry require it, and a model
// that lacks it makes one up: a guessed group answers 404
// ResourceGroupNotFound, a call spent for nothing.
const azureGroupHow = "take the group from a resource id you already have (/resourceGroups/<group>/ in the id), " +
	"from monitor_activitylog_list called with just the resource name (its answer carries the resource id and group), " +
	"or from group_list, then group_resource_list. monitor_metrics_query also takes just the name. " +
	"Never guess a resource group."

var (
	// The region is missing too, and the cluster's own region (in every pod's
	// topology labels) is the one at hand: a names-only answer once had Kobi
	// place a centralus server in the AKS cluster's eastus2.
	azureNamesOnlyNote = "This answer names resources without their resource groups, ids or locations. " +
		"A resource's region comes from a command that reads that resource (a server's config_get, for one), never from where the cluster runs. " +
		"For a command that needs a resource's group, " + azureGroupHow
	// ARM answers ResourceGroupNotFound for a group that does not exist, and
	// the same ResourceNotFound for a resource in another group and for a
	// misspelled name in the right one: the note holds for all three.
	azureGroupNotFoundNote = "Azure found no resource by that name in that group: check the name, and " + azureGroupHow
)

// azureGroupNotes says where to get a resource group when an answer leaves the
// model without one: a failure the group explains, or a list of names from a
// command that could have been narrowed to a group and was not. spec is nil
// for a direct tool; sent is what went to the server.
func azureGroupNotes(spec *azureCommand, sch azureSchema, sent map[string]json.RawMessage, res *mcpclient.CallResult) []string {
	if res.IsError {
		if azureGroupNotFound(res.Text()) {
			return []string{azureGroupNotFoundNote}
		}
		return nil
	}
	if spec == nil || !spec.lists || res.Truncated {
		return nil
	}
	_, declared := sch.props["resource-group"]
	// prepare never sends a blank group, so a group sent narrows the list.
	_, narrowed := sent["resource-group"]
	if !declared || narrowed || azureContains(sch.required, "resource-group") || !azureNamesOnly(res.Text()) {
		return nil
	}
	return []string{azureNamesOnlyNote}
}

// azureGroupNotFound reports an ARM 404 the resource group explains: the
// group does not exist (ResourceGroupNotFound), or the resource is not in it
// (ResourceNotFound, "... under resource group '<group>' was not found").
func azureGroupNotFound(text string) bool {
	return strings.Contains(text, "ResourceGroupNotFound") ||
		(strings.Contains(text, "ResourceNotFound") && strings.Contains(text, "under resource group"))
}

// azureNamesOnly reports whether a successful answer lists something and says
// nowhere which resource group it is in. An empty list, or a text that is not
// the server's JSON, is not one.
func azureNamesOnly(text string) bool {
	var body struct {
		Results any `json:"results"`
	}
	if json.Unmarshal([]byte(text), &body) != nil || len(azureStrings(body.Results, nil)) == 0 {
		return false
	}
	return !azureCarriesGroup(body.Results)
}

// azureCarriesGroup reports whether a decoded answer names a resource group:
// in a resource id (.../resourceGroups/<group>/...) or in a field named for
// one (resourceGroup, resourceGroupName, resource-group).
func azureCarriesGroup(x any) bool {
	switch t := x.(type) {
	case string:
		return strings.Contains(strings.ToLower(t), "/resourcegroups/")
	case []any:
		for _, e := range t {
			if azureCarriesGroup(e) {
				return true
			}
		}
	case map[string]any:
		for k, v := range t {
			if strings.Contains(azureFieldFold.Replace(strings.ToLower(k)), "resourcegroup") || azureCarriesGroup(v) {
				return true
			}
		}
	}
	return false
}

// azureFieldFold spells resource_group and resource-group as resourcegroup.
var azureFieldFold = strings.NewReplacer("-", "", "_", "")

func azureDenial(audit map[string]any, msg string) CallResult {
	audit["denied"] = true
	return CallResult{IsError: true, Own: true, Text: msg, Audit: audit}
}

// Test proves the sidecar answers and that its identity can read the
// configured subscription: initialize, tools/list, then subscription_list as
// the canary, which goes to ARM and reports the subscription's name and
// tenant.
func (b *azureBinding) Test(ctx context.Context) (TestResult, error) {
	start := time.Now()
	out := TestResult{}
	done := func() (TestResult, error) {
		out.LatencyMs = time.Since(start).Milliseconds()
		return out, nil
	}
	if _, err := b.client.Initialize(ctx); err != nil {
		out.Error, out.Hint = azureReachProblem(b.host, err)
		return done()
	}
	listed, err := b.client.ListTools(ctx)
	if err != nil {
		out.Error, out.Hint = azureReachProblem(b.host, err)
		return done()
	}
	offered := map[string]bool{}
	for _, t := range listed {
		offered[t.Name] = true
	}
	for _, n := range b.order[ConsumerKobi] {
		if offered[n] {
			out.Tools++
		}
	}

	out.Canary = &Canary{Tool: "subscription_list"}
	args := json.RawMessage(`{}`)
	if b.tenant != "" {
		args, _ = json.Marshal(map[string]string{"tenant": b.tenant})
	}
	res, err := b.client.CallTool(ctx, "subscription_list", args)
	if err != nil {
		out.Error, out.Hint = azureReachProblem(b.host, err)
		return done()
	}
	if res.IsError {
		out.Error = "The Azure MCP Server could not list subscriptions with its identity" + azureStatusSuffix(res.Text()) + "."
		out.Hint = "Check the service principal the sidecar runs with (AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET, and AZURE_TOKEN_CREDENTIALS=EnvironmentCredential), then restart it."
		return done()
	}
	out.Canary.OK = true

	var body struct {
		Results struct {
			Subscriptions []struct {
				SubscriptionID string `json:"subscriptionId"`
				DisplayName    string `json:"displayName"`
				TenantID       string `json:"tenantId"`
				State          string `json:"state"`
			} `json:"subscriptions"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(res.Text()), &body); err != nil {
		out.Error = "The Azure MCP Server answered the subscription list in an unexpected format."
		out.Hint = "Run the Azure MCP Server version in the setup guide (" + azmcpVersion + ")."
		return done()
	}
	for _, s := range body.Results.Subscriptions {
		if !strings.EqualFold(s.SubscriptionID, b.subscription) {
			continue
		}
		out.OK = true
		out.Identity = azureDisplay(s.DisplayName)
		if t := azureDisplay(s.TenantID); t != "" {
			out.Identity += " · tenant " + t
		}
		if len(b.missing) > 0 {
			out.Hint = "The Azure MCP Server does not offer: " + strings.Join(b.missing, ", ") +
				". Start it with the namespaces in the setup guide."
		}
		return done()
	}
	out.Error = "The Azure MCP Server's identity cannot see subscription " + b.subscription + "."
	out.Hint = "Assign the service principal Reader, Monitoring Reader and Log Analytics Reader on that subscription, or correct the subscription ID."
	return done()
}

func (b *azureBinding) Close(ctx context.Context) error { return b.client.Close(ctx) }

// Missing implements PartialBinding: the namespaces, commands and direct
// tools the allowlist expects and the sidecar did not offer at bind time. A
// router whose learn answer failed is among them, and a later bind may get
// it back.
func (b *azureBinding) Missing() []string { return append([]string(nil), b.missing...) }

// azureSidecarError is a failure to use the sidecar, phrased for the admin.
// Error is the sentence; Hint says what to check; the cause stays wrapped for
// errors.Is.
type azureSidecarError struct {
	msg, hint string
	cause     error
}

func (e *azureSidecarError) Error() string { return e.msg }
func (e *azureSidecarError) Hint() string  { return e.hint }
func (e *azureSidecarError) Unwrap() error { return e.cause }

func azureReachError(host string, err error) error {
	msg, hint := azureReachProblem(host, err)
	return &azureSidecarError{msg: msg, hint: hint, cause: err}
}

func azureReachProblem(host string, err error) (msg, hint string) {
	var blocked *mcpclient.BlockedError
	var opErr *net.OpError
	switch {
	case errors.As(err, &blocked):
		return "KubeBolt refused to connect to " + host + ".", "KUBEBOLT_MCP_AZMCP_URL must point at the Azure MCP Server on loopback."
	case errors.Is(err, syscall.ECONNREFUSED), errors.As(err, &opErr) && opErr.Op == "dial":
		return "Nothing answers at " + host + ": the Azure MCP Server is not running.",
			"Start it as the setup guide shows, or set KUBEBOLT_MCP_AZMCP_URL to where it listens."
	case azureConnectionDropped(err):
		return "The Azure MCP Server at " + host + " closed the connection: it is not running or restarting.",
			"Start it as the setup guide shows, or wait until it has restarted, then test again."
	case errors.Is(err, context.DeadlineExceeded):
		return "The Azure MCP Server at " + host + " did not answer in time.", "Check that it is running and not stuck signing in to Azure."
	}
	return "KubeBolt could not talk to the Azure MCP Server at " + host + ".",
		"Check that it runs with --transport http and that KUBEBOLT_MCP_AZMCP_URL points at its root path."
}

// azureConnectionDropped reports a connection the sidecar closed under the
// request: a reset, or an EOF where the HTTP answer should have been. That
// is what a request gets when it goes out on a kept-alive connection the
// moment the process stops, before the connection pool has noticed.
func azureConnectionDropped(err error) bool {
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var ue *url.Error
	return errors.As(err, &ue) && (errors.Is(ue.Err, io.EOF) || errors.Is(ue.Err, io.ErrUnexpectedEOF))
}

// azureStatusSuffix names the HTTP status in an Azure error answer
// ({status, message, results}) without repeating its message, which can
// carry principal and tenant details.
func azureStatusSuffix(text string) string {
	var e struct {
		Status int `json:"status"`
	}
	if json.Unmarshal([]byte(text), &e) == nil && e.Status > 0 {
		return fmt.Sprintf(" (Azure answered %d)", e.Status)
	}
	return ""
}

// azureLearned is one entry of a router's learn answer.
type azureLearned struct {
	Command     string `json:"command"`
	InputSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	} `json:"inputSchema"`
}

func (l azureLearned) schema() azureSchema {
	return azureSchemaFrom(l.InputSchema.Properties, l.InputSchema.Required)
}

// parseAzureLearn reads a learn answer: a fixed three-line preamble, then a
// JSON array of {command, description, inputSchema}. Only the array is
// trusted, and only its names and parameter shapes are used.
// The list is the first line-initial JSON array that decodes, so a preamble
// that grows a bracketed line in a later beta does not break the parse.
func parseAzureLearn(text string) ([]azureLearned, error) {
	var starts []int
	if strings.HasPrefix(text, "[") {
		starts = append(starts, 0)
	}
	for off := 0; ; {
		i := strings.Index(text[off:], "\n[")
		if i < 0 {
			break
		}
		starts = append(starts, off+i+1)
		off += i + 2
	}
	for _, i := range starts {
		var out []azureLearned
		if json.NewDecoder(strings.NewReader(text[i:])).Decode(&out) != nil || len(out) == 0 {
			continue
		}
		for _, c := range out {
			if c.Command == "" {
				return nil, errors.New("azure: learn answer has a command without a name")
			}
		}
		return out, nil
	}
	return nil, errors.New("azure: learn answer carries no command list")
}

func azureSchemaFromTool(in map[string]interface{}) azureSchema {
	raw, _ := json.Marshal(in)
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	_ = json.Unmarshal(raw, &s)
	return azureSchemaFrom(s.Properties, s.Required)
}

func azureSchemaFrom(props map[string]json.RawMessage, required []string) azureSchema {
	s := azureSchema{props: make(map[string]azureParam, len(props))}
	for name, raw := range props {
		var p struct {
			Type        json.RawMessage `json:"type"`
			Items       json.RawMessage `json:"items"`
			Description string          `json:"description"`
		}
		_ = json.Unmarshal(raw, &p)
		var items struct {
			Type json.RawMessage `json:"type"`
		}
		_ = json.Unmarshal(p.Items, &items)
		s.props[name] = azureParam{typ: azureJSONType(p.Type), items: azureJSONType(items.Type), desc: p.Description}
	}
	for _, r := range required {
		if _, ok := s.props[r]; ok {
			s.required = append(s.required, r)
		}
	}
	sort.Strings(s.required)
	return s
}

func azureJSONType(raw json.RawMessage) string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, t := range many {
			if t != "null" {
				return t
			}
		}
	}
	return ""
}

// azurePinned are set by KubeBolt, so the model never sees them as options.
var azurePinned = []string{"subscription", "tenant"}

// visible splits a command's parameters into the required and optional ones
// the model may pass, sorted.
func (s azureSchema) visible(strip []string) (required, optional []string) {
	req := map[string]bool{}
	for _, r := range s.required {
		if !azureContains(azurePinned, r) && !azureContains(strip, r) {
			required = append(required, r)
			req[r] = true
		}
	}
	for name := range s.props {
		if !req[name] && !azureContains(azurePinned, name) && !azureContains(strip, name) {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)
	return required, optional
}

// inputSchema is a direct tool's model-facing schema: type, properties and
// required only, without the pinned parameters.
func (s azureSchema) inputSchema(strip []string) map[string]any {
	required, optional := s.visible(strip)
	props := map[string]any{}
	for _, name := range append(append([]string{}, required...), optional...) {
		p := s.props[name]
		prop := map[string]any{}
		if p.typ != "" {
			prop["type"] = p.typ
		}
		if p.typ == "array" {
			item := p.items
			if item == "" {
				item = "string"
			}
			prop["items"] = map[string]any{"type": item}
		}
		if p.desc != "" {
			prop["description"] = azureClip(p.desc, 200)
		}
		props[name] = prop
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// azureRouterInputSchema is the same for every router but the enum. The
// parameters are explained once, in the description: repeating it in each of
// eighteen schemas costs about 4 KiB on every request that carries the tools.
func azureRouterInputSchema(commands []string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent":     map[string]any{"type": "string"},
			"command":    map[string]any{"type": "string", "enum": commands},
			"parameters": map[string]any{"type": "object", "description": "Keyed exactly as the description lists them."},
		},
		"required": []string{"command"},
	}
}

// routerDescription lists only the commands this consumer may run, with the
// parameters the server declares for them, so the model needs no discovery
// round trip (it has no sampling to fall back on).
func (t *azureTool) routerDescription(consumer Consumer) string {
	ns := t.router.ns
	var b strings.Builder
	b.WriteString(ns.title + " (read-only). " + ns.purpose + "\n")
	b.WriteString(`Call with {"command", "parameters"}; KubeBolt sets subscription and tenant.` + "\n")
	b.WriteString("Commands:")
	idx := azureIndex()
	for _, name := range azureSortedKeys(t.allowed) {
		c := idx[name]
		b.WriteString("\n- " + name + ": " + c.summary)
		if consumer == ConsumerAutopilot && c.autopilotNote != "" {
			b.WriteString(" For you, " + c.autopilotNote + ".")
		}
		required, optional := t.router.commands[name].visible(c.strip)
		if len(required) > 0 {
			b.WriteString(" Required: " + t.paramList(name, required) + ".")
		}
		if len(optional) > 0 {
			b.WriteString(" Optional: " + t.paramList(name, optional) + ".")
		}
	}
	return b.String()
}

func (t *azureTool) paramList(cmd string, names []string) string {
	sch := t.router.commands[cmd]
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n
		if typ := sch.props[n].typ; typ != "" && typ != "string" {
			parts[i] += " (" + typ + ")"
		}
	}
	return strings.Join(parts, ", ")
}

// learnText answers a model's learn=true in the server's own shape, listing
// only what this consumer may run.
func (t *azureTool) learnText(consumer Consumer) (header, list string) {
	type entry struct {
		Command     string         `json:"command"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	idx := azureIndex()
	var entries []entry
	for _, name := range azureSortedKeys(t.allowed) {
		c := idx[name]
		desc := c.summary
		if consumer == ConsumerAutopilot && c.autopilotNote != "" {
			desc += " For you, " + c.autopilotNote + "."
		}
		entries = append(entries, entry{Command: name, Description: desc, InputSchema: t.router.commands[name].inputSchema(c.strip)})
	}
	raw, _ := json.Marshal(entries)
	return "Commands available through KubeBolt for the '" + t.namespace + "' tool (read-only). " +
		"Run one with {\"command\": ..., \"parameters\": {...}}; subscription and tenant are set by KubeBolt.", string(raw)
}

func azureObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return map[string]json.RawMessage{}, true
	}
	if !strings.HasPrefix(s, "{") {
		return nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, true
}

func azureString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func azureBool(raw json.RawMessage) (bool, bool) {
	var v bool
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return false, false
	}
	return v, true
}

func azureJSONString(s string) json.RawMessage {
	raw, _ := json.Marshal(s)
	return raw
}

var azureSafeNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// azureSafeName is how a model-supplied name is echoed back or audited: as
// is when it looks like a name, never as arbitrary text.
func azureSafeName(s string) string {
	if azureSafeNameRE.MatchString(s) {
		return s
	}
	return "(invalid name)"
}

// azureDisplay bounds a value Azure returned before an admin sees it.
func azureDisplay(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	return azureClip(s, 128)
}

func azureClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func azureContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func azureSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// azureCloneMap copies a schema so a caller that edits a returned ToolSpec
// cannot change what the next caller gets.
func azureCloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = azureCloneValue(v)
	}
	return out
}

func azureCloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return azureCloneMap(t)
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = azureCloneValue(t[i])
		}
		return out
	}
	return v
}
