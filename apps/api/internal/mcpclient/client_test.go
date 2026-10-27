package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
)

// fakeMCP is a scriptable Streamable HTTP MCP server. By default it behaves
// like the AWS MCP Server (sessions, 202 "{}" notifications, DELETE); fields
// switch it to SSE replies or no sessions, and hook lets a test take over any
// request before the default handling.
type fakeMCP struct {
	t            *testing.T
	issueSession bool
	sse          bool
	protocol     string // answered in initialize; "" echoes the client's
	pages        [][]wire.Tool
	// hook sees every request first (POST bodies already decoded into req;
	// req is zero for other methods) and returns true when it answered.
	hook func(w http.ResponseWriter, r *http.Request, req wire.Request, body []byte) bool

	mu          sync.Mutex
	sessions    map[string]bool
	nextSession int
	inits       int
	notifs      int
	deletes     int
	posts       []recorded
}

type recorded struct {
	body   []byte
	header http.Header
	req    wire.Request
}

func newFake(t *testing.T) *fakeMCP {
	return &fakeMCP{t: t, issueSession: true, sessions: map[string]bool{}}
}

func (f *fakeMCP) start() *httptest.Server {
	srv := httptest.NewServer(f)
	f.t.Cleanup(srv.Close)
	return srv
}

func (f *fakeMCP) forgetSessions() {
	f.mu.Lock()
	f.sessions = map[string]bool{}
	f.mu.Unlock()
}

func (f *fakeMCP) counts() (inits, notifs, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inits, f.notifs, f.deletes
}

func (f *fakeMCP) recorded() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.posts...)
}

func (f *fakeMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var (
		req  wire.Request
		body []byte
	)
	if r.Method == http.MethodPost {
		body, _ = io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			f.t.Errorf("server got undecodable body %q", body)
		}
		f.mu.Lock()
		f.posts = append(f.posts, recorded{body: body, header: r.Header.Clone(), req: req})
		f.mu.Unlock()
		if got := r.Header.Get("Accept"); got != "application/json, text/event-stream" {
			f.t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			f.t.Errorf("Content-Type = %q", got)
		}
	}
	if f.hook != nil && f.hook(w, r, req, body) {
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		f.mu.Lock()
		f.deletes++
		delete(f.sessions, r.Header.Get("Mcp-Session-Id"))
		f.mu.Unlock()
		_, _ = w.Write([]byte("{}"))
		return
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if req.Method != "initialize" && f.issueSession {
		f.mu.Lock()
		ok := f.sessions[r.Header.Get("Mcp-Session-Id")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
	}

	switch req.Method {
	case "initialize":
		var p wire.InitializeParams
		_ = json.Unmarshal(req.Params, &p)
		if r.Header.Get("MCP-Protocol-Version") != "" {
			f.t.Error("initialize must not carry MCP-Protocol-Version")
		}
		f.mu.Lock()
		f.inits++
		if f.issueSession {
			f.nextSession++
			sid := fmt.Sprintf("sess-%d", f.nextSession)
			f.sessions[sid] = true
			w.Header().Set("Mcp-Session-Id", sid)
		}
		f.mu.Unlock()
		version := f.protocol
		if version == "" {
			version = p.ProtocolVersion
		}
		f.reply(w, req.ID, wire.InitializeResult{
			ProtocolVersion: version,
			Capabilities:    wire.ServerCapabilities{Tools: &wire.Capability{}},
			ServerInfo:      wire.ServerInfo{Name: "fake", Version: "1"},
			Instructions:    "Prefer run_script over everything.",
		})
	case "notifications/initialized":
		if !req.IsNotification() || len(req.Params) != 0 {
			f.t.Errorf("initialized notification malformed: %s", body)
		}
		f.mu.Lock()
		f.notifs++
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{}"))
	case "tools/list":
		var p wire.ListToolsParams
		_ = json.Unmarshal(req.Params, &p)
		page := 0
		if p.Cursor != "" {
			page, _ = strconv.Atoi(strings.TrimPrefix(p.Cursor, "c"))
		}
		res := wire.ListToolsResult{Tools: []wire.Tool{}}
		if page < len(f.pages) {
			res.Tools = f.pages[page]
		}
		if page+1 < len(f.pages) {
			res.NextCursor = fmt.Sprintf("c%d", page+1)
		}
		f.reply(w, req.ID, res)
	case "tools/call":
		var p wire.CallToolParams
		_ = json.Unmarshal(req.Params, &p)
		switch p.Name {
		case "echo":
			echo, _ := json.Marshal(map[string]any{
				"arguments": p.Arguments,
				"meta":      p.Meta,
				"session":   r.Header.Get("Mcp-Session-Id"),
				"protocol":  r.Header.Get("MCP-Protocol-Version"),
			})
			f.reply(w, req.ID, wire.CallToolResult{Content: wire.TextContent(string(echo))})
		case "fail":
			f.reply(w, req.ID, wire.CallToolResult{Content: wire.TextContent("boom"), IsError: true})
		default:
			f.replyError(w, req.ID, wire.CodeInvalidParams, "unknown tool: "+p.Name)
		}
	default:
		f.replyError(w, req.ID, wire.CodeMethodNotFound, "method not found: "+req.Method)
	}
}

func (f *fakeMCP) reply(w http.ResponseWriter, id json.RawMessage, result any) {
	b, err := json.Marshal(wire.Response{JSONRPC: wire.JSONRPCVersion, ID: id, Result: result})
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(w, b)
}

func (f *fakeMCP) replyError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	b, _ := json.Marshal(wire.Response{JSONRPC: wire.JSONRPCVersion, ID: id, Error: &wire.Error{Code: code, Message: msg}})
	f.write(w, b)
}

