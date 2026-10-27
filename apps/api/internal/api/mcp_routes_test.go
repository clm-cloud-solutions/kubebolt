package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpgateway"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// Wiring of the MCP connectors through the REAL router:
// NewRouter over a real gateway, a Bolt-backed settings runtime, real JWT and
// service-token authentication, and a fake connector registered as "fake".
// What the admin handlers and the gateway do on their own is tested in their
// packages; these tests check that the pieces are CONNECTED — a correct
// handler mounted without its guard, or a gateway the chat never asks, fails
// nothing anywhere else.

const (
	wiringJWTSecret = "mcp-wiring-test-secret-32-bytes!!"
	// wiringAPIKey is the fake connector's secret: it must never appear in an
	// audit record or a response.
	wiringAPIKey = "wiring-canary-4c1d-never-in-a-record"
	wiringEnable = `{"enabled":true,"mode":"key","fields":{"region":"us-east-1"},"secrets":{"apiKey":"` + wiringAPIKey + `"}}`
)

// wiringLocals are the fake connector's local tool names for Kobi.
// search_documentation is one of the provider knowledge tools, which the real
// bindings keep for Kobi: Autopilot is served call alone.
var wiringLocals = []string{"call", "search_documentation"}

type wiringCall struct {
	consumer mcpcatalog.Consumer
	tool     string
	args     string
}

// wiringBinder is the fake connector. Every call is recorded, so a test can
// tell a call that reached the connector from one refused before it.
type wiringBinder struct {
	mu    sync.Mutex
	calls []wiringCall
}

func (b *wiringBinder) Bind(context.Context, mcpcatalog.Resolved, mcpcatalog.Env) (mcpcatalog.Binding, error) {
	return &wiringBinding{b: b}, nil
}

func (b *wiringBinder) snapshot() []wiringCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]wiringCall(nil), b.calls...)
}

type wiringBinding struct{ b *wiringBinder }

func (f *wiringBinding) Tools(c mcpcatalog.Consumer) []mcpcatalog.ToolSpec {
	locals := wiringLocals
	if c == mcpcatalog.ConsumerAutopilot {
		locals = wiringLocals[:1]
	}
	out := make([]mcpcatalog.ToolSpec, 0, len(locals))
	for _, n := range locals {
		out = append(out, mcpcatalog.ToolSpec{
			Name:        n,
			Description: "Fake " + n + ".",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		})
	}
	return out
}

func (f *wiringBinding) Call(_ context.Context, c mcpcatalog.Consumer, tool string, args json.RawMessage) (mcpcatalog.CallResult, error) {
	f.b.mu.Lock()
	f.b.calls = append(f.b.calls, wiringCall{consumer: c, tool: tool, args: string(args)})
	f.b.mu.Unlock()
	return mcpcatalog.CallResult{
		Text:   `{"DBInstances":[{"DBInstanceIdentifier":"orders-db","DBInstanceStatus":"storage-full"}]}`,
		Digest: "1 DB instance",
	}, nil
}

func (f *wiringBinding) Test(context.Context) (mcpcatalog.TestResult, error) {
	return mcpcatalog.TestResult{OK: true, Tools: len(wiringLocals)}, nil
}

func (f *wiringBinding) Close(context.Context) error { return nil }

func wiringDef(b mcpcatalog.Binder) *mcpcatalog.ConnectorDef {
	return &mcpcatalog.ConnectorDef{
		ID:          "fake",
		Name:        "Fake Cloud",
		Category:    mcpcatalog.CategoryCloud,
		Description: "Reads fake cloud resources.",
		Tags:        []string{"cloud"},
		DocsURL:     "https://example.com/docs",
		MinPlan:     auth.PlanTeam,
		AuthModes: []mcpcatalog.AuthMode{{
			ID: "key", Label: "Access key", Description: "An API key you create.",
			Fields: []mcpcatalog.FieldSchema{
				{Name: "region", Label: "Region", Required: true},
				{Name: "apiKey", Label: "API key", Secret: true, Required: true},
			},
		}},
		Families: []mcpcatalog.Family{{Label: "Inventory", Detail: "Lists and reads things.", Kobi: true, Autopilot: true}},
		Never:    []string{"Reads secret values."},
		Guide: func(mcpcatalog.Env, map[string]string) mcpcatalog.SetupGuide {
			return mcpcatalog.SetupGuide{Steps: []mcpcatalog.SetupStep{{Title: "Create a key", Body: "Create it."}}}
		},
		Binder: b,
	}
}

