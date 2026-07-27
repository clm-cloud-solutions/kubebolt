package api

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestSSEStream_CommentIsIgnorableKeepalive locks the wire format of the
// keepalive heartbeat: an SSE comment line (":" prefix) with NO data field.
// The frontend parser drops any frame without a `data:` line, so this must
// never carry a payload — its only job is to push bytes so the browser
// (Safari's ~60s idle timeout) and Envoy's stream_idle_timeout don't abort a
// long, quiet turn. Regression guard for the "Load failed" fix.
func TestSSEStream_CommentIsIgnorableKeepalive(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &sseStream{w: rec, flusher: rec}

	s.comment()

	got := rec.Body.String()
	if got != ": keepalive\n\n" {
		t.Fatalf("comment() = %q, want %q", got, ": keepalive\n\n")
	}
	// A comment must NOT look like a delivered event — no event:/data: lines.
	if strings.Contains(got, "event:") || strings.Contains(got, "data:") {
		t.Fatalf("keepalive must not carry event/data lines, got %q", got)
	}
}

// TestSSEStream_EventFormat confirms event() still emits a well-formed SSE
// frame after the sseStream refactor (was the bare writeSSEEvent).
func TestSSEStream_EventFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &sseStream{w: rec, flusher: rec}

	s.event("text", map[string]string{"text": "hi"})

	got := rec.Body.String()
	if !strings.HasPrefix(got, "event: text\n") || !strings.Contains(got, `"text":"hi"`) || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("event() wire format = %q", got)
	}
}

// TestSSEStream_ConcurrentWritesDoNotInterleave runs event() and comment()
// from many goroutines at once — the exact race between the request goroutine
// and the heartbeat goroutine. With the mutex, every frame lands whole; run
// under `-race` this fails if the mutex is dropped. We assert no frame is torn
// (each written unit appears intact and the total count is exact).
func TestSSEStream_ConcurrentWritesDoNotInterleave(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &sseStream{w: rec, flusher: rec}

	const n = 200
	var wg sync.WaitGroup
	wg.Add(2 * n)
	for i := 0; i < n; i++ {
		go func() { defer wg.Done(); s.comment() }()
		go func() { defer wg.Done(); s.event("ping", nil) }()
	}
	wg.Wait()

	body := rec.Body.String()
	if gotC := strings.Count(body, ": keepalive\n\n"); gotC != n {
		t.Fatalf("keepalive frames = %d, want %d (torn writes?)", gotC, n)
	}
	if gotE := strings.Count(body, "event: ping\ndata: {}\n\n"); gotE != n {
		t.Fatalf("ping frames = %d, want %d (torn writes?)", gotE, n)
	}
}
