package channel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// upgradeRig builds the wiring for a tunnel test. Returns the
// transport, agent (so tests can drive incoming messages via
// agent.Pending.Deliver), and the captureSender (so tests can
// inspect what the transport writes back to the agent).
func upgradeRig(t *testing.T) (*AgentProxyTransport, *Agent, *captureSender) {
	t.Helper()
	sender := newCaptureSender()
	agent := NewAgent("c1", "agent-1", "node-a", nil, sender)
	reg := NewAgentRegistry()
	reg.Register(agent)
	tr := NewAgentProxyTransport("c1", reg)
	tr.DefaultTimeout = 0
	return tr, agent, sender
}

func TestIsUpgradeRequest(t *testing.T) {
	cases := []struct {
		name string
		conn string
		upg  string
		want bool
	}{
		{"both headers SPDY", "Upgrade", "SPDY/3.1", true},
		{"both headers WS", "Upgrade", "websocket", true},
		{"connection multi-token", "keep-alive, Upgrade", "SPDY/3.1", true},
		{"upgrade missing", "Upgrade", "", false},
		{"connection missing", "", "SPDY/3.1", false},
		{"connection close (no upgrade)", "close", "SPDY/3.1", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "https://kube/api/v1/...", nil)
		if tc.conn != "" {
			req.Header.Set("Connection", tc.conn)
		}
		if tc.upg != "" {
			req.Header.Set("Upgrade", tc.upg)
		}
		if got := isUpgradeRequest(req); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAgentProxyTransport_UpgradeHandshakeReturnsTunnelConn(t *testing.T) {
	tr, agent, sender := upgradeRig(t)

	type result struct {
		resp *http.Response
		err  error
	}
	out := make(chan result, 1)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/api/v1/namespaces/default/pods/p1/exec?command=sh", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		resp, err := tr.RoundTrip(req)
		out <- result{resp, err}
	}()

	sent := sender.lastRequest(t)
	kubeReq := sent.GetKubeRequest()
	if kubeReq == nil {
		t.Fatal("expected kube_request payload")
	}
	if kubeReq.GetMethod() != "POST" {
		t.Errorf("method = %q", kubeReq.GetMethod())
	}
	// Upgrade headers MUST be forwarded — the agent needs them to
	// perform the SPDY upgrade against its own apiserver. We don't
	// strip Connection here even though it's hop-by-hop in the
	// general case, because for upgrade requests it carries the
	// "Upgrade" token.
	if got := kubeReq.GetHeaders()["Upgrade"]; got != "SPDY/3.1" {
		t.Errorf("Upgrade header = %q, want SPDY/3.1 forwarded", got)
	}

	// Drive the 101 handshake from the agent.
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: sent.GetRequestId(),
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{
				StatusCode: 101,
				Headers:    map[string]string{"Upgrade": "SPDY/3.1", "Connection": "Upgrade"},
			},
		},
	})

	r := <-out
	if r.err != nil {
		t.Fatalf("RoundTrip err = %v", r.err)
	}
	if r.resp.StatusCode != 101 {
		t.Errorf("status = %d, want 101", r.resp.StatusCode)
	}
	conn, ok := r.resp.Body.(*TunnelConn)
	if !ok {
		t.Fatalf("Body = %T, want *TunnelConn", r.resp.Body)
	}
	defer conn.Close()
	// Must satisfy net.Conn for SPDY layer's hijack.
	var _ net.Conn = conn
}

func TestAgentProxyTransport_UpgradeFailsWith403(t *testing.T) {
	// Apiserver rejected the upgrade (RBAC denied, pod gone, etc.).
	// The agent forwards the non-101 response unchanged; the caller
	// sees a regular http.Response with StatusCode=403.
	tr, agent, sender := upgradeRig(t)
	out := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/api/v1/.../exec", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		resp, err := tr.RoundTrip(req)
		out <- struct {
			resp *http.Response
			err  error
		}{resp, err}
	}()
	sent := sender.lastRequest(t)
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: sent.GetRequestId(),
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{
				StatusCode: 403,
				Body:       []byte(`{"reason":"Forbidden"}`),
			},
		},
	})
	r := <-out
	if r.err != nil {
		t.Fatalf("err = %v, want clean response", r.err)
	}
	if r.resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 (apiserver upgrade rejection)", r.resp.StatusCode)
	}
}

