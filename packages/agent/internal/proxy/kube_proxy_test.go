package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// stubRoundTripper returns a canned response (or error) for whatever
// request lands on RoundTrip. Tests inspect `lastReq` to assert what
// the proxy actually sent.
type stubRoundTripper struct {
	resp     *http.Response
	err      error
	lastReq  *http.Request
	calls    int
}

func (s *stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

// newProxyWith wires a KubeAPIProxy directly with a stub transport,
// bypassing rest.Config. Tests don't need a real cluster.
func newProxyWith(rt http.RoundTripper) *KubeAPIProxy {
	return &KubeAPIProxy{transport: rt, baseURL: "https://apiserver.test"}
}

func okResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// ─── HandleRequest ────────────────────────────────────────────────────

func TestHandleRequest_GetSuccess(t *testing.T) {
	rt := &stubRoundTripper{resp: okResponse(200, `{"kind":"Pod"}`)}
	p := newProxyWith(rt)

	resp := p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{
		Method: "GET",
		Path:   "/api/v1/namespaces/default/pods/p1",
		Headers: map[string]string{
			"Accept": "application/json",
		},
	})

	if resp.GetError() != "" {
		t.Errorf("unexpected error: %s", resp.GetError())
	}
	if resp.GetStatusCode() != 200 {
		t.Errorf("status = %d, want 200", resp.GetStatusCode())
	}
	if string(resp.GetBody()) != `{"kind":"Pod"}` {
		t.Errorf("body = %q", string(resp.GetBody()))
	}
	if rt.lastReq.URL.String() != "https://apiserver.test/api/v1/namespaces/default/pods/p1" {
		t.Errorf("URL = %s", rt.lastReq.URL.String())
	}
	if rt.lastReq.Method != "GET" {
		t.Errorf("Method = %s", rt.lastReq.Method)
	}
	if rt.lastReq.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept header was not propagated")
	}
}

func TestHandleRequest_DefaultMethodIsGet(t *testing.T) {
	rt := &stubRoundTripper{resp: okResponse(200, "")}
	p := newProxyWith(rt)
	p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{Path: "/api"})
	if rt.lastReq.Method != "GET" {
		t.Errorf("default method should be GET, got %s", rt.lastReq.Method)
	}
}

func TestHandleRequest_AuthorizationHeaderStripped(t *testing.T) {
	// Pin the security contract: Authorization from the backend MUST
	// NOT reach the apiserver. The agent's transport carries its own
	// SA token; the backend's value would invert the trust model.
	rt := &stubRoundTripper{resp: okResponse(200, "")}
	p := newProxyWith(rt)
	p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{
		Method: "GET",
		Path:   "/api",
		Headers: map[string]string{
			"Authorization": "Bearer attacker-token",
			"Accept":        "application/json",
		},
	})
	if got := rt.lastReq.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization leaked: %q", got)
	}
	if got := rt.lastReq.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept stripped accidentally: %q", got)
	}
}

func TestHandleRequest_HopByHopHeadersStripped(t *testing.T) {
	rt := &stubRoundTripper{resp: okResponse(200, "")}
	p := newProxyWith(rt)
	p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{
		Method: "GET",
		Path:   "/api",
		Headers: map[string]string{
			"Host":              "evil.example.com",
			"Connection":        "close",
			"Transfer-Encoding": "chunked",
			"Content-Length":    "999",
		},
	})
	for _, h := range []string{"Host", "Connection", "Transfer-Encoding", "Content-Length"} {
		if got := rt.lastReq.Header.Get(h); got != "" {
			t.Errorf("%s leaked: %q", h, got)
		}
	}
}

func TestHandleRequest_PostWithBody(t *testing.T) {
	rt := &stubRoundTripper{resp: okResponse(201, `{"kind":"Pod"}`)}
	p := newProxyWith(rt)
	body := []byte(`{"kind":"Pod","metadata":{"name":"x"}}`)
	p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{
		Method:  "POST",
		Path:    "/api/v1/namespaces/default/pods",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	})
	if rt.lastReq.Method != "POST" {
		t.Errorf("Method = %s", rt.lastReq.Method)
	}
	got, _ := io.ReadAll(rt.lastReq.Body)
	if string(got) != string(body) {
		t.Errorf("body sent = %q, want %q", got, body)
	}
}

func TestHandleRequest_TransportError(t *testing.T) {
	rt := &stubRoundTripper{err: errors.New("connect: connection refused")}
	p := newProxyWith(rt)
	resp := p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{Path: "/api"})
	if resp.GetError() == "" {
		t.Error("expected Error to be populated on transport failure")
	}
	if resp.GetStatusCode() != 0 {
		t.Errorf("StatusCode = %d, want 0 on transport error", resp.GetStatusCode())
	}
}

func TestHandleRequest_HTTPErrorIsNotProxyError(t *testing.T) {
	// 4xx / 5xx must ride back as StatusCode + Body, NOT as Error.
	// Error is reserved for things the backend can't infer from the
	// HTTP envelope (network failures, parse errors).
	rt := &stubRoundTripper{resp: okResponse(404, `{"kind":"Status","status":"Failure"}`)}
	p := newProxyWith(rt)
	resp := p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{Path: "/api/v1/namespaces/x/pods/y"})
	if resp.GetError() != "" {
		t.Errorf("Error should be empty for HTTP 404, got %q", resp.GetError())
	}
	if resp.GetStatusCode() != 404 {
		t.Errorf("StatusCode = %d, want 404", resp.GetStatusCode())
	}
}