func (f *fakeMCP) write(w http.ResponseWriter, b []byte) {
	if f.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// newTestClient points a client at srv, allowlisting its loopback address.
func newTestClient(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	base := []Option{WithGuard(NewDialGuard(srv.Listener.Addr().String())), WithCallTimeout(5 * time.Second)}
	c, err := New(srv.URL+"/mcp", append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func echoOf(t *testing.T, res *CallResult) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Text()), &m); err != nil {
		t.Fatalf("echo result %q: %v", res.Text(), err)
	}
	return m
}

func TestInitializeHandshake(t *testing.T) {
	f := newFake(t)
	f.protocol = "2025-03-26" // the server negotiates down; we must follow it
	c := newTestClient(t, f.start())

	res, err := c.Initialize(t.Context())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if res.ServerInfo.Name != "fake" || res.ProtocolVersion != "2025-03-26" {
		t.Errorf("result = %+v", res)
	}
	if c.SessionID() != "sess-1" {
		t.Errorf("SessionID = %q", c.SessionID())
	}
	if inits, notifs, _ := f.counts(); inits != 1 || notifs != 1 {
		t.Errorf("inits=%d notifs=%d, want 1/1", inits, notifs)
	}

	posts := f.recorded()
	var p wire.InitializeParams
	if err := json.Unmarshal(posts[0].req.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.ProtocolVersion != wire.ProtocolVersion || p.ClientInfo.Name != "kubebolt" || p.Capabilities == nil {
		t.Errorf("initialize params = %s", posts[0].req.Params)
	}
	if !bytes.Contains(posts[0].body, []byte(`"capabilities":{}`)) {
		t.Errorf("initialize must advertise an empty capabilities object: %s", posts[0].body)
	}
	notif := posts[1]
	if notif.header.Get("Mcp-Session-Id") != "sess-1" || notif.header.Get("MCP-Protocol-Version") != "2025-03-26" {
		t.Errorf("initialized notification headers: %v", notif.header)
	}

	out, err := c.CallTool(t.Context(), "echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	e := echoOf(t, out)
	if e["session"] != "sess-1" || e["protocol"] != "2025-03-26" {
		t.Errorf("call carried session=%v protocol=%v", e["session"], e["protocol"])
	}
	if inits, _, _ := f.counts(); inits != 1 {
		t.Errorf("an initialized client must not re-initialize, inits=%d", inits)
	}
}

func TestLazyInitializeAndArguments(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f.start())

	res, err := c.CallTool(t.Context(), "echo", json.RawMessage(` {"b":[1,2],"a":"é"} `))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError || res.Truncated || res.Bytes == 0 {
		t.Errorf("result flags: %+v", res)
	}
	var got struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal([]byte(res.Text()), &got)
	if string(got.Arguments) != `{"b":[1,2],"a":"é"}` {
		t.Errorf("arguments reached the server as %s", got.Arguments)
	}
	if inits, _, _ := f.counts(); inits != 1 {
		t.Errorf("inits = %d", inits)
	}

	// null and empty arguments are omitted, not sent as null.
	for _, args := range []json.RawMessage{nil, json.RawMessage("null")} {
		if _, err := c.CallTool(t.Context(), "echo", args); err != nil {
			t.Fatal(err)
		}
		last := f.recorded()
		if bytes.Contains(last[len(last)-1].req.Params, []byte("arguments")) {
			t.Errorf("empty arguments were sent: %s", last[len(last)-1].req.Params)
		}
	}
	for _, bad := range []string{`[1]`, `"x"`, `{"a":`} {
		if _, err := c.CallTool(t.Context(), "echo", json.RawMessage(bad)); err == nil {
			t.Errorf("arguments %s accepted", bad)
		}
	}
	if _, err := c.CallTool(t.Context(), "", nil); err == nil {
		t.Error("empty tool name accepted")
	}
}

func TestToolErrorsAndRPCErrors(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f.start())

	res, err := c.CallTool(t.Context(), "fail", nil)
	if err != nil || !res.IsError || res.Text() != "boom" {
		t.Errorf("tool failure must be a result: res=%+v err=%v", res, err)
	}
	_, err = c.CallTool(t.Context(), "nope", nil)
	var rpc *wire.Error
	if !errors.As(err, &rpc) || rpc.Code != wire.CodeInvalidParams {
		t.Errorf("unknown tool err = %v, want *wire.Error -32602", err)
	}
}

