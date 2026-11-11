package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// fakeVM answers the import endpoint with whatever status the test sets, and
// keeps the bodies it took.
type fakeVM struct {
	srv    *httptest.Server
	status atomic.Int32
	mu     sync.Mutex
	got    []string
}

func newFakeVM(t *testing.T, status int) *fakeVM {
	t.Helper()
	f := &fakeVM{}
	f.status.Store(int32(status))
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := int(f.status.Load())
		if code < 300 {
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.got = append(f.got, string(b))
			f.mu.Unlock()
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeVM) bodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func spoolSample(name string, tsMillis int64) *agentv2.Sample {
	return &agentv2.Sample{MetricName: name, Value: 1, Labels: map[string]string{"cluster_id": "c1"}, Timestamp: timestamppb.New(time.UnixMilli(tsMillis))}
}

func newTestSpool(t *testing.T, url string, maxBytes int) (*SpoolingWriter, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	return NewSpoolingWriter(NewVMWriter(url), maxBytes, reg), reg
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The production shape: VictoriaMetrics restarts, the batches that arrive
// meanwhile are held, and they land once it answers — no gap.
func TestSpool_ARestartOfVictoriaMetricsLeavesNoGap(t *testing.T) {
	vm := newFakeVM(t, http.StatusServiceUnavailable)
	s, reg := newTestSpool(t, vm.srv.URL, DefaultIngestSpoolBytes)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	for i := int64(1); i <= 3; i++ {
		if err := s.Write(ctx, []*agentv2.Sample{spoolSample("kubelet_up", i*1000)}); err == nil {
			t.Fatal("a write to a VictoriaMetrics that is down reported success")
		}
	}
	if n, _ := s.Held(); n != 3 {
		t.Fatalf("held %d batches, want 3", n)
	}

	vm.status.Store(http.StatusNoContent)
	waitFor(t, "the held batches to be written", func() bool { n, _ := s.Held(); return n == 0 })

	bodies := vm.bodies()
	if len(bodies) != 3 || !strings.Contains(bodies[0], " 1000\n") || !strings.Contains(bodies[2], " 3000\n") {
		t.Fatalf("written later, oldest first, with their own timestamps; got %q", bodies)
	}
	if got := testutil.ToFloat64(s.samples.WithLabelValues("written")); got != 3 {
		t.Errorf("written = %v, want 3", got)
	}
	if got := testutil.ToFloat64(s.held); got != 0 {
		t.Errorf("held bytes = %v after draining", got)
	}
	_ = reg
}

func TestSpool_AnUnreachableStoreIsHeldToo(t *testing.T) {
	vm := newFakeVM(t, http.StatusNoContent)
	url := vm.srv.URL
	vm.srv.Close()
	s, _ := newTestSpool(t, url, DefaultIngestSpoolBytes)
	if err := s.Write(context.Background(), []*agentv2.Sample{spoolSample("up", 1)}); err == nil {
		t.Fatal("expected an error")
	}
	if n, _ := s.Held(); n != 1 {
		t.Fatalf("held %d, want 1: a connection refused passes", n)
	}
}

func TestSpool_DataTheStoreRefusesIsNotHeld(t *testing.T) {
	vm := newFakeVM(t, http.StatusBadRequest)
	s, _ := newTestSpool(t, vm.srv.URL, DefaultIngestSpoolBytes)
	if err := s.Write(context.Background(), []*agentv2.Sample{spoolSample("up", 1)}); err == nil {
		t.Fatal("expected the 400 to be returned")
	}
	if n, _ := s.Held(); n != 0 {
		t.Fatalf("held %d: a 400 would be retried forever", n)
	}
}

func TestSpool_OverTheBoundTheOldestGoesFirst(t *testing.T) {
	vm := newFakeVM(t, http.StatusServiceUnavailable)
	one := len(encodeSamples([]*agentv2.Sample{spoolSample("up", 1000)}))
	s, _ := newTestSpool(t, vm.srv.URL, 2*one)
	for i := int64(1); i <= 3; i++ {
		_ = s.Write(context.Background(), []*agentv2.Sample{spoolSample("up", i*1000)})
	}
	n, bytes := s.Held()
	if n != 2 || bytes > 2*one {
		t.Fatalf("held %d batches / %d bytes, want 2 within %d", n, bytes, 2*one)
	}
	if got := testutil.ToFloat64(s.samples.WithLabelValues("dropped")); got != 1 {
		t.Errorf("dropped = %v, want 1 (the oldest)", got)
	}
	if b, _ := s.peek(); !strings.Contains(string(b.body), " 2000\n") {
		t.Errorf("oldest kept = %q, want the second batch", b.body)
	}
}

func TestSpool_ZeroBoundIsAPlainWriter(t *testing.T) {
	vm := newFakeVM(t, http.StatusServiceUnavailable)
	s, _ := newTestSpool(t, vm.srv.URL, 0)
	_ = s.Write(context.Background(), []*agentv2.Sample{spoolSample("up", 1)})
	if n, _ := s.Held(); n != 0 {
		t.Fatalf("held %d with the hold off", n)
	}
}
