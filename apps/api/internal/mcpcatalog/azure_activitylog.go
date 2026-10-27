package mcpcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The activity log of one resource, as one row per operation.
//
// Azure writes an operation as several entries — Started, Accepted, then
// Succeeded or Failed — each about 1.3 KiB, most of it ids and localized
// copies of names. Twenty entries were six or seven operations, so a lab
// server's daily stop and start filled the answer within days, and the 32 KiB
// a tool call may hand the model held about 25 entries. The write that set a
// parameter's earlier value (a request body of "value":"0", 17 days back) was
// in the log and out of reach; Kobi, having read 24 hours, said the earlier
// value could not be read (2026-10-12). KubeBolt reads more entries than it
// was asked for, merges each operation into one row that keeps who, when,
// the final status, what was written and the Resource Health details, and
// says what the answer covers. Nothing here knows a resource type.

const (
	azureActivityLog = "monitor_activitylog_list"
	// azureActivityMatch is KubeBolt's own parameter: it is never forwarded.
	azureActivityMatch = "match"

	azureActivityDefaultTop   = 10
	azureActivityDefaultHours = 24.0
	// azureActivityRetainedHours is how far back Azure keeps the log: 90 days.
	azureActivityRetainedHours = 2160.0
	// azureActivityEntriesPerOperation is how many entries are read for each
	// operation asked for: a write is usually three.
	azureActivityEntriesPerOperation = 3
	// azureActivityFetchMax bounds the entries read in one call: at about
	// 1.3 KiB each, 150 stay under the client's 256 KiB call cap.
	azureActivityFetchMax = 150
	// azureActivityRequestMax bounds a request body kept whole.
	azureActivityRequestMax = 2 << 10
	// azureActivityErrorMax bounds a failed operation's message.
	azureActivityErrorMax = 600
)

// azureActivityMatchParam is how the model learns the parameter.
var azureActivityMatchParam = azureParam{typ: "string",
	desc: "Keep only the operations whose name or changed sub-resource contains this text (KubeBolt filters, case-insensitive)."}

// azureActivitySchema adds KubeBolt's own parameter to the command's schema
// as the server declares it.
func azureActivitySchema(s azureSchema) azureSchema {
	if _, declared := s.props[azureActivityMatch]; declared {
		return s
	}
	props := make(map[string]azureParam, len(s.props)+1)
	for k, v := range s.props {
		props[k] = v
	}
	props[azureActivityMatch] = azureActivityMatchParam
	return azureSchema{props: props, required: s.required}
}

const azureActivityMergedNote = "KubeBolt merged each operation's entries (Started, Accepted, then Succeeded or Failed) into one row with its final status, " +
	"and kept who made it, when, what was written (request) and the Resource Health details. Times are UTC."

// azureActivityPreviousNote goes with an answer that holds what a write sent.
const azureActivityPreviousNote = "A request is the value written, not the one it replaced. " +
	"The value before a change is in the previous write to the same \"on\": read further back, with hours up to 2160 and match set to that \"on\". " +
	"With no earlier write in 90 days, the value before was set longer ago or never changed from its default."

// azureActivityEmptyNote goes with an answer that holds no operation.
const azureActivityEmptyNote = "An empty answer covers this one resource: its network, its parent and every other resource keep their own activity log."

// azureActivityOp is one operation as the model reads it.
type azureActivityOp struct {
	Started    string            `json:"started,omitempty"`
	Ended      string            `json:"ended,omitempty"`
	Status     string            `json:"status,omitempty"`
	Operation  string            `json:"operation"`
	Title      string            `json:"title,omitempty"`
	On         string            `json:"on,omitempty"`
	ResourceID string            `json:"resourceId,omitempty"`
	Caller     string            `json:"caller,omitempty"`
	Category   string            `json:"category,omitempty"`
	Level      string            `json:"level,omitempty"`
	Request    json.RawMessage   `json:"request,omitempty"`
	Error      string            `json:"error,omitempty"`
	Health     map[string]string `json:"health,omitempty"`

	first, last time.Time
	lastStatus  time.Time
}

type azureActivityAnswer struct {
	Resource      string             `json:"resource,omitempty"`
	ResourceGroup string             `json:"resourceGroup,omitempty"`
	Operations    []*azureActivityOp `json:"operations"`
}

// azureActivityEntry is the part of an entry KubeBolt reads.
type azureActivityEntry struct {
	ResourceID     string                     `json:"resourceId"`
	OperationName  azureLocalized             `json:"operationName"`
	Level          string                     `json:"level"`
	EventTimestamp string                     `json:"eventTimestamp"`
	Properties     map[string]json.RawMessage `json:"properties"`
	Caller         *string                    `json:"caller"`
	CorrelationID  string                     `json:"correlationId"`
	Category       azureLocalized             `json:"category"`
	Status         azureLocalized             `json:"status"`
}

type azureLocalized struct {
	Value          string `json:"value"`
	LocalizedValue string `json:"localizedValue"`
}

