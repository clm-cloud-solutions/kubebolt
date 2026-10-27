package copilot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// How long an outage lasted is read from the logs of what served it, and the
// models kept giving a different number: the duration of the operation that
// caused it (an Azure PostgreSQL restart's 61 s for 12 s of errors), the time
// they checked, or none at all when they read logs without a grep, which is
// when matchSpan is absent (Haiku 7/12 on the lab). A
// skill with the method (incident-timeline) did not help: Haiku never loaded
// it. So get_pod_logs hands the answer over: the bursts of error lines — or of
// the grep's matches — with when each starts and ends.
//
// The end of a burst was also read as the cause going away on its own: after
// an operator restarted shop-db by hand, the options read «No hacer nada. El
// servicio se recuperó solo». The note says what the
// end of a burst does and does not show.
//
// The start of a burst was also read where the read began: a one-hour window
// opened inside an outage gave «17:10:14» for one that started at 16:50:14,
// and a grep answer cut at the head gave «229 errors from 16:50 to 17:23» for
// the 229 lines left after 17:14 (db-statement-timeout re-test, 2026-10-12).
// So get_pod_logs builds the bursts from every line the read kept, before the
// answer's byte cap, and the oldest burst says so (cutAtStart) when it begins
// at the edge of what was read: the period's start (readFrom), or older lines
// the read never reached. A first try pointed the model at matchSpan for the
// start instead; its first match was a stray line eight minutes before the
// outage, and Kobi gave that as when it began.
//
// A grep for status codes also matches numbers that are not one: «502|503|504»
// matched a 200 that took 504ms, minutes after the outage, and matchSpan and a
// third one-line burst carried it; Kobi gave it as when the store came back,
// 7 min for an outage of 35 s (node-image-upgrade, 2026-10-20). A matching line
// that reports success (an HTTP 2xx or 3xx status, no error level) is not the
// symptom: when other matches are, it is left out of the bursts and of
// matchSpan, and the answer says how many and shows one.
//
// Such lines also filled the answer itself: «catalog|orders» matched every
// «200 orders=up catalog=up» of shop-web, 54 KB of requests served, and the
// read went into Sonnet's cache with the rest of the turn (alb-targets-unhealthy,
// 2026-10-22). So the logs keep the most recent few, to show what is served
// now, and say how many were left out.

const (
	// burstGap splits two bursts: a minute with no matching line.
	burstGap = time.Minute
	// maxBursts keeps the most recent ones; older are counted.
	maxBursts = 5
	// successSample is how many success lines a grep's logs keep when other
	// matches are the symptom: the most recent ones.
	successSample = 3
)

// errorLevelRE recognises a line its writer marked as an error or warning:
// a level word, a structured level field, klog's E/W prefix, or an HTTP 5xx
// in an access line.
var errorLevelRE = regexp.MustCompile(`(?i)(\b(error|fatal|panic|critical|warn|warning|exception)\b|\blevel=(error|warn|warning|fatal)\b|"level"\s*:\s*"(error|warn|warning|fatal)"|^[EW]\d{4} |(->|status[=:]?|HTTP/\d(\.\d)?"?)\s*5\d\d\b)`)

// successStatusRE recognises a line that reports a request served: an HTTP
// 2xx or 3xx where an access line writes its status (the 5xx forms of
// errorLevelRE, and JSON's "status":200).
var successStatusRE = regexp.MustCompile(`(?i)(->|\bstatus"?\s*[=:]?|HTTP/\d(\.\d)?"?)\s*[23]\d\d\b`)

// successLine reports whether msg says a request succeeded and nothing in it
// is marked as an error.
func successLine(msg string) bool {
	return successStatusRE.MatchString(msg) && !errorLevelRE.MatchString(msg)
}

const successMatchesNote = "These lines matched the grep but report success (an HTTP 2xx or 3xx status), so the pattern matched something else in them, such as a latency (504ms) or an id. " +
	"They are left out of bursts and matchSpan: never read one as part of the outage or as when it ended. " +
	"To match only failing requests, anchor the code where the line writes its status (\"-> 50[0-9]\", \"status=5\")."

// successThinnedNote goes with logs that kept only the most recent success lines.
const successThinnedNote = "Past the most recent few, they are also left out of logs (leftOutOfLogs says how many): the logs hold the other matches."

const burstsNote = "How long the symptom lasted in THIS log: each burst runs from its first line to its last, with no gap over a minute. " +
	"Give the outage as the burst at the time the operator asked about (convert to their time zone). " +
	"The operation that caused it (a restart, a rollout, a cloud maintenance) has its own duration, usually longer; it is not the outage. " +
	"Where a burst ends says when the symptom stopped, not what stopped it: a dependency outside the cluster may have been restarted by someone, so say it came back, not that it recovered by itself. " +
	"Absence of lines is evidence only for this pod and this window. " +
	"A burst with cutAtStart began before the first line read: its from is where this read begins, not when it started, and its lines are not all of it. " +
	"Read from earlier to find when it started; never give its from as the start."

