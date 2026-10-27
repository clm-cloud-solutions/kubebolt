package mcpcatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// The real case (2026-10-12, Azure Database for PostgreSQL): someone set
// statement_timeout to 50. The write is three entries (Started with the
// request body, Accepted, Succeeded); the server had been started that
// morning (three more) after a Resource Health "Stopped" event. The write
// that set the earlier value, "0", was 17 days back, behind a lab's daily
// stops and starts.

const pgServerID = "/subscriptions/" + azTestSub + "/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-orders"

type actEntry struct {
	at, op, title, status, corr, on, caller, body, category string
	props                                                   map[string]string
}

func (e actEntry) json() map[string]any {
	id := pgServerID
	if e.on != "" {
		id += "/" + e.on
	}
	props := map[string]any{"eventCategory": "Administrative"}
	if e.body != "" {
		props["requestbody"] = e.body
	}
	for k, v := range e.props {
		props[k] = v
	}
	cat := e.category
	if cat == "" {
		cat = "Administrative"
	}
	var caller any
	if e.caller != "" {
		caller = e.caller
	}
	return map[string]any{
		"description": "", "resourceId": id,
		"operationName": map[string]any{"localizedValue": e.title, "value": e.op},
		"level":         "Informational", "eventTimestamp": e.at, "properties": props,
		"caller": caller, "correlationId": e.corr, "category": map[string]any{"localizedValue": cat, "value": cat},
		"operationId": "op-" + e.corr, "resourceGroupName": "rg-data",
		"status":    map[string]any{"localizedValue": e.status, "value": e.status},
		"subStatus": map[string]any{"localizedValue": "", "value": ""}, "subscriptionId": azTestSub,
	}
}

