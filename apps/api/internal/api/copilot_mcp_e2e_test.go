package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
)

// Kobi's chat and Autopilot's MCP profiles over the connector gateway, end to
// end: POST /api/v1/copilot/chat and /api/v1/mcp/... through the real router
// (fixture in mcp_routes_test.go), a scripted model in place of the provider
// and a fake connector behind the real gateway. Everything between the two is
// the production path: tool assembly, prompt blocks, the executor's routing,
// the per-turn budget, the gateway's barrier, wrapper and audit, and the
// usage record.

// argCanary travels in a tool call's arguments. The audit trail records that
// a call happened and how it ended, never what it asked.
const argCanary = "arg-canary-91be"

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

func lastToolResults(req copilot.ChatRequest) []copilot.ToolResult {
	if n := len(req.Messages); n > 0 {
		return req.Messages[n-1].ToolResults
	}
	return nil
}

func toolNames(defs []copilot.ToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

// firstUserText is what the model reads as the operator's first message: the
// per-session context prefix, then the question.
func firstUserText(req copilot.ChatRequest) string {
	for _, m := range req.Messages {
		if m.Role == copilot.RoleUser && m.Content != "" {
			return m.Content
		}
	}
	return ""
}

type sseEvent struct{ name, data string }

// chat runs one Kobi turn as the org admin and returns the SSE events.
func (w *wiring) chat(t *testing.T, question string) []sseEvent {
	t.Helper()
	return w.chatAs(t, w.admin, question)
}

// chatAs runs one Kobi turn as the holder of bearer.
func (w *wiring) chatAs(t *testing.T, bearer, question string) []sseEvent {
	t.Helper()
	return w.chatMessagesAs(t, bearer, []copilot.Message{{Role: copilot.RoleUser, Content: question}})
}

// chatMessagesAs sends a whole transcript, as the chat does on every turn
// after the first.
func (w *wiring) chatMessagesAs(t *testing.T, bearer string, messages []copilot.Message) []sseEvent {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"messages": messages})
	rec := w.do(t, bearer, http.MethodPost, "/api/v1/copilot/chat", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	var out []sseEvent
	for _, frame := range strings.Split(rec.Body.String(), "\n\n") {
		var ev sseEvent
		for _, line := range strings.Split(frame, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				ev.name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				ev.data = v
			}
		}
		if ev.name != "" {
			out = append(out, ev)
		}
	}
	if len(out) == 0 || out[len(out)-1].name != "done" {
		t.Fatalf("the turn did not end with done: %s", rec.Body)
	}
	return out
}

func mcpCall(id string) copilot.ToolCall {
	return copilot.ToolCall{ID: id, Name: "mcp__fake__call", Input: json.RawMessage(`{"service":"rds","operation":"DescribeDBInstances","params":{"note":"` + argCanary + `"}}`)}
}

// ----- Kobi -----

