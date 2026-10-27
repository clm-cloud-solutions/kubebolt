package copilot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// stampedLine is a line as get_pod_logs reads it with timestamps: the
// kubelet's stamp, then what the container wrote, its own time included.
func stampedLine(t time.Time, msg string) string {
	return t.Format(time.RFC3339Nano) + " " + t.Format("2006-01-02T15:04:05") + " " + msg
}

const readinessLine = "[orders-api] WARN readiness failed: cannot reach database shop-db.shop-data.svc.cluster.local:5432 ([Errno -5] Name has no usable address)"

// The read of the EKS lab's route53 round: a probe failing every 5 s for 15
// minutes, 185 copies of one line.
func TestFormatPodLogs_FoldsAProbeRepeatingTheSameLine(t *testing.T) {
	start := time.Date(2026, 10, 22, 12, 19, 59, 128407908, time.UTC)
	var lines []string
	for i := 0; i < 185; i++ {
		lines = append(lines, stampedLine(start.Add(time.Duration(i)*5*time.Second), readinessLine))
	}
	raw := strings.Join(lines, "\n") + "\n"

	var got map[string]any
	if err := json.Unmarshal([]byte(formatPodLogs(raw, "error|fail|db", nil)), &got); err != nil {
		t.Fatal(err)
	}
	logs, _ := got["logs"].(string)
	if strings.Count(logs, "\n") != 0 || !strings.HasSuffix(logs, readinessLine+" [×185 since 2026-10-22T12:19:59Z]") {
		t.Fatalf("logs = %q", logs)
	}
	if !strings.HasPrefix(logs, start.Add(184*5*time.Second).Format(time.RFC3339Nano)) {
		t.Errorf("the line kept must be the most recent copy: %q", logs)
	}
	if len(logs) > 400 {
		t.Errorf("folded logs are %d bytes", len(logs))
	}
	rep, _ := got["repeats"].(map[string]any)
	if rep["foldedLines"] != float64(184) || rep["note"] != repeatsNote {
		t.Errorf("repeats = %v", rep)
	}
	// The bursts still count every copy.
	items := got["bursts"].(map[string]any)["items"].([]any)
	if n := items[0].(map[string]any)["lines"]; len(items) != 1 || n != float64(185) {
		t.Errorf("bursts = %v", items)
	}
}

func TestFoldRepeats_LeavesLinesThatDifferInAnythingButTheTime(t *testing.T) {
	start := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	var lines []string
	for i := 0; i < 6; i++ {
		lines = append(lines, stampedLine(start.Add(time.Duration(i)*time.Second), fmt.Sprintf("GET /orders -> 200 in %dms", 500+i)))
	}
	if got, n := foldRepeatedLines(strings.Join(lines, "\n"), nil); got != strings.Join(lines, "\n") || n != 0 {
		t.Fatalf("lines with different latencies folded: %q", got)
	}

	// Two copies are not a repetition worth folding.
	two := strings.Join(lines[:1], "\n") + "\n" + stampedLine(start.Add(time.Second), "GET /orders -> 200 in 500ms")
	if got, n := foldRepeatedLines(two, nil); got != two || n != 0 {
		t.Errorf("two copies folded: %q", got)
	}
}

// One line never stands for two episodes: a gap over a minute ends the
// repetition, as it ends a burst.
func TestFoldRepeats_AGapStartsANewRepetition(t *testing.T) {
	start := time.Date(2026, 10, 22, 11, 15, 50, 0, time.UTC)
	var lines []string
	for i := 0; i < 3; i++ {
		lines = append(lines, stampedLine(start.Add(time.Duration(i)*5*time.Second), readinessLine))
	}
	later := start.Add(71 * time.Minute)
	for i := 0; i < 4; i++ {
		lines = append(lines, stampedLine(later.Add(time.Duration(i)*5*time.Second), readinessLine))
	}
	folded, _ := foldRepeatedLines(strings.Join(lines, "\n"), nil)
	got := strings.Split(folded, "\n")
	if len(got) != 2 ||
		!strings.HasSuffix(got[0], "[×3 since 2026-10-22T11:15:50Z]") ||
		!strings.HasSuffix(got[1], "[×4 since 2026-10-22T12:26:50Z]") {
		t.Fatalf("logs = %q", got)
	}
}

// Two failures interleaved fold separately, each where its last copy was, so
// the logs still end with what the container wrote last.
func TestFoldRepeats_InterleavedLinesFoldSeparately(t *testing.T) {
	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var lines []string
	for i := 0; i < 4; i++ {
		at := start.Add(time.Duration(i) * 10 * time.Second)
		lines = append(lines, stampedLine(at, "ERROR dial tcp 10.0.1.5:5432: connection refused"))
		lines = append(lines, stampedLine(at.Add(time.Second), "WARN retrying in 5s"))
	}
	folded, _ := foldRepeatedLines(strings.Join(lines, "\n"), nil)
	got := strings.Split(folded, "\n")
	if len(got) != 2 || !strings.Contains(got[0], "connection refused [×4 since") || !strings.Contains(got[1], "retrying in 5s [×4 since") {
		t.Fatalf("logs = %q", got)
	}
}

// A plain tail has no kubelet stamps: only copies in a row fold.
func TestFoldRepeats_UnstampedLinesFoldOnlyInARow(t *testing.T) {
	lines := []string{
		"I1022 12:00:01.000001       1 reflector.go:561] watch of *v1.Pod ended",
		"I1022 12:00:06.000001       1 reflector.go:561] watch of *v1.Pod ended",
		"I1022 12:00:11.000001       1 reflector.go:561] watch of *v1.Pod ended",
		`{"level":"info","ts":1729598411.5,"msg":"synced"}`,
		"I1022 12:00:16.000001       1 reflector.go:561] watch of *v1.Pod ended",
	}
	folded, _ := foldRepeatedLines(strings.Join(lines, "\n"), nil)
	got := strings.Split(folded, "\n")
	if len(got) != 3 || got[0] != lines[2]+" [×3 in a row]" || got[1] != lines[3] || got[2] != lines[4] {
		t.Fatalf("logs = %q", got)
	}
}

// The fold runs before the byte cap: a repetition longer than the cap still
// counts every copy and keeps the first, instead of starting where the cut
// fell.
func TestFormatPodLogs_FoldsBeforeTheCap(t *testing.T) {
	start := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, stampedLine(start.Add(time.Duration(i)*5*time.Second), readinessLine))
	}
	raw := strings.Join(lines, "\n")
	if len(raw) <= maxLogBytes {
		t.Fatalf("the fixture must pass the byte cap: %d bytes", len(raw))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(formatPodLogs(raw, "readiness", nil)), &got); err != nil {
		t.Fatal(err)
	}
	if got["truncated"] != nil {
		t.Errorf("folded logs were still cut: %v", got["bytesDropped"])
	}
	if logs, _ := got["logs"].(string); !strings.HasSuffix(logs, "[×1000 since 2026-10-22T09:00:00Z]") {
		t.Errorf("logs = %q", logs)
	}
}