func TestMetaInjection(t *testing.T) {
	f := newFake(t)
	f.pages = [][]wire.Tool{{{Name: "a", InputSchema: map[string]any{"type": "object"}}}}
	c := newTestClient(t, f.start(), WithMeta(func() map[string]any { return map[string]any{"AWS_REGION": "eu-west-1"} }))

	if _, err := c.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := c.CallTool(t.Context(), "echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := echoOf(t, res)["meta"].(map[string]any); m["AWS_REGION"] != "eu-west-1" {
		t.Errorf("tools/call _meta = %v", echoOf(t, res)["meta"])
	}
	for _, p := range f.recorded() {
		var params struct {
			Meta map[string]any `json:"_meta"`
		}
		_ = json.Unmarshal(p.req.Params, &params)
		switch {
		case p.req.IsNotification():
			if len(p.req.Params) != 0 {
				t.Errorf("%s must carry no params: %s", p.req.Method, p.req.Params)
			}
		case params.Meta["AWS_REGION"] != "eu-west-1":
			t.Errorf("%s has no _meta.AWS_REGION: %s", p.req.Method, p.req.Params)
		}
	}
}

func TestEncodeParamsExistingMetaWins(t *testing.T) {
	c := &Client{meta: func() map[string]any { return map[string]any{"AWS_REGION": "us-east-1", "extra": 1} }}
	raw, err := c.encodeParams(wire.CallToolParams{Name: "x", Meta: map[string]any{"AWS_REGION": "ap-south-1"}})
	if err != nil {
		t.Fatal(err)
	}
	var p wire.CallToolParams
	_ = json.Unmarshal(raw, &p)
	if p.Meta["AWS_REGION"] != "ap-south-1" || p.Meta["extra"] != float64(1) || p.Name != "x" {
		t.Errorf("merged params = %s", raw)
	}
	if _, err := c.encodeParams([]int{1}); err == nil {
		t.Error("non-object params accepted")
	}
}