func (b *azureBinding) callActivityLog(ctx context.Context, t *azureTool, prepared map[string]json.RawMessage, notes []string, audit map[string]any) (CallResult, error) {
	match := ""
	if v, ok := prepared[azureActivityMatch]; ok {
		match, _ = azureString(v)
		match = strings.TrimSpace(match)
	}
	top := azureActivityDefaultTop
	if n, ok := azureJSONNumber(prepared["top"]); ok && n >= 1 {
		top = int(n)
	}
	hours := azureActivityDefaultHours
	if h, ok := azureJSONNumber(prepared["hours"]); ok && h > 0 {
		hours = h
	}
	fetch := top * azureActivityEntriesPerOperation
	if match != "" || fetch > azureActivityFetchMax {
		fetch = azureActivityFetchMax
	}

	params := make(map[string]json.RawMessage, len(prepared))
	for k, v := range prepared {
		if k != azureActivityMatch {
			params[k] = v
		}
	}
	params["top"] = json.RawMessage(strconv.Itoa(fetch))
	res, err := b.forward(ctx, t.namespace, azureActivityLog, params)
	if err != nil {
		return CallResult{Audit: audit}, err
	}
	if res.Truncated && fetch > azureActivityFetchMax/3 {
		// Entries larger than usual: half as many fit.
		fetch /= 2
		params["top"] = json.RawMessage(strconv.Itoa(fetch))
		if res, err = b.forward(ctx, t.namespace, azureActivityLog, params); err != nil {
			return CallResult{Audit: audit}, err
		}
	}
	text := azureResultText(res)
	ans, entries, found, ok := azureCompactActivity(text, match, top)
	if res.IsError || res.Truncated || !ok {
		// Not an answer KubeBolt can read: Azure's own, as before.
		notes = append(notes, azureGroupNotes(azureIndex()[azureActivityLog], t.router.commands[azureActivityLog], prepared, res)...)
		return CallResult{Text: text, IsError: res.IsError, Notes: append(notes, azureTruncatedNote(res)...), Audit: audit}, nil
	}
	audit["entries"], audit["operations"] = entries, len(ans.Operations)
	raw, err := json.Marshal(ans)
	if err != nil {
		return CallResult{Audit: audit}, err
	}

	notes = append(notes, azureActivityMergedNote, azureActivityWindowNote(hours, match != "", len(ans.Operations), found, entries >= fetch))
	if len(ans.Operations) == 0 {
		notes = append(notes, azureActivityEmptyNote)
	}
	for _, op := range ans.Operations {
		if len(op.Request) > 0 && strings.HasSuffix(strings.ToLower(op.Operation), "/write") {
			notes = append(notes, azureActivityPreviousNote)
			break
		}
	}
	return CallResult{Text: string(raw), Notes: notes, Audit: audit, Digest: azureActivityDigest(ans)}, nil
}

