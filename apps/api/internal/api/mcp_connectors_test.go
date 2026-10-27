package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpgateway"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// The admin handlers over a REAL gateway and a Bolt-backed settings runtime,
// with a fake connector registered under the "aws" id (so the same harness
// serves the external-connections tests): what is exercised is the HTTP
// mapping, not a provider.

const (
	mcpTestJWT    = "mcp-connectors-test-secret-32-bytes!"
	mcpTestAPIKey = "sk-canary-7f3e9d2b-never-in-a-response"
)

type connFakeBinder struct {
	mu    sync.Mutex
	tools []string
	// autopilot, when set, is Autopilot's list; tools is then Kobi's alone.
	autopilot []string
	calls     []string
}

func (b *connFakeBinder) Bind(context.Context, mcpcatalog.Resolved, mcpcatalog.Env) (mcpcatalog.Binding, error) {
	return &connFakeBinding{b: b}, nil
}

// ToolNames makes the fake a mcpgateway.ToolLister, so the admin view lists
// tools before any bind.
func (b *connFakeBinder) ToolNames(c mcpcatalog.Consumer) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c == mcpcatalog.ConsumerAutopilot && b.autopilot != nil {
		return append([]string(nil), b.autopilot...)
	}
	return append([]string(nil), b.tools...)
}

type connFakeBinding struct{ b *connFakeBinder }

func (f *connFakeBinding) Tools(c mcpcatalog.Consumer) []mcpcatalog.ToolSpec {
	var out []mcpcatalog.ToolSpec
	for _, n := range f.b.ToolNames(c) {
		out = append(out, mcpcatalog.ToolSpec{Name: n, Description: "Fake " + n + ".", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}})
	}
	return out
}

func (f *connFakeBinding) Call(_ context.Context, _ mcpcatalog.Consumer, tool string, _ json.RawMessage) (mcpcatalog.CallResult, error) {
	f.b.mu.Lock()
	f.b.calls = append(f.b.calls, tool)
	f.b.mu.Unlock()
	return mcpcatalog.CallResult{Text: `{"DBInstances":[{"DBInstanceIdentifier":"orders-db","DBInstanceStatus":"storage-full"}]}`}, nil
}

func (f *connFakeBinding) Test(context.Context) (mcpcatalog.TestResult, error) {
	return mcpcatalog.TestResult{OK: true, Tools: len(f.b.ToolNames(mcpcatalog.ConsumerKobi)), Identity: "fake-identity"}, nil
}

func (f *connFakeBinding) Close(context.Context) error { return nil }