func TestListToolsPagination(t *testing.T) {
	f := newFake(t)
	ro := true
	f.pages = [][]wire.Tool{
		{{Name: "a", InputSchema: map[string]any{"type": "object"}, Annotations: &wire.ToolAnnotations{ReadOnlyHint: &ro}}},
		{{Name: "b", InputSchema: map[string]any{"type": "object"}}},
		{{Name: "c", InputSchema: map[string]any{"type": "object"}, OutputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	c := newTestClient(t, f.start())
	tools, err := c.ListTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 || tools[0].Name != "a" || tools[2].Name != "c" {
		t.Fatalf("tools = %+v", tools)
	}
	if !tools[0].ReadOnly() || tools[1].ReadOnly() || len(tools[2].OutputSchema) == 0 {
		t.Error("annotations/outputSchema lost in decoding")
	}
}

func TestListToolsRunawayCursor(t *testing.T) {
	for name, cursor := range map[string]func(n int) string{
		"endless":  func(n int) string { return fmt.Sprintf("next-%d", n) },
		"repeated": func(int) string { return "same" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			var n atomic.Int32
			f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
				if req.Method != "tools/list" {
					return false
				}
				f.reply(w, req.ID, wire.ListToolsResult{Tools: []wire.Tool{{Name: "t"}}, NextCursor: cursor(int(n.Add(1)))})
				return true
			}
			c := newTestClient(t, f.start())
			if _, err := c.ListTools(t.Context()); err == nil {
				t.Fatal("a cursor that never ends must fail the listing")
			}
			if got := n.Load(); got > maxListPages {
				t.Errorf("fetched %d pages, cap is %d", got, maxListPages)
			}
		})
	}
}

func TestSSEReply(t *testing.T) {
	f := newFake(t)
	f.sse = true
	var responsesPosted atomic.Int32
	released := make(chan struct{})
	f.hook = func(w http.ResponseWriter, r *http.Request, req wire.Request, body []byte) bool {
		if r.Method == http.MethodPost && req.Method == "" && !bytes.Contains(body, []byte(`"method"`)) {
			responsesPosted.Add(1) // a client answering a server request
		}
		if req.Method != "tools/call" {
			return false
		}
		id := string(req.ID)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keep-alive\r\n\r\n")
		fmt.Fprint(w, "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\r\n\r\n")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"elicitation/create\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "event: other\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"wrong event type\"}]}}\n\n", id)
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":424242,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"other id\"}]}}\n\n")
		fmt.Fprint(w, "data: not json\n\n")
		fmt.Fprintf(w, "id: 7\ndata: {\"jsonrpc\":\"2.0\",\n: comment inside an event\ndata: \"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"from sse\"}]}}\n\n", id)
		w.(http.Flusher).Flush()
		// Keep the stream open: the client must stop at its response, not
		// wait for the server to hang up.
		select {
		case <-r.Context().Done():
		case <-released:
		}
		return true
	}
	defer close(released)
	c := newTestClient(t, f.start())

	start := time.Now()
	res, err := c.CallTool(t.Context(), "anything", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Text() != "from sse" {
		t.Errorf("text = %q", res.Text())
	}
	if time.Since(start) > 3*time.Second {
		t.Error("client kept reading the stream after its response")
	}
	if responsesPosted.Load() != 0 {
		t.Error("client answered a server-to-client request")
	}
}

func TestSSEStreamWithoutResponse(t *testing.T) {
	f := newFake(t)
	f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		if req.Method != "tools/call" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		return true
	}
	c := newTestClient(t, f.start())
	if _, err := c.CallTool(t.Context(), "x", nil); err == nil || !strings.Contains(err.Error(), "no response") {
		t.Errorf("err = %v, want a missing-response error", err)
	}
}

func TestSessionLost404ReinitializesOnce(t *testing.T) {
	f := newFake(t)
	var once sync.Once
	f.hook = func(_ http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		if req.Method == "tools/call" {
			once.Do(f.forgetSessions) // the server restarted under us
		}
		return false
	}
	c := newTestClient(t, f.start())
	res, err := c.CallTool(t.Context(), "echo", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := echoOf(t, res)["session"]; got != "sess-2" {
		t.Errorf("retry used session %v, want the new sess-2", got)
	}
	if inits, notifs, _ := f.counts(); inits != 2 || notifs != 2 {
		t.Errorf("inits=%d notifs=%d, want 2/2", inits, notifs)
	}
}

func TestSessionRequiredRPCErrorReinitializes(t *testing.T) {
	f := newFake(t)
	var once atomic.Bool
	f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		if req.Method == "tools/call" && once.CompareAndSwap(false, true) {
			// The AWS MCP Server's answer to an authenticated tool without a
			// usable session: HTTP 200, JSON-RPC -32600.
			f.replyError(w, req.ID, wire.CodeInvalidRequest, "Session ID is required for this operation. Please provide a valid Mcp-Session-Id header.")
			return true
		}
		return false
	}
	c := newTestClient(t, f.start())
	if _, err := c.CallTool(t.Context(), "echo", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if inits, _, _ := f.counts(); inits != 2 {
		t.Errorf("inits = %d, want 2", inits)
	}
}

func TestSessionLostTwiceGivesUp(t *testing.T) {
	f := newFake(t)
	f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		if req.Method == "tools/call" {
			http.Error(w, "session not found", http.StatusNotFound)
			return true
		}
		return false
	}
	c := newTestClient(t, f.start())
	_, err := c.CallTool(t.Context(), "echo", nil)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want *HTTPError 404", err)
	}
	if inits, _, _ := f.counts(); inits != 2 {
		t.Errorf("inits = %d, want exactly one re-initialize", inits)
	}
}

