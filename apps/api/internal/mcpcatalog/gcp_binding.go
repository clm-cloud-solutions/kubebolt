package mcpcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// The Google Cloud binding: one MCP client per allowlisted endpoint, all on
// one guarded HTTP client, each call signed with the org's OAuth token and
// billed to its project (x-goog-user-project).
//
// Google's servers are stateless and their tools/list is public, so the
// catalog is listed WITHOUT credentials at bind time. A binding therefore
// comes up — with a stable tool list — even while the credentials are being
// fixed, and a token is fetched only for a real call; Test's canary is what
// proves the credentials.

const (
	// gcpListConcurrency bounds the parallel tools/list at bind time. The
	// gateway gives a bind 10 s and the heaviest catalog is 531 KB.
	gcpListConcurrency = 8
	// gcpCanaryTool proves the credentials reach a control plane: it lists
	// GKE clusters in every location of the project.
	gcpCanaryService = "container"
	gcpCanaryTool    = "list_clusters"
)

var gcpResourceSegmentRE = regexp.MustCompile(`^[A-Za-z0-9._~:@*+=-]+$`)

// gcpSegmentArgNames are identifiers a server may splice into a resource
// path next to the pinned project (compute's instance name and zone, Cloud
// SQL's instance). Wherever they are not a full resource name they must be
// one plain segment, so "../other-project/..." cannot climb out.
var gcpSegmentArgNames = map[string]bool{
	"name": true, "instance": true, "zone": true, "region": true, "location": true,
	"subscriptionId": true, "topicId": true, "snapshotId": true, "operation": true, "traceId": true,
}

// gcpBinder binds the Google Cloud connector. The zero value talks to
// Google; tests replace where endpoints live and where tokens come from.
type gcpBinder struct {
	endpointURL func(service string) string
	tokenSource func(ctx context.Context, r Resolved, env Env) (oauth2.TokenSource, error)
	logger      *slog.Logger
}

// gcpEndpoint is one bound MCP endpoint.
type gcpEndpoint struct {
	id   string
	host string
	// lister has no signer: tools/list is public on every Google endpoint.
	lister *mcpclient.Client
	caller *mcpclient.Client
}

// gcpBoundTool is one exposed tool with its rule narrowed to the fields the
// live schema actually has.
type gcpBoundTool struct {
	ep        *gcpEndpoint
	remote    string
	local     string
	rule      gcpToolRule
	allowed   map[string]bool // top-level arguments the model may send
	fill      map[string]bool
	kobi      bool
	autopilot bool
	spec      ToolSpec
}

// offeredTo reports whether the table gives consumer c this tool.
func (bt *gcpBoundTool) offeredTo(c Consumer) bool {
	return (c == ConsumerKobi && bt.kobi) || (c == ConsumerAutopilot && bt.autopilot)
}

type gcpBinding struct {
	project   string
	mode      string
	identity  string
	endpoints map[string]*gcpEndpoint
	tools     map[string]*gcpBoundTool // by local name, both consumers
	lists     map[Consumer][]ToolSpec
	// missing are the endpoints whose catalog could not be listed; their
	// tools are absent until the connector is bound again.
	missing []string
	log     *slog.Logger
}

func (b gcpBinder) log() *slog.Logger {
	if b.logger != nil {
		return b.logger
	}
	return slog.Default().With("connector", gcpConnectorID)
}

func (b gcpBinder) url(service string) string {
	if b.endpointURL != nil {
		return b.endpointURL(service)
	}
	s, _ := gcpServiceByID(service)
	path := "/mcp"
	if s != nil && s.path != "" {
		path = s.path
	}
	return "https://" + service + ".googleapis.com" + path
}