func TestKobiChat_ConnectorToolsEndToEnd(t *testing.T) {
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall {
		return []copilot.ToolCall{
			mcpCall("c1"),
			// The built-in that points the model at the cloud resource: wired
			// on the chat's executor, so it answers from this install rather
			// than as a tool this server cannot serve.
			{ID: "c2", Name: "get_external_connections", Input: json.RawMessage(`{"namespace":"shop"}`)},
		}
	}))
	w := newWiring(t, true, prov.name)
	w.enable(t)

	events := w.chat(t, "why is checkout failing?")
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("model rounds = %d, want 2", len(reqs))
	}

	// The model sees the built-ins first, unchanged, then the org's
	// connector tools in the gateway's order.
	builtins := toolNames(copilot.GovernedToolDefinitions(true, true))
	got := toolNames(reqs[0].Tools)
	want := append(append([]string(nil), builtins...), "mcp__fake__call", "mcp__fake__search_documentation")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools:\n got %v\nwant %v", got, want)
	}

	// The connector rules ride in the per-session prefix, and with no cluster
	// connected the no-cluster note keeps the connectors usable.
	prefix := firstUserText(reqs[0])
	for _, block := range []string{
		copilot.ExternalDependenciesBlock([]string{"fake"}),
		copilot.NoClusterContextBlockWithConnectors([]string{"fake"}),
	} {
		if !strings.Contains(prefix, block) {
			t.Errorf("the session prefix is missing a block:\n%s\n--- prefix ---\n%s", block, prefix)
		}
	}
	if strings.Contains(prefix, copilot.NoClusterContextBlock()) {
		t.Error("the plain no-cluster block forbids the connector tools the turn offers")
	}

	// The call reached the connector as Kobi's, with its arguments.
	calls := w.binder.snapshot()
	if len(calls) != 1 || calls[0].consumer != mcpcatalog.ConsumerKobi || calls[0].tool != "call" || !strings.Contains(calls[0].args, argCanary) {
		t.Fatalf("connector calls: %+v", calls)
	}

	// The result came back wrapped as data, under the caller's id.
	results := map[string]copilot.ToolResult{}
	for _, r := range lastToolResults(reqs[1]) {
		results[r.ToolCallID] = r
	}
	res := results["c1"]
	if res.IsError || !strings.HasPrefix(res.Content, `<external_tool_output connector="fake" tool="call">`) || !strings.Contains(res.Content, "orders-db") {
		t.Errorf("connector result: %+v", res)
	}
	ext := results["c2"]
	if ext.IsError || strings.Contains(ext.Content, "cannot see external connections") || !strings.Contains(ext.Content, `"clusterUIDKnown":false`) {
		t.Errorf("get_external_connections is not wired on the chat executor: %+v", ext)
	}

	// One access record, which says what ran and how it ended — never what
	// it asked for or what came back.
	var recs []audit.Record
	all, _ := w.audit.ListOrg("default", 0)
	for _, r := range all {
		if r.Action == "mcp_tool_call" {
			recs = append(recs, r)
		}
	}
	if len(recs) != 1 {
		t.Fatalf("mcp_tool_call records = %d, want 1", len(recs))
	}
	if r := recs[0]; r.Class != audit.ClassAccess || r.Source != "kobi" || r.TargetType != "mcp-connector" || r.TargetName != "fake" ||
		r.Params["tool"] != "call" || r.Params["result"] != "ok" || r.Username != "alice" {
		t.Errorf("audit record: %+v", r)
	}
	raw, _ := json.Marshal(recs)
	if strings.Contains(string(raw), argCanary) || strings.Contains(string(raw), "storage-full") {
		t.Errorf("the audit record carries arguments or response: %s", raw)
	}

	// Usage: the connector call counted on its own and in the breakdown.
	sess := w.sessions.last(t)
	if sess.MCPCalls != 1 || sess.ToolCalls != 2 || sess.Tools["mcp__fake__call"].Calls != 1 || sess.Tools["mcp__fake__call"].Errors != 0 {
		t.Errorf("session record: mcpCalls=%d toolCalls=%d tools=%+v", sess.MCPCalls, sess.ToolCalls, sess.Tools)
	}

	// The UI labels the call from the tool_call event's name.
	var sawCall bool
	for _, ev := range events {
		if ev.name == "tool_call" && strings.Contains(ev.data, `"mcp__fake__call"`) {
			sawCall = true
		}
	}
	if !sawCall {
		t.Error("no tool_call event named the connector tool")
	}
}

func TestKobiChat_ConnectorCallBudgetPerTurn(t *testing.T) {
	var n atomic.Int64
	n.Store(copilot.MaxMCPCallsPerTurn + 1)
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall {
		out := make([]copilot.ToolCall, n.Load())
		for i := range out {
			out[i] = mcpCall(fmt.Sprintf("c%d", i+1))
		}
		return out
	}))
	w := newWiring(t, true, prov.name)
	w.enable(t)

	w.chat(t, "list everything in the account")
	reqs := prov.requests()
	results := lastToolResults(reqs[len(reqs)-1])
	if len(results) != copilot.MaxMCPCallsPerTurn+1 {
		t.Fatalf("tool results = %d, want %d", len(results), copilot.MaxMCPCallsPerTurn+1)
	}
	for i, r := range results[:copilot.MaxMCPCallsPerTurn] {
		if r.IsError {
			t.Errorf("call %d within the budget failed: %s", i+1, r.Content)
		}
	}
	over := results[copilot.MaxMCPCallsPerTurn]
	if !over.IsError || over.ToolCallID != fmt.Sprintf("c%d", copilot.MaxMCPCallsPerTurn+1) ||
		!strings.Contains(over.Content, "budget exhausted") || !strings.Contains(over.Content, "NOT called") {
		t.Errorf("the call past the budget: %+v", over)
	}
	if got := len(w.binder.snapshot()); got != copilot.MaxMCPCallsPerTurn {
		t.Errorf("connector calls = %d, want %d: the call past the budget must not be made", got, copilot.MaxMCPCallsPerTurn)
	}
	sess := w.sessions.last(t)
	if sess.MCPCalls != copilot.MaxMCPCallsPerTurn || sess.ToolCalls != copilot.MaxMCPCallsPerTurn+1 ||
		sess.Tools["mcp__fake__call"].Calls != copilot.MaxMCPCallsPerTurn+1 || sess.Tools["mcp__fake__call"].Errors != 1 {
		t.Errorf("session record: mcpCalls=%d toolCalls=%d tools=%+v", sess.MCPCalls, sess.ToolCalls, sess.Tools)
	}

	// The budget is per turn: the next question starts from zero.
	n.Store(1)
	w.chat(t, "and the other database?")
	if got := len(w.binder.snapshot()); got != copilot.MaxMCPCallsPerTurn+1 {
		t.Errorf("connector calls after the next turn = %d, want %d", got, copilot.MaxMCPCallsPerTurn+1)
	}
	if sess := w.sessions.last(t); sess.MCPCalls != 1 {
		t.Errorf("next turn's mcpCalls = %d, want 1", sess.MCPCalls)
	}
}