func actAnswer(t *testing.T, entries []actEntry) string {
	t.Helper()
	logs := make([]map[string]any, len(entries))
	for i, e := range entries {
		logs[i] = e.json()
	}
	raw, err := json.Marshal(map[string]any{"status": 200, "message": "Success", "results": map[string]any{"activityLogs": logs}, "duration": 0})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const (
	cfgWrite = "Microsoft.DBforPostgreSQL/flexibleServers/configurations/write"
	cfgTitle = "Update PostgreSQL server configuration"
	cfgOn    = "configurations/statement_timeout"
)

// todaysLog is the newest-first answer of the incident's hours.
func todaysLog() []actEntry {
	return []actEntry{
		{at: "2026-10-12T14:50:20.1025554Z", op: cfgWrite, title: cfgTitle, status: "Succeeded", corr: "w50", on: cfgOn, caller: "ops@example.com"},
		{at: "2026-10-12T14:50:09.7755074Z", op: cfgWrite, title: cfgTitle, status: "Accepted", corr: "w50", on: cfgOn, caller: "ops@example.com"},
		{at: "2026-10-12T14:50:09.400507Z", op: cfgWrite, title: cfgTitle, status: "Started", corr: "w50", on: cfgOn, caller: "ops@example.com",
			body: `{"properties":{"value":"50","source":"user-override"}}`},
		{at: "2026-10-12T11:53:37.6805524Z", op: "Microsoft.Resourcehealth/healthevent/Resolved/action", title: "Health Event Resolved", status: "Resolved", corr: "h2",
			category: "ResourceHealth", props: map[string]string{"title": "Unknown", "currentHealthStatus": "Available", "previousHealthStatus": "Degraded", "cause": "Unknown"}},
		{at: "2026-10-12T11:50:31.7138452Z", op: "Microsoft.DBforPostgreSQL/flexibleServers/start/action", title: "Start PostgreSQL server", status: "Succeeded", corr: "s1", caller: "ops@example.com"},
		{at: "2026-10-12T11:48:31.1068968Z", op: "Microsoft.DBforPostgreSQL/flexibleServers/start/action", title: "Start PostgreSQL server", status: "Accepted", corr: "s1", caller: "ops@example.com"},
		{at: "2026-10-12T11:48:30.8725285Z", op: "Microsoft.DBforPostgreSQL/flexibleServers/start/action", title: "Start PostgreSQL server", status: "Started", corr: "s1", caller: "ops@example.com"},
	}
}

// olderWrite is the write that set the value before, 17 days earlier.
func olderWrite() []actEntry {
	return []actEntry{
		{at: "2026-09-25T03:19:13Z", op: cfgWrite, title: cfgTitle, status: "Succeeded", corr: "w0", on: cfgOn, caller: "ops@example.com"},
		{at: "2026-09-25T03:19:11Z", op: cfgWrite, title: cfgTitle, status: "Started", corr: "w0", on: cfgOn, caller: "ops@example.com",
			body: `{"properties":{"value":"0","source":"user-override"}}`},
	}
}

func activityArgs(t *testing.T, params map[string]any) string {
	p := map[string]any{"resource-name": "pg-orders"}
	for k, v := range params {
		p[k] = v
	}
	return azArgs(t, azureActivityLog, p)
}

func decodeActivity(t *testing.T, text string) azureActivityAnswer {
	t.Helper()
	var ans azureActivityAnswer
	if err := json.Unmarshal([]byte(text), &ans); err != nil {
		t.Fatalf("the answer is not KubeBolt's merged log: %v\n%s", err, text)
	}
	return ans
}

func TestAzureActivity_OneRowPerOperation(t *testing.T) {
	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		f := newFakeAzmcp(t)
		answer := actAnswer(t, todaysLog())
		f.respond = func(string, map[string]json.RawMessage) (string, bool) { return answer, false }
		b := bindAzure(t, f, nil)
		res := azCall(t, b, c, "monitor", activityArgs(t, map[string]any{"top": 20}))
		if res.IsError {
			t.Fatalf("%s: refused: %s", c, res.Text)
		}
		if p := azParams(t, f.last(t)); p["top"] != float64(60) {
			t.Errorf("%s: asked Azure for top=%v, want three entries per operation (60)", c, p["top"])
		}
		ans := decodeActivity(t, res.Text)
		if ans.Resource != pgServerID || ans.ResourceGroup != "rg-data" {
			t.Errorf("%s: resource %q group %q", c, ans.Resource, ans.ResourceGroup)
		}
		if len(ans.Operations) != 3 {
			t.Fatalf("%s: %d operations, want 3 (the write, the health event, the start): %s", c, len(ans.Operations), res.Text)
		}
		w := ans.Operations[0]
		if w.Operation != cfgWrite || w.On != cfgOn || w.Status != "Succeeded" || w.Caller != "ops@example.com" ||
			w.Started != "2026-10-12T14:50:09Z" || w.Ended != "2026-10-12T14:50:20Z" || w.ResourceID != "" {
			t.Errorf("%s: the write merged as %+v", c, w)
		}
		if string(w.Request) != `{"properties":{"value":"50","source":"user-override"}}` {
			t.Errorf("%s: request %s", c, w.Request)
		}
		h := ans.Operations[1]
		if h.Category != "ResourceHealth" || h.Health["currentHealthStatus"] != "Available" || h.Ended != "" {
			t.Errorf("%s: the health event merged as %+v", c, h)
		}
		if s := ans.Operations[2]; s.Status != "Succeeded" || s.Started != "2026-10-12T11:48:30Z" || s.Ended != "2026-10-12T11:50:31Z" {
			t.Errorf("%s: the start merged as %+v", c, s)
		}
		for _, gone := range []string{"correlationId", "subscriptionId", "localizedValue", "hierarchy", "subStatus"} {
			if strings.Contains(res.Text, gone) {
				t.Errorf("%s: the answer still carries %s", c, gone)
			}
		}
		for _, want := range []string{azureActivityMergedNote, azureActivityPreviousNote} {
			if !azContainsStr(res.Notes, want) {
				t.Errorf("%s: notes lack %q: %v", c, want, res.Notes)
			}
		}
		if !azNoteContains(res.Notes, "covers the last 24 hours of this resource's activity log and holds 3 operations.") ||
			!azNoteContains(res.Notes, "keeps 90 days (hours up to 2160)") {
			t.Errorf("%s: the window note: %v", c, res.Notes)
		}
		for _, n := range res.Notes {
			for _, word := range []string{"statement_timeout", "pg-orders", "ops@example.com", "50"} {
				if strings.Contains(n, word) {
					t.Errorf("%s: a note echoes %q: %s", c, word, n)
				}
			}
		}
	}
}