func TestAgentProxyTransport_UpgradeContextCancelBeforeHandshake(t *testing.T) {
	tr, _, sender := upgradeRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/.../exec", nil).WithContext(ctx)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		_, err := tr.RoundTrip(req)
		out <- err
	}()
	_ = sender.lastRequest(t)
	cancel()
	if err := <-out; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestTunnelConn_ReadDeliversBytesUntilEOF(t *testing.T) {
	tr, agent, sender := upgradeRig(t)
	out := make(chan *http.Response, 1)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/exec", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Errorf("RoundTrip err = %v", err)
			return
		}
		out <- resp
	}()
	sent := sender.lastRequest(t)
	requestID := sent.GetRequestId()
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{StatusCode: 101},
		},
	})
	resp := <-out
	conn := resp.Body.(*TunnelConn)

	// Stream a few stdout chunks then EOF.
	for _, chunk := range [][]byte{
		[]byte("hello "),
		[]byte("world\n"),
		[]byte("kubebolt\n"),
	} {
		agent.Pending.Deliver(streamData(requestID, chunk, false))
	}
	agent.Pending.Deliver(streamData(requestID, []byte("DONE"), true))

	// Read should return all bytes in order, then io.EOF.
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("ReadAll err = %v", err)
	}
	if want := "hello world\nkubebolt\nDONE"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTunnelConn_WriteEmitsKubeStreamData(t *testing.T) {
	tr, agent, sender := upgradeRig(t)
	out := make(chan *http.Response, 1)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/exec", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		resp, _ := tr.RoundTrip(req)
		out <- resp
	}()
	sent := sender.lastRequest(t)
	rid := sent.GetRequestId()
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: rid,
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{StatusCode: 101},
		},
	})
	resp := <-out
	conn := resp.Body.(*TunnelConn)
	defer conn.Close()

	// Write some stdin data — should emerge as one or more
	// BackendMessage{KubeStreamData}.
	payload := []byte("ls -la\n")
	n, err := conn.Write(payload)
	if err != nil {
		t.Fatalf("Write err = %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write n = %d, want %d", n, len(payload))
	}
	// Drain any subsequent BackendMessages from the sender.
	deadline := time.Now().Add(500 * time.Millisecond)
	var collected []byte
	for time.Now().Before(deadline) {
		select {
		case msg := <-sender.sent:
			if sd := msg.GetKubeStreamData(); sd != nil && msg.GetRequestId() == rid {
				collected = append(collected, sd.GetData()...)
				if len(collected) >= len(payload) {
					goto done
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
done:
	if string(collected) != string(payload) {
		t.Errorf("agent received %q, want %q", collected, payload)
	}
}

func TestTunnelConn_WriteChunksLargePayload(t *testing.T) {
	tr, agent, sender := upgradeRig(t)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/exec", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Errorf("RoundTrip err = %v", err)
			return
		}
		// Use a large window so we don't block on credits — the chunking
		// behavior is what we care about here, not flow control.
		conn := resp.Body.(*TunnelConn)
		defer conn.Close()
		big := make([]byte, MaxTunnelChunkBytes*3+512)
		for i := range big {
			big[i] = byte(i % 251)
		}
		_, _ = conn.Write(big)
	}()
	first := sender.lastRequest(t)
	rid := first.GetRequestId()
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: rid,
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{StatusCode: 101},
		},
	})

	// Expect at least 4 KubeStreamData messages (3 full chunks + tail).
	chunkCount := 0
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && chunkCount < 4 {
		select {
		case msg := <-sender.sent:
			if msg.GetKubeStreamData() != nil && msg.GetRequestId() == rid {
				if len(msg.GetKubeStreamData().GetData()) > MaxTunnelChunkBytes {
					t.Errorf("chunk %d > MaxTunnelChunkBytes (%d > %d)",
						chunkCount, len(msg.GetKubeStreamData().GetData()), MaxTunnelChunkBytes)
				}
				chunkCount++
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if chunkCount < 4 {
		t.Errorf("got %d chunks, expected ≥4 for payload > 3*MaxTunnelChunkBytes", chunkCount)
	}
}

func TestTunnelConn_WriteBlocksOnSaturatedWindow(t *testing.T) {
	// With a small window (8 KiB), writing 32 KiB without ACKs should
	// block after ~8 KiB. After we deliver an ACK the writer unblocks
	// and progresses.
	sender := newCaptureSender()
	agent := NewAgent("c1", "agent-1", "node-a", nil, sender)
	reg := NewAgentRegistry()
	reg.Register(agent)
	tr := &AgentProxyTransport{ClusterID: "c1", Registry: reg, TunnelWindowBytes: 8192}

	out := make(chan *http.Response, 1)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/exec", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		resp, _ := tr.RoundTrip(req)
		out <- resp
	}()
	first := sender.lastRequest(t)
	rid := first.GetRequestId()
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: rid,
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{StatusCode: 101},
		},
	})
	resp := <-out
	conn := resp.Body.(*TunnelConn)
	defer conn.Close()

	writeDone := make(chan error, 1)
	payload := make([]byte, 32*1024)
	go func() {
		_, err := conn.Write(payload)
		writeDone <- err
	}()

	// Without ACKs, the writer should NOT complete within ~50ms (it
	// stalls on credits after the first 8 KiB).
	select {
	case <-writeDone:
		t.Fatal("Write completed despite saturated window — flow control not enforced")
	case <-time.After(50 * time.Millisecond):
	}

	// Drain the writes already on the wire so the writer is blocked
	// purely on credit, then ACK enough to release the rest.
	go func() {
		for {
			select {
			case <-sender.sent:
			case <-time.After(50 * time.Millisecond):
				return
			}
		}
	}()
	agent.Pending.Deliver(streamAck(rid, 32*1024))

	select {
	case err := <-writeDone:
		if err != nil {
			t.Errorf("Write err after ACK = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Write did not unblock after KubeStreamAck")
	}
}