// recordingSessions is the Kobi usage store: it keeps every SessionRecord the
// chat handler persists.
type recordingSessions struct {
	mu   sync.Mutex
	recs []copilot.SessionRecord
}

func (s *recordingSessions) Record(rec *copilot.SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, *rec)
	return nil
}

func (s *recordingSessions) Query(time.Time, time.Time, int) ([]copilot.SessionRecord, error) {
	return nil, nil
}

func (s *recordingSessions) Count() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs), nil
}

func (s *recordingSessions) last(t *testing.T) copilot.SessionRecord {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recs) == 0 {
		t.Fatal("the chat persisted no session record")
	}
	return s.recs[len(s.recs)-1]
}

// wiring is one install: the real router and what a test inspects behind it.
type wiring struct {
	router   http.Handler
	gw       *mcpgateway.Gateway // nil when the feature is off
	binder   *wiringBinder
	audit    *audit.MemoryStore
	sessions *recordingSessions
	// Bearer credentials: an org admin and an editor (JWTs), and a platform
	// service token (kbs_) such as Autopilot's.
	admin, editor, service string
	// jwt mints further users' tokens, e.g. a member of another org.
	jwt *auth.JWTService
}

// tokenFor mints an access token for u. A User.OrgID becomes the token's
// tenant claim, which ResolveTenant honours even in a single-tenant build —
// how a test puts a second org behind the same router.
func (w *wiring) tokenFor(t *testing.T, u auth.User) string {
	t.Helper()
	tok, err := w.jwt.GenerateAccessToken(&u)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return tok
}

// wiringEscalation is the escalation model the next wiring is built with;
// nil (no escalation) unless a test sets it and puts it back.
var wiringEscalation *config.ProviderConfig

// newWiring builds the install. provider is the Kobi provider name the chat
// should use ("" leaves Kobi unconfigured). Not parallel-safe: it installs the
// process-wide audit sink, as the other audit tests in this package do.
func newWiring(t *testing.T, gatewayOn bool, provider string) *wiring {
	t.Helper()
	// A manager with no cluster at all: the connectors must work without one.
	return newWiringOn(t, gatewayOn, provider, &cluster.Manager{})
}

// newWiringOn is newWiring over a manager the test built, for a test that
// needs clusters (or their names) behind the router.
func newWiringOn(t *testing.T, gatewayOn bool, provider string, manager *cluster.Manager) *wiring {
	t.Helper()
	return newWiringWithAgents(t, gatewayOn, provider, manager, nil)
}

