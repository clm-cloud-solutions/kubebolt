package channel

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// ─── Fakes ────────────────────────────────────────────────────────────

// fakeServer implements agentv2.AgentChannelServer for tests. Each
// Channel call gets its own session struct so concurrent connections
// can be inspected independently.
type fakeServer struct {
	agentv2.UnimplementedAgentChannelServer

	mu       sync.Mutex
	sessions []*fakeSession
}

type fakeSession struct {
	stream      agentv2.AgentChannel_ChannelServer
	helloDone   chan struct{}
	helloMsg    *agentv2.Hello
	receivedMu  sync.Mutex
	received    []*agentv2.AgentMessage
}

func (s *fakeServer) latestSession() *fakeSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) == 0 {
		return nil
	}
	return s.sessions[len(s.sessions)-1]
}

// Channel implements the bidi RPC. Sends Welcome immediately, then
// records every incoming message + lets the test drive backend → agent
// pushes via direct calls on the session.
func (s *fakeServer) Channel(stream agentv2.AgentChannel_ChannelServer) error {
	sess := &fakeSession{
		stream:    stream,
		helloDone: make(chan struct{}),
	}
	s.mu.Lock()
	s.sessions = append(s.sessions, sess)
	s.mu.Unlock()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return errors.New("first message was not Hello")
	}
	sess.helloMsg = hello
	close(sess.helloDone)

	if err := stream.Send(&agentv2.BackendMessage{
		Kind: &agentv2.BackendMessage_Welcome{
			Welcome: &agentv2.Welcome{
				AgentId:   "agent-test-1234",
				ClusterId: "cluster-test",
				Config: &agentv2.AgentConfig{
					SampleIntervalSeconds: 15,
					BatchSize:             100,
					BatchFlushSeconds:     1,
				},
			},
		},
	}); err != nil {
		return err
	}

	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil // EOF or test-driven cancel
		}
		sess.receivedMu.Lock()
		sess.received = append(sess.received, msg)
		sess.receivedMu.Unlock()
	}
}

func (s *fakeSession) recordedKinds() []string {
	s.receivedMu.Lock()
	defer s.receivedMu.Unlock()
	out := make([]string, 0, len(s.received))
	for _, m := range s.received {
		switch m.GetKind().(type) {
		case *agentv2.AgentMessage_Hello:
			out = append(out, "hello")
		case *agentv2.AgentMessage_Heartbeat:
			out = append(out, "heartbeat")
		case *agentv2.AgentMessage_Metrics:
			out = append(out, "metrics")
		case *agentv2.AgentMessage_KubeResponse:
			out = append(out, "kube_response")
		case *agentv2.AgentMessage_KubeEvent:
			out = append(out, "kube_event")
		case *agentv2.AgentMessage_StreamClosed:
			out = append(out, "stream_closed")
		default:
			out = append(out, "?")
		}
	}
	return out
}

// stubSamples is a SamplesProvider that returns canned batches.
type stubSamples struct {
	mu      sync.Mutex
	batches [][]*agentv2.Sample
}

func (s *stubSamples) push(batch []*agentv2.Sample) {
	s.mu.Lock()
	s.batches = append(s.batches, batch)
	s.mu.Unlock()
}

func (s *stubSamples) PopBatch(_ int) []*agentv2.Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.batches) == 0 {
		return nil
	}
	b := s.batches[0]
	s.batches = s.batches[1:]
	return b
}

// recordingHandler counts handler invocations + remembers the last
// values seen. Tests assert on these to confirm Client dispatch.
type recordingHandler struct {
	heartbeatAcks    atomic.Int32
	configUpdates    atomic.Int32
	disconnects      atomic.Int32
	kubeRequests     atomic.Int32
	disconnectErrFn  func(*agentv2.Disconnect) error
	kubeRequestFn    func(c *Client, rid string, req *agentv2.KubeProxyRequest)
}