// AnnotateLogBursts adds "bursts" to a get_pod_logs result. A line is placed
// by the kubelet's stamp, or by a leading timestamp that carries its zone;
// one without a zone is skipped, never assumed UTC.
func AnnotateLogBursts(content string) string { return annotateLogBursts(content, false) }

// AnnotateRecordedLogBursts is AnnotateLogBursts for the eval harness, whose
// recorded lab reads predate the stamps on windowed reads: the lab's apps
// write their own timestamps in UTC without a zone, so those count as UTC.
func AnnotateRecordedLogBursts(content string) string { return annotateLogBursts(content, true) }

func annotateLogBursts(content string, zonelessUTC bool) string {
	var payload map[string]any
	if json.Unmarshal([]byte(content), &payload) != nil || payload == nil {
		return content
	}
	if b, done := payload["bursts"].(map[string]any); done {
		// Recorded by an older build: keep its bursts, give it today's note.
		if b["note"] == burstsNote {
			return content
		}
		b["note"] = burstsNote
		return jsonString(payload)
	}
	logs, _ := payload["logs"].(string)
	if logs == "" {
		return content
	}
	// A recording holds only the lines the answer returned: the byte cap cut
	// the rest, so a burst at its head began earlier.
	if !withBursts(payload, logs, zonelessUTC, true) {
		return content
	}
	return jsonString(payload)
}

// podLogsWithBursts adds the bursts to a get_pod_logs answer, built from all,
// every line the read kept before the answer's byte cap, and the AWS
// authorization errors among them (log_aws_denials.go).
func podLogsWithBursts(payload map[string]any, all string) string {
	withBursts(payload, all, false, false)
	withAWSDenials(payload, all)
	return jsonString(payload)
}

// withBursts puts the bursts of logs into payload and reports whether there
// were any. capCut: logs is what was left after the byte cap (payload
// "truncated"), so its head is an edge.
func withBursts(payload map[string]any, logs string, zonelessUTC, capCut bool) bool {
	_, grep := payload["grep"]
	basis := "error- and warning-level lines"
	keep := errorLevelRE.MatchString
	if grep {
		basis = "the grep's matching lines"
		keep = nil
		if n, example, first, last, rest := successMatches(logs, zonelessUTC); n > 0 && rest > 0 {
			keep = func(msg string) bool { return !successLine(msg) }
			basis = "the grep's matching lines, without those that report success"
			sm := map[string]any{"lines": n, "example": example, "note": successMatchesNote}
			if left := thinSuccessLines(payload, logs, zonelessUTC, capCut); left > 0 {
				sm["leftOutOfLogs"] = left
				sm["note"] = successMatchesNote + " " + successThinnedNote
			}
			payload["successMatches"] = sm
			if _, ok := payload["matchSpan"]; ok {
				if span := logMatchSpan(first, last, rest); span != "" {
					payload["matchSpan"] = span
				} else {
					delete(payload, "matchSpan")
				}
			}
		}
	}
	bursts, older, firstLine := logBursts(logs, keep, zonelessUTC)
	if len(bursts) == 0 {
		return false
	}
	if older == 0 {
		if why := cutAtStart(payload, bursts[0], firstLine, capCut); why != "" {
			bursts[0]["cutAtStart"] = why
		}
	}
	b := map[string]any{"basis": basis, "items": bursts, "note": burstsNote}
	if older > 0 {
		b["olderBursts"] = older
	}
	payload["bursts"] = b
	return true
}

type logBurst struct {
	from, to time.Time
	lines    int
	first    string
}

// successMatches counts the lines of a grep's answer that report success,
// keeps the first as an example, and returns the first and last stamps of
// the others and how many they are.
func successMatches(logs string, zonelessUTC bool) (n int, example string, first, last time.Time, rest int) {
	for _, line := range strings.Split(logs, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ts, msg, stamped := logLine(line, zonelessUTC)
		if !stamped {
			msg = line
		}
		if successLine(msg) {
			if n == 0 {
				example = clipLine(msg)
			}
			n++
			continue
		}
		rest++
		if stamped {
			if first.IsZero() {
				first = ts
			}
			last = ts
		}
	}
	return n, example, first, last, rest
}

