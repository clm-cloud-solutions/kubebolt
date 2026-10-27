package mcpgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// A fake connector behind the real gateway, catalog and settings: the binder
// counts binds, records what it was handed, and lets each test script the
// tools, the calls and the connectivity test.

const (
	fakeID       = "fake"
	testAPIKey   = "sk-fake-9f2c7e1a-plaintext-canary"
	testJWTKey   = "test-jwt-secret-32-bytes-padding-"
	regionFieldA = "us-east-1"
	regionFieldB = "eu-west-1"
)

type fakeBinder struct {
	mu       sync.Mutex
	binds    int
	bindErr  error
	bindGate chan struct{}
	// ignoreCtx makes Bind wait on bindGate without honouring its context,
	// like a token fetch that drops cancellation.
	ignoreCtx bool
	// halfBuilt makes a failing Bind also return the binding it had built.
	halfBuilt bool
	// missing is what the next binding reports it could not bring up
	// (mcpcatalog.PartialBinding).
	missing []string
	// closeGate, when set, holds every binding's Close until it is closed:
	// a provider DELETE that takes a while.
	closeGate chan struct{}
	tools     map[mcpcatalog.Consumer][]mcpcatalog.ToolSpec
	call      func(ctx context.Context, b *fakeBinding, c mcpcatalog.Consumer, tool string, args json.RawMessage) (mcpcatalog.CallResult, error)
	test      func(ctx context.Context, b *fakeBinding) (mcpcatalog.TestResult, error)
	resolved  []mcpcatalog.Resolved
	bindings  []*fakeBinding
	calls     atomic.Int32
}