// The earlier value is in the log, and match reaches it in one call: the
// whole window is read, the parameter is never forwarded, and only that
// sub-resource's writes come back.
func TestAzureActivity_MatchFindsThePreviousWrite(t *testing.T) {
	f := newFakeAzmcp(t)
	var noise []actEntry
	for i := 0; i < 20; i++ {
		day := "2026-10-0" + string(rune('1'+i%9))
		noise = append(noise, actEntry{at: day + "T06:00:00Z", op: "Microsoft.DBforPostgreSQL/flexibleServers/stop/action", status: "Succeeded", corr: "n" + string(rune('a'+i))})
	}
	answer := actAnswer(t, append(append(todaysLog(), noise...), olderWrite()...))
	f.respond = func(string, map[string]json.RawMessage) (string, bool) { return answer, false }
	b := bindAzure(t, f, nil)
	res := azCall(t, b, ConsumerKobi, "monitor", activityArgs(t, map[string]any{"hours": 2160, "top": 5, "match": "Configurations/Statement_Timeout"}))
	if res.IsError {
		t.Fatalf("refused: %s", res.Text)
	}
	p := azParams(t, f.last(t))
	if _, sent := p["match"]; sent || p["top"] != float64(azureActivityFetchMax) {
		t.Errorf("forwarded %v: match must stay in KubeBolt and the whole window be read", p)
	}
	ans := decodeActivity(t, res.Text)
	if len(ans.Operations) != 2 || string(ans.Operations[0].Request) != `{"properties":{"value":"50","source":"user-override"}}` ||
		string(ans.Operations[1].Request) != `{"properties":{"value":"0","source":"user-override"}}` {
		t.Fatalf("operations: %s", res.Text)
	}
	if !azNoteContains(res.Notes, "covers the last 90 days of this resource's activity log and holds 2 operations that match what was asked.") ||
		azNoteContains(res.Notes, "keeps 90 days") {
		t.Errorf("window note: %v", res.Notes)
	}
}

func TestAzureActivity_TopCountsOperations(t *testing.T) {
	f := newFakeAzmcp(t)
	answer := actAnswer(t, append(todaysLog(), olderWrite()...))
	f.respond = func(string, map[string]json.RawMessage) (string, bool) { return answer, false }
	b := bindAzure(t, f, nil)
	res := azCall(t, b, ConsumerKobi, "monitor", activityArgs(t, map[string]any{"top": 2}))
	ans := decodeActivity(t, res.Text)
	if len(ans.Operations) != 2 || ans.Operations[0].Operation != cfgWrite {
		t.Fatalf("operations: %s", res.Text)
	}
	if !azNoteContains(res.Notes, "holds 2 operations, the newest 2 of 4: raise top for the older ones.") {
		t.Errorf("window note: %v", res.Notes)
	}
	// Six entries read for two operations asked for: the window held more.
	if !azNoteContains(res.Notes, "the oldest in it were not reached") {
		t.Errorf("a full read is not said: %v", res.Notes)
	}
}

func TestAzureActivity_AFailureKeepsItsMessage(t *testing.T) {
	f := newFakeAzmcp(t)
	answer := actAnswer(t, []actEntry{
		{at: "2026-10-12T10:00:05Z", op: cfgWrite, status: "Failed", corr: "f1", on: cfgOn,
			props: map[string]string{"statusMessage": `{"error":{"code":"InvalidParameterValue","message":"Invalid value given for parameter statement_timeout."}}`}},
		{at: "2026-10-12T10:00:01Z", op: cfgWrite, status: "Started", corr: "f1", on: cfgOn, body: `{"properties":{"value":"-5"}}`},
	})
	f.respond = func(string, map[string]json.RawMessage) (string, bool) { return answer, false }
	res := azCall(t, bindAzure(t, f, nil), ConsumerKobi, "monitor", activityArgs(t, nil))
	op := decodeActivity(t, res.Text).Operations[0]
	if op.Status != "Failed" || !strings.Contains(op.Error, "InvalidParameterValue") {
		t.Errorf("failure merged as %+v", op)
	}
}

func TestAzureActivity_AnEmptyWindowSaysWhatItCovers(t *testing.T) {
	f := newFakeAzmcp(t)
	answer := actAnswer(t, nil)
	f.respond = func(string, map[string]json.RawMessage) (string, bool) { return answer, false }
	res := azCall(t, bindAzure(t, f, nil), ConsumerKobi, "monitor", activityArgs(t, nil))
	if res.IsError || len(decodeActivity(t, res.Text).Operations) != 0 {
		t.Fatalf("empty answer: %s", res.Text)
	}
	if !azContainsStr(res.Notes, azureActivityEmptyNote) || azContainsStr(res.Notes, azureActivityPreviousNote) {
		t.Errorf("notes: %v", res.Notes)
	}
}

// Anything KubeBolt cannot read goes back as Azure wrote it.
func TestAzureActivity_AzuresOwnAnswerOtherwise(t *testing.T) {
	f := newFakeAzmcp(t)
	const refusal = `{"status":404,"message":"Resource 'pg-other' was not found."}`
	f.respond = func(string, map[string]json.RawMessage) (string, bool) { return refusal, true }
	res := azCall(t, bindAzure(t, f, nil), ConsumerKobi, "monitor", activityArgs(t, nil))
	if !res.IsError || res.Text != refusal || azContainsStr(res.Notes, azureActivityMergedNote) {
		t.Errorf("refusal came back as %v %q %v", res.IsError, res.Text, res.Notes)
	}
}

func azNoteContains(notes []string, s string) bool {
	for _, n := range notes {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}
