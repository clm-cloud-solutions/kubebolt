package api

import (
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
)

func newAccessSink(t *testing.T) *audit.MemoryStore {
	t.Helper()
	sink := audit.NewMemoryStore()
	audit.SetSink(sink, func() string { return "cluster-xyz" })
	t.Cleanup(func() { audit.SetSink(nil, nil) })
	return sink
}

var testActor = accessActor{UserID: "u1", Username: "alice", Role: "editor", TenantID: "org-a"}

func TestAuditAccessSession_EmitsOpenAndClose(t *testing.T) {
	sink := newAccessSink(t)

	end := auditAccessSession(testActor, "pod_exec_session", "pods", "prod", "api-0",
		map[string]any{"container": "api", "shell": "/bin/bash"})

	// The open record must exist BEFORE the close: a session that dies with the
	// process has to leave a trace, and an open with no close is that signal.
	recs, _ := sink.ListOrg("org-a", 0)
	if len(recs) != 1 || recs[0].Action != "pod_exec_session_open" {
		t.Fatalf("after open: %d records, first=%v", len(recs), actions(recs))
	}
	if recs[0].Class != audit.ClassAccess {
		t.Errorf("class = %q, want %q", recs[0].Class, audit.ClassAccess)
	}
	openID, _ := recs[0].Params["sessionId"].(string)
	if openID == "" {
		t.Fatal("open record carries no sessionId")
	}

	end("stream ended", map[string]any{"exitCode": 0})

	recs, _ = sink.ListOrg("org-a", 0)
	if len(recs) != 2 {
		t.Fatalf("after close: %d records, want 2 — %v", len(recs), actions(recs))
	}
	var closeRec *audit.Record
	for i := range recs {
		if recs[i].Action == "pod_exec_session_close" {
			closeRec = &recs[i]
		}
	}
	if closeRec == nil {
		t.Fatalf("no close record: %v", actions(recs))
	}
	// The pair has to be linkable, or two concurrent terminals on the same pod
	// cannot be told apart.
	if got, _ := closeRec.Params["sessionId"].(string); got != openID {
		t.Errorf("close sessionId = %q, want %q", got, openID)
	}
	if _, ok := closeRec.Params["durationMs"]; !ok {
		t.Error("close record has no durationMs")
	}
	if closeRec.Params["reason"] != "stream ended" {
		t.Errorf("reason = %v", closeRec.Params["reason"])
	}
}

// The closer is deferred AND called explicitly on the clean path, so it must be
// idempotent or every terminal would record two closes.
func TestAuditAccessSession_CloseIsIdempotent(t *testing.T) {
	sink := newAccessSink(t)
	end := auditAccessSession(testActor, "port_forward", "pods", "prod", "db-0", nil)
	end("stopped by user", nil)
	end("stopped by user", nil)

	recs, _ := sink.ListOrg("org-a", 0)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (one open, one close) — %v", len(recs), actions(recs))
	}
}

// A port-forward can be torn down by the HTTP delete handler and by its own
// forwarding goroutine at the same instant, so "call it twice" is not enough —
// the closer has to be safe under genuine concurrency. Run with -race.
func TestAuditAccessSession_CloseIsConcurrencySafe(t *testing.T) {
	sink := newAccessSink(t)
	end := auditAccessSession(testActor, "port_forward", "pods", "prod", "db-0", nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			end("torn down", nil)
		}()
	}
	wg.Wait()

	recs, _ := sink.ListOrg("org-a", 0)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want exactly 2 — %v", len(recs), actions(recs))
	}
}

func TestAuditAccess_RecordsPathNeverContent(t *testing.T) {
	sink := newAccessSink(t)

	auditAccess(testActor, "pod_file_read", "pods", "prod", "api-0", map[string]any{
		"container": "api",
		"path":      "/etc/secret.conf",
		"bytes":     412,
	}, nil)

	recs, _ := sink.ListOrg("org-a", 0)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	got := recs[0]
	if got.Class != audit.ClassAccess {
		t.Errorf("class = %q, want access", got.Class)
	}
	if got.Params["path"] != "/etc/secret.conf" || got.Params["bytes"] != 412 {
		t.Errorf("params lost the access metadata: %v", got.Params)
	}
	// Whatever the file held must not be anywhere in the record.
	for k := range got.Params {
		switch k {
		case "content", "body", "stdout", "data":
			t.Errorf("params carry file content under %q", k)
		}
	}
}