// The budget belongs to the TURN, not to a round: a model that spreads its
// connector calls over several rounds meets the same cap as one that makes
// them all at once. A counter reset per round would let a sequential model
// (one that calls tools a few at a time) spend twelve calls on every round.
func TestKobiChat_ConnectorCallBudgetSpansRounds(t *testing.T) {
	const perRound = 7 // two rounds of 7 cross the cap of 12 in the second
	prov := newScripted(func(req copilot.ChatRequest) *copilot.ChatResponse {
		rounds := 0
		for _, m := range req.Messages {
			if len(m.ToolCalls) > 0 {
				rounds++
			}
		}
		if rounds >= 2 {
			return &copilot.ChatResponse{Text: "orders-db ran out of storage.", StopReason: "end_turn"}
		}
		out := make([]copilot.ToolCall, perRound)
		for i := range out {
			out[i] = mcpCall(fmt.Sprintf("r%d-c%d", rounds+1, i+1))
		}
		return &copilot.ChatResponse{ToolCalls: out, StopReason: "tool_use"}
	})
	w := newWiring(t, true, prov.name)
	w.enable(t)

	w.chat(t, "check every database")
	reqs := prov.requests()
	if len(reqs) != 3 {
		t.Fatalf("model rounds = %d, want 3", len(reqs))
	}
	second := lastToolResults(reqs[2])
	if len(second) != perRound {
		t.Fatalf("second round results = %d, want %d", len(second), perRound)
	}
	allowed := copilot.MaxMCPCallsPerTurn - perRound
	for i, r := range second {
		refused := strings.Contains(r.Content, "budget exhausted")
		if want := i >= allowed; refused != want || (want && !r.IsError) {
			t.Errorf("second round call %d: refused=%v, want %v: %+v", i+1, refused, want, r)
		}
	}
	if got := len(w.binder.snapshot()); got != copilot.MaxMCPCallsPerTurn {
		t.Errorf("connector calls = %d, want %d across both rounds", got, copilot.MaxMCPCallsPerTurn)
	}
	if sess := w.sessions.last(t); sess.MCPCalls != copilot.MaxMCPCallsPerTurn {
		t.Errorf("mcpCalls = %d, want %d", sess.MCPCalls, copilot.MaxMCPCallsPerTurn)
	}
	// A refused call never reaches the gateway, so it leaves no access
	// record: the trail says what was read from the account, nothing else.
	all, _ := w.audit.ListOrg("default", 0)
	var recs int
	for _, r := range all {
		if r.Action == "mcp_tool_call" {
			recs++
		}
	}
	if recs != copilot.MaxMCPCallsPerTurn {
		t.Errorf("mcp_tool_call records = %d, want %d", recs, copilot.MaxMCPCallsPerTurn)
	}
}