// Bind validates the configuration, builds the token source (no network for
// a key; ADC may consult the metadata server), lists every allowlisted
// endpoint in parallel and keeps the intersection of what Google serves with
// what the table allows.
func (b gcpBinder) Bind(ctx context.Context, r Resolved, env Env) (Binding, error) {
	project := strings.TrimSpace(r.Fields["projectId"])
	if !gcpProjectIDRE.MatchString(project) {
		return nil, &FieldError{Field: "projectId", Message: "Project ID is not in the expected format"}
	}
	location := strings.TrimSpace(r.Fields["location"])
	if location != "" && !gcpLocationRE.MatchString(location) {
		return nil, &FieldError{Field: "location", Message: "Default location is not in the expected format"}
	}

	ts, identity, err := b.credentials(ctx, r, env)
	if err != nil {
		return nil, err
	}

	guard := mcpclient.NewDialGuard(env.AllowHostPorts...)
	httpc := guard.HTTPClient()
	headers := map[string]string{}
	if env.UserAgent != "" {
		headers["User-Agent"] = env.UserAgent
	}
	signer := mcpclient.NewBearerSigner(ts, project)

	bd := &gcpBinding{
		project:   project,
		mode:      r.Mode,
		identity:  identity,
		endpoints: map[string]*gcpEndpoint{},
		tools:     map[string]*gcpBoundTool{},
		lists:     map[Consumer][]ToolSpec{},
		log:       b.log(),
	}
	for _, s := range gcpServices {
		u := b.url(s.id)
		pu, err := url.Parse(u)
		if err != nil {
			return nil, fmt.Errorf("google cloud: endpoint for %s: %w", s.id, err)
		}
		common := []mcpclient.Option{
			mcpclient.WithHTTPClient(httpc), mcpclient.WithGuard(guard),
			mcpclient.WithCallTimeout(env.CallTimeout), mcpclient.WithHeaders(headers),
			mcpclient.WithLogger(bd.log),
		}
		lister, err := mcpclient.New(u, common...)
		if err != nil {
			return nil, fmt.Errorf("google cloud: endpoint for %s: %w", s.id, err)
		}
		caller, err := mcpclient.New(u, append(common, mcpclient.WithSigner(signer))...)
		if err != nil {
			return nil, fmt.Errorf("google cloud: endpoint for %s: %w", s.id, err)
		}
		bd.endpoints[s.id] = &gcpEndpoint{id: s.id, host: pu.Host, lister: lister, caller: caller}
	}

	listed := bd.listAll(ctx)
	for _, s := range gcpServices {
		tools, ok := listed[s.id]
		if !ok {
			bd.missing = append(bd.missing, s.id)
			continue
		}
		// A name the catalog lists twice is ambiguous: the two entries may
		// disagree on annotations or arguments, and the server runs only
		// one of them. Neither is bound.
		seen := make(map[string]int, len(tools))
		for _, t := range tools {
			seen[t.Name]++
		}
		for _, t := range tools {
			if seen[t.Name] != 1 {
				if _, listed := s.tools[t.Name]; listed && seen[t.Name] > 1 {
					bd.log.Warn("google cloud: tool not bound", "service", s.id, "tool", t.Name, "reason", "listed more than once")
				}
				seen[t.Name] = 0 // warn once
				continue
			}
			bt, reason := bindGCPTool(bd.endpoints[s.id], &s, t, project, location)
			if bt == nil {
				if reason != "" {
					bd.log.Warn("google cloud: tool not bound", "service", s.id, "tool", t.Name, "reason", reason)
				}
				continue
			}
			bd.tools[bt.local] = bt
		}
	}
	if len(bd.tools) == 0 {
		return nil, errors.New("google cloud: none of Google's MCP endpoints could be listed; check that KubeBolt can reach *.googleapis.com")
	}
	sort.Strings(bd.missing)
	if len(bd.missing) > 0 {
		bd.log.Warn("google cloud: some endpoints could not be listed; their tools are unavailable until the connector is bound again",
			"endpoints", strings.Join(bd.missing, ","))
	}
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		var specs []ToolSpec
		for _, name := range gcpSortedKeys(bd.tools) {
			bt := bd.tools[name]
			if bt.offeredTo(c) {
				specs = append(specs, bt.spec)
			}
		}
		bd.lists[c] = specs
	}
	return bd, nil
}