func (r *recordingHandler) HandleHeartbeatAck(*agentv2.HeartbeatAck) {
	r.heartbeatAcks.Add(1)
}
func (r *recordingHandler) HandleConfigUpdate(*agentv2.ConfigUpdate) {
	r.configUpdates.Add(1)
}
func (r *recordingHandler) HandleDisconnect(d *agentv2.Disconnect) error {
	r.disconnects.Add(1)
	if r.disconnectErrFn != nil {
		return r.disconnectErrFn(d)
	}
	return nil
}
func (r *recordingHandler) HandleKubeRequest(c *Client, rid string, req *agentv2.KubeProxyRequest) {
	r.kubeRequests.Add(1)
	if r.kubeRequestFn != nil {
		r.kubeRequestFn(c, rid, req)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────

func startFakeServer(t *testing.T) (*fakeServer, *grpc.ClientConn, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	fake := &fakeServer{}
	agentv2.RegisterAgentChannelServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stop := func() {
		conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return fake, conn, stop
}

func newTestClient(conn *grpc.ClientConn, samples SamplesProvider, handler Handler) *Client {
	c := NewClient(conn, samples, HelloInfo{NodeName: "node-test", AgentVersion: "vtest"}, handler)
	// Tighten timers so tests don't wait seconds.
	c.FlushEvery = 25 * time.Millisecond
	c.HeartbeatEvery = 50 * time.Millisecond
	return c
}

// ─── Tests ────────────────────────────────────────────────────────────

func TestClient_HelloWelcomePopulatesIdentity(t *testing.T) {
	fake, conn, stop := startFakeServer(t)
	defer stop()

	client := newTestClient(conn, &stubSamples{}, &recordingHandler{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	// Wait for Hello to land.
	deadline := time.Now().Add(time.Second)
	var sess *fakeSession
	for time.Now().Before(deadline) {
		sess = fake.latestSession()
		if sess != nil {
			select {
			case <-sess.helloDone:
				goto helloOK
			default:
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Hello never arrived")
helloOK:

	if sess.helloMsg.GetNodeName() != "node-test" {
		t.Errorf("node_name = %q, want node-test", sess.helloMsg.GetNodeName())
	}
	if sess.helloMsg.GetAgentVersion() != "vtest" {
		t.Errorf("agent_version = %q, want vtest", sess.helloMsg.GetAgentVersion())
	}

	// Welcome should arrive shortly and populate AgentID/ClusterID.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if client.AgentID() != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := client.AgentID(); got != "agent-test-1234" {
		t.Errorf("AgentID = %q, want agent-test-1234", got)
	}
	if got := client.ClusterID(); got != "cluster-test" {
		t.Errorf("ClusterID = %q, want cluster-test", got)
	}
}

func TestClient_FlushesMetricsBatch(t *testing.T) {
	fake, conn, stop := startFakeServer(t)
	defer stop()

	samples := &stubSamples{}
	samples.push([]*agentv2.Sample{
		{MetricName: "kubebolt_test", Value: 1, Timestamp: timestamppb.Now()},
	})

	client := newTestClient(conn, samples, &recordingHandler{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sess := fake.latestSession()
		if sess != nil {
			kinds := sess.recordedKinds()
			for _, k := range kinds {
				if k == "metrics" {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no metrics batch received in 1s")
}

func TestClient_EmitsHeartbeats(t *testing.T) {
	fake, conn, stop := startFakeServer(t)
	defer stop()

	client := newTestClient(conn, &stubSamples{}, &recordingHandler{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sess := fake.latestSession()
		if sess != nil {
			for _, k := range sess.recordedKinds() {
				if k == "heartbeat" {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no heartbeat received in 1s")
}

func TestClient_HandlerReceivesHeartbeatAck(t *testing.T) {
	fake, conn, stop := startFakeServer(t)
	defer stop()

	h := &recordingHandler{}
	client := newTestClient(conn, &stubSamples{}, h)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	// Wait for the session to exist.
	for i := 0; i < 100; i++ {
		if fake.latestSession() != nil {
			select {
			case <-fake.latestSession().helloDone:
				goto ready
			default:
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session never reached hello-done")
ready:

	// Server pushes a HeartbeatAck.
	if err := fake.latestSession().stream.Send(&agentv2.BackendMessage{
		Kind: &agentv2.BackendMessage_HeartbeatAck{
			HeartbeatAck: &agentv2.HeartbeatAck{ReceivedAt: timestamppb.Now()},
		},
	}); err != nil {
		t.Fatalf("server send: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.heartbeatAcks.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("HandleHeartbeatAck never invoked")
}

func TestClient_HandleKubeRequestReceivesPayload(t *testing.T) {
	fake, conn, stop := startFakeServer(t)
	defer stop()

	receivedPath := make(chan string, 1)
	h := &recordingHandler{
		kubeRequestFn: func(c *Client, rid string, req *agentv2.KubeProxyRequest) {
			receivedPath <- req.GetPath()
		},
	}
	client := newTestClient(conn, &stubSamples{}, h)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	for i := 0; i < 100; i++ {
		if fake.latestSession() != nil {
			select {
			case <-fake.latestSession().helloDone:
				goto ready
			default:
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session not ready")
ready:

	if err := fake.latestSession().stream.Send(&agentv2.BackendMessage{
		RequestId: "rid-1",
		Kind: &agentv2.BackendMessage_KubeRequest{
			KubeRequest: &agentv2.KubeProxyRequest{
				Method: "GET",
				Path:   "/api/v1/namespaces/default/pods",
			},
		},
	}); err != nil {
		t.Fatalf("server send: %v", err)
	}

	select {
	case got := <-receivedPath:
		if got != "/api/v1/namespaces/default/pods" {
			t.Errorf("path = %q", got)
		}
	case <-time.After(time.Second):
		t.Errorf("HandleKubeRequest never invoked")
	}
	if got := h.kubeRequests.Load(); got != 1 {
		t.Errorf("kubeRequests = %d, want 1", got)
	}
}

func TestClient_DisconnectTerminatesRun(t *testing.T) {
	fake, conn, stop := startFakeServer(t)
	defer stop()

	h := &recordingHandler{}
	client := newTestClient(conn, &stubSamples{}, h)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(ctx) }()

	for i := 0; i < 100; i++ {
		if fake.latestSession() != nil {
			select {
			case <-fake.latestSession().helloDone:
				goto ready
			default:
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session not ready")
ready:

	if err := fake.latestSession().stream.Send(&agentv2.BackendMessage{
		Kind: &agentv2.BackendMessage_Disconnect{
			Disconnect: &agentv2.Disconnect{Reason: "test-disconnect"},
		},
	}); err != nil {
		t.Fatalf("server send: %v", err)
	}

	select {
	case err := <-runErr:
		if err == nil {
			t.Error("expected non-nil error from Run on Disconnect")
		} else if !contains(err.Error(), "test-disconnect") {
			t.Errorf("err = %v, want it to mention 'test-disconnect'", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not terminate after Disconnect")
	}
	if got := h.disconnects.Load(); got != 1 {
		t.Errorf("disconnects = %d, want 1", got)
	}
}

func TestClient_SendBeforeRunReturnsErrNotRunning(t *testing.T) {
	_, conn, stop := startFakeServer(t)
	defer stop()

	client := NewClient(conn, &stubSamples{}, HelloInfo{NodeName: "x"}, nil)
	if err := client.Send(&agentv2.AgentMessage{}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Send before Run = %v, want ErrNotRunning", err)
	}
}

func TestClient_SendDuringRunSucceeds(t *testing.T) {
	fake, conn, stop := startFakeServer(t)
	defer stop()

	// Use a kube_request handler that replies via Send. This exercises
	// the same code path that commit 4 (KubeAPIProxy) will use.
	sendDone := make(chan error, 1)
	h := &recordingHandler{
		kubeRequestFn: func(c *Client, rid string, req *agentv2.KubeProxyRequest) {
			err := c.Send(&agentv2.AgentMessage{
				RequestId: rid,
				Kind: &agentv2.AgentMessage_KubeResponse{
					KubeResponse: &agentv2.KubeProxyResponse{StatusCode: 200},
				},
			})
			sendDone <- err
		},
	}
	client := newTestClient(conn, &stubSamples{}, h)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	for i := 0; i < 100; i++ {
		if fake.latestSession() != nil {
			select {
			case <-fake.latestSession().helloDone:
				goto ready
			default:
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session not ready")
ready:

	if err := fake.latestSession().stream.Send(&agentv2.BackendMessage{
		RequestId: "rid-2",
		Kind: &agentv2.BackendMessage_KubeRequest{
			KubeRequest: &agentv2.KubeProxyRequest{Method: "GET", Path: "/healthz"},
		},
	}); err != nil {
		t.Fatalf("server send: %v", err)
	}

	select {
	case err := <-sendDone:
		if err != nil {
			t.Errorf("Client.Send during Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("KubeRequest handler never replied")
	}

	// Verify the kube_response landed on the server side.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, k := range fake.latestSession().recordedKinds() {
			if k == "kube_response" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("server never received kube_response")
}

func TestClient_NilHandlerFallsBackToNoop(t *testing.T) {
	_, conn, stop := startFakeServer(t)
	defer stop()

	client := newTestClient(conn, &stubSamples{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	// Should not panic — NoopHandler kicks in.
	_ = client.Run(ctx)
}

// utility — strings.Contains without importing strings just for this.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