// Connectors are ORG state. One org turning a connector on must not hand its
// tools, its prompt block or its cloud account to another org's Kobi served by
// the same process — the failure a context without the org (or a cache keyed
// without it) anywhere between the chat and the gateway would produce.
func TestKobiChat_ConnectorsAreThePerOrgSet(t *testing.T) {
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall { return []copilot.ToolCall{mcpCall("c1")} }))
	w := newWiring(t, true, prov.name)
	w.enable(t) // the default org
	other := w.tokenFor(t, auth.User{ID: "u-other", Username: "carol", Role: auth.RoleAdmin, OrgID: "org-other"})

	// The other org's admin sees the connector as never configured.
	rec := w.do(t, other, http.MethodGet, connectorsBase+"/fake", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("other org GET: %d %s", rec.Code, rec.Body)
	}
	var view struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || view.Status != "available" {
		t.Errorf("the other org sees the connector as %q (%v), want available", view.Status, err)
	}

	// Its Kobi is offered no connector tools and reads the plain prompt, and
	// a call to one — from an old transcript, or a guess — is refused before
	// any connector is touched.
	w.chatAs(t, other, "is orders-db healthy?")
	reqs := prov.requests()
	for _, name := range toolNames(reqs[0].Tools) {
		if copilot.IsMCPToolName(name) {
			t.Errorf("another org's connector tool is offered: %s", name)
		}
	}
	if prefix := firstUserText(reqs[0]); strings.Contains(prefix, "# Cloud connectors") {
		t.Errorf("another org's connector rules are in the prompt:\n%s", prefix)
	}
	res := lastToolResults(reqs[1])
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "Tool not allowed") {
		t.Fatalf("the other org calling the connector: %+v", res)
	}
	if calls := w.binder.snapshot(); len(calls) != 0 {
		t.Fatalf("the other org reached the default org's connector: %+v", calls)
	}
	// The refusal is on the other org's record, not the default org's.
	all, _ := w.audit.ListOrg("org-other", 0)
	var denied int
	for _, r := range all {
		if r.Action == "mcp_tool_call" {
			if r.TenantID != "org-other" || r.Params["result"] != "denied" {
				t.Errorf("the other org's call is recorded as %+v", r)
			}
			denied++
		}
	}
	if denied != 1 {
		t.Errorf("the other org's mcp_tool_call records = %d, want 1", denied)
	}

	// And the org that turned it on still has it: nothing the other org did
	// took it away.
	w.chat(t, "and now?")
	reqs = prov.requests()
	if names := toolNames(reqs[2].Tools); !hasName(names, "mcp__fake__call") {
		t.Errorf("the default org lost its connector tools: %v", names)
	}
	if calls := w.binder.snapshot(); len(calls) != 1 || calls[0].consumer != mcpcatalog.ConsumerKobi {
		t.Errorf("the default org's call: %+v", calls)
	}
}

// A connector switched off mid-conversation: its tools leave the list and
// the prompt, and a model that calls one from memory gets a tool error — the
// gateway refuses it before the connector is touched, and says so on the
// record.
func TestKobiChat_PausedConnectorIsAbsentAndRefused(t *testing.T) {
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall { return []copilot.ToolCall{mcpCall("c1")} }))
	w := newWiring(t, true, prov.name)
	w.enable(t)
	if rec := w.do(t, w.admin, http.MethodPut, connectorsBase+"/fake", `{"paused":true}`); rec.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body)
	}

	w.chat(t, "is orders-db healthy?")
	reqs := prov.requests()
	for _, name := range toolNames(reqs[0].Tools) {
		if copilot.IsMCPToolName(name) {
			t.Errorf("a paused connector's tool is offered: %s", name)
		}
	}
	prefix := firstUserText(reqs[0])
	if strings.Contains(prefix, "# Cloud connectors") || !strings.Contains(prefix, copilot.NoClusterContextBlock()) {
		t.Errorf("with no connector serving, the prompt must be the plain one:\n%s", prefix)
	}

	res := lastToolResults(reqs[1])
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "Tool not allowed") {
		t.Fatalf("a call to a paused connector's tool: %+v", res)
	}
	if calls := w.binder.snapshot(); len(calls) != 0 {
		t.Errorf("the paused connector was called: %+v", calls)
	}
	all, _ := w.audit.ListOrg("default", 0)
	var denied int
	for _, r := range all {
		if r.Action == "mcp_tool_call" && r.Params["result"] == "denied" {
			denied++
		}
	}
	if denied != 1 {
		t.Errorf("denied mcp_tool_call records = %d, want 1", denied)
	}
}

// With the feature off the chat is the one that shipped before connectors:
// same tools, same prompt, and a connector name from an old transcript is
// refused without anything being called or counted.
// mcpToolsOffered is the connector tools a model round was offered.
func mcpToolsOffered(req copilot.ChatRequest) []string {
	var out []string
	for _, n := range toolNames(req.Tools) {
		if copilot.IsMCPToolName(n) {
			out = append(out, n)
		}
	}
	return out
}