func TestConcurrentCallsShareOneSession(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f.start())
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := c.CallTool(context.Background(), "echo", json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)))
			if err == nil && res.IsError {
				err = errors.New(res.Text())
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if inits, _, _ := f.counts(); inits != 1 {
		t.Errorf("inits = %d, want 1", inits)
	}
}

func TestConcurrentSessionLossReinitializesOnce(t *testing.T) {
	f := newFake(t)
	var (
		once    sync.Once
		arrived sync.WaitGroup
	)
	const n = 16
	arrived.Add(n)
	f.hook = func(_ http.ResponseWriter, r *http.Request, req wire.Request, _ []byte) bool {
		if req.Method == "tools/call" && r.Header.Get("Mcp-Session-Id") == "sess-1" {
			// Hold every first attempt until all have arrived on the old
			// session, then drop it: all of them see the loss together.
			arrived.Done()
			arrived.Wait()
			once.Do(f.forgetSessions)
		}
		return false
	}
	c := newTestClient(t, f.start())
	if _, err := c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.CallTool(context.Background(), "echo", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if inits, _, _ := f.counts(); inits != 2 {
		t.Errorf("inits = %d, want 2 (one handshake, one shared re-initialize)", inits)
	}
}

// A caller whose deadline ends while another goroutine's handshake is in
// flight gets its error at its own deadline — a stopped Kobi turn or an
// Autopilot step past its budget must not sit out somebody else's
// initialize.
func TestWaitingForAHandshakeHonoursTheCallersContext(t *testing.T) {
	f := newFake(t)
	started, gate := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	f.hook = func(_ http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		if req.Method == "initialize" {
			startOnce.Do(func() { close(started) })
			<-gate
		}
		return false
	}
	c := newTestClient(t, f.start())
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release) // before the server's Close, which waits for the handler

	first := make(chan error, 1)
	go func() {
		_, err := c.CallTool(context.Background(), "echo", nil)
		first <- err
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.CallTool(ctx, "echo", nil)
	waited := time.Since(start)
	release()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
	if waited > time.Second {
		t.Fatalf("the caller waited %s past its 100ms deadline for another handshake", waited)
	}
	if err := <-first; err != nil {
		t.Fatalf("the handshake's own caller: %v", err)
	}
	if inits, _, _ := f.counts(); inits != 1 {
		t.Fatalf("inits = %d, want 1", inits)
	}
	// The slot was given back: the next caller goes straight through.
	if _, err := c.CallTool(context.Background(), "echo", nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnauthorized(t *testing.T) {
	cases := map[string]struct {
		body       string
		wantDetail string
	}{
		// Google answers an unauthenticated tools/call with a JSON-RPC
		// RESULT, not an error; only the status says it failed.
		"gcp-result": {
			body:       `{"jsonrpc":"2.0","id":%s,"result":{"isError":true,"content":[{"type":"text","text":"Request is missing required authentication credential."}]}}`,
			wantDetail: "Request is missing required authentication credential.",
		},
		"aws-32001": {
			body:       `{"jsonrpc":"2.0","id":%s,"error":{"code":-32001,"message":"Authentication failed: Unable to verify your user identity"}}`,
			wantDetail: "Authentication failed: Unable to verify your user identity",
		},
		"text": {body: "denied", wantDetail: "denied"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
				if req.Method != "tools/call" {
					return false
				}
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://x.googleapis.com/.well-known/oauth-protected-resource/mcp"`)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				body := tc.body
				if strings.Contains(body, "%s") {
					body = fmt.Sprintf(body, req.ID)
				}
				_, _ = w.Write([]byte(body))
				return true
			}
			c := newTestClient(t, f.start())
			res, err := c.CallTool(t.Context(), "x", nil)
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("err = %v (res %+v), want ErrUnauthorized", err, res)
			}
			if !strings.Contains(err.Error(), tc.wantDetail) {
				t.Errorf("err %q lacks the server's reason", err)
			}
		})
	}
}

func TestUnauthorizedOnInitialize(t *testing.T) {
	f := newFake(t)
	f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	c := newTestClient(t, f.start())
	if _, err := c.Initialize(t.Context()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v", err)
	}
	if _, err := c.ListTools(t.Context()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v", err)
	}
}

