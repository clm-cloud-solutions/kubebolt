package proxy

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// captureSender is a thread-safe stand-in for *channel.Client.Send.
// Records every AgentMessage so tests can assert on what the pump
// emitted.
type captureSender struct {
	mu   sync.Mutex
	msgs []*agentv2.AgentMessage
	sent chan *agentv2.AgentMessage
	err  error
}

func newCaptureSender() *captureSender {
	return &captureSender{sent: make(chan *agentv2.AgentMessage, 64)}
}

func (s *captureSender) Send(msg *agentv2.AgentMessage) error {
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

func (s *captureSender) snapshot() []*agentv2.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*agentv2.AgentMessage, len(s.msgs))
	copy(out, s.msgs)
	return out
}

// fakeReadWriteCloser is a duplex pipe-like conn for upgrade tests.
// reads come from `inbound`, writes go to `outbound`. Close closes
// both.
type fakeReadWriteCloser struct {
	inbound  io.ReadCloser
	outbound io.WriteCloser
	closed   chan struct{}
	once     sync.Once
}

func newFakePipe() (apiServerEnd *fakeReadWriteCloser, controlReader *io.PipeReader, controlWriter *io.PipeWriter) {
	// apiserver-side conn: bytes apiserver "would write" come from
	// controlWriter (test side); apiserver "reads" via controlReader.
	pr1, pw1 := io.Pipe() // test → apiserver writes; apiserver reads from pr1
	pr2, pw2 := io.Pipe() // apiserver writes to pw2; test reads from pr2
	apiServerEnd = &fakeReadWriteCloser{
		inbound:  pr1,
		outbound: pw2,
		closed:   make(chan struct{}),
	}
	return apiServerEnd, pr2, pw1
}

func (f *fakeReadWriteCloser) Read(p []byte) (int, error)  { return f.inbound.Read(p) }
func (f *fakeReadWriteCloser) Write(p []byte) (int, error) { return f.outbound.Write(p) }
func (f *fakeReadWriteCloser) Close() error {
	f.once.Do(func() {
		_ = f.inbound.Close()
		_ = f.outbound.Close()
		close(f.closed)
	})
	return nil
}

// ─── HandleUpgrade ────────────────────────────────────────────────────

