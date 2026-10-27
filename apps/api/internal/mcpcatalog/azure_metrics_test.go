package mcpcatalog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The real case (2026-09-25, Azure Database for PostgreSQL, enhanced metrics
// on): Kobi asked for active_connections, max_connections and more in one
// query at PT5M. max_connections is only published every 30 minutes or more,
// so Azure refused the whole query (400, "can not support requested time
// grain"), and Kobi closed without those metrics. Asked on their own,
// sessions_by_wait_event_type came back as one unlabeled "10" — without a
// filter on its WaitEventType dimension Azure folds the values; it was
// Lock=10. The definitions it had read listed both the intervals and the
// dimension.

const pgNamespace = "Microsoft.DBforPostgreSQL/flexibleServers"

// pgDefinitions is the definitions answer, shaped as the live one.
const pgDefinitions = `{"status":200,"message":"All 4 metric definitions returned.","results":{"results":[` +
	`{"name":"active_connections","allowedIntervals":["PT1M","PT5M","PT15M","PT30M","PT1H","PT6H","PT12H","P1D"],"dimensions":["ServerName"]},` +
	`{"name":"max_connections","allowedIntervals":["PT30M","PT1H","PT6H","PT12H","P1D"],"dimensions":[]},` +
	`{"name":"sessions_by_wait_event_type","allowedIntervals":["P1D","PT12H","PT15M","PT1H","PT1M","PT30M","PT5M","PT6H"],"dimensions":["WaitEventType","ServerName"]},` +
	`{"name":"longest_transaction_time_sec","allowedIntervals":["PT1M","PT5M","PT15M","PT30M","PT1H"],"dimensions":[]}` +
	`]},"duration":0}`

// pgGrainRefusal is Azure's answer to a query whose metrics share no grain
// finer than 30 minutes.
func pgGrainRefusal(asked string) string {
	return fmt.Sprintf(`{"status":400,"message":"Commonly allowed time grains: 00:30:00,01:00:00,06:00:00,12:00:00,1.00:00:00 between metrics: active_connections,max_connections, can not support requested time grain: %s please adjust your interval parameter to use one of the common grains"}`, asked)
}

// pgAzure answers like Azure: a query mixing max_connections with a grain
// finer than 30 minutes is refused; max_connections alone below 30 minutes
// too; sessions_by_wait_event_type split by WaitEventType names Lock=10,
// unsplit it is one unlabeled series.
func pgAzure(defs string) func(string, map[string]json.RawMessage) (string, bool) {
	return func(name string, args map[string]json.RawMessage) (string, bool) {
		var cmd string
		_ = json.Unmarshal(args["command"], &cmd)
		var p map[string]string
		_ = json.Unmarshal(args["parameters"], &p)
		switch cmd {
		case azureMetricsDefs:
			if defs == "" {
				return `{"status":403,"message":"AuthorizationFailed"}`, true
			}
			return defs, false
		case azureMetricsQuery:
			names, iv, filter := p["metric-names"], p["interval"], p["filter"]
			if strings.Contains(names, "max_connections") && iv != "PT30M" && iv != "PT1H" {
				return pgGrainRefusal(map[string]string{"PT5M": "00:05:00", "PT1M": "00:01:00"}[iv]), true
			}
			var items []string
			for _, n := range strings.Split(names, ",") {
				series := `{"metadata":{},"interval":"` + iv + `","maxBuckets":[10]}`
				if n == "sessions_by_wait_event_type" && strings.Contains(filter, "WaitEventType eq '*'") {
					series = `{"metadata":{"waiteventtype":"Lock"},"interval":"` + iv + `","maxBuckets":[10]},` +
						`{"metadata":{"waiteventtype":"Client"},"interval":"` + iv + `","maxBuckets":[0]}`
				}
				items = append(items, `{"name":"`+n+`","unit":"Count","timeSeries":[`+series+`]}`)
			}
			return `{"status":200,"message":"Success","results":{"results":[` + strings.Join(items, ",") + `]},"duration":0}`, false
		}
		return `{"status":200,"message":"Success","results":{"items":[]},"duration":1}`, false
	}
}

func pgQuery(t *testing.T, names any, extra map[string]any) string {
	t.Helper()
	params := map[string]any{"resource": "pg-shop", "resource-group": "rg-data", "metric-namespace": pgNamespace,
		"metric-names": names, "interval": "PT5M", "aggregation": "Maximum"}
	for k, v := range extra {
		params[k] = v
	}
	return azArgs(t, azureMetricsQuery, params)
}

// queriesSent is each metrics query that reached the sidecar: its metric
// names, interval and filter.
func queriesSent(t *testing.T, calls []fakeAzCall) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, c := range calls {
		var cmd string
		_ = json.Unmarshal(c.Args["command"], &cmd)
		if cmd != azureMetricsQuery {
			continue
		}
		var p map[string]string
		_ = json.Unmarshal(c.Args["parameters"], &p)
		out = append(out, map[string]string{"metric-names": p["metric-names"], "interval": p["interval"], "filter": p["filter"]})
	}
	return out
}