// credentials turns the configuration into a token source and a
// human-readable identity. Errors name the field to fix, never its value.
func (b gcpBinder) credentials(ctx context.Context, r Resolved, env Env) (oauth2.TokenSource, string, error) {
	var identity string
	switch r.Mode {
	case gcpModeServiceAccount:
		raw := r.Secrets[gcpFieldCredential]
		if strings.TrimSpace(raw) == "" {
			return nil, "", &FieldError{Field: gcpFieldCredential, Message: "Credential JSON is required"}
		}
		id, err := gcpCheckCredentialJSON(raw, env.AmbientAllowed)
		if err != nil {
			return nil, "", err
		}
		identity = id
	case gcpModeADC:
		if !env.AmbientAllowed {
			return nil, "", &FieldError{Field: "mode", Message: "Application Default Credentials are only available on self-hosted installs"}
		}
		identity = "Application Default Credentials of the KubeBolt host"
	default:
		return nil, "", &FieldError{Field: "mode", Message: "Choose how KubeBolt authenticates to Google Cloud"}
	}
	if b.tokenSource != nil {
		ts, err := b.tokenSource(ctx, r, env)
		return ts, identity, err
	}
	if r.Mode == gcpModeADC {
		ts, err := mcpclient.GoogleDefaultTokenSource(ctx)
		if err != nil {
			return nil, "", errors.New("google cloud: Application Default Credentials were not found on the KubeBolt host")
		}
		return ts, identity, nil
	}
	ts, err := mcpclient.GoogleTokenSourceFromJSON(ctx, []byte(r.Secrets[gcpFieldCredential]))
	if err != nil {
		return nil, "", gcpCredentialRefused(err)
	}
	return ts, identity, nil
}

// gcpCredentialRefused turns mcpclient's refusal of a credential document
// into a field error. Its validator names fields, never values; what the
// oauth2 library says when it cannot load a document is not repeated, since
// a parser error can quote the input.
func gcpCredentialRefused(err error) error {
	msg := strings.TrimPrefix(err.Error(), "google: ")
	if strings.HasPrefix(msg, "load credentials") {
		msg = "Google's library could not load it; paste the key file exactly as downloaded"
	}
	return &FieldError{Field: gcpFieldCredential, Message: "Credential JSON was refused: " + msg}
}