func TestHandleRequest_RequestBodyTooLarge(t *testing.T) {
	p := newProxyWith(&stubRoundTripper{resp: okResponse(200, "")})
	huge := make([]byte, MaxBodyBytes+1)
	resp := p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{
		Method: "POST",
		Path:   "/api",
		Body:   huge,
	})
	if resp.GetError() == "" {
		t.Error("expected Error for over-limit request body")
	}
}

func TestHandleRequest_ResponseBodyTooLarge(t *testing.T) {
	huge := strings.Repeat("a", MaxBodyBytes+10)
	rt := &stubRoundTripper{resp: okResponse(200, huge)}
	p := newProxyWith(rt)
	resp := p.HandleRequest(context.Background(), &agentv2.KubeProxyRequest{Path: "/api"})
	if resp.GetError() == "" {
		t.Error("expected Error for over-limit response body")
	}
}

// ─── HandleWatch ──────────────────────────────────────────────────────

// watchBody is a *bytes.Buffer-like body that emits NDJSON one line at
// a time so we can exercise streaming + ctx cancellation.
type watchBody struct {
	lines  []string
	idx    int
	closed bool
	delay  time.Duration
}

func (b *watchBody) Read(p []byte) (int, error) {
	if b.closed {
		return 0, io.EOF
	}
	if b.idx >= len(b.lines) {
		return 0, io.EOF
	}
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	line := b.lines[b.idx] + "\n"
	b.idx++
	n := copy(p, line)
	return n, nil
}

func (b *watchBody) Close() error { b.closed = true; return nil }

func TestHandleWatch_EmitsEventsInOrder(t *testing.T) {
	body := &watchBody{lines: []string{
		`{"type":"ADDED","object":{"kind":"Pod","metadata":{"name":"p1"}}}`,
		`{"type":"MODIFIED","object":{"kind":"Pod","metadata":{"name":"p1"}}}`,
		`{"type":"DELETED","object":{"kind":"Pod","metadata":{"name":"p1"}}}`,
	}}
	rt := &stubRoundTripper{resp: &http.Response{StatusCode: 200, Body: body, Header: http.Header{}}}
	p := newProxyWith(rt)

	events, err := p.HandleWatch(context.Background(), &agentv2.KubeProxyRequest{
		Method: "GET",
		Path:   "/api/v1/pods?watch=true",
		Watch:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"ADDED", "MODIFIED", "DELETED"}
	for i, w := range want {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("chan closed early at %d", i)
			}
			if ev.GetEventType() != w {
				t.Errorf("event[%d] = %s, want %s", i, ev.GetEventType(), w)
			}
		case <-time.After(time.Second):
			t.Fatalf("starved at event %d", i)
		}
	}
	// Chan must close after the last event.
	select {
	case _, ok := <-events:
		if ok {
			t.Error("chan should close after watch ends")
		}
	case <-time.After(time.Second):
		t.Fatal("chan did not close")
	}
}

func TestHandleWatch_HTTPErrorBecomesError(t *testing.T) {
	rt := &stubRoundTripper{resp: &http.Response{
		StatusCode: 403,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"kind":"Status","reason":"Forbidden"}`)),
	}}
	p := newProxyWith(rt)
	_, err := p.HandleWatch(context.Background(), &agentv2.KubeProxyRequest{Watch: true, Path: "/api"})
	if err == nil {
		t.Error("expected error on HTTP 403 watch")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention 403, got %v", err)
	}
}

func TestHandleWatch_CtxCancelStopsStream(t *testing.T) {
	body := &watchBody{
		lines: []string{
			`{"type":"ADDED","object":{}}`,
			`{"type":"MODIFIED","object":{}}`,
			`{"type":"MODIFIED","object":{}}`,
		},
		delay: 50 * time.Millisecond,
	}
	rt := &stubRoundTripper{resp: &http.Response{StatusCode: 200, Body: body, Header: http.Header{}}}
	p := newProxyWith(rt)

	ctx, cancel := context.WithCancel(context.Background())
	events, err := p.HandleWatch(ctx, &agentv2.KubeProxyRequest{Watch: true, Path: "/api"})
	if err != nil {
		t.Fatal(err)
	}

	// Drain one event, then cancel and confirm chan closes.
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("first event never arrived")
	}
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return // chan closed — expected
			}
		case <-deadline:
			t.Fatal("chan did not close after ctx cancel")
		}
	}
}

func TestHandleWatch_ParsesEmptyObjectsAndBookmark(t *testing.T) {
	body := &watchBody{lines: []string{
		`{"type":"BOOKMARK","object":{"kind":"Pod","metadata":{"resourceVersion":"42"}}}`,
		`{"type":"ERROR","object":{"kind":"Status","status":"Failure"}}`,
	}}
	rt := &stubRoundTripper{resp: &http.Response{StatusCode: 200, Body: body, Header: http.Header{}}}
	p := newProxyWith(rt)
	events, err := p.HandleWatch(context.Background(), &agentv2.KubeProxyRequest{Watch: true, Path: "/api"})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, 2)
	for ev := range events {
		got = append(got, ev.GetEventType())
	}
	if len(got) != 2 || got[0] != "BOOKMARK" || got[1] != "ERROR" {
		t.Errorf("got %v, want [BOOKMARK ERROR]", got)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────

func TestIsStrippedHeader(t *testing.T) {
	cases := map[string]bool{
		"Authorization":     true,
		"authorization":     true, // case-insensitive
		"AUTHORIZATION":     true,
		"Host":              true,
		"Connection":        true,
		"Transfer-Encoding": true,
		"Content-Length":    true,
		"Accept":            false,
		"User-Agent":        false,
		"X-Custom":          false,
	}
	for h, want := range cases {
		if got := isStrippedHeader(h); got != want {
			t.Errorf("isStrippedHeader(%q) = %v, want %v", h, got, want)
		}
	}
}