func TestAzureMetrics_EachMetricAtItsIntervalAndSplitByItsDimensions(t *testing.T) {
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		f := newFakeAzmcp(t)
		f.respond = pgAzure(pgDefinitions)
		b := bindAzure(t, f, nil)
		res := azCall(t, b, c, "monitor", pgQuery(t, "active_connections,max_connections,sessions_by_wait_event_type", nil))
		if res.IsError {
			t.Fatalf("%s: refused: %s", c, res.Text)
		}
		sent := queriesSent(t, f.recorded())
		want := []map[string]string{
			{"metric-names": "active_connections", "interval": "PT5M", "filter": "ServerName eq '*'"},
			{"metric-names": "max_connections", "interval": "PT30M", "filter": ""},
			{"metric-names": "sessions_by_wait_event_type", "interval": "PT5M", "filter": "WaitEventType eq '*' and ServerName eq '*'"},
		}
		if fmt.Sprint(sent) != fmt.Sprint(want) {
			t.Errorf("%s: sent\n %v\nwant\n %v", c, sent, want)
		}
		var body struct {
			Results struct {
				Results []struct {
					Name       string            `json:"name"`
					TimeSeries []json.RawMessage `json:"timeSeries"`
				} `json:"results"`
			} `json:"results"`
			Failed []any `json:"failed"`
		}
		if err := json.Unmarshal([]byte(res.Text), &body); err != nil {
			t.Fatalf("%s: merged answer is not JSON: %v\n%s", c, err, res.Text)
		}
		if len(body.Results.Results) != 3 || len(body.Failed) != 0 {
			t.Errorf("%s: merged %d metrics, %d failed: %s", c, len(body.Results.Results), len(body.Failed), res.Text)
		}
		if !strings.Contains(res.Text, `"waiteventtype":"Lock"`) {
			t.Errorf("%s: the wait event split did not come back labeled: %s", c, res.Text)
		}
		if !azContainsStr(res.Notes, azureMetricsNote) {
			t.Errorf("%s: notes = %v", c, res.Notes)
		}
		for _, n := range res.Notes {
			for _, word := range []string{"max_connections", "WaitEventType", "sessions_by"} {
				if strings.Contains(n, word) {
					t.Errorf("%s: a note echoes %q: %s", c, word, n)
				}
			}
		}
	}
}

// With no definitions (not readable here), the query goes as asked; Azure's
// refusal lists the grains it takes, and the metrics go again one at a time,
// each at the nearest one it takes.
func TestAzureMetrics_WithoutDefinitionsTheRefusalIsTheFallback(t *testing.T) {
	f := newFakeAzmcp(t)
	f.respond = pgAzure("")
	b := bindAzure(t, f, nil)
	res := azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, "active_connections,max_connections", nil))
	if res.IsError {
		t.Fatalf("refused: %s", res.Text)
	}
	sent := queriesSent(t, f.recorded())
	want := []map[string]string{
		{"metric-names": "active_connections,max_connections", "interval": "PT5M", "filter": ""},
		{"metric-names": "active_connections", "interval": "PT5M", "filter": ""},
		{"metric-names": "max_connections", "interval": "PT5M", "filter": ""},
		{"metric-names": "max_connections", "interval": "PT30M", "filter": ""},
	}
	if fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Errorf("sent\n %v\nwant\n %v", sent, want)
	}
	if strings.Contains(res.Text, `"failed"`) || !azContainsStr(res.Notes, azureMetricsNote) {
		t.Errorf("answer %s notes %v", res.Text, res.Notes)
	}
}

// Nothing to change: one call, as asked, and the server's own answer.
func TestAzureMetrics_NothingToChangeGoesAsAsked(t *testing.T) {
	f := newFakeAzmcp(t)
	f.respond = pgAzure(pgDefinitions)
	b := bindAzure(t, f, nil)
	res := azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, "longest_transaction_time_sec", nil))
	sent := queriesSent(t, f.recorded())
	if len(sent) != 1 || sent[0]["interval"] != "PT5M" || sent[0]["filter"] != "" {
		t.Fatalf("sent %v", sent)
	}
	if res.IsError || len(res.Notes) != 0 || !strings.HasPrefix(res.Text, `{"status":200,"message":"Success","results":{"results":[{"name":"longest_transaction_time_sec"`) {
		t.Errorf("not the server's answer: %s notes %v", res.Text, res.Notes)
	}
}

