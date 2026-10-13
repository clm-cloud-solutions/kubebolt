package api

// A router-level fixture for Kobi's doors (the chat, the public /mcp route):
// the real router over a manager the test builds, a scripted model, and bearer
// credentials.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

const wiringJWTSecret = "mcp-wiring-test-secret-32-bytes!!"

// scriptedProvider is the model: script decides each round's answer from the
// request, and every request is kept for the test to inspect.
type scriptedProvider struct {
	name   string
	script func(req copilot.ChatRequest) *copilot.ChatResponse

	mu   sync.Mutex
	reqs []copilot.ChatRequest
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Chat(_ context.Context, req copilot.ChatRequest) (*copilot.ChatResponse, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	resp := p.script(req)
	resp.Usage = copilot.Usage{InputTokens: 10, OutputTokens: 5}
	return resp, nil
}

func (p *scriptedProvider) requests() []copilot.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]copilot.ChatRequest(nil), p.reqs...)
}

var scriptedSeq atomic.Int64

// newScripted registers a provider under a name no other test uses: the
// provider registry is process-wide.
func newScripted(script func(req copilot.ChatRequest) *copilot.ChatResponse) *scriptedProvider {
	p := &scriptedProvider{name: fmt.Sprintf("kbtest-mcp-wiring-%d", scriptedSeq.Add(1)), script: script}
	copilot.RegisterProvider(p)
	return p
}

// callsThenAnswer is a model that makes `calls` in the first round of every
// turn and answers once it has read their results.
func callsThenAnswer(calls func() []copilot.ToolCall) func(copilot.ChatRequest) *copilot.ChatResponse {
	return func(req copilot.ChatRequest) *copilot.ChatResponse {
		if len(lastToolResults(req)) == 0 {
			return &copilot.ChatResponse{ToolCalls: calls(), StopReason: "tool_use"}
		}
		return &copilot.ChatResponse{Text: "orders-db ran out of storage.", StopReason: "end_turn"}
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

// newWiringWithAgents is newWiringOn with the agent registry the router
// consults: GET /clusters keeps an agent-proxy cluster only for the org its
// live agent authenticated under, so a test of what an org may see needs it.
func newWiringWithAgents(t *testing.T, provider string, manager *cluster.Manager, agents *channel.AgentRegistry) *wiring {
	t.Helper()
	if auth.MultiTenantEnabled {
		// The fixture authenticates as the single default org, and a
		// multi-tenant build resolves orgs, plans and Kobi's model per tenant.
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
		MaxTokens:                 1024,
		ActionsEnabled:            true,
		DestructiveActionsEnabled: true,
	}
	rt, err := settings.NewRuntime(store, store, copilotCfg, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte(wiringJWTSecret))
	if err != nil {
		t.Fatalf("settings runtime: %v", err)
	}

	w := &wiring{audit: audit.NewMemoryStore(), sessions: &recordingSessions{}}
	audit.SetSink(w.audit, nil)
	t.Cleanup(func() { audit.SetSink(nil, nil) })

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
		nil, nil, "", nil, nil, nil, nil, nil, nil, nil, rt, nil, agents, nil, nil, nil)
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

type rpcTool struct {
	Name string `json:"name"`
}

type rpcReply struct {
	Result struct {
		Tools   []rpcTool `json:"tools"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func withMultiTenant(t *testing.T, on bool) {
	t.Helper()
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = on
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })
}

func lastToolResults(req copilot.ChatRequest) []copilot.ToolResult {
	if n := len(req.Messages); n > 0 {
		return req.Messages[n-1].ToolResults
	}
	return nil
}

// newManagerOn is a real Manager over a kubeconfig whose contexts point at
// the given servers, none of them current.
func newManagerOn(t *testing.T, servers map[string]string) *cluster.Manager {
	t.Helper()
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\ncurrent-context: \"\"\nclusters:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "- name: %s\n  cluster:\n    server: %s\n", n, servers[n])
	}
	b.WriteString("contexts:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "- name: %s\n  context:\n    cluster: %s\n", n, n)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := cluster.NewManager(path, websocket.NewHub(), time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)
	m.SetConnectTimeoutProvider(func() time.Duration { return 10 * time.Second })
	m.SetCacheSyncTimeoutProvider(func() time.Duration { return 10 * time.Second })
	return m
}