func connFakeDef(b mcpcatalog.Binder) *mcpcatalog.ConnectorDef {
	return &mcpcatalog.ConnectorDef{
		ID:          "aws",
		Name:        "Fake AWS",
		Category:    mcpcatalog.CategoryCloud,
		Description: "Reads fake cloud resources.",
		Tags:        []string{"cloud"},
		DocsURL:     "https://example.com/docs",
		MinPlan:     auth.PlanTeam,
		AuthModes: []mcpcatalog.AuthMode{{
			ID: "key", Label: "Access key", Description: "An API key you create.",
			Fields: []mcpcatalog.FieldSchema{
				{Name: "region", Label: "Region", Required: true, Pattern: `^[a-z]{2}-[a-z]+-[0-9]$`},
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

type mcpAPIHarness struct {
	t      *testing.T
	h      *handlers
	rt     *settings.Runtime
	fb     *connFakeBinder
	router http.Handler
	token  string
}

func newMCPAPIHarness(t *testing.T, mutate func(o *mcpgateway.Options)) *mcpAPIHarness {
	t.Helper()
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rt, err := settings.NewRuntime(store, store, config.CopilotConfig{}, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte(mcpTestJWT))
	if err != nil {
		t.Fatalf("settings runtime: %v", err)
	}
	fb := &connFakeBinder{tools: []string{"call", "search_documentation"}}
	cat, err := mcpcatalog.NewWith(connFakeDef(fb))
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	opts := mcpgateway.Options{
		AmbientAllowed: true,
		CallTimeout:    2 * time.Second,
		BindTimeout:    2 * time.Second,
		OrgRatePerMin:  6000,
		Now:            func() time.Time { return now },
	}
	if mutate != nil {
		mutate(&opts)
	}
	gw := mcpgateway.New(cat, rt, opts)
	t.Cleanup(gw.Close)

	authCfg := config.AuthConfig{Enabled: true, JWTSecret: []byte(mcpTestJWT), AccessTokenExpiry: 15 * time.Minute, RefreshTokenExpiry: time.Hour}
	jwtSvc := auth.NewJWTService(authCfg)
	authH := auth.NewHandlers(store, jwtSvc, authCfg)
	token, err := jwtSvc.GenerateAccessToken(&auth.User{ID: "u-1", Username: "alice", Role: auth.RoleAdmin})
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	h := &handlers{mcpGateway: gw, settingsRuntime: rt}
	r := chi.NewRouter()
	r.Use(authH.RequireAuth, authH.ResolveTenant)
	mountMCPConnectorsForTest(r, h)
	return &mcpAPIHarness{t: t, h: h, rt: rt, fb: fb, router: r, token: token}
}

// mountMCPConnectorsForTest mirrors the connector routes; the real
// mount (with RequireRole and auditAdminRoutes) lives in router.go.
func mountMCPConnectorsForTest(r chi.Router, h *handlers) {
	r.Get("/admin/mcp-connectors", h.handleListMCPConnectors)
	r.Get("/admin/mcp-connectors/{id}", h.handleGetMCPConnector)
	r.Put("/admin/mcp-connectors/{id}", h.handlePutMCPConnector)
	r.Post("/admin/mcp-connectors/{id}/test", h.handleTestMCPConnector)
	r.Delete("/admin/mcp-connectors/{id}", h.handleDeleteMCPConnector)
}

func (m *mcpAPIHarness) do(method, path, body string) *httptest.ResponseRecorder {
	m.t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+m.token)
	rec := httptest.NewRecorder()
	m.router.ServeHTTP(rec, req)
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not a JSON object (%d): %s", rec.Code, rec.Body)
	}
	return m
}

const enableBody = `{"enabled":true,"mode":"key","fields":{"region":"us-east-1"},"secrets":{"apiKey":"` + mcpTestAPIKey + `"}}`

func TestMCPConnectorsDisabledAnswer404(t *testing.T) {
	h := &handlers{}
	r := chi.NewRouter()
	mountMCPConnectorsForTest(r, h)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/admin/mcp-connectors"},
		{http.MethodGet, "/admin/mcp-connectors/aws"},
		{http.MethodPut, "/admin/mcp-connectors/aws"},
		{http.MethodPost, "/admin/mcp-connectors/aws/test"},
		{http.MethodDelete, "/admin/mcp-connectors/aws"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), errConnectorsDisabled) {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if h.kobiMCPRouter() != nil {
		t.Error("with no gateway the router must be an untyped nil interface")
	}
	if h.kobiConnectorTools(context.Background()) != nil {
		t.Error("with no gateway there are no tools")
	}
}

// A connector that is built but not offered (Google Cloud; Azure in the
// Enterprise build) is listed apart, as coming soon: its card says it is
// next, and nothing about it can be set up or read through the connector
// routes.
func TestMCPConnectorsListsComingSoon(t *testing.T) {
	m := newMCPAPIHarness(t, nil)

	rec := m.do(http.MethodGet, "/admin/mcp-connectors", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Connectors []mcpgateway.ConnectorView `json:"connectors"`
		ComingSoon []comingSoonConnector      `json:"comingSoon"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list body: %s (%v)", rec.Body, err)
	}
	preview := mcpcatalog.Preview()
	if len(list.ComingSoon) != len(preview) {
		t.Fatalf("comingSoon = %+v, want the %d connectors registered for preview", list.ComingSoon, len(preview))
	}
	for i, soon := range list.ComingSoon {
		if soon.ID != preview[i].ID || soon.Name == "" || soon.Category != mcpcatalog.CategoryCloud || soon.Description == "" || len(soon.Families) == 0 {
			t.Errorf("coming-soon card = %+v", soon)
		}
		for _, c := range list.Connectors {
			if c.ID == soon.ID {
				t.Errorf("%s is listed as a connector too: %+v", soon.Name, c)
			}
		}
		if rec := m.do(http.MethodGet, "/admin/mcp-connectors/"+soon.ID, ""); rec.Code != http.StatusNotFound {
			t.Errorf("coming-soon %s opens: %d %s", soon.ID, rec.Code, rec.Body)
		}
	}
}

func TestMCPConnectorsLifecycle(t *testing.T) {
	m := newMCPAPIHarness(t, nil)

	rec := m.do(http.MethodGet, "/admin/mcp-connectors", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Connectors []mcpgateway.ConnectorView `json:"connectors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Connectors) != 1 || list.Connectors[0].ID != "aws" || list.Connectors[0].Status != mcpgateway.StatusAvailable {
		t.Fatalf("list body: %s (%v)", rec.Body, err)
	}

	if rec := m.do(http.MethodGet, "/admin/mcp-connectors/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rec.Code)
	}

	// Save and enable.
	rec = m.do(http.MethodPut, "/admin/mcp-connectors/aws", enableBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), mcpTestAPIKey) {
		t.Fatal("the PUT response echoes the secret")
	}
	var view mcpgateway.ConnectorView
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if view.Status != mcpgateway.StatusEnabled || !view.Config.SecretsConfigured["apiKey"] || view.Config.Fields["region"] != "us-east-1" {
		t.Errorf("view after enable: %+v", view)
	}
	ctx := auth.WithTenantID(context.Background(), auth.DefaultTenantName)
	all, err := m.rt.MCP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := all.Connectors["aws"].UpdatedBy; got != "alice" {
		t.Errorf("actor = %q, want the JWT username", got)
	}

	// A partial update keeps the secret (null and absent both mean keep).
	rec = m.do(http.MethodPut, "/admin/mcp-connectors/aws", `{"paused":true,"secrets":{"apiKey":null}}`)
	var paused mcpgateway.ConnectorView
	_ = json.Unmarshal(rec.Body.Bytes(), &paused)
	if rec.Code != http.StatusOK || paused.Status != mcpgateway.StatusPaused || !paused.Config.SecretsConfigured["apiKey"] {
		t.Errorf("pause: %d %+v", rec.Code, paused)
	}

	rec = m.do(http.MethodGet, "/admin/mcp-connectors/aws", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), mcpTestAPIKey) {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}

	if rec := m.do(http.MethodDelete, "/admin/mcp-connectors/aws", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	rec = m.do(http.MethodGet, "/admin/mcp-connectors/aws", "")
	var after mcpgateway.ConnectorView
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if after.Status != mcpgateway.StatusAvailable || after.Config.SecretsConfigured["apiKey"] || after.Config.Fields["region"] != "" {
		t.Errorf("after delete: %+v", after)
	}
}

func TestMCPConnectorsPutErrors(t *testing.T) {
	m := newMCPAPIHarness(t, nil)

	// Enabling without the required secret: 400 with the field.
	rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", `{"enabled":true,"mode":"key","fields":{"region":"us-east-1"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validation: %d %s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["error"] != "validation failed" || body["field"] == "" || body["message"] == "" {
		t.Errorf("validation body: %v", body)
	}

	// A malformed body is refused without quoting it back.
	rec = m.do(http.MethodPut, "/admin/mcp-connectors/aws", `{"secrets":{"apiKey":"`+mcpTestAPIKey+`"`)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), mcpTestAPIKey) {
		t.Errorf("bad JSON: %d %s", rec.Code, rec.Body)
	}

	// Oversized.
	big := `{"fields":{"region":"` + strings.Repeat("x", mcpConnectorsMaxBody) + `"}}`
	if rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rec.Code)
	}

	if rec := m.do(http.MethodPut, "/admin/mcp-connectors/nope", `{"enabled":false}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rec.Code)
	}

	// Nothing to change: the current view, and no record written. Under the
	// keep/clear/set rules a null value keeps and an empty mode picks nothing,
	// so those bodies change nothing either.
	for _, empty := range []string{
		`{}`, `null`, `{"fields":{},"secrets":{}}`,
		`{"mode":""}`, `{"fields":{"region":null}}`, `{"secrets":{"apiKey":null}}`,
		`{"kobiMinRole":null}`,
	} {
		rec = m.do(http.MethodPut, "/admin/mcp-connectors/aws", empty)
		if rec.Code != http.StatusOK || decodeMap(t, rec)["status"] != mcpgateway.StatusAvailable {
			t.Errorf("empty PUT %s: %d %s", empty, rec.Code, rec.Body)
		}
	}
	ctx := auth.WithTenantID(context.Background(), auth.DefaultTenantName)
	if all, _ := m.rt.MCP(ctx); len(all.Connectors) != 0 {
		t.Errorf("an empty PUT wrote a record: %v", all.Connectors)
	}
	// With no record, a test without a draft has nothing to test and leaves
	// no error behind on a connector nobody configured.
	if rec := m.do(http.MethodPost, "/admin/mcp-connectors/aws/test", ""); rec.Code != http.StatusOK || decodeMap(t, rec)["ok"] != false {
		t.Errorf("test of an unconfigured connector: %d %s", rec.Code, rec.Body)
	}
	if v := decodeMap(t, m.do(http.MethodGet, "/admin/mcp-connectors/aws", "")); v["status"] != mcpgateway.StatusAvailable || v["lastError"] != nil {
		t.Errorf("an unconfigured connector carries state after a test: %v", v)
	}
	if rec := m.do(http.MethodPut, "/admin/mcp-connectors/nope", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("empty PUT on an unknown id: %d", rec.Code)
	}
}

func TestMCPConnectorsTest(t *testing.T) {
	m := newMCPAPIHarness(t, nil)

	// Nothing stored and no draft: a failed test, not an HTTP error.
	rec := m.do(http.MethodPost, "/admin/mcp-connectors/aws/test", "")
	if rec.Code != http.StatusOK || decodeMap(t, rec)["ok"] != false {
		t.Fatalf("unconfigured test: %d %s", rec.Code, rec.Body)
	}

	// A draft is tested without being saved.
	rec = m.do(http.MethodPost, "/admin/mcp-connectors/aws/test", `{"mode":"key","fields":{"region":"us-east-1"},"secrets":{"apiKey":"`+mcpTestAPIKey+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("draft test: %d %s", rec.Code, rec.Body)
	}
	res := decodeMap(t, rec)
	if res["ok"] != true || res["tools"] != float64(2) || res["identity"] != "fake-identity" || strings.Contains(rec.Body.String(), mcpTestAPIKey) {
		t.Errorf("draft test body: %s", rec.Body)
	}
	ctx := auth.WithTenantID(context.Background(), auth.DefaultTenantName)
	if all, _ := m.rt.MCP(ctx); len(all.Connectors) != 0 {
		t.Errorf("a draft test must not save anything: %v", all.Connectors)
	}

	// Paced per org: the next test that reaches the provider within 5 s is
	// 429 with the wait. (A test with nothing to test never spends it.)
	rec = m.do(http.MethodPost, "/admin/mcp-connectors/aws/test", `{"mode":"key","fields":{"region":"eu-west-1"},"secrets":{"apiKey":"`+mcpTestAPIKey+`"}}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("pace: %d %s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["error"] != "rate_limited" || body["retryAfterSeconds"] != float64(5) || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("429 body: %v, Retry-After %q", body, rec.Header().Get("Retry-After"))
	}
}

// A stored-config test records its outcome on the connector.
func TestMCPConnectorsTestStoredRecordsVerification(t *testing.T) {
	m := newMCPAPIHarness(t, nil)
	if rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", enableBody); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	rec := m.do(http.MethodPost, "/admin/mcp-connectors/aws/test", `{}`)
	if rec.Code != http.StatusOK || decodeMap(t, rec)["ok"] != true {
		t.Fatalf("stored test: %d %s", rec.Code, rec.Body)
	}
	var view mcpgateway.ConnectorView
	_ = json.Unmarshal(m.do(http.MethodGet, "/admin/mcp-connectors/aws", "").Body.Bytes(), &view)
	if view.VerifiedAt == nil || view.VerifiedTools != 2 {
		t.Errorf("verification not recorded: %+v", view)
	}
}

// Without an org in the context the gateway refuses; the answer says whether
// the caller is not signed in (401) or signed in without an org (403).
func TestMCPConnectorsWithoutOrg(t *testing.T) {
	m := newMCPAPIHarness(t, nil)

	rec := httptest.NewRecorder()
	m.h.handleListMCPConnectors(rec, httptest.NewRequest(http.MethodGet, "/admin/mcp-connectors", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no claims, no org: %d %s", rec.Code, rec.Body)
	}

	// Claims but no ResolveTenant.
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "db2"))
	t.Cleanup(func() { _ = store.Close() })
	authCfg := config.AuthConfig{Enabled: true, JWTSecret: []byte(mcpTestJWT), AccessTokenExpiry: time.Minute, RefreshTokenExpiry: time.Hour}
	authH := auth.NewHandlers(store, auth.NewJWTService(authCfg), authCfg)
	r := chi.NewRouter()
	r.Use(authH.RequireAuth)
	mountMCPConnectorsForTest(r, m.h)
	req := httptest.NewRequest(http.MethodGet, "/admin/mcp-connectors", nil)
	req.Header.Set("Authorization", "Bearer "+m.token)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("claims without an org: %d %s", rec.Code, rec.Body)
	}
}

// The Kobi role floor is set from the drawer on its own. A PUT carrying only
// kobiMinRole changes the connector, so it must reach the gateway and not be
// answered as a request that changes nothing.
func TestMCPConnectorsPutKobiMinRole(t *testing.T) {
	m := newMCPAPIHarness(t, nil)
	if rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", enableBody); rec.Code != http.StatusOK || decodeMap(t, rec)["kobiMinRole"] != "editor" {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
	for _, role := range []string{"viewer", "admin"} {
		rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", `{"kobiMinRole":"`+role+`"}`)
		if rec.Code != http.StatusOK || decodeMap(t, rec)["kobiMinRole"] != role {
			t.Errorf("kobiMinRole %s: %d %s", role, rec.Code, rec.Body)
		}
		if v := decodeMap(t, m.do(http.MethodGet, "/admin/mcp-connectors/aws", "")); v["kobiMinRole"] != role {
			t.Errorf("kobiMinRole %s was not stored: %v", role, v["kobiMinRole"])
		}
	}
	// "" restores the default.
	if rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", `{"kobiMinRole":""}`); rec.Code != http.StatusOK || decodeMap(t, rec)["kobiMinRole"] != "editor" {
		t.Errorf("reset: %d %s", rec.Code, rec.Body)
	}
	rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", `{"kobiMinRole":"owner"}`)
	if body := decodeMap(t, rec); rec.Code != http.StatusBadRequest || body["field"] != "kobiMinRole" {
		t.Errorf("an unknown role: %d %v", rec.Code, body)
	}
}

// The Kobi seams: the org's tools through the gateway, and calls routed with
// the consumer set and the tool call id restored.
func TestKobiMCPSeams(t *testing.T) {
	m := newMCPAPIHarness(t, nil)
	if rec := m.do(http.MethodPut, "/admin/mcp-connectors/aws", enableBody); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	ctx := auth.WithTenantID(context.Background(), auth.DefaultTenantName)

	if got := m.h.mcpGateway.EnabledConnectors(ctx); len(got) != 1 || got[0] != "aws" {
		t.Errorf("enabled = %v", got)
	}
	var names []string
	for _, d := range m.h.kobiConnectorTools(ctx) {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "mcp__aws__call,mcp__aws__search_documentation" {
		t.Errorf("Kobi tools = %v", names)
	}

	exec := copilot.NewExecutor(nil).WithMCP(m.h.kobiMCPRouter())
	res := exec.ExecuteCtx(ctx, copilot.ToolCall{ID: "tc-9", Name: "mcp__aws__call", Input: json.RawMessage(`{}`)})
	if res.IsError || res.ToolCallID != "tc-9" || !strings.Contains(res.Content, `<external_tool_output connector="aws" tool="call">`) || !strings.Contains(res.Content, "storage-full") {
		t.Errorf("routed call: %+v", res)
	}
	// A name the org does not have is refused by the gateway, as a result.
	res = exec.ExecuteCtx(ctx, copilot.ToolCall{ID: "tc-10", Name: "mcp__aws__run_script"})
	if !res.IsError || res.ToolCallID != "tc-10" {
		t.Errorf("unlisted tool: %+v", res)
	}

	// get_external_connections sees the org's connector and the tools it
	// serves, without binding anything new.
	v := m.h.externalViewFor(ctx, flowScope{})
	if !v.gateway || !v.enabled["aws"] {
		t.Fatalf("view: gateway=%v enabled=%v", v.gateway, v.enabled)
	}
	if tools := v.toolsOf("aws"); !tools["call"] || !tools["search_documentation"] || len(tools) != 2 {
		t.Errorf("toolsOf(aws) = %v", tools)
	}
	d := aggregateExternal([]FlowEdge{edge("shop", "orders-1", "10.0.3.17", rdsHost, 1)}, v)[0]
	if !d.ConnectorEnabled || len(d.FirstTools) == 0 {
		t.Errorf("RDS with the aws connector enabled: %+v", d.ExternalClassification)
	}
}

func TestMCPActor(t *testing.T) {
	if got := mcpActor(httptest.NewRequest(http.MethodGet, "/", bytes.NewReader(nil))); got != "" {
		t.Errorf("no claims: %q", got)
	}
}
