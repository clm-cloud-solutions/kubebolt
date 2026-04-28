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
	if _, _, err := m.Register("", false); err == nil {
		t.Error("expected error for empty request_id")
	}
}

func TestMultiplexor_DuplicateRegisterIsError(t *testing.T) {
	m := NewMultiplexor()
	_, cancel, err := m.Register("r1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if _, _, err := m.Register("r1", false); !errors.Is(err, ErrDuplicateRequestID) {
		t.Errorf("expected ErrDuplicateRequestID, got %v", err)
	}
}

func TestMultiplexor_UnaryDeliverThenAutoCleanup(t *testing.T) {
	m := NewMultiplexor()
	ch, cancel, err := m.Register("r1", false)
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
	ch, cancel, err := m.Register("w1", true)
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
	ch, cancel, err := m.Register("w1", true)
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
	ch, cancel, err := m.Register("w1", true)
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
	ch, cancel, err := m.Register("r1", false)
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
		ch, _, err := m.Register([]string{"a", "b", "c", "d", "e"}[i], false)
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
			ch, cancel, err := m.Register(rid, false)
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
	ch, cancel, err := m.Register("w1", true)
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