// newWiringWithAgents is newWiringOn with the agent registry the router
// consults: GET /clusters keeps an agent-proxy cluster only for the org its
// live agent authenticated under, so a test of what an org may see needs it.
func newWiringWithAgents(t *testing.T, gatewayOn bool, provider string, manager *cluster.Manager, agents *channel.AgentRegistry) *wiring {
	t.Helper()
	if auth.MultiTenantEnabled {
		// The fixture authenticates as the single default org, and a
		// multi-tenant build resolves orgs, plans and Kobi's model per tenant.
		// The gateway's multi-tenant behaviour is tested in its own package.
		t.Skip("wiring fixture is single-tenant")
	}
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	copilotCfg := config.CopilotConfig{
		Enabled:                   provider != "",
		Primary:                   config.ProviderConfig{Provider: provider, Model: "fake-model", APIKey: "test-key"},
		Escalation:                wiringEscalation,
		MaxTokens:                 1024,
		ActionsEnabled:            true,
		DestructiveActionsEnabled: true,
	}
	rt, err := settings.NewRuntime(store, store, copilotCfg, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte(wiringJWTSecret))
	if err != nil {
		t.Fatalf("settings runtime: %v", err)
	}

	w := &wiring{binder: &wiringBinder{}, audit: audit.NewMemoryStore(), sessions: &recordingSessions{}}
	audit.SetSink(w.audit, nil)
	t.Cleanup(func() { audit.SetSink(nil, nil) })

	// Built with the gateway off too, as main.go does: the price lists read
	// the catalog whether or not this install runs connectors.
	cat, err := mcpcatalog.NewWith(wiringDef(w.binder))
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if gatewayOn {
		w.gw = mcpgateway.New(cat, rt, mcpgateway.Options{
			AmbientAllowed: true,
			CallTimeout:    2 * time.Second,
			BindTimeout:    2 * time.Second,
			// Well above the per-turn cap, so the cap is what a test meets.
			OrgRatePerMin: 6000,
		})
		t.Cleanup(w.gw.Close)
	}

	authCfg := config.AuthConfig{Enabled: true, JWTSecret: []byte(wiringJWTSecret), AccessTokenExpiry: 15 * time.Minute, RefreshTokenExpiry: time.Hour}
	jwtSvc := auth.NewJWTService(authCfg)
	w.jwt = jwtSvc
	authH := auth.NewHandlers(store, jwtSvc, authCfg)
	for _, u := range []struct {
		dst  *string
		user auth.User
	}{
		{&w.admin, auth.User{ID: "u-admin", Username: "alice", Role: auth.RoleAdmin}},
		{&w.editor, auth.User{ID: "u-editor", Username: "bob", Role: auth.RoleEditor}},
	} {
		tok, err := jwtSvc.GenerateAccessToken(&u.user)
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		*u.dst = tok
	}
	apiTokens, err := auth.NewAPITokenStore(store.DB())
	if err != nil {
		t.Fatalf("api token store: %v", err)
	}
	authH.SetAPITokenStore(apiTokens)
	w.service, _, err = apiTokens.Issue(context.Background(), auth.TokenTypeService, auth.RoleViewer, []string{auth.ScopeAll}, "autopilot", "test", nil)
	if err != nil {
		t.Fatalf("service token: %v", err)
	}

	w.router = NewRouter(manager, nil, nil, copilotCfg, w.sessions, nil, authH, nil, nil, nil, "",
		nil, nil, "", nil, nil, nil, nil, nil, nil, nil, rt, nil, agents, nil, nil, nil, w.gw, cat)
	return w
}

func (w *wiring) do(t *testing.T, bearer, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	w.router.ServeHTTP(rec, req)
	return rec
}

// enable turns the fake connector on through the admin API, the way the UI
// does.
func (w *wiring) enable(t *testing.T) {
	t.Helper()
	if rec := w.do(t, w.admin, http.MethodPut, "/api/v1/admin/mcp-connectors/fake", wiringEnable); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
}