// A record written before the class field existed is a mutation, and must read
// as one rather than as an empty string the UI has to special-case.
func TestClassOf_DefaultsToMutation(t *testing.T) {
	if got := audit.ClassOf(&audit.Record{}); got != audit.ClassMutation {
		t.Errorf("ClassOf(zero) = %q, want %q", got, audit.ClassMutation)
	}
	if got := audit.ClassOf(&audit.Record{Class: audit.ClassAccess}); got != audit.ClassAccess {
		t.Errorf("ClassOf(access) = %q", got)
	}
}

func actions(recs []audit.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Action
	}
	return out
}

// A terminal the cluster refuses before its stream runs leaves ONE open record
// carrying the error — never an open that reads as success.
func TestAuditAccessSessionPending_RefusedBeforeStartIsOneFailedOpen(t *testing.T) {
	sink := newAccessSink(t)
	_, end := auditAccessSessionPending(testActor, "pod_exec_session", "pods", "prod", "api-0", map[string]any{"container": "api"})

	end("stream error", map[string]any{"error": `pods "api-0" not found`, "exitCode": 0})
	end("handler returned", nil) // the handler's backstop must not add a record

	recs, _ := sink.ListOrg("org-a", 0)
	if len(recs) != 1 {
		t.Fatalf("%d records, want 1: %v", len(recs), actions(recs))
	}
	r := recs[0]
	if r.Action != "pod_exec_session_open" || r.Result != "error" || r.Error != `pods "api-0" not found` {
		t.Errorf("got action=%q result=%q error=%q", r.Action, r.Result, r.Error)
	}
	if r.Params["sessionId"] == nil || r.Params["container"] != "api" {
		t.Errorf("params lost the session's description: %v", r.Params)
	}
}

func TestAuditAccessSessionPending_StartedSessionOpensAndCloses(t *testing.T) {
	sink := newAccessSink(t)
	start, end := auditAccessSessionPending(testActor, "pod_exec_session", "pods", "prod", "api-0", nil)

	start()
	start() // every read and write of the stream fires it
	if recs, _ := sink.ListOrg("org-a", 0); len(recs) != 1 || recs[0].Result != "success" {
		t.Fatalf("after start: %v", actions(recs))
	}
	end("shell exited", map[string]any{"exitCode": 0})
	start() // a late copy loop after the end records nothing

	recs, _ := sink.ListOrg("org-a", 0)
	if len(recs) != 2 {
		t.Fatalf("%d records, want open + close: %v", len(recs), actions(recs))
	}
}

// The stream fires start from its own goroutines while the handler may be
// ending the session: one open and at most one close, whatever the order.
func TestAuditAccessSessionPending_ConcurrentStartAndEnd(t *testing.T) {
	sink := newAccessSink(t)
	start, end := auditAccessSessionPending(testActor, "pod_exec_session", "pods", "prod", "api-0", nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); start() }()
		go func() { defer wg.Done(); end("stream ended", nil) }()
	}
	wg.Wait()
	recs, _ := sink.ListOrg("org-a", 0)
	opens := 0
	for _, r := range recs {
		if r.Action == "pod_exec_session_open" {
			opens++
		}
	}
	if opens != 1 || len(recs) > 2 {
		t.Errorf("records %v: want exactly one open and at most one close", actions(recs))
	}
}

func TestStreamStartWrappers_FireOnFirstTouch(t *testing.T) {
	n := 0
	fire := func() { n++ }
	_, _ = streamStartReader{r: strings.NewReader("x"), start: fire}.Read(make([]byte, 1))
	_, _ = streamStartWriter{w: io.Discard, start: fire}.Write([]byte("x"))
	if n != 2 {
		t.Errorf("start fired %d times, want 2", n)
	}
}