// listAll lists every endpoint in parallel, retrying a failure once. It
// returns the catalogs that could be read.
func (bd *gcpBinding) listAll(ctx context.Context) map[string][]wire.Tool {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = map[string][]wire.Tool{}
		sem = make(chan struct{}, gcpListConcurrency)
	)
	for _, id := range gcpSortedKeys(bd.endpoints) {
		ep := bd.endpoints[id]
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			tools, err := ep.lister.ListTools(ctx)
			if err != nil && ctx.Err() == nil && !errors.Is(err, mcpclient.ErrResponseTooLarge) {
				tools, err = ep.lister.ListTools(ctx)
			}
			if err != nil {
				bd.log.Warn("google cloud: tools/list failed", "endpoint", ep.host, "error", err)
				return
			}
			mu.Lock()
			out[id] = tools
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// bindGCPTool applies the table to one listed tool. It returns nil and the
// reason when the tool is refused ("" for tools the table simply does not
// name, which is most of every catalog).
func bindGCPTool(ep *gcpEndpoint, s *gcpService, t wire.Tool, project, location string) (*gcpBoundTool, string) {
	rule, listed := s.tools[t.Name]
	if !listed {
		return nil, ""
	}
	if why := gcpDenied(s.id, t.Name); why != "" {
		return nil, "denied: " + why
	}
	if !t.ReadOnly() {
		return nil, "Google no longer marks it read-only"
	}
	local := gcpLocalName(s.id, t.Name)
	if err := ValidateLocalName(gcpConnectorID, local); err != nil {
		return nil, err.Error()
	}
	schema, err := gcpNormalizeSchema(t.InputSchema)
	if err != nil {
		return nil, "schema: " + err.Error()
	}
	props := gcpSchemaProps(schema)

	// Narrow the rule to the fields this schema has, and refuse a tool whose
	// schema grew a field that could name a resource the table does not
	// classify.
	classified := map[string]bool{}
	has := func(names []string) []string {
		var out []string
		for _, n := range names {
			classified[n] = true
			if props[n] != nil {
				out = append(out, n)
			}
		}
		return out
	}
	eff := rule
	eff.paths = has(rule.paths)
	eff.pathLists = has(rule.pathLists)
	eff.projectIDs = has(rule.projectIDs)
	eff.free = has(rule.free)
	eff.drop = has(rule.drop)
	eff.fixed = map[string]string{}
	for k, v := range rule.fixed {
		classified[k] = true
		if props[k] != nil {
			eff.fixed[k] = v
		}
	}
	for name := range props {
		if gcpScopedArgNames[name] && !classified[name] {
			return nil, fmt.Sprintf("argument %q is not classified by the allowlist", name)
		}
	}
	if eff.page != nil && props[eff.page.field] == nil {
		eff.page = nil
	}

	hidden := map[string]bool{}
	for _, n := range eff.projectIDs {
		hidden[n] = true
	}
	for n := range eff.fixed {
		hidden[n] = true
	}
	for _, n := range eff.drop {
		hidden[n] = true
	}
	fill := map[string]bool{}
	for _, n := range rule.fill {
		if props[n] != nil {
			fill[n] = true
		}
	}
	gcpShapeSchema(schema, hidden, fill)
	props = gcpSchemaProps(schema)
	// Only top-level fields are pinned, so a nested one that could name a
	// resource (an object argument that grew a project) cannot be offered.
	if nested := gcpNestedScopedArg(props); nested != "" {
		return nil, fmt.Sprintf("nested argument %q is not classified by the allowlist", nested)
	}
	// Google's own text would now mislead: a filled field is no longer
	// required and names only one project, and an omitted page size is no
	// longer Google's default.
	for name := range fill {
		p, _ := props[name].(map[string]any)
		if p == nil {
			continue
		}
		def := "projects/" + project
		if p["type"] == "array" {
			def = `["projects/` + project + `"]`
		}
		p["description"] = "Defaults to " + def + "; no other project can be read."
	}
	if eff.page != nil {
		if p, _ := props[eff.page.field].(map[string]any); p != nil {
			p["description"] = fmt.Sprintf("Page size: defaults to %d, at most %d.", eff.page.def, eff.page.max)
		}
	}

	allowed := map[string]bool{}
	for name := range gcpSchemaProps(schema) {
		allowed[name] = true
	}
	// Hidden identity fields may still be sent, as long as they carry the
	// value KubeBolt would have filled in; dropped ones may not.
	for _, n := range eff.projectIDs {
		allowed[n] = true
	}
	for n := range eff.fixed {
		allowed[n] = true
	}

	return &gcpBoundTool{
		ep: ep, remote: t.Name, local: local, rule: eff, allowed: allowed, fill: fill,
		kobi: rule.kobi, autopilot: rule.autopilot,
		spec: ToolSpec{Name: local, Description: gcpDescription(rule, t.Description, project, location, len(eff.projectIDs) > 0), InputSchema: schema},
	}, ""
}

// gcpDescription builds a tool's description: the hand-written text when
// there is one, else Google's own, cleaned and capped, plus the note and one
// sentence of scope.
func gcpDescription(rule gcpToolRule, google, project, location string, projectFilled bool) string {
	subst := func(s string) string {
		loc := location
		if loc == "" {
			loc = "<location>"
		}
		return strings.NewReplacer("{project}", project, "{location}", loc).Replace(s)
	}
	var tail []string
	if rule.desc == "" {
		if rule.note != "" {
			tail = append(tail, subst(rule.note))
		}
		switch {
		case len(rule.fill) > 0:
			tail = append(tail, fmt.Sprintf("%s defaults to projects/%s.", strings.Join(rule.fill, " and "), project))
		case len(rule.paths)+len(rule.pathLists) > 0:
			tail = append(tail, fmt.Sprintf("Project %s: resource names start with projects/%s/.", project, project))
		case projectFilled:
			tail = append(tail, fmt.Sprintf("The project (%s) is filled in.", project))
		}
	}
	if rule.loc && location != "" {
		tail = append(tail, "Default location: "+location+".")
	}
	suffix := strings.Join(tail, " ")

	base := gcpCleanText(subst(rule.desc))
	if base == "" {
		base = gcpCleanText(google)
	}
	room := gcpMaxToolDescription
	if suffix != "" {
		room -= len(suffix) + 2 // the joining space and a closing period
	}
	base = gcpCapText(base, room)
	if suffix == "" {
		return base
	}
	if base == "" {
		return suffix
	}
	// Google's descriptions often end without punctuation ("Get an
	// instance"), and the scope sentence must not read as part of them.
	if !strings.HasSuffix(base, ".") && !strings.HasSuffix(base, "…") {
		base += "."
	}
	return base + " " + suffix
}

// Tools returns the consumer's tools, sorted by name. Each call hands out
// copies of the schemas.
func (bd *gcpBinding) Tools(c Consumer) []ToolSpec {
	src := bd.lists[c]
	out := make([]ToolSpec, len(src))
	for i, s := range src {
		out[i] = ToolSpec{Name: s.Name, Description: s.Description, InputSchema: gcpCopyJSON(s.InputSchema).(map[string]any)}
	}
	return out
}

// gcpCallError is a provider failure with a sentence an admin can act on. It
// unwraps to the mcpclient error, so the gateway still sees ErrUnauthorized
// (and retires the cached token) or an *HTTPError.
type gcpCallError struct {
	msg   string
	cause error
}

func (e *gcpCallError) Error() string { return e.msg }
func (e *gcpCallError) Unwrap() error { return e.cause }

// gcpHumanError explains a failed Google call. Nothing in it comes from the
// credentials; the provider's own words (a short, HTML-stripped reason) are
// already in the mcpclient error.
func gcpHumanError(host, mode string, err error) error {
	var httpErr *mcpclient.HTTPError
	// In ADC mode there is no key and the identity may be a user: the
	// messages name what KubeBolt runs as instead.
	who, revoked := "the service account", "the service account key may be revoked or disabled"
	if mode == gcpModeADC {
		who, revoked = "the identity KubeBolt runs as", "the identity KubeBolt runs as was refused"
	}
	switch {
	case errors.Is(err, mcpclient.ErrUnauthorized):
		return &gcpCallError{cause: err, msg: "Google Cloud rejected the connector's credentials (HTTP 401) at " + host +
			": " + revoked + ", or the token could not be refreshed"}
	case errors.Is(err, mcpclient.ErrRateLimited):
		return &gcpCallError{cause: err, msg: "Google Cloud is rate limiting " + host + " (HTTP 429)"}
	case errors.Is(err, context.DeadlineExceeded):
		return &gcpCallError{cause: err, msg: "Google Cloud did not answer in time at " + host}
	case strings.Contains(err.Error(), "google: get token"):
		// The token exchange failed before anything was sent. Google's token
		// endpoint says why (invalid_grant for a deleted key or account) and
		// never echoes the credential.
		_, detail, _ := strings.Cut(err.Error(), "google: get token: ")
		return &gcpCallError{cause: err, msg: "KubeBolt could not get an access token for Google Cloud: " + gcpCapText(detail, 200)}
	case errors.As(err, &httpErr) && httpErr.Status == 403:
		return &gcpCallError{cause: err, msg: "Google Cloud refused the call at " + host + " (HTTP 403): " + who + " is missing roles/mcp.toolUser or the product's viewer role, or the product API is not enabled"}
	case errors.As(err, &httpErr):
		return &gcpCallError{cause: err, msg: fmt.Sprintf("Google Cloud answered HTTP %d at %s", httpErr.Status, host)}
	}
	return &gcpCallError{cause: err, msg: "Google Cloud call to " + host + " failed: " + err.Error()}
}

// Call re-checks the tool against the consumer's list, pins every argument
// that names a project, and forwards the call. Refusals are tool errors the
// model can correct; transport failures are Go errors for the gateway.
func (bd *gcpBinding) Call(ctx context.Context, c Consumer, tool string, args json.RawMessage) (CallResult, error) {
	bt, ok := bd.tools[tool]
	if !ok || !bt.offeredTo(c) {
		return CallResult{
			IsError: true,
			Own:     true,
			Text:    fmt.Sprintf("The Google Cloud tool %q is not available here.", tool),
			Audit:   map[string]any{"tool": tool, "refused": "not allowed"},
		}, nil
	}
	audit := map[string]any{"endpoint": bt.ep.host, "tool": bt.remote}
	pinned, err := bt.pinArgs(args, bd.project)
	if err != nil {
		audit["refused"] = "arguments"
		return CallResult{IsError: true, Own: true, Text: "Not sent to Google Cloud: " + err.Error(), Audit: audit}, nil
	}
	res, err := bt.ep.caller.CallTool(ctx, bt.remote, pinned)
	if err != nil {
		return CallResult{Audit: audit}, gcpHumanError(bt.ep.host, bd.mode, err)
	}
	text := res.Text()
	if strings.TrimSpace(text) == "" && len(res.StructuredContent) > 0 {
		// Every Google tool declares an outputSchema; a server that sends
		// only structuredContent must not look like an empty answer.
		text = string(res.StructuredContent)
	}
	var notes []string
	if res.Truncated {
		audit["truncated"] = true
		// A note, outside the wrapper: appended to the answer it was data.
		notes = append(notes, "the response was larger than KubeBolt reads and is cut: narrow it with a filter, a shorter time range or a smaller page size")
	}
	out := CallResult{Text: text, IsError: res.IsError, Notes: notes, Audit: audit}
	if !res.IsError {
		out.Digest = digestJSON(text)
	}
	return out, nil
}

// pinArgs validates and completes a call's arguments. Only fields of the
// schema the model was shown are accepted — anything else could be a field
// Google's server still honours (a deprecated projectId next to parent) —
// and every field that names a project must name the configured one.
func (bt *gcpBoundTool) pinArgs(raw json.RawMessage, project string) (json.RawMessage, error) {
	args := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if trimmed[0] != '{' || json.Unmarshal(trimmed, &args) != nil {
			return nil, errors.New("the arguments must be a JSON object")
		}
	}
	shown := gcpSchemaProps(bt.spec.InputSchema)
	for _, k := range gcpSortedKeys(args) {
		if !bt.allowed[k] {
			return nil, fmt.Errorf("unknown argument %q for %s", gcpCapText(k, 64), bt.local)
		}
		// The same holds inside object arguments: Google's parser also
		// takes snake_case aliases and fields its schema no longer shows.
		if p, _ := shown[k].(map[string]any); p != nil {
			if err := gcpCheckNestedKeys(k, args[k], p, 0); err != nil {
				return nil, err
			}
		}
	}
	base := "projects/" + project
	quoted := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
	// absent treats an explicit null like an omitted field: it is filled or
	// left out, never forwarded as null.
	absent := func(f string) bool {
		v, ok := args[f]
		if ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			delete(args, f)
			return true
		}
		return !ok
	}
	isPath := map[string]bool{}

	for _, f := range bt.rule.paths {
		isPath[f] = true
		if absent(f) {
			if bt.fill[f] {
				args[f] = quoted(base)
			}
			continue
		}
		v := args[f]
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil, fmt.Errorf("%s must be a string", f)
		}
		if err := gcpCheckResourceName(f, s, project); err != nil {
			return nil, err
		}
	}
	for _, f := range bt.rule.pathLists {
		isPath[f] = true
		if absent(f) {
			if bt.fill[f] {
				b, _ := json.Marshal([]string{base})
				args[f] = b
			}
			continue
		}
		v := args[f]
		var list []string
		if json.Unmarshal(v, &list) != nil || len(list) == 0 {
			return nil, fmt.Errorf("%s must be a non-empty list of resource names", f)
		}
		for _, s := range list {
			if err := gcpCheckResourceName(f, s, project); err != nil {
				return nil, err
			}
		}
	}
	pinExact := func(field, want, why string) error {
		isPath[field] = true
		if absent(field) {
			args[field] = quoted(want)
			return nil
		}
		var s string
		if json.Unmarshal(args[field], &s) != nil || s != want {
			return fmt.Errorf("%s must be %q%s", field, want, why)
		}
		return nil
	}
	for _, f := range bt.rule.projectIDs {
		if err := pinExact(f, project, ": this connector only reads project "+project); err != nil {
			return nil, err
		}
	}
	for _, f := range gcpSortedKeys(bt.rule.fixed) {
		if err := pinExact(f, bt.rule.fixed[f], ""); err != nil {
			return nil, err
		}
	}
	for _, f := range gcpSortedKeys(args) {
		if !gcpSegmentArgNames[f] || isPath[f] {
			continue
		}
		var s string
		if json.Unmarshal(args[f], &s) != nil || len(s) > 256 || s == "." || s == ".." || !gcpResourceSegmentRE.MatchString(s) {
			return nil, fmt.Errorf("%s must be a plain identifier such as a name, a zone or a region, not a path", f)
		}
	}
	if p := bt.rule.page; p != nil {
		if absent(p.field) {
			args[p.field] = json.RawMessage(strconv.Itoa(p.def))
		} else {
			v := args[p.field]
			var n float64
			if json.Unmarshal(v, &n) != nil || n < 1 || n != float64(int64(n)) {
				return nil, fmt.Errorf("%s must be a positive whole number", p.field)
			}
			if n > float64(p.max) {
				args[p.field] = json.RawMessage(strconv.Itoa(p.max))
			}
		}
	}
	return json.Marshal(args)
}

