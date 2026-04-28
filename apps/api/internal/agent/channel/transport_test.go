package channel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// captureSender is a chan-backed Sender stub. It records every
// BackendMessage the transport hands to the agent so tests can
// assert on the wire shape, then optionally signals via sent so the
// driver can simulate the agent's reply.
type captureSender struct {
	mu   sync.Mutex
	msgs []*agentv2.BackendMessage
	sent chan *agentv2.BackendMessage
	err  error
}

func newCaptureSender() *captureSender {
	return &captureSender{sent: make(chan *agentv2.BackendMessage, 8)}
}

func (s *captureSender) Send(msg *agentv2.BackendMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.msgs = append(s.msgs, msg)
	select {
	case s.sent <- msg:
	default:
	}
	return nil
}

func (s *captureSender) lastRequest(t *testing.T) *agentv2.BackendMessage {
	t.Helper()
	select {
	case m := <-s.sent:
		return m
	case <-time.After(time.Second):
		t.Fatal("no BackendMessage sent within 1s")
		return nil
	}
}

// newTestRig wires up an Agent attached to a captureSender, registered
// in a fresh AgentRegistry, and returns a transport pointed at it.
func newTestRig(t *testing.T) (*AgentProxyTransport, *Agent, *captureSender) {
	t.Helper()
	sender := newCaptureSender()
	agent := NewAgent("c1", "agent-1", "node-a", nil, sender)
	reg := NewAgentRegistry()
	reg.Register(agent)
	tr := NewAgentProxyTransport("c1", reg)
	tr.DefaultTimeout = 0 // tests bound their own timing
	return tr, agent, sender
}

func TestAgentProxyTransport_NoAgent(t *testing.T) {
	tr := NewAgentProxyTransport("c-missing", NewAgentRegistry())
	req := httptest.NewRequest("GET", "https://kube/api/v1/pods", nil)
	_, err := tr.RoundTrip(req)
	if !errors.Is(err, ErrAgentNotConnected) {
		t.Fatalf("err = %v, want ErrAgentNotConnected", err)
	}
}

func TestAgentProxyTransport_Unary(t *testing.T) {
	tr, agent, sender := newTestRig(t)

	type result struct {
		resp *http.Response
		err  error
	}
	out := make(chan result, 1)
	go func() {
		req := httptest.NewRequest("GET", "https://kube/api/v1/namespaces/default/pods?limit=10", nil)
		resp, err := tr.RoundTrip(req)
		out <- result{resp, err}
	}()

	sent := sender.lastRequest(t)
	kubeReq := sent.GetKubeRequest()
	if kubeReq == nil {
		t.Fatal("expected kube_request payload")
	}
	if kubeReq.GetMethod() != "GET" {
		t.Errorf("method = %q, want GET", kubeReq.GetMethod())
	}
	if want := "/api/v1/namespaces/default/pods?limit=10"; kubeReq.GetPath() != want {
		t.Errorf("path = %q, want %q", kubeReq.GetPath(), want)
	}
	if kubeReq.GetWatch() {
		t.Error("watch flag should be false for unary GET")
	}

	// Simulate the agent's reply.
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: sent.GetRequestId(),
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "application/json"},
				Body:       []byte(`{"items":[]}`),
			},
		},
	})

	r := <-out
	if r.err != nil {
		t.Fatalf("RoundTrip err = %v", r.err)
	}
	if r.resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", r.resp.StatusCode)
	}
	body, _ := io.ReadAll(r.resp.Body)
	if string(body) != `{"items":[]}` {
		t.Errorf("body = %q", body)
	}
	if got := r.resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if r.resp.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d", r.resp.ContentLength, len(body))
	}
}

func TestAgentProxyTransport_UnaryAgentError(t *testing.T) {
	tr, agent, sender := newTestRig(t)

	out := make(chan error, 1)
	go func() {
		req := httptest.NewRequest("GET", "https://kube/api/v1/pods", nil)
		_, err := tr.RoundTrip(req)
		out <- err
	}()
	sent := sender.lastRequest(t)
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: sent.GetRequestId(),
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{Error: "dial apiserver: connection refused"},
		},
	})
	err := <-out
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want network error surfaced", err)
	}
}