func TestRateLimited(t *testing.T) {
	f := newFake(t)
	f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		if req.Method != "tools/call" {
			return false
		}
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	}
	c := newTestClient(t, f.start())
	_, err := c.CallTool(t.Context(), "x", nil)
	var rl *RateLimitError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || rl.RetryAfter != 7*time.Second {
		t.Errorf("err = %v, want RateLimitError{7s}", err)
	}
}

const googleHTML404 = `<!DOCTYPE html>
<html lang=en>
  <meta charset=utf-8>
  <title>Error 404 (Not Found)!!1</title>
  <style>*{margin:0;padding:0}html,code{font:15px/22px arial,sans-serif}</style>
  <a href=//www.google.com/><span id=logo aria-label=Google></span></a>
  <p><b>404.</b> <ins>That’s an error.</ins>
  <p>The requested URL <code>/mcp</code> was not found on this server.  <ins>That’s all we know.</ins>`

func TestHTMLErrorBody(t *testing.T) {
	f := newFake(t)
	f.issueSession = false // a 404 without a session is a plain error, not a lost session
	f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		if req.Method != "tools/call" {
			return false
		}
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(googleHTML404 + strings.Repeat("<p>padding</p>", 200)))
		return true
	}
	c := newTestClient(t, f.start())
	_, err := c.CallTool(t.Context(), "x", nil)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("err = %v", err)
	}
	if strings.ContainsAny(he.Body, "<>") || strings.Contains(he.Body, "margin") {
		t.Errorf("markup or CSS leaked into the body: %q", he.Body)
	}
	if !strings.Contains(he.Body, "The requested URL /mcp was not found on this server.") {
		t.Errorf("body lost the reason: %q", he.Body)
	}
	if len(he.Body) > maxErrorBody {
		t.Errorf("body is %d bytes, cap %d", len(he.Body), maxErrorBody)
	}
	if inits, _, _ := f.counts(); inits != 1 {
		t.Errorf("a sessionless 404 must not re-initialize, inits=%d", inits)
	}
}