// thinSuccessLines sets payload's logs to logs without its success lines but
// the last successSample, and returns how many it left out. A live read
// passes every line it kept, so the byte cap is applied again to what is left
// and may no longer cut anything; a recording (recorded) was already cut, and
// its truncation stays as it was.
func thinSuccessLines(payload map[string]any, logs string, zonelessUTC, recorded bool) int {
	lines := strings.Split(logs, "\n")
	var success []int
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		_, msg, stamped := logLine(line, zonelessUTC)
		if !stamped {
			msg = line
		}
		if successLine(msg) {
			success = append(success, i)
		}
	}
	if len(success) <= successSample {
		return 0
	}
	left := len(success) - successSample
	drop := make(map[int]bool, left)
	for _, i := range success[:left] {
		drop[i] = true
	}
	kept := make([]string, 0, len(lines)-left)
	for i, line := range lines {
		if !drop[i] && strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	body := strings.Join(kept, "\n")
	if !recorded {
		// The other matches fold before the cap, as in formatPodLogs; the
		// success lines kept are already the most recent few.
		var folded int
		body, folded = foldRepeatedLines(body, successLine)
		setRepeats(payload, folded)
		if len(body) > maxLogBytes {
			head := len(body) - maxLogBytes
			if nl := strings.IndexByte(body[head:], '\n'); nl >= 0 {
				head += nl + 1
			}
			body = body[head:]
			payload["truncated"] = true
			payload["bytesDropped"] = head
		} else if payload["truncated"] == true {
			delete(payload, "truncated")
			delete(payload, "bytesDropped")
			if h, _ := payload["hint"].(string); strings.HasPrefix(h, "logs truncated") {
				delete(payload, "hint")
			}
		}
	}
	payload["logs"] = body
	returned := 0
	if body != "" {
		returned = strings.Count(body, "\n") + 1
	}
	payload["returnedLines"] = returned
	return left
}

// logLine splits a line into its stamp and message: the kubelet's stamp, or
// with zonelessUTC a leading zoneless timestamp read as UTC.
func logLine(line string, zonelessUTC bool) (time.Time, string, bool) {
	ts, msg, stamped := cluster.SplitLogTimestamp(line)
	if !stamped && zonelessUTC {
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			if t, err := time.ParseInLocation("2006-01-02T15:04:05", line[:sp], time.UTC); err == nil {
				return t, line[sp+1:], true
			}
		}
	}
	return ts, msg, stamped
}

// logBursts groups the stamped lines keep admits (all of them when keep is
// nil) into bursts and returns the most recent maxBursts, oldest first, how
// many older ones it left out, and the stamp of the first line of any kind.
// Lines arrive in log order; a kubelet stamp is required to place a line.
func logBursts(logs string, keep func(msg string) bool, zonelessUTC bool) ([]map[string]any, int, time.Time) {
	var all []logBurst
	var firstLine time.Time
	for _, line := range strings.Split(logs, "\n") {
		ts, msg, stamped := logLine(line, zonelessUTC)
		if !stamped || strings.TrimSpace(msg) == "" {
			continue
		}
		if firstLine.IsZero() {
			firstLine = ts
		}
		if keep != nil && !keep(msg) {
			continue
		}
		if n := len(all); n > 0 && ts.Sub(all[n-1].to) <= burstGap && !ts.Before(all[n-1].from) {
			all[n-1].to = ts
			all[n-1].lines++
			continue
		}
		all = append(all, logBurst{from: ts, to: ts, lines: 1, first: clipLine(msg)})
	}
	older := 0
	if len(all) > maxBursts {
		older = len(all) - maxBursts
		all = all[older:]
	}
	out := make([]map[string]any, 0, len(all))
	for _, b := range all {
		out = append(out, map[string]any{
			"from":      b.from.UTC().Format(time.RFC3339),
			"to":        b.to.UTC().Format(time.RFC3339),
			"duration":  burstDuration(b.to.Sub(b.from)),
			"lines":     b.lines,
			"firstLine": b.first,
		})
	}
	return out, older, firstLine
}

// cutAtStart says why the oldest burst began before the first line read, or
// "" when it did not: it opens within a gap of the period's start (readFrom),
// or of the first line when older lines were never read (windowLinesDropped,
// tailNote) or, with capCut, were cut from the answer (truncated).
func cutAtStart(payload map[string]any, oldest map[string]any, firstLine time.Time, capCut bool) string {
	from, err := time.Parse(time.RFC3339, fmt.Sprint(oldest["from"]))
	if err != nil {
		return ""
	}
	if s, _ := payload["readFrom"].(string); s != "" {
		// A line before the start is not this read's: a replay can hand over
		// a recording of a wider window.
		if start, err := time.Parse(time.RFC3339, s); err == nil && from.Sub(start) > -time.Second && from.Sub(start) <= burstGap {
			return "it was already under way when the read began (readFrom)"
		}
	}
	dropped, _ := payload["windowLinesDropped"].(float64)
	tail, _ := payload["tailNote"].(string)
	if (capCut && payload["truncated"] == true) || dropped > 0 || tail != "" {
		if !firstLine.IsZero() && from.Sub(firstLine) <= burstGap {
			return "older lines were cut from this answer"
		}
	}
	return ""
}

func burstDuration(d time.Duration) string {
	if d < time.Second {
		return "under 1s"
	}
	return d.Round(time.Second).String()
}

func clipLine(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 140 {
		return string(r[:140]) + "…"
	}
	return s
}
