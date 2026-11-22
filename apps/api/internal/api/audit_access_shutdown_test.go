package api

import "testing"

// A terminal or a port-forward still open when the API stops is a hijacked or
// background connection server.Shutdown does not wait for: the process took it
// down and the trail kept an open with no close. CloseOpenAccessSessions records
// those closes on shutdown.

func TestCloseOpenAccessSessions_RecordsEachOpenSessionOnce(t *testing.T) {
	sink := newAccessSink(t)

	endPF := auditAccessSession(testActor, "port_forward", "pods", "prod", "db-0", nil)
	startExec, endExec := auditAccessSessionPending(testActor, "pod_exec_session", "pods", "prod", "api-0", nil)
	startExec()
	// A terminal the cluster refused never started: it already left its own
	// error record and is not an open session.
	_, endRefused := auditAccessSessionPending(testActor, "pod_exec_session", "pods", "prod", "gone-0", nil)
	endRefused("pod not found", nil)
	// One that closed normally before the shutdown.
	endDone := auditAccessSession(testActor, "port_forward", "pods", "prod", "cache-0", nil)
	endDone("stopped by user", nil)

	if n := CloseOpenAccessSessions("server shutdown"); n != 2 {
		t.Fatalf("closed %d sessions, want 2 (the port-forward and the terminal still open)", n)
	}
	// Their own goroutines end them a moment later: recorded once, not twice.
	endPF("stream ended", nil)
	endExec("stream ended", nil)
	if n := CloseOpenAccessSessions("server shutdown"); n != 0 {
		t.Errorf("a second pass closed %d sessions, want 0", n)
	}

	recs, _ := sink.ListOrg("org-a", 0)
	closes := map[string]string{}
	for _, r := range recs {
		if r.Action == "port_forward_close" || r.Action == "pod_exec_session_close" {
			reason, _ := r.Params["reason"].(string)
			if prev, dup := closes[r.TargetName]; dup {
				t.Errorf("%s closed twice (%q, then %q)", r.TargetName, prev, reason)
			}
			closes[r.TargetName] = reason
		}
	}
	for name, want := range map[string]string{"db-0": "server shutdown", "api-0": "server shutdown", "cache-0": "stopped by user"} {
		if got := closes[name]; got != want {
			t.Errorf("%s close reason = %q, want %q", name, got, want)
		}
	}
	if _, ok := closes["gone-0"]; ok {
		t.Error("a session the cluster refused got a close record")
	}
}
