package copilot

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// A failing probe or a retry loop writes the same line every few seconds, and
// get_pod_logs returned every copy: a 15-minute grep of orders-api was 185
// lines of «readiness failed: cannot reach database …», 35 KB of the 37 KB
// answer, twice in one turn (route53-record-drift, 2026-10-22). The answer
// now shows such a line once, the most recent, with how many it stands for
// and when the first was written.
//
// Only lines identical but for their time fold: the kubelet's stamp and the
// timestamps the container writes itself. A line with any other difference —
// a count, an id, a latency — stays as it is. A repetition ends at a gap over
// burstGap, as a burst does, so one line never stands for two separate
// episodes. The fold runs before the byte cap, so a count and a first copy
// describe the whole read; the bursts are built from every line, unfolded.

// repeatMin is how many copies a line needs before they fold.
const repeatMin = 3

// logTimeRE matches the timestamps a container writes into its lines: ISO
// 8601 date-times, times of day (klog, syslog, nginx), and the time fields of
// JSON logs.
var logTimeRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?|\b\d{1,2}:\d{2}:\d{2}(?:[.,]\d+)?\b|"(?:ts|time|timestamp|@timestamp)"\s*:\s*(?:"[^"]*"|-?\d+(?:\.\d+)?)`)

const repeatsNote = "Lines identical but for their time, repeated with no gap over a minute, are shown once: the most recent copy, followed by [×N since <first>] or [×N in a row]. " +
	"N counts every copy the read returned; bursts and matchSpan are built from all of them."

type logRepeat struct {
	count       int
	last        int
	first, seen time.Time
	stamped     bool
}

// foldRepeatedLines folds the repeated lines of logs and returns the result
// and how many lines the fold took out. keep, when set, leaves the lines whose
// message it admits as they are: the success lines thinSuccessLines already
// cut to the most recent few, whose copies a count would no longer describe.
func foldRepeatedLines(logs string, keep func(msg string) bool) (string, int) {
	if logs == "" {
		return logs, 0
	}
	lines := strings.Split(logs, "\n")
	of := make([]*logRepeat, len(lines))
	open := map[string]*logRepeat{}
	folds := false
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ts, msg, stamped := cluster.SplitLogTimestamp(line)
		if keep != nil && keep(msg) {
			continue
		}
		key := strings.TrimSpace(logTimeRE.ReplaceAllString(msg, ""))
		r := open[key]
		continues := r != nil && r.stamped == stamped &&
			((stamped && ts.Sub(r.seen) <= burstGap) || (!stamped && r.last == i-1))
		if !continues {
			r = &logRepeat{first: ts, stamped: stamped}
			open[key] = r
		}
		r.count++
		r.last, r.seen = i, ts
		of[i] = r
		folds = folds || r.count >= repeatMin
	}
	if !folds {
		return logs, 0
	}
	kept := make([]string, 0, len(lines))
	folded := 0
	for i, line := range lines {
		r := of[i]
		switch {
		case r == nil || r.count < repeatMin:
			kept = append(kept, line)
		case i == r.last:
			kept = append(kept, line+" "+r.mark())
		default:
			folded++
		}
	}
	return strings.Join(kept, "\n"), folded
}

// setRepeats says in payload how many lines the fold took out, or nothing
// when it took none.
func setRepeats(payload map[string]any, folded int) {
	if folded == 0 {
		delete(payload, "repeats")
		return
	}
	payload["repeats"] = map[string]any{"foldedLines": folded, "note": repeatsNote}
}

func (r *logRepeat) mark() string {
	if r.stamped {
		return fmt.Sprintf("[×%d since %s]", r.count, r.first.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("[×%d in a row]", r.count)
}
