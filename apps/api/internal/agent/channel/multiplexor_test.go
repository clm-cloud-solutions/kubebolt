package channel

import (
	"errors"
	"sync"
	"testing"
	"time"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// helper: build a kube_response message with the given request_id.
func resp(id string, status uint32) *agentv2.AgentMessage {
	return &agentv2.AgentMessage{
		RequestId: id,
		Kind: &agentv2.AgentMessage_KubeResponse{
			KubeResponse: &agentv2.KubeProxyResponse{StatusCode: status},
		},
	}
}

func event(id, eventType string) *agentv2.AgentMessage {
	return &agentv2.AgentMessage{
		RequestId: id,
		Kind: &agentv2.AgentMessage_KubeEvent{
			KubeEvent: &agentv2.KubeProxyWatchEvent{EventType: eventType},
		},
	}
}

func streamClosed(id string) *agentv2.AgentMessage {
	return &agentv2.AgentMessage{
		RequestId: id,
		Kind: &agentv2.AgentMessage_StreamClosed{
			StreamClosed: &agentv2.StreamClosed{Reason: "test"},
		},
	}
}

func TestMultiplexor_RegisterRequiresRequestID(t *testing.T) {
	m := NewMultiplexor()
	if _, _, err := m.Register("", SlotUnary); err == nil {
		t.Error("expected error for empty request_id")
	}
}

func TestMultiplexor_DuplicateRegisterIsError(t *testing.T) {
	m := NewMultiplexor()
	_, cancel, err := m.Register("r1", SlotUnary)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if _, _, err := m.Register("r1", SlotUnary); !errors.Is(err, ErrDuplicateRequestID) {
		t.Errorf("expected ErrDuplicateRequestID, got %v", err)
	}
}

func TestMultiplexor_UnaryDeliverThenAutoCleanup(t *testing.T) {
	m := NewMultiplexor()
	ch, cancel, err := m.Register("r1", SlotUnary)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	m.Deliver(resp("r1", 200))

	select {
	case got := <-ch:
		if got.GetKubeResponse().GetStatusCode() != 200 {
			t.Errorf("status = %d, want 200", got.GetKubeResponse().GetStatusCode())
		}
	case <-time.After(time.Second):
		t.Fatal("Deliver did not push to chan")
	}

	// Auto-cleanup: pending count drops to 0 after a unary terminal message.
	if got := m.Pending(); got != 0 {
		t.Errorf("Pending = %d, want 0 (unary auto-cleanup)", got)
	}

	// Chan must be closed after cleanup.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected chan closed after cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("chan not closed after auto-cleanup")
	}
}

func TestMultiplexor_DeliverWithoutRegisterIsDrop(t *testing.T) {
	m := NewMultiplexor()
	m.Deliver(resp("nobody", 200)) // no panic; nothing happens
	if got := m.Pending(); got != 0 {
		t.Errorf("Pending = %d, want 0", got)
	}
}

func TestMultiplexor_DeliverIgnoresEmptyRequestID(t *testing.T) {
	m := NewMultiplexor()
	m.Deliver(&agentv2.AgentMessage{}) // no request_id; safe drop
	if got := m.Pending(); got != 0 {
		t.Error("empty-request_id Deliver must be a no-op")
	}
}

func TestMultiplexor_WatchReceivesMultipleEvents(t *testing.T) {
	m := NewMultiplexor()
	ch, cancel, err := m.Register("w1", SlotWatch)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	for _, et := range []string{"ADDED", "MODIFIED", "DELETED"} {
		m.Deliver(event("w1", et))
	}

	for _, want := range []string{"ADDED", "MODIFIED", "DELETED"} {
		select {
		case got := <-ch:
			if got.GetKubeEvent().GetEventType() != want {
				t.Errorf("event = %s, want %s", got.GetKubeEvent().GetEventType(), want)
			}
		case <-time.After(time.Second):
			t.Fatalf("watch chan starved waiting for %s", want)
		}
	}
	// Slot still open — no terminal message yet.
	if got := m.Pending(); got != 1 {
		t.Errorf("Pending = %d, want 1 (watch still active)", got)
	}
}

func TestMultiplexor_WatchTerminatesOnKubeResponse(t *testing.T) {
	m := NewMultiplexor()
	ch, cancel, err := m.Register("w1", SlotWatch)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	m.Deliver(event("w1", "ADDED"))
	m.Deliver(resp("w1", 200)) // terminal

	// Drain.
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if got := m.Pending(); got != 0 {
					t.Errorf("Pending = %d, want 0 after terminal", got)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("chan not closed after terminal kube_response")
		}
	}
}

func TestMultiplexor_WatchTerminatesOnStreamClosed(t *testing.T) {
	m := NewMultiplexor()
	ch, cancel, err := m.Register("w1", SlotWatch)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	m.Deliver(streamClosed("w1"))

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("chan not closed after stream_closed")
		}
	}
}