// gcpCheckResourceName accepts projects/<project> or a name under it, made of
// plain path segments: no empty, "." or ".." segment and no percent-escapes,
// so a name cannot climb out of the project once a server joins it into a
// URL path.
func gcpCheckResourceName(field, v, project string) error {
	base := "projects/" + project
	if len(v) > 1024 {
		return fmt.Errorf("%s is too long", field)
	}
	if v == base {
		return nil
	}
	rest, ok := strings.CutPrefix(v, base+"/")
	if !ok {
		return fmt.Errorf("%s must be %q or start with %q: this connector only reads project %s", field, base, base+"/", project)
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "." || seg == ".." || !gcpResourceSegmentRE.MatchString(seg) {
			return fmt.Errorf("%s is not a valid resource name", field)
		}
	}
	return nil
}

// gcpCheckNestedKeys refuses, below the top level, any key an object's
// schema does not declare. An object whose schema declares no properties (a
// map such as labels) is free-form. A value of the wrong type is left for
// Google to reject: only a key could carry a field KubeBolt never reviewed.
func gcpCheckNestedKeys(path string, raw json.RawMessage, schema map[string]any, depth int) error {
	if depth > gcpMaxSchemaDepth {
		return fmt.Errorf("%s nests too deeply", gcpCapText(path, 96))
	}
	if items, _ := schema["items"].(map[string]any); items != nil {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) == nil {
			for _, v := range list {
				if err := gcpCheckNestedKeys(path+"[]", v, items, depth+1); err != nil {
					return err
				}
			}
		}
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	for _, k := range gcpSortedKeys(obj) {
		sub, _ := props[k].(map[string]any)
		if sub == nil {
			return fmt.Errorf("unknown argument %q", gcpCapText(path+"."+k, 96))
		}
		if err := gcpCheckNestedKeys(path+"."+k, obj[k], sub, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// gcpNestedScopedArg returns the path of the first property below the top
// level whose name could point a call at a resource, or "".
func gcpNestedScopedArg(props map[string]any) string {
	for _, name := range gcpSortedKeys(props) {
		if p, _ := props[name].(map[string]any); p != nil {
			if found := gcpScopedBelow(p, name); found != "" {
				return found
			}
		}
	}
	return ""
}

func gcpScopedBelow(node map[string]any, path string) string {
	if items, _ := node["items"].(map[string]any); items != nil {
		if found := gcpScopedBelow(items, path+"[]"); found != "" {
			return found
		}
	}
	sub, _ := node["properties"].(map[string]any)
	for _, name := range gcpSortedKeys(sub) {
		if gcpScopedArgNames[name] {
			return path + "." + name
		}
		if p, _ := sub[name].(map[string]any); p != nil {
			if found := gcpScopedBelow(p, path+"."+name); found != "" {
				return found
			}
		}
	}
	return ""
}

// Test lists the GKE endpoint (reachability) and makes one harmless
// authenticated read (the credentials, roles/mcp.toolUser and a viewer role).
func (bd *gcpBinding) Test(ctx context.Context) (TestResult, error) {
	start := time.Now()
	res := TestResult{Tools: len(bd.lists[ConsumerKobi]), Identity: bd.identity}
	finish := func() (TestResult, error) {
		res.LatencyMs = time.Since(start).Milliseconds()
		return res, nil
	}
	ep := bd.endpoints[gcpCanaryService]
	if ep == nil {
		res.Error = "The Kubernetes Engine endpoint is not configured."
		return finish()
	}
	if _, err := ep.lister.ListTools(ctx); err != nil {
		res.Error = "KubeBolt could not reach Google's MCP endpoint at " + ep.host + "."
		res.Hint = "Check that the KubeBolt API can reach *.googleapis.com over HTTPS (egress rules, proxy)."
		return finish()
	}
	local := gcpLocalName(gcpCanaryService, gcpCanaryTool)
	res.Canary = &Canary{Tool: local}
	// The canary goes through the same gate as a model's call: a tool this
	// binding did not bind (Google stopped marking it read-only, or the
	// catalog changed) is not called, even with fixed arguments.
	bt := bd.tools[local]
	var canaryArgs json.RawMessage
	if bt != nil {
		canaryArgs, _ = bt.pinArgs(gcpMustJSON(map[string]string{"parent": "projects/" + bd.project + "/locations/-"}), bd.project)
	}
	if canaryArgs == nil {
		res.Error = "Google's Kubernetes Engine endpoint no longer offers " + gcpCanaryTool + " as KubeBolt reviewed it, so the credentials were not tested."
		res.Hint = "Test again in a few minutes; if it keeps failing, contact KubeBolt support."
		return finish()
	}
	who := "the service account"
	if bd.mode == gcpModeADC {
		who = "the identity KubeBolt runs as"
	}
	rolesHint := "Grant " + who + " " + gcpToolUserRole + " and " + gcpServiceRoles[gcpCanaryService] + " on project " + bd.project +
		", and make sure the Kubernetes Engine API is enabled."
	out, err := bt.ep.caller.CallTool(ctx, bt.remote, canaryArgs)
	var httpErr *mcpclient.HTTPError
	switch {
	case errors.Is(err, mcpclient.ErrUnauthorized):
		res.Error = "Google Cloud rejected the credentials."
		res.Hint = "Check that the key belongs to an enabled service account and has not been deleted, or paste a new key."
		if bd.mode == gcpModeADC {
			res.Hint = "Check the identity KubeBolt runs as: its Workload Identity binding, the metadata server or gcloud's application default credentials."
		}
		return finish()
	case errors.As(err, &httpErr) && httpErr.Status == 403:
		res.Error = gcpHumanError(ep.host, bd.mode, err).Error() + "."
		res.Hint = rolesHint
		return finish()
	case err != nil && strings.Contains(err.Error(), "google: get token"):
		res.Error = gcpHumanError(ep.host, bd.mode, err).Error() + "."
		res.Hint = "The key may belong to a deleted or disabled service account, or have been revoked; paste a new key. " +
			"If it is valid, check that KubeBolt can reach oauth2.googleapis.com."
		if bd.mode == gcpModeADC {
			res.Hint = "Check the identity KubeBolt runs as: its Workload Identity binding, the metadata server or gcloud's application default credentials " +
				"(renew them with gcloud auth application-default login if they expired). " +
				"If it is in place, check that KubeBolt can reach the metadata server or oauth2.googleapis.com."
		}
		return finish()
	case err != nil:
		res.Error = gcpHumanError(ep.host, bd.mode, err).Error() + "."
		res.Hint = "Check that the KubeBolt API can reach *.googleapis.com and oauth2.googleapis.com over HTTPS, then test again."
		return finish()
	case out.IsError:
		res.Error = "Google Cloud refused the test call: " + gcpCapText(gcpCleanText(out.Text()), 300)
		res.Hint = rolesHint
		return finish()
	}
	res.Canary.OK = true
	res.OK = true
	if len(bd.missing) > 0 {
		res.Hint = "Some Google endpoints could not be listed (" + strings.Join(bd.missing, ", ") +
			"); KubeBolt lists them again in the background and their tools come back once they answer."
	}
	return finish()
}

// Missing implements PartialBinding: the endpoints whose catalog could not
// be listed at bind time (a timeout, a 5xx), sorted. The gateway binds a
// partial connector again later and keeps whichever binding misses less.
func (bd *gcpBinding) Missing() []string { return append([]string(nil), bd.missing...) }

// Close ends the clients' sessions. Google's servers are stateless, so this
// sends nothing today; it stays correct if one ever issues a session.
func (bd *gcpBinding) Close(ctx context.Context) error {
	var errs []error
	for _, id := range gcpSortedKeys(bd.endpoints) {
		ep := bd.endpoints[id]
		errs = append(errs, ep.caller.Close(ctx), ep.lister.Close(ctx))
	}
	return errors.Join(errs...)
}

func gcpMustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