func (w *wiring) auditRecords(t *testing.T, action string) []audit.Record {
	t.Helper()
	recs, err := w.audit.ListOrg(auth.DefaultTenantName, 0)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var out []audit.Record
	for _, r := range recs {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

// ----- route shape -----

const connectorsBase = "/api/v1/admin/mcp-connectors"

// wantConnectorRoutes are the connector routes, as chi.Walk prints them.
var wantConnectorRoutes = []string{
	"DELETE " + connectorsBase + "/{id}",
	"GET " + connectorsBase + "/",
	"GET " + connectorsBase + "/{id}",
	"POST " + connectorsBase + "/{id}/test",
	"PUT " + connectorsBase + "/{id}",
}

type walkedRoute struct {
	route string
	mws   []func(http.Handler) http.Handler
}

func walkRoutes(t *testing.T, r http.Handler) []walkedRoute {
	t.Helper()
	var out []walkedRoute
	if err := chi.Walk(r.(chi.Routes), func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		out = append(out, walkedRoute{route: method + " " + route, mws: mws})
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

// carries reports whether one of mws is the middleware whose symbol contains
// name. By symbol, not by code pointer as service_token_routes_test does:
// RequireRole and auditAdminRoutes return closures, and when the compiler
// inlines the constructor the closure gets a symbol — and a code pointer — of
// its own at every call site (NewRouter.func2.1.3.RequireRole.1).
func carries(mws []func(http.Handler) http.Handler, name string) bool {
	for _, mw := range mws {
		if fn := runtime.FuncForPC(reflect.ValueOf(mw).Pointer()); fn != nil && strings.Contains(fn.Name(), name) {
			return true
		}
	}
	return false
}

func TestMCPConnectorRoutes_MountedWithTheirGuards(t *testing.T) {
	w := newWiring(t, true, "")

	// The symbol names the middleware, not its argument: the behavioural
	// test below pins the role (admin), the audit target (mcp-connector) and
	// that no cluster is needed. chi.Walk does not report the middlewares of
	// the group a subrouter is mounted in, so requireConnector can only be
	// ruled out by behaviour.
	var got []string
	for _, r := range walkRoutes(t, w.router) {
		if !strings.Contains(r.route, "mcp-connectors") {
			continue
		}
		got = append(got, r.route)
		if !carries(r.mws, ".RequireRole.") {
			t.Errorf("%s is not behind RequireRole: any signed-in user could read or change the org's cloud credentials", r.route)
		}
		if !carries(r.mws, ".auditAdminRoutes.") {
			t.Errorf("%s is not audited: a connector change would leave no record", r.route)
		}
		if strings.Contains(r.route, "/api/v1/mcp") {
			t.Errorf("%s is under /mcp, which is KubeBolt's own MCP server", r.route)
		}
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, wantConnectorRoutes) {
		t.Errorf("connector routes:\n got %v\nwant %v", got, wantConnectorRoutes)
	}
}

func TestMCPConnectorRoutes_AbsentWhenTheFeatureIsOff(t *testing.T) {
	w := newWiring(t, false, "")
	for _, r := range walkRoutes(t, w.router) {
		if strings.Contains(r.route, "mcp-connectors") {
			t.Errorf("%s is mounted with no gateway", r.route)
		}
	}
	// The UI's feature-off state keys on a plain 404.
	for _, path := range []string{connectorsBase, connectorsBase + "/aws"} {
		if rec := w.do(t, w.admin, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with the feature off: %d, want 404", path, rec.Code)
		}
	}
}

// The guards pinned by behaviour: an editor is refused, an admin's change is
// applied and audited as an mcp-connector change, and the record carries
// neither the body nor the secret in it.
func TestMCPConnectorRoutes_AdminOnlyAndAudited(t *testing.T) {
	w := newWiring(t, true, "")

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, connectorsBase, ""},
		{http.MethodGet, connectorsBase + "/fake", ""},
		{http.MethodPut, connectorsBase + "/fake", wiringEnable},
		{http.MethodPost, connectorsBase + "/fake/test", ""},
		{http.MethodDelete, connectorsBase + "/fake", ""},
	} {
		if rec := w.do(t, w.editor, c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("editor %s %s: %d, want 403", c.method, c.path, rec.Code)
		}
	}
	for _, r := range w.auditRecords(t, "mcp-connector_update") {
		if r.Result == "success" {
			t.Errorf("a refused editor PUT reads as applied in the trail: %+v", r)
		}
	}

	// The manager has no cluster at all, so a 200 here also proves the
	// routes are outside requireConnector.
	rec := w.do(t, w.admin, http.MethodGet, connectorsBase, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"fake"`) {
		t.Fatalf("admin list with no cluster connected: %d %s", rec.Code, rec.Body)
	}
	rec = w.do(t, w.admin, http.MethodPut, connectorsBase+"/fake", wiringEnable)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin PUT: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), wiringAPIKey) {
		t.Fatal("the PUT response echoes the secret")
	}

	var applied []audit.Record
	for _, r := range w.auditRecords(t, "mcp-connector_update") {
		if r.Result == "success" {
			applied = append(applied, r)
		}
	}
	if len(applied) != 1 {
		t.Fatalf("got %d applied mcp-connector_update records, want 1", len(applied))
	}
	got := applied[0]
	if got.TargetType != "mcp-connector" || got.TargetName != "fake" || got.Username != "alice" {
		t.Errorf("audit record: %+v", got)
	}
	if got.ClusterID != "" {
		t.Errorf("a connector is org state; the record names cluster %q", got.ClusterID)
	}
	raw, _ := json.Marshal(w.auditRecords(t, "mcp-connector_update"))
	if strings.Contains(string(raw), wiringAPIKey) || strings.Contains(string(raw), "us-east-1") {
		t.Errorf("the audit trail copied the request body: %s", raw)
	}
}