// azureActivityWindowNote says what the answer covers: its window, how many
// operations it holds and what it left out. It names nothing the model or
// the provider wrote.
func azureActivityWindowNote(hours float64, matched bool, kept, found int, readAll bool) string {
	span := strconv.FormatFloat(hours, 'f', -1, 64) + " hours"
	if hours >= 48 && hours == float64(int(hours)) && int(hours)%24 == 0 {
		span = strconv.Itoa(int(hours)/24) + " days"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This answer covers the last %s of this resource's activity log and holds %d operations", span, kept)
	if matched {
		b.WriteString(" that match what was asked")
	}
	if found > kept {
		fmt.Fprintf(&b, ", the newest %d of %d: raise top for the older ones", kept, found)
	}
	b.WriteString(".")
	if readAll {
		b.WriteString(" The window held more entries than KubeBolt reads in one call, so the oldest in it were not reached: narrow it with match or fewer hours.")
	}
	if hours < azureActivityRetainedHours {
		b.WriteString(" The activity log keeps 90 days (hours up to 2160).")
	}
	return b.String()
}

// azureCompactActivity turns the server's answer into one row per operation,
// newest first, the top that match. found is how many operations matched
// before top; ok is false when the text is not an activity log answer.
func azureCompactActivity(text, match string, top int) (ans azureActivityAnswer, entries, found int, ok bool) {
	var env struct {
		Status  int `json:"status"`
		Results *struct {
			ActivityLogs []azureActivityEntry `json:"activityLogs"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil || env.Results == nil || (env.Status != 0 && env.Status != 200) {
		return ans, 0, 0, false
	}
	logs := env.Results.ActivityLogs
	ans.Operations = []*azureActivityOp{}
	if len(logs) == 0 {
		return ans, 0, 0, true
	}

	base := azureActivityBase(logs)
	ans.Resource = base
	if m := azureGroupInID.FindStringSubmatch(base); m != nil {
		ans.ResourceGroup = m[1]
	}

	byKey := map[string]*azureActivityOp{}
	var ops []*azureActivityOp
	for i, e := range logs {
		key := strings.Join([]string{e.CorrelationID, e.OperationName.Value, strings.ToLower(e.ResourceID)}, "|")
		if e.CorrelationID == "" {
			key = "#" + strconv.Itoa(i)
		}
		op := byKey[key]
		if op == nil {
			op = &azureActivityOp{Operation: e.OperationName.Value, Category: e.Category.Value}
			if e.OperationName.LocalizedValue != "" && e.OperationName.LocalizedValue != e.OperationName.Value {
				op.Title = e.OperationName.LocalizedValue
			}
			lower, lowerBase := strings.ToLower(e.ResourceID), strings.ToLower(base)
			switch {
			case base == "" || lower == lowerBase:
			case strings.HasPrefix(lower, lowerBase+"/"):
				op.On = e.ResourceID[len(base)+1:]
			default:
				op.ResourceID = e.ResourceID
			}
			byKey[key] = op
			ops = append(ops, op)
		}
		op.add(e)
	}

	sort.SliceStable(ops, func(i, j int) bool { return ops[i].last.After(ops[j].last) })
	for _, op := range ops {
		if match != "" && !op.matches(match) {
			continue
		}
		found++
		if len(ans.Operations) < top {
			op.finish()
			ans.Operations = append(ans.Operations, op)
		}
	}
	return ans, len(logs), found, true
}

// azureGroupInID reads the resource group out of a resource id.
var azureGroupInID = regexp.MustCompile(`(?i)/resourceGroups/([^/]+)`)

// azureActivityBase is the resource the log was read for: the shortest id
// every other entry's id starts with, in the spelling that is not all upper
// case (Resource Health writes ids upper case). "" when there is none.
func azureActivityBase(logs []azureActivityEntry) string {
	base := ""
	for _, e := range logs {
		id := e.ResourceID
		if id == "" {
			continue
		}
		if base == "" || len(id) < len(base) || (len(id) == len(base) && base == strings.ToUpper(base) && id != strings.ToUpper(id)) {
			base = id
		}
	}
	lowerBase := strings.ToLower(base)
	for _, e := range logs {
		lower := strings.ToLower(e.ResourceID)
		if lower != "" && lower != lowerBase && !strings.HasPrefix(lower, lowerBase+"/") {
			return ""
		}
	}
	return base
}

func (op *azureActivityOp) add(e azureActivityEntry) {
	at, err := time.Parse(time.RFC3339Nano, e.EventTimestamp)
	if err != nil {
		at = time.Time{}
	}
	if !at.IsZero() && (op.first.IsZero() || at.Before(op.first)) {
		op.first = at
	}
	if op.last.IsZero() || at.After(op.last) {
		op.last = at
	}
	// The final status is the latest entry's; Started and Accepted give way
	// to one of the same instant that says how it ended.
	if s := e.Status.Value; s != "" && (op.Status == "" || at.After(op.lastStatus) ||
		(at.Equal(op.lastStatus) && (op.Status == "Started" || op.Status == "Accepted"))) {
		op.Status, op.lastStatus = s, at
	}
	if op.Caller == "" && e.Caller != nil {
		op.Caller = *e.Caller
	}
	if e.Level != "" && !strings.EqualFold(e.Level, "Informational") {
		op.Level = e.Level
	}
	if body, ok := azureString(e.Properties["requestbody"]); ok && len(op.Request) == 0 {
		op.Request = azureActivityRequest(body)
	}
	if strings.EqualFold(e.Status.Value, "Failed") {
		if msg, ok := azureString(e.Properties["statusMessage"]); ok {
			op.Error = azureClip(msg, azureActivityErrorMax)
		}
	}
	if strings.EqualFold(e.Category.Value, "ResourceHealth") {
		for _, k := range []string{"title", "details", "currentHealthStatus", "previousHealthStatus", "type", "cause"} {
			if v, ok := azureString(e.Properties[k]); ok && v != "" {
				if op.Health == nil {
					op.Health = map[string]string{}
				}
				op.Health[k] = v
			}
		}
	}
}

// finish writes the times: started, and ended when the operation took time.
func (op *azureActivityOp) finish() {
	if !op.first.IsZero() {
		op.Started = op.first.UTC().Format(time.RFC3339)
	}
	if !op.last.IsZero() && op.last.Sub(op.first) >= time.Second {
		op.Ended = op.last.UTC().Format(time.RFC3339)
	}
}

func (op *azureActivityOp) matches(text string) bool {
	text = strings.ToLower(text)
	for _, s := range []string{op.Operation, op.On, op.ResourceID, op.Title} {
		if strings.Contains(strings.ToLower(s), text) {
			return true
		}
	}
	return false
}

// azureActivityRequest keeps a request body as JSON when it is JSON and fits,
// otherwise as a clipped string.
func azureActivityRequest(body string) json.RawMessage {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	if len(body) <= azureActivityRequestMax && json.Valid([]byte(body)) {
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(body)); err == nil {
			return json.RawMessage(buf.Bytes())
		}
	}
	return azureJSONString(azureClip(body, azureActivityRequestMax))
}

// azureJSONNumber reads a number the coercion already typed; false when the
// value is absent or not one.
func azureJSONNumber(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}
