package mcpcatalog

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// gcpKobiBudgetBytes is what the whole Google Cloud tool set may cost in a
// prompt: name, description and input schema of every tool, as JSON.
const gcpKobiBudgetBytes = 40 * 1024

const (
	gcpTestProject = "kb-test-project"
	gcpTestToken   = "ya29.test-token-not-a-secret"
	gcpTestSA      = "kubebolt-reader@kb-test-project.iam.gserviceaccount.com"
)

// gcpTestKey is shaped like a service account key. The binder under test
// never parses the private key: its token source is replaced.
var gcpTestKey = `{"type":"service_account","project_id":"kb-test-project","private_key_id":"0",` +
	`"private_key":"-----BEGIN PRIVATE KEY-----\nTESTKEYMATERIAL\n-----END PRIVATE KEY-----\n",` +
	`"client_email":"` + gcpTestSA + `","token_uri":"https://oauth2.googleapis.com/token"}`

func gcpStaticTokens(context.Context, Resolved, Env) (oauth2.TokenSource, error) {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: gcpTestToken, Expiry: time.Now().Add(time.Hour)}), nil
}

func gcpTestLogger(t *testing.T) *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func gcpTestResolved(project string) Resolved {
	return Resolved{
		Org:     "org-1",
		Mode:    gcpModeServiceAccount,
		Fields:  map[string]string{"projectId": project},
		Secrets: map[string]string{gcpFieldCredential: gcpTestKey},
	}
}

// gcpDefinitionSize is what one tool costs in a prompt.
func gcpDefinitionSize(c Consumer, s ToolSpec) int {
	b, _ := json.Marshal(map[string]any{
		"name":         ToolName(c, gcpConnectorID, s.Name),
		"description":  s.Description,
		"input_schema": s.InputSchema,
	})
	return len(b)
}

func gcpSpecNames(specs []ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// gcpSnapshot is testdata/gcp_tools_snapshot.json.
type gcpSnapshot struct {
	Note      string                 `json:"_note"`
	Endpoints map[string][]wire.Tool `json:"endpoints"`
}

func gcpLoadSnapshot(t *testing.T) gcpSnapshot {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gcp_tools_snapshot.json"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var s gcpSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return s
}

// gcpFakeRequest is one request a fake endpoint saw.
type gcpFakeRequest struct {
	service       string
	method        string
	tool          string
	args          map[string]any
	authorization string
	userProject   string
}

// gcpFake is a set of Google-shaped MCP endpoints on one server, routed by
// path (/<service>/mcp): stateless, JSON replies, 401 as a JSON-RPC result.
type gcpFake struct {
	srv *httptest.Server

	mu       sync.Mutex
	tools    map[string][]wire.Tool
	listFail map[string]int
	// callStatus, when set, is the HTTP status of every tools/call.
	callStatus int
	// callResult, when set, builds the tools/call result.
	callResult func(service, tool string) map[string]any
	requests   []gcpFakeRequest
}

func newGCPFake(t *testing.T, tools map[string][]wire.Tool) *gcpFake {
	f := &gcpFake{tools: tools, listFail: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *gcpFake) binder(t *testing.T) gcpBinder {
	return gcpBinder{
		endpointURL: func(service string) string { return f.srv.URL + "/" + service + "/mcp" },
		tokenSource: gcpStaticTokens,
		logger:      gcpTestLogger(t),
	}
}

func (f *gcpFake) env(t *testing.T) Env {
	hp, err := mcpclient.EndpointHostPort(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return Env{AllowHostPorts: []string{hp}}
}

func (f *gcpFake) bind(t *testing.T, r Resolved) *gcpBinding {
	t.Helper()
	b, err := f.binder(t).Bind(t.Context(), r, f.env(t))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	return b.(*gcpBinding)
}

func (f *gcpFake) calls() []gcpFakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gcpFakeRequest
	for _, r := range f.requests {
		if r.method == "tools/call" {
			out = append(out, r)
		}
	}
	return out
}

func (f *gcpFake) all() []gcpFakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gcpFakeRequest(nil), f.requests...)
}

func (f *gcpFake) serve(w http.ResponseWriter, r *http.Request) {
	service, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/mcp")
	if !ok || r.Method != http.MethodPost {
		http.Error(w, "<html><body>Not Found</body></html>", http.StatusNotFound)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, gcpFakeRequest{
		service: service, method: req.Method, tool: req.Params.Name, args: req.Params.Arguments,
		authorization: r.Header.Get("Authorization"), userProject: r.Header.Get("x-goog-user-project"),
	})
	tools := f.tools[service]
	listFail := f.listFail[service]
	callStatus, callResult := f.callStatus, f.callResult
	f.mu.Unlock()

	reply := func(status int, result any) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	switch req.Method {
	case "initialize":
		reply(http.StatusOK, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "StatelessServer", "version": "ESF"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		if listFail != 0 {
			http.Error(w, "<html><body><p>Server Error</p></body></html>", listFail)
			return
		}
		if tools == nil {
			tools = []wire.Tool{}
		}
		reply(http.StatusOK, map[string]any{"tools": tools})
	case "tools/call":
		if callStatus == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://`+service+`.googleapis.com/.well-known/oauth-protected-resource/`+req.Params.Name+`"`)
			reply(http.StatusUnauthorized, map[string]any{"isError": true, "content": []any{map[string]any{
				"type": "text", "text": "Request is missing required authentication credential. Expected OAuth 2 access token.",
			}}})
			return
		}
		if callStatus != 0 {
			http.Error(w, "<html><body>Error "+http.StatusText(callStatus)+"</body></html>", callStatus)
			return
		}
		if callResult != nil {
			reply(http.StatusOK, callResult(service, req.Params.Name))
			return
		}
		reply(http.StatusOK, map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"ok":"` + req.Params.Name + `"}`}}})
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "Method not supported"}})
	}
}

// gcpTool builds a tool for a fake catalog. ro nil means no annotations.
func gcpTool(name string, ro *bool, props map[string]any, required ...string) wire.Tool {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		schema["required"] = req
	}
	t := wire.Tool{Name: name, Description: "Google's description of " + name + ".", InputSchema: schema}
	if ro != nil {
		t.Annotations = &wire.ToolAnnotations{ReadOnlyHint: ro}
	}
	return t
}

func gcpBool(b bool) *bool { return &b }

func gcpStr(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// gcpSnapshotTools is the snapshot as a fake's catalog.
func gcpSnapshotTools(t *testing.T) map[string][]wire.Tool {
	return gcpLoadSnapshot(t).Endpoints
}