// The model's own filter is its choice: nothing is added to it, and the
// interval still follows the metric.
func TestAzureMetrics_TheModelsFilterIsKept(t *testing.T) {
	f := newFakeAzmcp(t)
	f.respond = pgAzure(pgDefinitions)
	b := bindAzure(t, f, nil)
	azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, "sessions_by_wait_event_type,max_connections", map[string]any{"filter": "WaitEventType eq 'Lock'"}))
	sent := queriesSent(t, f.recorded())
	want := []map[string]string{
		{"metric-names": "sessions_by_wait_event_type", "interval": "PT5M", "filter": "WaitEventType eq 'Lock'"},
		{"metric-names": "max_connections", "interval": "PT30M", "filter": "WaitEventType eq 'Lock'"},
	}
	if fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Errorf("sent\n %v\nwant\n %v", sent, want)
	}
}

// The definitions are read once per resource, not per question.
func TestAzureMetrics_DefinitionsAreCached(t *testing.T) {
	f := newFakeAzmcp(t)
	f.respond = pgAzure(pgDefinitions)
	b := bindAzure(t, f, nil)
	for i := 0; i < 3; i++ {
		azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, "max_connections", nil))
	}
	defsCalls := 0
	for _, c := range f.recorded() {
		var cmd string
		_ = json.Unmarshal(c.Args["command"], &cmd)
		if cmd == azureMetricsDefs {
			defsCalls++
		}
	}
	if defsCalls != 1 {
		t.Errorf("definitions read %d times, want 1", defsCalls)
	}
}

// A split whose answer is too large for the tool output comes back unsplit.
func TestAzureMetrics_ATooLargeSplitComesBackUnsplit(t *testing.T) {
	f := newFakeAzmcp(t)
	base := pgAzure(pgDefinitions)
	f.respond = func(name string, args map[string]json.RawMessage) (string, bool) {
		var cmd string
		_ = json.Unmarshal(args["command"], &cmd)
		var p map[string]string
		_ = json.Unmarshal(args["parameters"], &p)
		if cmd == azureMetricsQuery && p["filter"] != "" {
			return `{"status":200,"message":"Success","results":{"results":[{"name":"sessions_by_wait_event_type","timeSeries":[{"metadata":{"x":"` +
				strings.Repeat("v", azureMetricSplitMax) + `"}}]}]}}`, false
		}
		return base(name, args)
	}
	b := bindAzure(t, f, nil)
	res := azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, "sessions_by_wait_event_type", nil))
	sent := queriesSent(t, f.recorded())
	if len(sent) != 2 || sent[0]["filter"] == "" || sent[1]["filter"] != "" {
		t.Fatalf("sent %v", sent)
	}
	if !azContainsStr(res.Notes, azureMetricsSplitDroppedNote) || len(res.Text) > azureMetricSplitMax {
		t.Errorf("answer %d bytes, notes %v", len(res.Text), res.Notes)
	}
}

// 1a: models hand metric-names over as a JSON list; the server takes one
// comma-separated string. A list of plain names is joined; anything else is
// refused as before.
func TestAzureMetrics_AListOfMetricNamesIsJoined(t *testing.T) {
	f := newFakeAzmcp(t)
	f.respond = pgAzure(pgDefinitions)
	b := bindAzure(t, f, nil)
	res := azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, []string{"active_connections", " longest_transaction_time_sec "}, nil))
	if res.IsError {
		t.Fatalf("a list of names was refused: %s", res.Text)
	}
	sent := queriesSent(t, f.recorded())
	if len(sent) != 2 || sent[0]["metric-names"] != "active_connections" || sent[1]["metric-names"] != "longest_transaction_time_sec" {
		t.Errorf("sent %v", sent)
	}
	// monitor_metrics_batchquery takes the same comma-separated string.
	before := len(f.recorded())
	res = azCall(t, b, ConsumerKobi, "monitor", azArgs(t, "monitor_metrics_batchquery", map[string]any{
		"resources": "pg1,pg2", "metric-names": []string{"cpu_percent", "memory_percent"}, "metric-namespace": pgNamespace}))
	if res.IsError || len(f.recorded()) != before+1 {
		t.Fatalf("batchquery: %s", res.Text)
	}
	if p := azParams(t, f.last(t)); p["metric-names"] != "cpu_percent,memory_percent" {
		t.Errorf("batchquery forwarded %v", p["metric-names"])
	}

	for _, bad := range []any{
		[]string{"a", "-b"},      // an option to the sidecar
		[]string{"a", "@creds"},  // a response file
		[]string{"a,b", "c"},     // a comma inside a name
		[]any{"a", 5},            // not all strings
		map[string]any{"a": "b"}, // not a list
		[]string{"a", "  "},      // a blank name
	} {
		before := len(f.recorded())
		if res := azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, bad, nil)); !res.IsError || len(f.recorded()) != before {
			t.Errorf("%v: forwarded (%s)", bad, res.Text)
		}
	}
	// A comma-joined string cannot slip an option in the middle either.
	before = len(f.recorded())
	if res := azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, "a,-b", nil)); !res.IsError || azCallsBut(f.recorded()[before:], azureMetricsDefs) != 0 {
		t.Errorf("a,-b was forwarded: %s", res.Text)
	}
}