// Each connector has a minimum role for Kobi, editor unless an admin changes
// it, and the gateway reads the caller's role from the chat request's own
// claims. A viewer's turn is offered no connector tool and a call it makes
// anyway is refused, audited under the viewer; an editor's turn is offered
// them; an admin lowering the floor through the admin API opens them to the
// viewer.
func TestKobiChat_ConnectorRoleFloor(t *testing.T) {
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall { return []copilot.ToolCall{mcpCall("c1")} }))
	w := newWiring(t, true, prov.name)
	w.enable(t)
	viewer := w.tokenFor(t, auth.User{ID: "u-viewer", Username: "carol", Role: auth.RoleViewer})
	want := "mcp__fake__call,mcp__fake__search_documentation"

	if v := decodeMap(t, w.do(t, w.admin, http.MethodGet, connectorsBase+"/fake", "")); v["kobiMinRole"] != "editor" {
		t.Errorf("default kobiMinRole = %v, want editor", v["kobiMinRole"])
	}

	w.chatAs(t, viewer, "is orders-db healthy?")
	reqs := prov.requests()
	if got := mcpToolsOffered(reqs[0]); len(got) != 0 {
		t.Errorf("a viewer under the editor floor is offered %v", got)
	}
	if prefix := firstUserText(reqs[0]); strings.Contains(prefix, "# Cloud connectors") {
		t.Errorf("a viewer's prompt names connectors it cannot use:\n%s", prefix)
	}
	res := lastToolResults(reqs[1])
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "Tool not allowed") {
		t.Fatalf("a viewer's call to a connector tool: %+v", res)
	}
	if calls := w.binder.snapshot(); len(calls) != 0 {
		t.Fatalf("a viewer's call reached the connector: %+v", calls)
	}
	var denied []audit.Record
	for _, r := range w.auditRecords(t, "mcp_tool_call") {
		if r.Params["result"] == "denied" {
			denied = append(denied, r)
		}
	}
	if len(denied) != 1 || denied[0].Username != "carol" || denied[0].Role != string(auth.RoleViewer) {
		t.Errorf("denied records = %+v, want one under carol (viewer)", denied)
	}

	w.chatAs(t, w.editor, "is orders-db healthy?")
	reqs = prov.requests()[2:]
	if got := strings.Join(mcpToolsOffered(reqs[0]), ","); got != want {
		t.Errorf("an editor is offered %q, want %q", got, want)
	}
	if calls := w.binder.snapshot(); len(calls) != 1 || calls[0].consumer != mcpcatalog.ConsumerKobi {
		t.Fatalf("an editor's call: %+v", calls)
	}

	// The floor goes through the admin JSON the drawer sends, alone.
	rec := w.do(t, w.admin, http.MethodPut, connectorsBase+"/fake", `{"kobiMinRole":"viewer"}`)
	if rec.Code != http.StatusOK || decodeMap(t, rec)["kobiMinRole"] != "viewer" {
		t.Fatalf("lower the floor: %d %s", rec.Code, rec.Body)
	}
	w.chatAs(t, viewer, "is orders-db healthy?")
	reqs = prov.requests()[4:]
	if got := strings.Join(mcpToolsOffered(reqs[0]), ","); got != want {
		t.Errorf("a viewer over a viewer floor is offered %q, want %q", got, want)
	}
	if calls := w.binder.snapshot(); len(calls) != 2 {
		t.Errorf("the viewer's call once the floor allows it: %+v", calls)
	}

	rec = w.do(t, w.admin, http.MethodPut, connectorsBase+"/fake", `{"kobiMinRole":"owner"}`)
	if body := decodeMap(t, rec); rec.Code != http.StatusBadRequest || body["field"] != "kobiMinRole" {
		t.Errorf("an unknown role: %d %v", rec.Code, body)
	}
}