func TestMultiplexor_CancelIsIdempotent(t *testing.T) {
	m := NewMultiplexor()
	ch, cancel, err := m.Register("r1", SlotUnary)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	cancel() // must not panic
	cancel() // ditto

	// chan must be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("chan should be closed after Cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel did not close chan")
	}
}

func TestMultiplexor_CancelAll(t *testing.T) {
	m := NewMultiplexor()
	chs := make([]<-chan *agentv2.AgentMessage, 5)
	for i := 0; i < 5; i++ {
		ch, _, err := m.Register([]string{"a", "b", "c", "d", "e"}[i], SlotUnary)
		if err != nil {
			t.Fatal(err)
		}
		chs[i] = ch
	}
	if got := m.Pending(); got != 5 {
		t.Fatalf("Pending = %d, want 5", got)
	}
	m.CancelAll()
	if got := m.Pending(); got != 0 {
		t.Errorf("Pending = %d, want 0 after CancelAll", got)
	}
	for i, ch := range chs {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("chan[%d] should be closed", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("chan[%d] not closed", i)
		}
	}
}

func TestMultiplexor_ConcurrentRegisterDeliverCancel(t *testing.T) {
	// Stress: 50 concurrent Register/Deliver pairs + a couple of Cancel.
	// Just verify no panics and Pending settles to 0.
	m := NewMultiplexor()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := []byte("r000")
			id[3] = byte('0' + (i % 10))
			id = append(id, byte('0'+(i/10)))
			rid := string(id)
			ch, cancel, err := m.Register(rid, SlotUnary)
			if err != nil {
				t.Errorf("register: %v", err)
				return
			}
			m.Deliver(resp(rid, 200))
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Errorf("did not receive for %s", rid)
			}
			cancel()
		}(i)
	}
	wg.Wait()
	if got := m.Pending(); got != 0 {
		t.Errorf("Pending = %d, want 0 after stress", got)
	}
}

func TestMultiplexor_WatchSaturationDropsOldest(t *testing.T) {
	// Buffer is 64 for watches. Push 70 events without draining; the
	// chan should accept the most recent 64 and drop the older 6.
	m := NewMultiplexor()
	ch, cancel, err := m.Register("w1", SlotWatch)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	for i := 0; i < 70; i++ {
		m.Deliver(event("w1", "EV"))
	}
	// Drain everything available (non-blocking) and count.
	count := 0
DRAIN:
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				break DRAIN
			}
			count++
		default:
			break DRAIN
		}
	}
	// At minimum the buffer capacity should have been preserved.
	if count < 60 {
		t.Errorf("count = %d, expected >= 60 (buffered watch)", count)
	}
}

// ─── SlotTunnel — SPDY/WebSocket upgrade tunnels (Sprint A.5 §0.7-§0.9) ──

func streamData(id string, data []byte, eof bool) *agentv2.AgentMessage {
	return &agentv2.AgentMessage{
		RequestId: id,
		Kind: &agentv2.AgentMessage_KubeStreamData{
			KubeStreamData: &agentv2.KubeStreamData{Data: data, Eof: eof},
		},
	}
}

func streamAck(id string, consumed uint64) *agentv2.AgentMessage {
	return &agentv2.AgentMessage{
		RequestId: id,
		Kind: &agentv2.AgentMessage_KubeStreamAck{
			KubeStreamAck: &agentv2.KubeStreamAck{BytesConsumed: consumed},
		},
	}
}

func TestMultiplexor_TunnelDeliversStreamData(t *testing.T) {
	m := NewMultiplexor()
	ch, cancel, err := m.Register("t1", SlotTunnel)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	m.Deliver(streamData("t1", []byte("stdout: hello"), false))
	got := <-ch
	if got.GetKubeStreamData() == nil {
		t.Fatalf("expected stream_data, got %T", got.GetKind())
	}
	if string(got.GetKubeStreamData().GetData()) != "stdout: hello" {
		t.Errorf("data = %q", got.GetKubeStreamData().GetData())
	}
	if m.Pending() != 1 {
		t.Error("non-eof KubeStreamData must NOT terminate the slot")
	}
}

func TestMultiplexor_TunnelEofTerminates(t *testing.T) {
	m := NewMultiplexor()
	ch, _, err := m.Register("t1", SlotTunnel)
	if err != nil {
		t.Fatal(err)
	}

	m.Deliver(streamData("t1", []byte("last"), true))
	// EOF message is delivered first…
	first := <-ch
	if !first.GetKubeStreamData().GetEof() {
		t.Error("expected EOF flag on first message")
	}
	// …then the chan closes (auto-cleanup).
	if _, open := <-ch; open {
		t.Error("chan should close after EOF")
	}
	if m.Pending() != 0 {
		t.Error("EOF should clean up the slot")
	}
}

func TestMultiplexor_TunnelStreamClosedTerminates(t *testing.T) {
	m := NewMultiplexor()
	ch, _, err := m.Register("t1", SlotTunnel)
	if err != nil {
		t.Fatal(err)
	}
	m.Deliver(streamClosed("t1"))
	if _, open := <-ch; open {
		// First receive returns the StreamClosed message…
		// (we drained nothing, this returns the message itself)
	}
	// Second receive: chan closed.
	if _, open := <-ch; open {
		t.Error("chan should close after StreamClosed")
	}
	if m.Pending() != 0 {
		t.Error("StreamClosed should clean up tunnel slot")
	}
}