func TestAgentProxyTransport_StripsAuthAndHopByHop(t *testing.T) {
	tr, _, sender := newTestRig(t)

	go func() {
		req := httptest.NewRequest("GET", "https://kube/api/v1/pods", nil)
		req.Header.Set("Authorization", "Bearer leaked-token")
		req.Header.Set("Connection", "keep-alive")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "kubebolt/test")
		_, _ = tr.RoundTrip(req)
	}()

	sent := sender.lastRequest(t)
	kubeReq := sent.GetKubeRequest()
	if _, ok := kubeReq.GetHeaders()["Authorization"]; ok {
		t.Error("Authorization must be stripped before forwarding to agent")
	}
	if _, ok := kubeReq.GetHeaders()["Connection"]; ok {
		t.Error("Connection (hop-by-hop) must be stripped")
	}
	if got := kubeReq.GetHeaders()["Accept"]; got != "application/json" {
		t.Errorf("Accept = %q, want passed through", got)
	}
	if got := kubeReq.GetHeaders()["User-Agent"]; got != "kubebolt/test" {
		t.Errorf("User-Agent = %q, want passed through", got)
	}
}

func TestAgentProxyTransport_PassesBody(t *testing.T) {
	tr, agent, sender := newTestRig(t)

	out := make(chan error, 1)
	go func() {
		req := httptest.NewRequest("PATCH", "https://kube/api/v1/pods/foo",
			strings.NewReader(`{"metadata":{"labels":{"a":"b"}}}`))
		req.Header.Set("Content-Type", "application/strategic-merge-patch+json")
		_, err := tr.RoundTrip(req)
		out <- err
	}()
	sent := sender.lastRequest(t)
	if got := string(sent.GetKubeRequest().GetBody()); got != `{"metadata":{"labels":{"a":"b"}}}` {
		t.Errorf("body = %q", got)
	}
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: sent.GetRequestId(),
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{StatusCode: 200, Body: []byte("{}")},
		},
	})
	if err := <-out; err != nil {
		t.Fatalf("RoundTrip err = %v", err)
	}
}

func TestAgentProxyTransport_Watch(t *testing.T) {
	tr, agent, sender := newTestRig(t)

	type result struct {
		resp *http.Response
		err  error
	}
	out := make(chan result, 1)
	go func() {
		req := httptest.NewRequest("GET", "https://kube/api/v1/pods?watch=true", nil)
		resp, err := tr.RoundTrip(req)
		out <- result{resp, err}
	}()

	sent := sender.lastRequest(t)
	kubeReq := sent.GetKubeRequest()
	if !kubeReq.GetWatch() {
		t.Fatal("watch flag must be true when URL has ?watch=true")
	}
	if kubeReq.GetTimeoutSeconds() != 0 {
		t.Errorf("watch timeout = %d, want 0 (unbounded)", kubeReq.GetTimeoutSeconds())
	}

	r := <-out
	if r.err != nil {
		t.Fatalf("watch RoundTrip err = %v", r.err)
	}
	if r.resp.StatusCode != 200 {
		t.Errorf("status = %d", r.resp.StatusCode)
	}

	// Drive 2 events then a stream_closed terminator.
	requestID := sent.GetRequestId()
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind: &agentv2.AgentMessage_KubeEvent{
			KubeEvent: &agentv2.KubeProxyWatchEvent{
				EventType: "ADDED",
				Object:    []byte(`{"kind":"Pod","metadata":{"name":"p1"}}`),
			},
		},
	})
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind: &agentv2.AgentMessage_KubeEvent{
			KubeEvent: &agentv2.KubeProxyWatchEvent{
				EventType: "MODIFIED",
				Object:    []byte(`{"kind":"Pod","metadata":{"name":"p1","resourceVersion":"42"}}`),
			},
		},
	})
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind:      &agentv2.AgentMessage_StreamClosed{StreamClosed: &agentv2.StreamClosed{Reason: "client_done"}},
	})

	// Decode the NDJSON stream.
	type wevent struct {
		Type   string          `json:"type"`
		Object json.RawMessage `json:"object"`
	}
	dec := json.NewDecoder(r.resp.Body)
	var events []wevent
	for {
		var e wevent
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode: %v", err)
		}
		events = append(events, e)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	if events[0].Type != "ADDED" || events[1].Type != "MODIFIED" {
		t.Errorf("event types = %s, %s", events[0].Type, events[1].Type)
	}
	if !strings.Contains(string(events[1].Object), `"resourceVersion":"42"`) {
		t.Errorf("event[1] object = %s", events[1].Object)
	}
}

