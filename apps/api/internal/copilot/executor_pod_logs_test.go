package copilot

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// Tests for the pure helpers behind the get_pod_logs container
// auto-selection added after the gitlab-webservice multi-container
// failure on yagan-eks-prod-v2. The full executor flow (which
// requires a live Connector) is exercised in vivo; here we lock
// down the deterministic pieces:
//   1. extractPodContainerNames — handles the GetResourceDetail
//      output shapes (multi, single, missing, malformed).
//   2. formatPodLogs — the new `extra` parameter merges into the
//      response without clobbering the truncation hint when both
//      apply.

func TestExtractPodContainerNames(t *testing.T) {
	cases := []struct {
		name   string
		detail map[string]interface{}
		want   []string
	}{
		{
			name: "multi-container pod (gitlab-webservice shape)",
			detail: map[string]interface{}{
				"containers": []map[string]interface{}{
					{"name": "certificates"},
					{"name": "configure"},
					{"name": "dependencies"},
					{"name": "webservice"},
					{"name": "gitlab-workhorse"},
				},
			},
			want: []string{"certificates", "configure", "dependencies", "webservice", "gitlab-workhorse"},
		},
		{
			name: "single-container pod",
			detail: map[string]interface{}{
				"containers": []map[string]interface{}{
					{"name": "app"},
				},
			},
			want: []string{"app"},
		},
		{
			name:   "detail missing containers field",
			detail: map[string]interface{}{"name": "x"},
			want:   nil,
		},
		{
			name: "container row missing name",
			detail: map[string]interface{}{
				"containers": []map[string]interface{}{
					{"name": "good"},
					{"image": "no-name-here"},
					{"name": ""},
					{"name": "also-good"},
				},
			},
			// Empty / missing names are filtered out so the
			// auto-selection picks a real container.
			want: []string{"good", "also-good"},
		},
		{
			name:   "nil detail",
			detail: nil,
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPodContainerNames(tc.detail)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("idx %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestFormatPodLogsMergesExtra(t *testing.T) {
	t.Run("extra fields merged into response", func(t *testing.T) {
		raw := "line1\nline2\nline3\n"
		extra := map[string]any{
			"containerSelected":     "webservice",
			"containerAutoSelected": true,
			"availableContainers":   []string{"certificates", "webservice"},
			"hint":                  "auto-selected; can re-call with another container",
		}
		out := formatPodLogs(raw, "", extra)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("response is not JSON: %v\n%s", err, out)
		}
		if decoded["containerSelected"] != "webservice" {
			t.Errorf("containerSelected missing or wrong: %v", decoded["containerSelected"])
		}
		if decoded["containerAutoSelected"] != true {
			t.Errorf("containerAutoSelected flag missing: %v", decoded["containerAutoSelected"])
		}
		if decoded["hint"] == nil {
			t.Errorf("hint missing")
		}
		// Standard log fields still present.
		if _, ok := decoded["logs"]; !ok {
			t.Errorf("logs field missing from response: %v", decoded)
		}
	})

	t.Run("truncation hint wins when both apply", func(t *testing.T) {
		// 60KB of logs → truncated. Pass extra with its own "hint";
		// the truncation-hint should remain because operators care
		// more about "your data is incomplete" than "I picked a
		// container".
		// Each line differs (its number), or repeated lines would fold
		// under the cap (log_repeats.go).
		var b strings.Builder
		for i := 0; i < 5000; i++ {
			b.WriteString("a much longer log line " + strconv.Itoa(i) + " so we actually exceed maxLogBytes\n")
		}
		extra := map[string]any{
			"containerSelected": "webservice",
			"hint":              "i should NOT appear because truncation hint wins",
		}
		out := formatPodLogs(b.String(), "", extra)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		// Truncation set its own hint first; merge logic should NOT
		// overwrite it with the caller's hint.
		hint, _ := decoded["hint"].(string)
		if !strings.Contains(hint, "truncated") {
			t.Errorf("truncation hint got overwritten by extra hint: %q", hint)
		}
		// But other caller fields still came through.
		if decoded["containerSelected"] != "webservice" {
			t.Errorf("containerSelected missing despite hint conflict: %v", decoded["containerSelected"])
		}
	})

	t.Run("nil extra is safe (existing callers unaffected)", func(t *testing.T) {
		out := formatPodLogs("line1\n", "", nil)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		if _, ok := decoded["logs"]; !ok {
			t.Errorf("expected logs field with nil extra")
		}
	})
}

func TestLogWindowMeta(t *testing.T) {
	if m := logWindowMeta(cluster.LogWindow{}, time.Time{}, time.Time{}); m != nil {
		t.Errorf("an open read carries no window meta, got %v", m)
	}
	cut := logWindowMeta(cluster.LogWindow{Bounded: true, Lines: 543, Dropped: 343, PastEnd: true}, time.Time{}, time.Time{})
	if cut["windowLines"] != 543 || cut["windowLinesDropped"] != 343 || !strings.Contains(cut["windowNote"].(string), "newest 200") {
		t.Errorf("a cut window must say so: %v", cut)
	}
	quiet := logWindowMeta(cluster.LogWindow{Bounded: true, PastEnd: true}, time.Time{}, time.Time{})
	if !strings.Contains(quiet["windowNote"].(string), "nothing was logged") {
		t.Errorf("an empty window before later lines: %v", quiet)
	}
	gone := logWindowMeta(cluster.LogWindow{Bounded: true}, time.Time{}, time.Time{})
	if !strings.Contains(gone["windowNote"].(string), "rotated") {
		t.Errorf("an empty log: %v", gone)
	}
	// The note travels next to the logs; formatPodLogs keeps it.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(formatPodLogs("", "", cut)), &decoded); err != nil || decoded["windowNote"] == nil {
		t.Errorf("windowNote lost in formatPodLogs: %v (%v)", decoded, err)
	}
}

func TestLogReadTail(t *testing.T) {
	if got := logReadTail(200, true, "error|5xx"); got != 0 {
		t.Errorf("window + grep must read the whole window, got tail %d", got)
	}
	if got := logReadTail(200, true, ""); got != 200 {
		t.Errorf("window without grep keeps the tail, got %d", got)
	}
	if got := logReadTail(200, false, "error"); got != 200 {
		t.Errorf("grep without a window keeps the tail (else the read starts at the container's first line), got %d", got)
	}
}

func TestLogTailNote(t *testing.T) {
	full := strings.Repeat("x\n", 200)
	if n := logTailNote(full, 200, false, false, "error"); !strings.Contains(n, "grep ran over only the newest 200") {
		t.Errorf("a filled tail with grep must say grep saw only it: %q", n)
	}
	if n := logTailNote(full, 200, false, false, ""); !strings.Contains(n, "only the newest 200") {
		t.Errorf("a filled tail must say so: %q", n)
	}
	if n := logTailNote(strings.Repeat("x\n", 37), 200, false, false, ""); n != "" {
		t.Errorf("a tail that was not filled read everything: %q", n)
	}
	if n := logTailNote(full, 200, true, false, ""); n != "" {
		t.Errorf("a closed window reports through windowNote, not tailNote: %q", n)
	}
	if n := logTailNote(full, 0, false, false, ""); n != "" {
		t.Errorf("no tail, no note: %q", n)
	}
}

func TestLogTailNoteWindowedOpenRead(t *testing.T) {
	// since without grep keeps the tail: the newest lines of the period.
	n := logTailNote(strings.Repeat("x\n", 200), 200, false, true, "")
	if !strings.Contains(n, "newest 200 lines of the period") || !strings.Contains(n, "add grep") {
		t.Errorf("a filled tail of a since read must say it is the newest part and point at grep: %q", n)
	}
}

func TestLogWindowError(t *testing.T) {
	end := time.Date(2026, 10, 9, 23, 12, 0, 0, time.UTC)
	if msg := logWindowError(cluster.LogQuery{EndTime: end}); !strings.Contains(msg, "needs sinceTime or since") {
		t.Errorf("endTime alone reads from the container's first line and must be refused: %q", msg)
	}
	if msg := logWindowError(cluster.LogQuery{SinceTime: end, EndTime: end}); !strings.Contains(msg, "not before endTime") {
		t.Errorf("an empty window must be refused: %q", msg)
	}
	for _, ok := range []cluster.LogQuery{
		{},
		{SinceSeconds: 3600},
		{SinceTime: end.Add(-time.Hour), EndTime: end},
		{SinceSeconds: 3600, EndTime: end},
	} {
		if msg := logWindowError(ok); msg != "" {
			t.Errorf("%+v refused: %q", ok, msg)
		}
	}
}

func TestContainerStartedAt(t *testing.T) {
	detail := map[string]interface{}{"containers": []map[string]interface{}{
		{"name": "sidecar", "state": map[string]interface{}{"state": "running", "startedAt": "2026-10-09T20:00:00Z"}},
		{"name": "app", "state": map[string]interface{}{"state": "running", "startedAt": "2026-10-09T23:06:00Z"}},
		{"name": "waiting", "state": map[string]interface{}{"state": "waiting", "reason": "CrashLoopBackOff"}},
	}}
	if got := containerStartedAt(detail, "app"); !got.Equal(time.Date(2026, 10, 9, 23, 6, 0, 0, time.UTC)) {
		t.Errorf("app started at %v", got)
	}
	for _, c := range []string{"waiting", "missing"} {
		if got := containerStartedAt(detail, c); !got.IsZero() {
			t.Errorf("%s has no start, got %v", c, got)
		}
	}
	if got := containerStartedAt(nil, "app"); !got.IsZero() {
		t.Errorf("no detail, no start: %v", got)
	}
}

func TestLogPeriodStart(t *testing.T) {
	now := time.Date(2026, 10, 9, 23, 30, 0, 0, time.UTC)
	since := time.Date(2026, 10, 9, 23, 3, 0, 0, time.UTC)
	if got := logPeriodStart(cluster.LogQuery{SinceTime: since, SinceSeconds: 60}, now); !got.Equal(since) {
		t.Errorf("sinceTime wins: %v", got)
	}
	if got := logPeriodStart(cluster.LogQuery{SinceSeconds: 3600}, now); !got.Equal(now.Add(-time.Hour)) {
		t.Errorf("since 1h: %v", got)
	}
	if got := logPeriodStart(cluster.LogQuery{}, now); !got.IsZero() {
		t.Errorf("a plain tail has no period: %v", got)
	}
}

// The incident in a restarted container: OOM at 23:05, the container back at
// 23:06, a window of 23:00–23:10 asked at 23:30.
func TestLogWindowMetaRestart(t *testing.T) {
	from := time.Date(2026, 10, 9, 23, 0, 0, 0, time.UTC)
	before := from.Add(-time.Hour)
	restart := time.Date(2026, 10, 9, 23, 6, 0, 0, time.UTC)

	t.Run("container started after the window began and logged nothing in it", func(t *testing.T) {
		m := logWindowMeta(cluster.LogWindow{Bounded: true, PastEnd: true}, from, from.Add(15*time.Minute))
		note, _ := m["windowNote"].(string)
		if !strings.Contains(note, "previous=true") || strings.Contains(note, "nothing was logged") {
			t.Errorf("a window before the container started is in the previous instance, not silent: %v", m)
		}
		if m["containerNote"] != nil {
			t.Errorf("one note is enough: %v", m)
		}
	})
	t.Run("restart inside the window", func(t *testing.T) {
		m := logWindowMeta(cluster.LogWindow{Bounded: true, Lines: 240, PastEnd: true}, from, restart)
		if note, _ := m["containerNote"].(string); !strings.Contains(note, "2026-10-09T23:06:00Z") || !strings.Contains(note, "previous=true") {
			t.Errorf("lines before the restart are in the previous instance: %v", m)
		}
	})
	t.Run("restart inside an open since read", func(t *testing.T) {
		m := logWindowMeta(cluster.LogWindow{}, from, restart)
		if m["containerNote"] == nil {
			t.Errorf("since 1h over a container 24 minutes old must say where the rest is: %v", m)
		}
	})
	t.Run("container older than the window", func(t *testing.T) {
		m := logWindowMeta(cluster.LogWindow{Bounded: true, PastEnd: true}, from, before)
		if note, _ := m["windowNote"].(string); !strings.Contains(note, "nothing was logged") || m["containerNote"] != nil {
			t.Errorf("a container older than the window that logged nothing in it was silent: %v", m)
		}
	})
}

func TestLogWindowMetaStoppedReads(t *testing.T) {
	until := time.Date(2026, 10, 9, 23, 7, 30, 0, time.UTC)

	capped := logWindowMeta(cluster.LogWindow{Bounded: true, Lines: 900, Dropped: 700, Capped: true, ReadUntil: until}, time.Time{}, time.Time{})
	note, _ := capped["windowNote"].(string)
	if capped["readCapped"] != true || capped["readStoppedAt"] != "2026-10-09T23:07:30Z" ||
		!strings.Contains(note, "more than 10 MiB") || !strings.Contains(note, "sinceTime=2026-10-09T23:07:30Z") {
		t.Errorf("a capped window must say where it stopped and how to go on: %v", capped)
	}
	if strings.Contains(note, "the window held") {
		t.Errorf("a capped window did not hold only what was read: %q", note)
	}

	cut := logWindowMeta(cluster.LogWindow{Bounded: true, Lines: 40, Interrupted: "context deadline exceeded", ReadUntil: until}, time.Time{}, time.Time{})
	if cut["readInterrupted"] != "context deadline exceeded" || !strings.Contains(cut["windowNote"].(string), "the read was cut") {
		t.Errorf("an interrupted window: %v", cut)
	}

	open := logWindowMeta(cluster.LogWindow{Capped: true}, time.Time{}, time.Time{})
	if n, _ := open["readNote"].(string); !strings.Contains(n, "newest part is missing") {
		t.Errorf("an open read that hit the cap lost its newest part and must say so: %v", open)
	}
}

// The kubelet's stamp is not part of what the container wrote: "500" must not
// match the nanoseconds of "…:01.500123456Z".
func TestFormatPodLogsGrepSkipsTheStamp(t *testing.T) {
	raw := "2026-10-10T01:06:00.500123456Z GET / -> 200\n" +
		"2026-10-10T01:06:01.100000000Z GET / -> 503 db=unreachable\n" +
		"2026-10-10T01:06:05.000000000Z GET / -> 503 db=unreachable\n" +
		"2026-10-10T01:06:09.400000000Z GET / -> 500 query failed\n"
	var out map[string]any
	if err := json.Unmarshal([]byte(formatPodLogs(raw, "500|503", nil)), &out); err != nil {
		t.Fatal(err)
	}
	if out["returnedLines"] != float64(3) {
		t.Errorf("the 200 line matched through its stamp: %v", out)
	}
	if span, _ := out["matchSpan"].(string); span != "first match 2026-10-10T01:06:01Z, last 2026-10-10T01:06:09Z: 8s across 3 lines" {
		t.Errorf("matchSpan = %q", span)
	}
}

func TestLogMatchSpan(t *testing.T) {
	at := time.Date(2026, 10, 10, 1, 6, 1, 0, time.UTC)
	if s := logMatchSpan(time.Time{}, time.Time{}, 4); s != "" {
		t.Errorf("unstamped lines have no span: %q", s)
	}
	if s := logMatchSpan(at, at, 1); s != "one matching line, at 2026-10-10T01:06:01Z" {
		t.Errorf("one line: %q", s)
	}
	if s := logMatchSpan(at, at, 3); s != "3 matching lines, all at 2026-10-10T01:06:01Z" {
		t.Errorf("one instant: %q", s)
	}
	if s := logMatchSpan(at, at.Add(350*time.Millisecond), 2); !strings.Contains(s, "350ms across 2 lines") {
		t.Errorf("under a second keeps the milliseconds: %q", s)
	}
	// Without a stamp (a read without timestamps) grep still works, with no span.
	var out map[string]any
	_ = json.Unmarshal([]byte(formatPodLogs("GET / -> 503\nGET / -> 200\n", "503", nil)), &out)
	if out["returnedLines"] != float64(1) || out["matchSpan"] != nil {
		t.Errorf("unstamped grep: %v", out)
	}
}