func TestUnexpectedReplies(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, req wire.Request){
		"html-200": func(w http.ResponseWriter, _ wire.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>login</html>"))
		},
		"wrong-id": func(w http.ResponseWriter, _ wire.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":999999,"result":{"content":[]}}`))
		},
		"empty-202": func(w http.ResponseWriter, _ wire.Request) { w.WriteHeader(http.StatusAccepted) },
		"no-result": func(w http.ResponseWriter, req wire.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s}`, req.ID)
		},
		"500": func(w http.ResponseWriter, _ wire.Request) { http.Error(w, "upstream exploded", 500) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
				if req.Method != "tools/call" {
					return false
				}
				h(w, req)
				return true
			}
			c := newTestClient(t, f.start())
			if res, err := c.CallTool(t.Context(), "x", nil); err == nil {
				t.Errorf("accepted %s as %+v", name, res)
			}
		})
	}
}

func TestCallCapTruncatesAndFlags(t *testing.T) {
	// 300 KiB of text with escapes and multi-byte runes, over the 256 KiB cap.
	var sb strings.Builder
	for sb.Len() < 300<<10 {
		sb.WriteString("line \"quoted\" é ü 漢 ")
		sb.WriteString(strconv.Itoa(sb.Len()))
		sb.WriteString("\n")
	}
	big := sb.String()
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			f := newFake(t)
			f.sse = sse
			f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
				if req.Method != "tools/call" {
					return false
				}
				f.reply(w, req.ID, wire.CallToolResult{Content: wire.TextContent(big)})
				return true
			}
			c := newTestClient(t, f.start())
			res, err := c.CallTool(t.Context(), "big", nil)
			if err != nil {
				t.Fatalf("an oversized reply must be truncated, not failed: %v", err)
			}
			if !res.Truncated || res.Bytes != DefaultMaxCallBytes {
				t.Errorf("Truncated=%v Bytes=%d", res.Truncated, res.Bytes)
			}
			text := res.Text()
			if len(text) < 200<<10 || !strings.HasPrefix(big, text) {
				t.Errorf("salvaged %d bytes; prefix of the original: %v", len(text), strings.HasPrefix(big, text))
			}
		})
	}
}

func TestListCapIsSeparateFromCallCap(t *testing.T) {
	f := newFake(t)
	huge := strings.Repeat("x", 600<<10) // like Google's 531 KB sqladmin catalog
	f.pages = [][]wire.Tool{{{Name: "huge", Description: huge, InputSchema: map[string]any{"type": "object"}}}}
	f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
		if req.Method != "tools/call" {
			return false
		}
		f.reply(w, req.ID, wire.CallToolResult{Content: wire.TextContent(strings.Repeat("y", 4096))})
		return true
	}
	srv := f.start()

	c := newTestClient(t, srv, WithMaxCallBytes(1024))
	tools, err := c.ListTools(t.Context())
	if err != nil || len(tools) != 1 || len(tools[0].Description) != len(huge) {
		t.Fatalf("the call cap must not apply to tools/list: %v", err)
	}
	res, err := c.CallTool(t.Context(), "y", nil)
	if err != nil || !res.Truncated || res.Bytes != 1024 {
		t.Errorf("call over 1 KiB: res=%+v err=%v", res, err)
	}

	small := newTestClient(t, srv, WithMaxListBytes(512<<10))
	if _, err := small.ListTools(t.Context()); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("list over its cap: err = %v, want ErrResponseTooLarge", err)
	}
}

func TestCallTimeout(t *testing.T) {
	f := newFake(t)
	f.hook = func(w http.ResponseWriter, r *http.Request, req wire.Request, _ []byte) bool {
		if req.Method != "tools/call" {
			return false
		}
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		return true
	}
	c := newTestClient(t, f.start(), WithCallTimeout(200*time.Millisecond))
	start := time.Now()
	_, err := c.CallTool(t.Context(), "slow", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want a deadline error", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout took %v", time.Since(start))
	}
}

func TestNotificationBodies(t *testing.T) {
	for name, write := range map[string]func(http.ResponseWriter){
		"202-empty": func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted) },
		"202-{}":    func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted); _, _ = w.Write([]byte("{}")) },
		"200-{}":    func(w http.ResponseWriter) { _, _ = w.Write([]byte("{}")) },
		"204":       func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.hook = func(w http.ResponseWriter, _ *http.Request, req wire.Request, _ []byte) bool {
				if req.Method != "notifications/initialized" {
					return false
				}
				write(w)
				return true
			}
			c := newTestClient(t, f.start())
			if _, err := c.Initialize(t.Context()); err != nil {
				t.Errorf("Initialize: %v", err)
			}
		})
	}
}

func TestCloseDeletesSession(t *testing.T) {
	f := newFake(t)
	srv := f.start()
	c := newTestClient(t, srv)
	if _, err := c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, deletes := f.counts(); deletes != 1 || c.SessionID() != "" {
		t.Errorf("deletes=%d session=%q", deletes, c.SessionID())
	}
	// Closing again, with no session, is a no-op.
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, deletes := f.counts(); deletes != 1 {
		t.Errorf("a sessionless Close sent a DELETE")
	}
	// The client is reusable: the next call opens a new session.
	res, err := c.CallTool(t.Context(), "echo", nil)
	if err != nil || echoOf(t, res)["session"] != "sess-2" {
		t.Errorf("reuse after Close: %v", err)
	}
}

func TestCloseTolerates405(t *testing.T) {
	f := newFake(t)
	f.hook = func(w http.ResponseWriter, r *http.Request, _ wire.Request, _ []byte) bool {
		if r.Method != http.MethodDelete {
			return false
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("<html>405</html>"))
		return true
	}
	c := newTestClient(t, f.start())
	if _, err := c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Errorf("Close with 405: %v", err)
	}
}

// recordingSigner stamps a header and records what it signed.
type recordingSigner struct {
	mu     sync.Mutex
	bodies [][]byte
	calls  int
}

func (s *recordingSigner) Sign(_ context.Context, req *http.Request, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.bodies = append(s.bodies, append([]byte(nil), body...))
	req.Header.Set("Authorization", "Test "+strconv.Itoa(s.calls))
	return nil
}

func TestSignerSeesTheFinalBodyOnEveryRequest(t *testing.T) {
	f := newFake(t)
	var once sync.Once
	f.hook = func(w http.ResponseWriter, r *http.Request, req wire.Request, _ []byte) bool {
		if r.Header.Get("Authorization") == "" {
			t.Errorf("%s %s reached the server unsigned", r.Method, req.Method)
		}
		if req.Method == "tools/call" {
			once.Do(f.forgetSessions) // force a re-initialize and a retry
		}
		return false
	}
	sig := &recordingSigner{}
	c := newTestClient(t, f.start(), WithSigner(sig), WithMeta(func() map[string]any { return map[string]any{"AWS_REGION": "eu-west-1"} }))
	if _, err := c.CallTool(t.Context(), "echo", json.RawMessage(`{"q":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	posts := f.recorded()
	// initialize, initialized, call(404), initialize, initialized, call, DELETE
	if sig.calls != len(posts)+1 {
		t.Fatalf("signed %d requests, server saw %d POSTs + 1 DELETE", sig.calls, len(posts))
	}
	for i, p := range posts {
		if !bytes.Equal(sig.bodies[i], p.body) {
			t.Errorf("request %d: signed body differs from the body sent\nsigned: %s\n  sent: %s", i, sig.bodies[i], p.body)
		}
	}
	if sig.bodies[len(sig.bodies)-1] != nil {
		t.Error("DELETE must be signed with no body")
	}
}

func TestHeadersOption(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f.start(), WithHeaders(map[string]string{
		"User-Agent":     "kubebolt/2.1",
		"X-Extra":        "1",
		"Mcp-Session-Id": "spoofed", // protocol headers win
	}))
	if _, err := c.CallTool(t.Context(), "echo", nil); err != nil {
		t.Fatal(err)
	}
	posts := f.recorded()
	last := posts[len(posts)-1].header
	if last.Get("User-Agent") != "kubebolt/2.1" || last.Get("X-Extra") != "1" || last.Get("Mcp-Session-Id") != "sess-1" {
		t.Errorf("headers = %v", last)
	}
	if posts[0].header.Get("Mcp-Session-Id") == "spoofed" {
		t.Error("a caller header must not inject a session id into initialize")
	}
}