func TestKobiChat_NoGatewayIsTheChatWithoutConnectors(t *testing.T) {
	prov := newScripted(callsThenAnswer(func() []copilot.ToolCall { return []copilot.ToolCall{mcpCall("c1")} }))
	w := newWiring(t, false, prov.name)

	w.chat(t, "is orders-db healthy?")
	reqs := prov.requests()
	if got, want := toolNames(reqs[0].Tools), toolNames(copilot.GovernedToolDefinitions(true, true)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tools with the feature off:\n got %v\nwant %v", got, want)
	}
	prefix := firstUserText(reqs[0])
	if strings.Contains(prefix, "# Cloud connectors") || !strings.Contains(prefix, copilot.NoClusterContextBlock()) {
		t.Errorf("prompt with the feature off:\n%s", prefix)
	}
	res := lastToolResults(reqs[1])
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "not enabled here") {
		t.Errorf("a connector call with the feature off: %+v", res)
	}
	if sess := w.sessions.last(t); sess.MCPCalls != 0 {
		t.Errorf("mcpCalls = %d with no gateway to call", sess.MCPCalls)
	}
}

// ----- Autopilot and the public MCP door -----

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

func (w *wiring) rpc(t *testing.T, bearer, path, method string, params any) rpcReply {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		msg["params"] = params
	}
	body, _ := json.Marshal(msg)
	rec := w.do(t, bearer, http.MethodPost, path, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", path, method, rec.Code, rec.Body)
	}
	var out rpcReply
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: not JSON-RPC: %s", path, method, rec.Body)
	}
	return out
}

func (w *wiring) listTools(t *testing.T, bearer, path string) []string {
	t.Helper()
	reply := w.rpc(t, bearer, path, "tools/list", nil)
	if reply.Error != nil {
		t.Fatalf("%s tools/list: %s", path, reply.Error.Message)
	}
	out := make([]string, 0, len(reply.Result.Tools))
	for _, tl := range reply.Result.Tools {
		out = append(out, tl.Name)
	}
	return out
}

func cloudNames(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, "cloud_") {
			out = append(out, n)
		}
	}
	return out
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// The public /mcp door serves external MCP hosts, which the connectors were
// never reviewed for: with a connector on, it still lists and runs cluster
// reads only.
func TestPublicMCP_NeverServesConnectorTools(t *testing.T) {
	w := newWiring(t, true, "")
	w.enable(t)

	names := w.listTools(t, w.admin, "/api/v1/mcp")
	for _, n := range names {
		if strings.HasPrefix(n, "mcp__") || strings.HasPrefix(n, "cloud_") {
			t.Errorf("the public MCP door lists the connector tool %s", n)
		}
	}
	if !hasName(names, "get_external_connections") {
		t.Error("the public MCP door lost get_external_connections")
	}
	for _, name := range []string{"mcp__fake__call", "cloud_fake_call"} {
		reply := w.rpc(t, w.admin, "/api/v1/mcp", "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
		if reply.Error == nil && !reply.Result.IsError {
			t.Errorf("the public MCP door ran %s: %+v", name, reply)
		}
	}
	reply := w.rpc(t, w.admin, "/api/v1/mcp", "tools/call", map[string]any{"name": "get_external_connections", "arguments": map[string]any{}})
	if reply.Error != nil || reply.Result.IsError || strings.Contains(reply.Result.Content[0].Text, "cannot see external connections") {
		t.Errorf("get_external_connections on the public door: %+v", reply)
	}
	if calls := w.binder.snapshot(); len(calls) != 0 {
		t.Errorf("the public MCP door reached the connector: %+v", calls)
	}
}

// An answer that stops at the response cap says so: Kobi's answers ended
// mid-sentence with nothing to tell the operator why (three-tier-shop lab,
// 2026-10-10). The configured cap reaches the provider as set.
func TestKobiChat_AnswerCutByTheTokenLimitSaysSo(t *testing.T) {
	prov := newScripted(func(copilot.ChatRequest) *copilot.ChatResponse {
		return &copilot.ChatResponse{Text: "La base y los backends se", StopReason: "max_tokens"}
	})
	w := newWiring(t, false, prov.name)

	events := w.chat(t, "que paso con shop-web?")
	if reqs := prov.requests(); len(reqs) != 1 || reqs[0].MaxTokens != 1024 {
		t.Fatalf("the configured cap must reach the provider: %+v", reqs)
	}
	var sawNote bool
	for _, ev := range events {
		if ev.name == "text" && strings.Contains(ev.data, "stopped at the response token limit") {
			sawNote = true
		}
	}
	if !sawNote {
		t.Error("the cut was not announced while streaming")
	}
	if done := events[len(events)-1].data; !strings.Contains(done, "La base y los backends se") || !strings.Contains(done, "stopped at the response token limit") {
		t.Errorf("the saved answer must keep the cut text and the note: %s", done)
	}
}