func TestTunnelConn_CloseSendsEofAndUnregisters(t *testing.T) {
	tr, agent, sender := upgradeRig(t)
	out := make(chan *http.Response, 1)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/exec", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		resp, _ := tr.RoundTrip(req)
		out <- resp
	}()
	sent := sender.lastRequest(t)
	rid := sent.GetRequestId()
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: rid,
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{StatusCode: 101},
		},
	})
	conn := (<-out).Body.(*TunnelConn)

	if got := agent.Pending.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1", got)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("Close err = %v", err)
	}
	// Idempotent.
	_ = conn.Close()

	// Slot must be released.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if agent.Pending.Pending() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := agent.Pending.Pending(); got != 0 {
		t.Errorf("Pending = %d after Close, want 0", got)
	}

	// EOF marker must be sent.
	sawEof := false
	deadline = time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && !sawEof {
		select {
		case msg := <-sender.sent:
			if sd := msg.GetKubeStreamData(); sd != nil && sd.GetEof() && msg.GetRequestId() == rid {
				sawEof = true
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !sawEof {
		t.Error("expected KubeStreamData{eof:true} on Close")
	}
}

func TestTunnelConn_ReadDeadline(t *testing.T) {
	tr, agent, sender := upgradeRig(t)
	go func() {
		req := httptest.NewRequest("POST", "https://kube/exec", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "SPDY/3.1")
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Errorf("RoundTrip err = %v", err)
			return
		}
		conn := resp.Body.(*TunnelConn)
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		buf := make([]byte, 16)
		_, err = conn.Read(buf)
		if err == nil {
			t.Error("expected deadline error, got nil")
			return
		}
		ne, ok := err.(net.Error)
		if !ok || !ne.Timeout() {
			t.Errorf("err = %v (%T), want net.Error.Timeout()=true", err, err)
		}
	}()
	first := sender.lastRequest(t)
	agent.Pending.Deliver(&agentv2.AgentMessage{
		RequestId: first.GetRequestId(),
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{StatusCode: 101},
		},
	})
	time.Sleep(150 * time.Millisecond)
}

func TestTunnelConn_AddrSurfaceClusterID(t *testing.T) {
	tc := &TunnelConn{clusterID: "c-prod-eu"}
	if !strings.Contains(tc.RemoteAddr().String(), "c-prod-eu") {
		t.Errorf("RemoteAddr = %q must include cluster_id", tc.RemoteAddr().String())
	}
	if got := tc.LocalAddr().Network(); got != "agent-proxy" {
		t.Errorf("Network = %q, want agent-proxy", got)
	}
}