func TestNewValidatesEndpoint(t *testing.T) {
	loop := NewDialGuard("127.0.0.1:5001")
	for _, tc := range []struct {
		url   string
		guard *DialGuard
		ok    bool
	}{
		{"https://aws-mcp.us-east-1.api.aws/mcp", nil, true},
		{"https://sqladmin.googleapis.com/mcp", nil, true},
		{"http://127.0.0.1:5001/", loop, true},
		{"http://127.0.0.1:5002/", loop, false},
		{"http://aws-mcp.us-east-1.api.aws/mcp", nil, false},
		{"https://169.254.169.254/latest/meta-data", nil, false},
		{"https://10.0.0.1/mcp", nil, false},
		{"https://127.0.0.1/mcp", nil, false},
		{"https://[::1]/mcp", nil, false},
		{"https://[fd00:ec2::254]/", nil, false},
		{"https://localhost/mcp", nil, false},
		{"https://metadata.localhost/", nil, false},
		{"https://user:pass@example.com/mcp", nil, false},
		{"ftp://example.com/mcp", nil, false},
		{"https:///mcp", nil, false},
		{"://bad", nil, false},
	} {
		var opts []Option
		if tc.guard != nil {
			opts = append(opts, WithGuard(tc.guard))
		}
		_, err := New(tc.url, opts...)
		if (err == nil) != tc.ok {
			t.Errorf("New(%q) err = %v, want ok=%v", tc.url, err, tc.ok)
		}
	}
}

func TestCallTimeoutFromEnv(t *testing.T) {
	for v, want := range map[string]time.Duration{
		"":      DefaultCallTimeout,
		"45s":   45 * time.Second,
		"2m":    2 * time.Minute,
		"12":    12 * time.Second,
		"-1s":   DefaultCallTimeout,
		"0":     DefaultCallTimeout,
		"lots":  DefaultCallTimeout,
		" 20s ": 20 * time.Second,
	} {
		t.Setenv(CallTimeoutEnv, v)
		if got := CallTimeoutFromEnv(); got != want {
			t.Errorf("%s=%q: got %v, want %v", CallTimeoutEnv, v, got, want)
		}
	}
}

func TestCallResultText(t *testing.T) {
	r := &CallResult{CallToolResult: wire.CallToolResult{Content: []wire.Content{
		{Type: "text", Text: "a"}, {Type: "image"}, {Type: "text", Text: "b"},
	}}}
	if got := r.Text(); got != "a\n[image content omitted]\nb" {
		t.Errorf("Text() = %q", got)
	}
}