func TestAzureMetricInterval(t *testing.T) {
	pg := []string{"PT30M", "PT1H", "PT6H", "PT12H", "P1D"}
	fine := []string{"P1D", "PT12H", "PT15M", "PT1H", "PT1M", "PT30M", "PT5M", "PT6H"}
	for _, c := range []struct {
		requested string
		allowed   []string
		want      string
	}{
		{"PT5M", fine, ""},    // offered: unchanged
		{"PT5M", pg, "PT30M"}, // the nearest coarser one
		{"PT2H", pg, "PT6H"},  // between two
		{"P2D", pg, "P1D"},    // coarser than all: the coarsest
		{"", fine, ""},        // Azure's default PT1M is offered
		{"", pg, "PT30M"},     // not offered: the finest
		{"PT5M", nil, ""},     // nothing known
		{"five", pg, ""},      // not a duration: Azure answers for it
		{"pt1h", pg, ""},      // case does not matter
	} {
		if got := azureMetricInterval(c.requested, c.allowed); got != c.want {
			t.Errorf("azureMetricInterval(%q, %v) = %q, want %q", c.requested, c.allowed, got, c.want)
		}
	}
}

func TestAzureGrainRetry(t *testing.T) {
	if got := azureGrainRetry(pgGrainRefusal("00:05:00"), "PT5M"); got != "PT30M" {
		t.Errorf("got %q", got)
	}
	if got := azureGrainRetry(pgGrainRefusal("00:01:00"), ""); got != "PT30M" {
		t.Errorf("no interval asked: got %q", got)
	}
	if got := azureGrainRetry(`{"status":400,"message":"something else"}`, "PT5M"); got != "" {
		t.Errorf("another refusal: got %q", got)
	}
	for in, want := range map[string]int{"00:30:00": 30, "01:00:00": 60, "12:00:00": 720, "1.00:00:00": 1440, "x": 0} {
		if got, _ := azureTimeSpanMinutes(in); got != want {
			t.Errorf("azureTimeSpanMinutes(%q) = %d, want %d", in, got, want)
		}
	}
}

// Dimension names come from the provider: only plain names go into a filter.
func TestAzureSplitFilterTakesPlainNamesOnly(t *testing.T) {
	got := azureSplitFilter([]string{"WaitEventType", "Name eq 'x' or 1", "Server.Name", ""})
	if got != "WaitEventType eq '*' and Server.Name eq '*'" {
		t.Errorf("filter = %q", got)
	}
}

// A split Azure refuses (a dimension the metric lists but will not filter
// on) must not cost the metric: it comes back unsplit.
func TestAzureMetrics_ARefusedSplitComesBackUnsplit(t *testing.T) {
	f := newFakeAzmcp(t)
	base := pgAzure(pgDefinitions)
	f.respond = func(name string, args map[string]json.RawMessage) (string, bool) {
		var cmd string
		_ = json.Unmarshal(args["command"], &cmd)
		var p map[string]string
		_ = json.Unmarshal(args["parameters"], &p)
		if cmd == azureMetricsQuery && p["filter"] != "" {
			return `{"status":400,"message":"Invalid filter"}`, true
		}
		return base(name, args)
	}
	b := bindAzure(t, f, nil)
	res := azCall(t, b, ConsumerKobi, "monitor", pgQuery(t, "sessions_by_wait_event_type", nil))
	if res.IsError || strings.Contains(res.Text, `"failed"`) {
		t.Fatalf("the metric was lost to its split: %s", res.Text)
	}
	if sent := queriesSent(t, f.recorded()); len(sent) != 2 || sent[1]["filter"] != "" {
		t.Errorf("sent %v", sent)
	}
}