func newFakeBinder() *fakeBinder {
	return &fakeBinder{tools: map[mcpcatalog.Consumer][]mcpcatalog.ToolSpec{
		// Deliberately out of order: the gateway sorts.
		mcpcatalog.ConsumerKobi: {
			{Name: "list_things", Description: "List things.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
			{Name: "get_thing", Description: "Get a thing.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}}},
		},
		mcpcatalog.ConsumerAutopilot: {
			{Name: "get_thing", Description: "Get a thing."},
		},
	}}
}

func (f *fakeBinder) Bind(ctx context.Context, r mcpcatalog.Resolved, _ mcpcatalog.Env) (mcpcatalog.Binding, error) {
	f.mu.Lock()
	f.binds++
	f.resolved = append(f.resolved, r)
	err, gate, ignoreCtx, halfBuilt := f.bindErr, f.bindGate, f.ignoreCtx, f.halfBuilt
	f.mu.Unlock()
	if gate != nil && ignoreCtx {
		<-gate
	} else if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil && !halfBuilt {
		return nil, err
	}
	b := &fakeBinding{f: f, r: r}
	f.mu.Lock()
	b.missing = append([]string(nil), f.missing...)
	f.bindings = append(f.bindings, b)
	f.mu.Unlock()
	return b, err
}

func (f *fakeBinder) bindCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.binds
}

func (f *fakeBinder) lastResolved() mcpcatalog.Resolved {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolved[len(f.resolved)-1]
}

func (f *fakeBinder) binding(i int) *fakeBinding {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bindings[i]
}

func (f *fakeBinder) set(fn func(f *fakeBinder)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

type fakeBinding struct {
	f       *fakeBinder
	r       mcpcatalog.Resolved
	missing []string
	calls   atomic.Int32
	closed  atomic.Bool
}

func (b *fakeBinding) Missing() []string { return b.missing }

func (b *fakeBinding) Tools(c mcpcatalog.Consumer) []mcpcatalog.ToolSpec {
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	return append([]mcpcatalog.ToolSpec(nil), b.f.tools[c]...)
}

func (b *fakeBinding) Call(ctx context.Context, c mcpcatalog.Consumer, tool string, args json.RawMessage) (mcpcatalog.CallResult, error) {
	b.calls.Add(1)
	b.f.calls.Add(1)
	b.f.mu.Lock()
	fn := b.f.call
	b.f.mu.Unlock()
	if fn != nil {
		return fn(ctx, b, c, tool, args)
	}
	return mcpcatalog.CallResult{Text: "ok from " + b.r.Org + " in " + b.r.Fields["region"]}, nil
}

func (b *fakeBinding) Test(ctx context.Context) (mcpcatalog.TestResult, error) {
	b.f.mu.Lock()
	fn := b.f.test
	b.f.mu.Unlock()
	if fn != nil {
		return fn(ctx, b)
	}
	return mcpcatalog.TestResult{OK: true, Tools: len(b.Tools(mcpcatalog.ConsumerKobi)), Identity: "fake-identity"}, nil
}

func (b *fakeBinding) Close(ctx context.Context) error {
	b.f.mu.Lock()
	gate := b.f.closeGate
	b.f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.closed.Store(true)
	return nil
}

var regionPattern = `^[a-z]{2}-[a-z]+-[0-9]$`

func fakeDef(id string, b mcpcatalog.Binder) *mcpcatalog.ConnectorDef {
	return &mcpcatalog.ConnectorDef{
		ID:          id,
		Name:        "Fake Cloud",
		Category:    mcpcatalog.CategoryCloud,
		Description: "Reads fake cloud resources.",
		Tags:        []string{"cloud", "fake"},
		DocsURL:     "https://example.com/docs",
		MinPlan:     auth.PlanTeam,
		AuthModes: []mcpcatalog.AuthMode{
			{
				ID: "key", Label: "Access key", Description: "An API key you create.",
				Fields: []mcpcatalog.FieldSchema{
					{Name: "region", Label: "Region", Required: true, Pattern: regionPattern},
					{Name: "account", Label: "Account"},
					{Name: "apiKey", Label: "API key", Secret: true, Required: true},
				},
			},
			{
				ID: "ambient", Label: "This server's identity", Description: "The machine's own credentials.", SelfHostedOnly: true,
				Fields: []mcpcatalog.FieldSchema{
					{Name: "region", Label: "Region", Required: true, Pattern: regionPattern},
				},
			},
		},
		Families: []mcpcatalog.Family{{Label: "Inventory", Detail: "Lists and reads things.", Kobi: true, Autopilot: true}},
		Never:    []string{"Reads secret values."},
		Guide: func(_ mcpcatalog.Env, fields map[string]string) mcpcatalog.SetupGuide {
			return mcpcatalog.SetupGuide{Steps: []mcpcatalog.SetupStep{{
				Title: "Create an API key", Body: "Create it in " + fields["region"] + ".", Code: "fake keys create", Language: "shell",
			}}}
		},
		Binder: b,
	}
}

// fakeClock is a settable clock for the rate limits, the pace of tests and
// the bind-retry window.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// countingStore counts writes of the MCP settings blob, to prove what the
// request path does not write.
type countingStore struct {
	*auth.Store
	mcpWrites atomic.Int32
}

func (c *countingStore) SetOrgSetting(ctx context.Context, key string, value []byte) error {
	if key == "mcp:connectors" {
		c.mcpWrites.Add(1)
	}
	return c.Store.SetOrgSetting(ctx, key, value)
}

type harness struct {
	t     *testing.T
	store *auth.Store
	cs    *countingStore
	st    *settings.Runtime
	gw    *Gateway
	fb    *fakeBinder
	clock *fakeClock
	audit *audit.MemoryStore
	reg   *prometheus.Registry
	opts  Options
}

func newHarness(t *testing.T, mutate func(o *Options)) *harness {
	t.Helper()
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &harness{t: t, store: store, cs: &countingStore{Store: store}, fb: newFakeBinder()}
	h.st = h.runtime(testJWTKey)
	h.clock = &fakeClock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	h.audit = audit.NewMemoryStore()
	audit.SetSink(h.audit, nil)
	t.Cleanup(func() { audit.SetSink(nil, nil) })
	h.reg = prometheus.NewRegistry()
	h.opts = Options{
		AmbientAllowed: true,
		CallTimeout:    2 * time.Second,
		BindTimeout:    2 * time.Second,
		OrgRatePerMin:  6000,
		Registerer:     h.reg,
		Now:            h.clock.Now,
	}
	if mutate != nil {
		mutate(&h.opts)
	}
	h.gw = h.gateway(h.st, fakeDef(fakeID, h.fb))
	return h
}

func (h *harness) runtime(jwt string) *settings.Runtime {
	h.t.Helper()
	rt, err := settings.NewRuntime(h.store, h.cs, config.CopilotConfig{}, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte(jwt))
	if err != nil {
		h.t.Fatalf("settings runtime: %v", err)
	}
	return rt
}

func (h *harness) gateway(st *settings.Runtime, defs ...*mcpcatalog.ConnectorDef) *Gateway {
	h.t.Helper()
	cat, err := mcpcatalog.NewWith(defs...)
	if err != nil {
		h.t.Fatalf("catalog: %v", err)
	}
	gw := New(cat, st, h.opts)
	h.t.Cleanup(gw.Close)
	return gw
}

func orgCtx(org string) context.Context {
	return auth.WithTenantID(context.Background(), org)
}

// roleCtx is a request context for org signed in with role, built the way
// RequireAuth builds it.
func (h *harness) roleCtx(org string, role auth.Role) context.Context {
	h.t.Helper()
	cfg := config.AuthConfig{Enabled: true, JWTSecret: []byte(testJWTKey), AccessTokenExpiry: time.Hour, RefreshTokenExpiry: time.Hour}
	jwtSvc := auth.NewJWTService(cfg)
	token, err := jwtSvc.GenerateAccessToken(&auth.User{ID: "u-" + string(role), Username: string(role), Role: role})
	if err != nil {
		h.t.Fatalf("token: %v", err)
	}
	var ctx context.Context
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	auth.NewHandlers(h.store, jwtSvc, cfg).RequireAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctx = r.Context()
	})).ServeHTTP(httptest.NewRecorder(), req)
	if ctx == nil || auth.ContextRole(req.WithContext(ctx)) != role {
		h.t.Fatalf("no %s claims in the request context", role)
	}
	return auth.WithTenantID(ctx, org)
}