func TestMultiplexor_TunnelHandshake101DoesNotTerminate(t *testing.T) {
	// The 101 Switching Protocols KubeProxyResponse on a tunnel slot
	// is the upgrade handshake — bytes phase is just starting. It
	// MUST NOT terminate the slot, otherwise the consumer hangs
	// before any KubeStreamData arrives.
	m := NewMultiplexor()
	ch, cancel, err := m.Register("t1", SlotTunnel)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	m.Deliver(resp("t1", 101))
	<-ch
	if m.Pending() != 1 {
		t.Error("101 Switching Protocols on tunnel slot must NOT terminate")
	}

	// Subsequent stream data still flows.
	m.Deliver(streamData("t1", []byte("x"), false))
	got := <-ch
	if got.GetKubeStreamData() == nil {
		t.Error("expected stream_data after 101 handshake")
	}
}

func TestMultiplexor_TunnelNon101ResponseTerminates(t *testing.T) {
	// Anything other than 101 on a tunnel slot is a protocol error
	// (e.g. 403 from apiserver during upgrade) and DOES terminate.
	m := NewMultiplexor()
	ch, _, err := m.Register("t1", SlotTunnel)
	if err != nil {
		t.Fatal(err)
	}
	m.Deliver(resp("t1", 403))
	<-ch
	if _, open := <-ch; open {
		t.Error("non-101 response on tunnel slot should terminate")
	}
}

func TestMultiplexor_TunnelBufferLargerThanWatch(t *testing.T) {
	// Pin the buffer size differential — tunnel slots must accept
	// substantially more pre-consumption messages than watch slots
	// (256 vs 64 today). Without the headroom, exec sessions with
	// momentary consumer pauses would hit overflow on every burst.
	m := NewMultiplexor()
	ch, cancel, err := m.Register("t1", SlotTunnel)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// Push more than the watch buffer (64) but less than tunnel (256).
	const want = 100
	for i := 0; i < want; i++ {
		m.Deliver(streamData("t1", []byte{byte(i)}, false))
	}
	got := 0
	for {
		select {
		case <-ch:
			got++
			if got >= want {
				return
			}
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("read %d/%d before timeout — tunnel buffer too small or drops", got, want)
		}
	}
}

func TestMultiplexor_TunnelSaturationClosesWithOverflow(t *testing.T) {
	// Tunnels can't tolerate byte loss. When the buffer overflows the
	// slot MUST close with a synthetic StreamClosed{reason=
	// "buffer_overflow"} so the consumer tears the tunnel down
	// instead of accepting corrupted bytes.
	m := NewMultiplexor()
	ch, _, err := m.Register("t1", SlotTunnel)
	if err != nil {
		t.Fatal(err)
	}

	// Fill the buffer plus one extra to trigger overflow handling.
	for i := 0; i < tunnelSlotBufferSize+1; i++ {
		m.Deliver(streamData("t1", []byte{byte(i % 256)}, false))
	}

	// Drain the channel: we expect to find a StreamClosed with the
	// overflow reason somewhere before EOF.
	sawOverflow := false
	for msg := range ch {
		if sc := msg.GetStreamClosed(); sc != nil {
			if sc.GetReason() != "buffer_overflow" {
				t.Errorf("StreamClosed reason = %q, want buffer_overflow", sc.GetReason())
			}
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Error("expected StreamClosed{reason=buffer_overflow} on tunnel saturation")
	}
	if m.Pending() != 0 {
		t.Error("overflowed tunnel slot must be released")
	}
}

func TestMultiplexor_TunnelAckDeliveredToChan(t *testing.T) {
	// KubeStreamAck flows through the same chan as stream_data.
	// The hijackedConn consumer demuxes — the Multiplexor itself
	// stays protocol-agnostic. Pin that the message is NOT silently
	// dropped or treated as terminal.
	m := NewMultiplexor()
	ch, cancel, err := m.Register("t1", SlotTunnel)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	m.Deliver(streamAck("t1", 4096))
	got := <-ch
	if got.GetKubeStreamAck() == nil {
		t.Fatalf("expected stream_ack, got %T", got.GetKind())
	}
	if got.GetKubeStreamAck().GetBytesConsumed() != 4096 {
		t.Errorf("bytes_consumed = %d", got.GetKubeStreamAck().GetBytesConsumed())
	}
	if m.Pending() != 1 {
		t.Error("KubeStreamAck must NOT terminate the tunnel slot")
	}
}

func TestSlotMode_String(t *testing.T) {
	cases := map[SlotMode]string{
		SlotUnary:     "unary",
		SlotWatch:     "watch",
		SlotTunnel:    "tunnel",
		SlotMode(99): "SlotMode(99)",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(mode), got, want)
		}
	}
}
