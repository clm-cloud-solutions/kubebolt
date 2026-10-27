package copilot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The outage is the burst of errors at the time asked, not the operation
// that caused it (an Azure PostgreSQL restart's 61 s for 12 s of errors) and
// not the time someone checked. get_pod_logs hands the bursts over.
func TestAnnotateLogBursts_SplitsBurstsOnTheKubeletsClock(t *testing.T) {
	logs := strings.Join([]string{
		"2026-10-10T01:06:01.1Z [orders-api] ERROR GET / -> 503 db=unreachable",
		"2026-10-10T01:06:09.4Z [orders-api] ERROR GET / -> 503 db=unreachable",
		"2026-10-10T02:42:58.0Z [orders-api] INFO GET / -> 200 rows=4 120ms",
		"2026-10-10T02:43:02.7Z [orders-api] ERROR GET / -> 500 query failed: the database system is shutting down",
		"2026-10-10T02:43:11.5Z [orders-api] WARN readiness failed: cannot reach database",
		"2026-10-10T02:43:13.6Z [orders-api] ERROR GET / -> 503 db=unreachable",
		"2026-10-10T02:43:20.0Z [orders-api] INFO GET / -> 200 rows=4 118ms",
	}, "\n")
	out := AnnotateLogBursts(jsonString(map[string]any{"logs": logs}))
	var p struct {
		Bursts struct {
			Basis string           `json:"basis"`
			Items []map[string]any `json:"items"`
			Note  string           `json:"note"`
		} `json:"bursts"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.Bursts.Basis != "error- and warning-level lines" || len(p.Bursts.Items) != 2 {
		t.Fatalf("%+v", p.Bursts)
	}
	last := p.Bursts.Items[1]
	if last["from"] != "2026-10-10T02:43:02Z" || last["to"] != "2026-10-10T02:43:13Z" || last["duration"] != "11s" || last["lines"] != float64(3) {
		t.Errorf("outage burst: %v", last)
	}
	if !strings.Contains(p.Bursts.Note, "has its own duration") {
		t.Errorf("note: %q", p.Bursts.Note)
	}
}

// A line without the kubelet's stamp or a zone is not placed in production:
// an app may log local time. The harness reads the lab's (UTC) ones.
func TestAnnotateLogBursts_ZonelessOnlyInTheHarness(t *testing.T) {
	in := jsonString(map[string]any{"logs": "2026-10-10T02:43:02 [orders-api] ERROR GET / -> 503\n2026-10-10T02:43:09 [orders-api] ERROR GET / -> 503\n"})
	if AnnotateLogBursts(in) != in {
		t.Error("placed zoneless lines in production")
	}
	if !strings.Contains(AnnotateRecordedLogBursts(in), `"duration":"7s"`) {
		t.Errorf("harness: %s", AnnotateRecordedLogBursts(in))
	}
}

// A read recorded by an older build keeps its bursts and gets today's note,
// or a replay would measure the old wording.
func TestAnnotateLogBursts_RefreshesAnOlderNote(t *testing.T) {
	old := `{"logs":"x","bursts":{"basis":"b","items":[{"from":"2026-10-11T16:08:03Z"}],"note":"an older wording"}}`
	got := AnnotateLogBursts(old)
	if !strings.Contains(got, "not what stopped it") || !strings.Contains(got, `"from":"2026-10-11T16:08:03Z"`) {
		t.Fatalf("got %s", got)
	}
	if AnnotateLogBursts(got) != got {
		t.Error("annotated twice")
	}
}

func burstItems(t *testing.T, out string) []map[string]any {
	t.Helper()
	var p struct {
		Bursts struct {
			Items []map[string]any `json:"items"`
		} `json:"bursts"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	return p.Bursts.Items
}

// The oldest burst says when it began before the first line read: a one-hour
// window opened inside the outage gave «17:10:14» for one that started at
// 16:50:14, and a grep answer cut at the head gave 229 errors «from 16:50»
// for the lines left after 17:14 (2026-10-12).
func TestAnnotateLogBursts_SaysWhenTheOldestBurstBeganBeforeTheRead(t *testing.T) {
	// One burst: a 503 every 10 s from 15:10:14 to 15:13:04.
	var lines []string
	at := time.Date(2026, 10, 12, 15, 10, 14, 0, time.UTC)
	for i := 0; i < 18; i++ {
		lines = append(lines, at.Add(time.Duration(i)*10*time.Second).Format(time.RFC3339Nano)+" [shop-web] ERROR GET / -> 503 orders-api returned 500")
	}
	continuous := strings.Join(lines, "\n")

	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"the window opened inside it", map[string]any{"logs": continuous, "grep": "503", "readFrom": "2026-10-12T15:10:07Z"},
			"it was already under way when the read began (readFrom)"},
		{"older lines were cut", map[string]any{"logs": continuous, "grep": "503", "truncated": true, "readFrom": "2026-10-12T13:10:00Z"},
			"older lines were cut from this answer"},
		{"the window's older lines were dropped", map[string]any{"logs": continuous, "windowLinesDropped": 120},
			"older lines were cut from this answer"},
		{"it began inside the read", map[string]any{"logs": continuous, "grep": "503", "readFrom": "2026-10-12T14:10:00Z"}, ""},
	}
	for _, c := range cases {
		items := burstItems(t, AnnotateLogBursts(jsonString(c.payload)))
		if len(items) == 0 {
			t.Fatalf("%s: no bursts", c.name)
		}
		got, _ := items[0]["cutAtStart"].(string)
		if got != c.want {
			t.Errorf("%s: cutAtStart = %q, want %q (%v)", c.name, got, c.want, items[0])
		}
	}
	if !strings.Contains(burstsNote, "never give its from as the start") {
		t.Errorf("the note does not say what cutAtStart means: %q", burstsNote)
	}
}