// setLastError records a failed test of the stored configuration straight
// in settings.
func (h *harness) setLastError(ctx context.Context, msg string) {
	h.t.Helper()
	all, err := h.st.MCP(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.SetMCPConnectorVerification(ctx, fakeID, settings.MCPVerification{ConfigAt: all.Connectors[fakeID].UpdatedAt, LastError: msg}); err != nil {
		h.t.Fatal(err)
	}
}

// generatedDef is the fake connector with a value KubeBolt generates for the
// key mode, like the AWS external ID.
func generatedDef(b mcpcatalog.Binder) *mcpcatalog.ConnectorDef {
	def := fakeDef(fakeID, b)
	def.AuthModes[0].Fields = append(def.AuthModes[0].Fields, mcpcatalog.FieldSchema{
		Name: "externalId", Label: "External ID", Required: true, Generated: true, Pattern: `^[\w+=,.@:/-]{16,}$`, MaxLen: 1224,
	})
	return def
}

func ptr[T any](v T) *T { return &v }

// enable stores a complete, enabled configuration through the gateway.
func (h *harness) enable(ctx context.Context, region string) ConnectorView {
	h.t.Helper()
	v, err := h.gw.Put(ctx, fakeID, PutRequest{
		Enabled: ptr(true),
		Mode:    ptr("key"),
		Fields:  map[string]*string{"region": ptr(region)},
		Secrets: map[string]*string{"apiKey": ptr(testAPIKey)},
	}, "admin")
	if err != nil {
		h.t.Fatalf("enable: %v", err)
	}
	return v
}

func (h *harness) toolNames(ctx context.Context, c mcpcatalog.Consumer) []string {
	h.t.Helper()
	defs, err := h.gw.ToolsFor(ctx, c)
	if err != nil {
		h.t.Fatalf("tools for %s: %v", c, err)
	}
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names
}

func (h *harness) records(org string) []audit.Record {
	h.t.Helper()
	recs, err := h.audit.ListOrg(org, 0)
	if err != nil {
		h.t.Fatalf("audit list: %v", err)
	}
	return recs
}

func (h *harness) masked(ctx context.Context) settings.MaskedMCPConnector {
	h.t.Helper()
	m, _, err := h.st.MaskedMCPConnector(ctx, fakeID)
	if err != nil {
		h.t.Fatalf("masked: %v", err)
	}
	return m
}

// eventually polls cond for up to a second: closing a replaced binding
// happens in the background.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