func TestAgentProxyTransport_WatchContextCancel(t *testing.T) {
	tr, agent, sender := newTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())

	out := make(chan error, 1)
	go func() {
		req := httptest.NewRequest("GET", "https://kube/api/v1/pods?watch=true", nil).WithContext(ctx)
		resp, err := tr.RoundTrip(req)
		if err != nil {
			out <- err
			return
		}
		// Drain until the writer goroutine sees ctx done and closes the pipe.
		_, err = io.Copy(io.Discard, resp.Body)
		out <- err
	}()

	sent := sender.lastRequest(t)
	requestID := sent.GetRequestId()

	if got := agent.Pending.Pending(); got != 1 {
		t.Fatalf("Pending = %d before cancel, want 1", got)
	}

	cancel()

	if err := <-out; err != nil {
		t.Fatalf("watch body read err = %v", err)
	}
	// Cancel must release the slot so the agent can stop streaming.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if agent.Pending.Pending() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("Pending = %d after ctx cancel, want 0 (id=%s)", agent.Pending.Pending(), requestID)
}

func TestAgentProxyTransport_UnaryContextCancel(t *testing.T) {
	tr, agent, sender := newTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())

	out := make(chan error, 1)
	go func() {
		req := httptest.NewRequest("GET", "https://kube/api/v1/pods", nil).WithContext(ctx)
		_, err := tr.RoundTrip(req)
		out <- err
	}()
	_ = sender.lastRequest(t)

	cancel()

	err := <-out
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := agent.Pending.Pending(); got != 0 {
		t.Errorf("Pending = %d after ctx cancel, want 0", got)
	}
}

func TestAgentProxyTransport_AgentDisconnects(t *testing.T) {
	tr, agent, sender := newTestRig(t)

	out := make(chan error, 1)
	go func() {
		req := httptest.NewRequest("GET", "https://kube/api/v1/pods", nil)
		_, err := tr.RoundTrip(req)
		out <- err
	}()
	_ = sender.lastRequest(t)

	// Simulate the agent stream tearing down mid-flight.
	agent.Close()

	err := <-out
	if !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("err = %v, want ErrAgentClosed", err)
	}
}

func TestAgentProxyTransport_DefaultTimeout(t *testing.T) {
	sender := newCaptureSender()
	agent := NewAgent("c1", "agent-1", "node-a", nil, sender)
	reg := NewAgentRegistry()
	reg.Register(agent)

	tr := &AgentProxyTransport{ClusterID: "c1", Registry: reg, DefaultTimeout: 50 * time.Millisecond}

	req := httptest.NewRequest("GET", "https://kube/api/v1/pods", nil)
	start := time.Now()
	_, err := tr.RoundTrip(req)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want timeout", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %s, expected ~50ms", elapsed)
	}
	if got := agent.Pending.Pending(); got != 0 {
		t.Errorf("Pending = %d after timeout, want 0", got)
	}
}

func TestAgentProxyTransport_PropagatesContextDeadlineToProto(t *testing.T) {
	tr, _, sender := newTestRig(t)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		req := httptest.NewRequest("GET", "https://kube/api/v1/pods", nil).WithContext(ctx)
		_, _ = tr.RoundTrip(req)
	}()
	sent := sender.lastRequest(t)
	got := sent.GetKubeRequest().GetTimeoutSeconds()
	// Allow a small fudge (round-up + scheduling slack).
	if got < 6 || got > 9 {
		t.Errorf("timeout_seconds = %d, want ~7", got)
	}
}

func TestAgentProxyTransport_SendError(t *testing.T) {
	sender := newCaptureSender()
	sender.err = errors.New("stream broken")
	agent := NewAgent("c1", "agent-1", "node-a", nil, sender)
	reg := NewAgentRegistry()
	reg.Register(agent)
	tr := NewAgentProxyTransport("c1", reg)

	req := httptest.NewRequest("GET", "https://kube/api/v1/pods", nil)
	_, err := tr.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "stream broken") {
		t.Fatalf("err = %v, want Send error surfaced", err)
	}
	if got := agent.Pending.Pending(); got != 0 {
		t.Errorf("Pending = %d after Send error, want 0 (slot must be released)", got)
	}
}