// get_pod_logs builds the bursts from every matching line, before the byte
// cap: the outage keeps its real start and count, and a stray match earlier
// in the window is not the outage's start. With the bursts built from what
// the cap left, the outage began where the cut fell, and the start Kobi took
// instead (matchSpan's first match) was the stray line, eight minutes early
// (2026-10-12). That line was a 200 the pattern matched by its latency
// (2581ms): it is now left out and counted in successMatches.
func TestFormatPodLogs_BurstsCoverEveryLineTheCapCut(t *testing.T) {
	var lines []string
	lines = append(lines, "2026-10-12T16:00:04.1Z [shop-web] INFO GET / -> 200 orders=up catalog=up 2581ms")
	at := time.Date(2026, 10, 12, 16, 8, 19, 0, time.UTC)
	// Each 503 has its own latency, or the copies would fold under the cap
	// (log_repeats.go).
	for i := 0; i < 700; i++ {
		lines = append(lines, at.Add(time.Duration(i)*2*time.Second).Format(time.RFC3339Nano)+fmt.Sprintf(" [shop-web] ERROR GET / -> 503 orders-api returned 500 %dms", 100+i))
	}
	out := formatPodLogs(strings.Join(lines, "\n"), "error|5[0-9][0-9]", map[string]any{"readFrom": "2026-10-12T15:51:39Z"})
	var p struct {
		Truncated bool `json:"truncated"`
		Returned  int  `json:"returnedLines"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || !p.Truncated || p.Returned >= 701 {
		t.Fatalf("the fixture must pass the byte cap: %v truncated=%v returned=%d", err, p.Truncated, p.Returned)
	}
	items := burstItems(t, out)
	if len(items) != 1 {
		t.Fatalf("bursts: %v", items)
	}
	if items[0]["from"] != "2026-10-12T16:08:19Z" || items[0]["lines"] != float64(700) || items[0]["cutAtStart"] != nil {
		t.Errorf("the outage: %v", items[0])
	}
	if !strings.Contains(out, `"successMatches":{`) {
		t.Errorf("the stray 200 is not reported: %s", out[:300])
	}
}

// node-image-upgrade, 2026-10-20: the grep «502|503|504» matched a 200 that
// took 504ms, five minutes after the last 503. matchSpan said 7m1s and the
// line was a burst of its own; Kobi gave it as when the store came back.
func TestFormatPodLogs_SuccessLinesAreNotTheOutage(t *testing.T) {
	lines := []string{
		"2026-10-20T21:14:53.1Z 2026-10-20T21:14:53 [shop-web] ERROR GET / -> 503 orders-api unreachable 4004ms",
		"2026-10-20T21:15:24.9Z 2026-10-20T21:15:24 [shop-web] ERROR GET / -> 503 orders-api unreachable 2470ms",
		"2026-10-20T21:16:35.9Z 2026-10-20T21:16:35 [shop-web] ERROR GET / -> 503 orders-api unreachable 4003ms",
		"2026-10-20T21:16:41.3Z 2026-10-20T21:16:41 [shop-web] ERROR GET / -> 503 orders-api unreachable 2040ms",
		"2026-10-20T21:21:53.9Z 2026-10-20T21:21:53 [shop-web] INFO GET / -> 200 orders=up catalog=up 504ms",
	}
	out := formatPodLogs(strings.Join(lines, "\n"), "502|503|504", map[string]any{"readFrom": "2026-10-20T21:05:00Z"})
	var p struct {
		MatchSpan      string `json:"matchSpan"`
		Logs           string `json:"logs"`
		SuccessMatches struct {
			Lines   int    `json:"lines"`
			Example string `json:"example"`
			Note    string `json:"note"`
		} `json:"successMatches"`
		Bursts struct {
			Basis string `json:"basis"`
		} `json:"bursts"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.MatchSpan != "first match 2026-10-20T21:14:53Z, last 2026-10-20T21:16:41Z: 1m48s across 4 lines" {
		t.Errorf("matchSpan = %q", p.MatchSpan)
	}
	if p.SuccessMatches.Lines != 1 || !strings.Contains(p.SuccessMatches.Example, "-> 200") || !strings.Contains(p.SuccessMatches.Note, "anchor the code") {
		t.Errorf("successMatches = %+v", p.SuccessMatches)
	}
	if !strings.Contains(p.Logs, "504ms") {
		t.Error("the line itself must stay in the logs: only the summaries leave it out")
	}
	if p.Bursts.Basis != "the grep's matching lines, without those that report success" {
		t.Errorf("basis = %q", p.Bursts.Basis)
	}
	items := burstItems(t, out)
	if len(items) != 2 || items[1]["to"] != "2026-10-20T21:16:41Z" {
		t.Fatalf("bursts: %v", items)
	}
}

// «catalog|orders» matched every request shop-web served: the logs keep the
// most recent three of those and every other match (alb-targets-unhealthy,
// 2026-10-22).
func TestFormatPodLogs_SuccessLinesThinnedToTheLatest(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("2026-10-21T22:%02d:00.0Z 2026-10-21T22:%02d:00 [shop-web] INFO GET / -> 200 orders=up catalog=up 48ms", 10+i, 10+i))
		if i == 3 || i == 4 || i == 5 {
			lines = append(lines, fmt.Sprintf("2026-10-21T22:%02d:30.0Z 2026-10-21T22:%02d:30 [shop-web] ERROR GET / -> 503 orders-api unreachable", 10+i, 10+i))
		}
	}
	out := formatPodLogs(strings.Join(lines, "\n"), "catalog|orders", nil)
	var p struct {
		Logs           string `json:"logs"`
		ReturnedLines  int    `json:"returnedLines"`
		SuccessMatches struct {
			Lines         int    `json:"lines"`
			LeftOutOfLogs int    `json:"leftOutOfLogs"`
			Note          string `json:"note"`
		} `json:"successMatches"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.SuccessMatches.Lines != 10 || p.SuccessMatches.LeftOutOfLogs != 7 || !strings.Contains(p.SuccessMatches.Note, successThinnedNote) {
		t.Fatalf("successMatches = %+v", p.SuccessMatches)
	}
	// The three 503s fold into their most recent copy; the success lines
	// kept are not folded: three copies of thirteen is not a count.
	if p.ReturnedLines != 4 || strings.Count(p.Logs, "-> 503") != 1 || strings.Count(p.Logs, "-> 200") != 3 ||
		!strings.Contains(p.Logs, "orders-api unreachable [×3 since 2026-10-21T22:13:30Z]") {
		t.Fatalf("returned %d lines:\n%s", p.ReturnedLines, p.Logs)
	}
	for _, kept := range []string{"22:17:00", "22:18:00", "22:19:00"} {
		if !strings.Contains(p.Logs, kept) {
			t.Errorf("the most recent success line at %s is gone", kept)
		}
	}
	if strings.Contains(p.Logs, "22:16:00") {
		t.Error("an older success line stayed")
	}
}

// Left out, the success lines no longer push the errors out of the byte cap.
func TestFormatPodLogs_ThinningLiftsTheCap(t *testing.T) {
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("2026-10-21T21:35:%02d.0Z 2026-10-21T21:35:%02d [shop-web] ERROR GET / -> 503 orders-api unreachable", i, i))
	}
	pad := strings.Repeat("x", 180)
	for i := 0; i < 400; i++ {
		lines = append(lines, fmt.Sprintf("2026-10-21T22:%02d:%02d.0Z 2026-10-21T22:%02d:%02d [shop-web] INFO GET / -> 200 orders=up %s", i/60, i%60, i/60, i%60, pad))
	}
	out := formatPodLogs(strings.Join(lines, "\n"), "orders", nil)
	var p map[string]any
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p["truncated"] != nil || p["bytesDropped"] != nil || p["hint"] != nil {
		t.Fatalf("still marked as cut: truncated=%v bytesDropped=%v hint=%v", p["truncated"], p["bytesDropped"], p["hint"])
	}
	if logs, _ := p["logs"].(string); !strings.Contains(logs, "orders-api unreachable [×5 since 2026-10-21T21:35:00Z]") {
		t.Fatalf("the errors did not come back:\n%.300s", logs)
	}
}

// A recording was cut when it was taken: thinning it keeps its truncation.
func TestAnnotateRecordedLogBursts_ThinsAndKeepsTheCut(t *testing.T) {
	var lines []string
	for i := 0; i < 6; i++ {
		lines = append(lines, fmt.Sprintf("2026-10-21T22:0%d:00 [shop-web] INFO GET / -> 200 orders=up", i))
	}
	lines = append(lines, "2026-10-21T22:07:00 [shop-web] ERROR GET / -> 503 orders-api unreachable")
	rec, _ := json.Marshal(map[string]any{"logs": strings.Join(lines, "\n"), "grep": "orders", "truncated": true, "returnedLines": 7})
	out := AnnotateRecordedLogBursts(string(rec))
	var p map[string]any
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p["truncated"] != true || p["returnedLines"] != float64(4) {
		t.Fatalf("truncated=%v returnedLines=%v", p["truncated"], p["returnedLines"])
	}
}

// A grep whose every match reports success is about those requests (the
// slow ones, say): they are the bursts, and nothing is left out.
func TestFormatPodLogs_OnlySuccessMatchesStay(t *testing.T) {
	lines := []string{
		"2026-10-20T21:00:01.0Z GET /a -> 200 2504ms",
		"2026-10-20T21:00:31.0Z GET /b -> 200 3120ms",
		`2026-10-20T21:00:45.0Z {"level":"info","status":200,"ms":2999}`,
	}
	out := formatPodLogs(strings.Join(lines, "\n"), "[0-9]{4}ms|\"ms\":[0-9]{4}", nil)
	if strings.Contains(out, "successMatches") {
		t.Fatalf("all matches are the subject: %s", out)
	}
	if items := burstItems(t, out); len(items) != 1 || items[0]["lines"] != float64(3) {
		t.Fatalf("bursts: %v", items)
	}
}

func TestSuccessLine(t *testing.T) {
	for _, s := range []string{
		"INFO GET / -> 200 orders=up 504ms",
		`10.0.0.1 - - "GET / HTTP/1.1" 304 0`,
		"status=201 path=/orders",
		`{"status":200,"latency":"504ms"}`,
	} {
		if !successLine(s) {
			t.Errorf("not a success line: %s", s)
		}
	}
	for _, s := range []string{
		"ERROR GET / -> 503 orders-api unreachable 504ms",
		"GET / -> 504 gateway timeout",
		"WARN upstream returned 200 but body empty, retry 3 -> 200", // a warning
		"connection refused to 10.0.0.5:5432",
		"processed 200 orders in 504ms", // no status field
	} {
		if successLine(s) {
			t.Errorf("read as success: %s", s)
		}
	}
}