func TestIsUpgradeRequest_AgentSide(t *testing.T) {
	cases := []struct {
		name string
		hdrs map[string]string
		want bool
	}{
		{"both", map[string]string{"Connection": "Upgrade", "Upgrade": "SPDY/3.1"}, true},
		{"connection multi", map[string]string{"Connection": "keep-alive, Upgrade", "Upgrade": "SPDY/3.1"}, true},
		{"upgrade missing", map[string]string{"Connection": "Upgrade"}, false},
		{"connection close", map[string]string{"Connection": "close", "Upgrade": "SPDY/3.1"}, false},
		{"empty", map[string]string{}, false},
		{"nil headers", nil, false},
	}
	for _, tc := range cases {
		req := &agentv2.KubeProxyRequest{Headers: tc.hdrs}
		if got := isUpgradeRequest(req); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// rwResponse is an *http.Response whose Body satisfies
// io.ReadWriteCloser. Mimics what Go's net/http hands back for a 101
// Switching Protocols response.
//
// Kept around even though the legacy transport-based HandleUpgrade
// tests were removed (they exercised an http.Transport-based code
// path we replaced with a manual TLS dial after smoke testing
// surfaced that Go's transport returns EOF on read after 101).
// Future integration tests with a fake apiserver (commit 8h) will
// reuse this helper.
type rwBody struct {
	io.Reader
	io.Writer
	io.Closer
}






// ─── tunnelSession bidi pump ──────────────────────────────────────────

func TestTunnelSession_PumpToBackendForwardsBytes(t *testing.T) {
	// Pipe simulates the apiserver writing stdout to the agent.
	apiR, apiW := io.Pipe()
	sender := newCaptureSender()
	sess := newTunnelSession("rid-1")

	go func() {
		// Apiserver sends 3 chunks then closes.
		_, _ = apiW.Write([]byte("hello "))
		_, _ = apiW.Write([]byte("world\n"))
		_, _ = apiW.Write([]byte("done"))
		_ = apiW.Close()
	}()

	// Window is large enough to never block on credits.
	sess.pumpToBackend(context.Background(), apiR, sender, 1<<20)

	// Collect what the sender saw.
	msgs := sender.snapshot()
	var data []byte
	sawEof := false
	for _, m := range msgs {
		if sd := m.GetKubeStreamData(); sd != nil {
			data = append(data, sd.GetData()...)
			if sd.GetEof() {
				sawEof = true
			}
		}
	}
	if string(data) != "hello world\ndone" {
		t.Errorf("forwarded = %q", data)
	}
	if !sawEof {
		t.Error("expected KubeStreamData{eof:true} on apiserver close")
	}
}

func TestTunnelSession_PumpToBackendBlocksOnCreditWindow(t *testing.T) {
	// Window 8 KiB; apiserver writes 32 KiB. Without ACKs the pump
	// stops after ~8 KiB. After we ACK 8K, another 8K flows.
	apiR, apiW := io.Pipe()
	sender := newCaptureSender()
	sess := newTunnelSession("rid-1")

	pumpDone := make(chan struct{})
	go func() {
		sess.pumpToBackend(context.Background(), apiR, sender, 8192)
		close(pumpDone)
	}()

	// Producer goroutine blocks on the pipe write — pump only reads
	// 8K before stalling.
	go func() {
		_, _ = apiW.Write(make([]byte, 32*1024))
		_ = apiW.Close()
	}()

	// After ~50ms the pump should have stalled at ~8K bytes sent.
	time.Sleep(50 * time.Millisecond)
	first := sender.snapshot()
	var firstBytes int
	for _, m := range first {
		if sd := m.GetKubeStreamData(); sd != nil {
			firstBytes += len(sd.GetData())
		}
	}
	if firstBytes != 8192 {
		t.Errorf("first phase bytes = %d, want 8192 (window)", firstBytes)
	}

	// ACK 8K — pump should send another 8K.
	sess.acks <- 8192
	time.Sleep(50 * time.Millisecond)
	second := sender.snapshot()
	var secondBytes int
	for _, m := range second {
		if sd := m.GetKubeStreamData(); sd != nil {
			secondBytes += len(sd.GetData())
		}
	}
	if secondBytes != 16384 {
		t.Errorf("after first ACK total = %d, want 16384", secondBytes)
	}

	// Drain the rest with a fat ACK so the test cleans up.
	sess.acks <- 24 * 1024
	select {
	case <-pumpDone:
	case <-time.After(time.Second):
		t.Fatal("pumpToBackend did not finish after ACKs released window")
	}
}

func TestTunnelSession_PumpToApiserverWritesAndAcks(t *testing.T) {
	// Inbound chan delivers backend → agent KubeStreamData; pump
	// writes to a pipe (apiserver side) and sends an ACK back.
	apiR, apiW := io.Pipe()
	wrappedConn := &fakeReadWriteCloser{inbound: io.NopCloser(strings.NewReader("")), outbound: apiW, closed: make(chan struct{})}

	sender := newCaptureSender()
	sess := newTunnelSession("rid-1")

	pumpDone := make(chan struct{})
	go func() {
		sess.pumpToApiserver(context.Background(), wrappedConn, sender)
		close(pumpDone)
	}()

	// Reader goroutine drains the pipe so apiW.Write doesn't block.
	got := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := apiR.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				got <- cp
			}
			if err != nil {
				close(got)
				return
			}
		}
	}()

	sess.inbound <- &agentv2.KubeStreamData{Data: []byte("ls -la\n")}
	sess.inbound <- &agentv2.KubeStreamData{Data: []byte("exit\n")}
	sess.inbound <- &agentv2.KubeStreamData{Eof: true}

	select {
	case <-pumpDone:
	case <-time.After(time.Second):
		t.Fatal("pumpToApiserver did not exit after eof")
	}
	_ = wrappedConn.Close()

	// Drain whatever the apiserver received.
	var collected []byte
	for chunk := range got {
		collected = append(collected, chunk...)
	}
	if string(collected) != "ls -la\nexit\n" {
		t.Errorf("apiserver got %q", collected)
	}

	// Verify ACKs were sent for each non-eof payload.
	var totalAcked uint64
	for _, m := range sender.snapshot() {
		if ack := m.GetKubeStreamAck(); ack != nil {
			totalAcked += ack.GetBytesConsumed()
		}
	}
	if want := uint64(len("ls -la\nexit\n")); totalAcked != want {
		t.Errorf("acked = %d, want %d", totalAcked, want)
	}
}

func TestTunnelSession_CloseTerminatesBothPumps(t *testing.T) {
	apiR, _ := io.Pipe()
	wrappedConn := &fakeReadWriteCloser{inbound: apiR, outbound: io.WriteCloser(nopWriteCloser{}), closed: make(chan struct{})}
	sender := newCaptureSender()
	sess := newTunnelSession("rid-1")

	runDone := make(chan struct{})
	go func() {
		sess.run(context.Background(), wrappedConn, sender, DefaultTunnelWindowBytes)
		close(runDone)
	}()

	// Trigger early teardown.
	sess.close()

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("session.run did not exit after close()")
	}
}

// nopWriteCloser is io.Discard with a Close method.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                 { return nil }
