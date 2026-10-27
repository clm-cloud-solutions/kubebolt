package mcpcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

// Azure Monitor metric queries, made the way each metric allows.
//
// A model asks monitor_metrics_query for several metrics at once, at one
// interval, with no dimension filter. Azure answers 400 when the metrics do
// not share a time grain (a PostgreSQL server's max_connections is published
// every 30 minutes or more, its active connections every minute), and without
// a filter it folds a metric's dimensions into one unlabeled series (sessions
// by wait event type came back as "10": it was Lock=10). Both answers are in
// the metric definitions Azure publishes for every resource type — each
// metric's allowedIntervals and dimensions — so KubeBolt reads them and
// queries each metric on its own: at the interval asked for when the metric
// offers it, otherwise the nearest coarser one it offers; split by every
// dimension it has when the model gave no filter. Nothing here knows a
// resource type.

const (
	azureMetricsQuery = "monitor_metrics_query"
	azureMetricsDefs  = "monitor_metrics_definitions"

	// azureMetricDefsTTL keeps a resource's metric definitions: they change
	// with the resource type's release, not between two questions.
	azureMetricDefsTTL = time.Hour
	// azureMetricDefsLimit asks for every definition at once: a PostgreSQL
	// flexible server has 74, the default answer 10.
	azureMetricDefsLimit = 500
	// azureMetricSplitMax bounds one metric's split answer. A dimension with
	// many values (a queue per entity) splits into many series; past this the
	// unsplit series is what fits the tool output.
	azureMetricSplitMax = 16 << 10
)

// azureMetricsNote is the one note a metrics query can carry. It names no
// metric or dimension: those are the model's words or the provider's, and a
// note is KubeBolt's.
const azureMetricsNote = "KubeBolt queried each metric on its own: at the interval asked for when the metric offers it, " +
	"otherwise at the nearest coarser one it offers (each series states its interval), and split by the dimensions the metric has " +
	"when no filter was given (each series names its dimension values)."

// azureMetricsSplitDroppedNote says a split answer was too large and the
// metric came back unsplit.
const azureMetricsSplitDroppedNote = "A metric whose split answer was too large came back as one unsplit series; " +
	"query it with a filter on one dimension value to see that value alone."

// azureMetricDef is what a definition says about querying one metric.
type azureMetricDef struct {
	intervals []string // ISO 8601, as Azure lists them
	dims      []string
}

// azureMetricDefCache holds definitions per resource. The binding is
// immutable after Bind; this is the one part that is not, and it carries its
// own lock.
type azureMetricDefCache struct {
	mu      sync.Mutex
	entries map[string]azureMetricDefEntry
}

type azureMetricDefEntry struct {
	at   time.Time
	defs map[string]azureMetricDef // metric name, lower-case
}

func (c *azureMetricDefCache) get(key string, now time.Time) (map[string]azureMetricDef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.Sub(e.at) > azureMetricDefsTTL {
		return nil, false
	}
	return e.defs, true
}

func (c *azureMetricDefCache) put(key string, defs map[string]azureMetricDef, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]azureMetricDefEntry{}
	}
	for k, e := range c.entries {
		if now.Sub(e.at) > azureMetricDefsTTL {
			delete(c.entries, k)
		}
	}
	c.entries[key] = azureMetricDefEntry{at: now, defs: defs}
}

// azureMetricResourceKeys are the parameters both metrics commands take to
// name the resource: they key the definitions and are copied into their call.
var azureMetricResourceKeys = []string{"resource", "resource-group", "resource-type", "metric-namespace", "subscription", "tenant"}

// callMetrics runs a prepared monitor_metrics_query the way each of its
// metrics allows. With the definitions read and nothing to change — every
// metric offers the interval asked for, and none has a dimension to split or
// the model gave its own filter — the query goes as asked, in one call.
// Otherwise each metric goes on its own, with its interval and split. With no
// definitions, the query goes as asked and only Azure's time-grain refusal
// sends it again, one metric at a time at a grain Azure listed. prepared has
// passed every check prepare runs; what is added to it here is a single name
// out of its own metric-names, an interval Azure listed, and a filter built
// from dimension names that pass azureDimName.
func (b *azureBinding) callMetrics(ctx context.Context, t *azureTool, prepared map[string]json.RawMessage, notes []string, audit map[string]any) (CallResult, error) {
	names := azureMetricNames(prepared["metric-names"])
	for _, n := range names {
		if azureValueReparsed(azureJSONString(n)) {
			return azureDenial(audit, "\"metric-names\" cannot hold a name starting with '@' or '-': the Azure MCP Server would read it as a file or an option, not as a value."), nil
		}
	}
	audit["metrics"] = len(names)
	requested, _ := azureString(prepared["interval"])
	requested = strings.TrimSpace(requested)
	_, ownFilter := prepared["filter"]

	// The plan: per metric, the interval to set ("" keeps the request's) and
	// the filter to add ("" adds none).
	defs := b.metricDefs(ctx, t, prepared)
	type plan struct{ name, interval, filter string }
	plans := make([]plan, 0, len(names))
	change := false
	for _, n := range names {
		def := defs[strings.ToLower(n)]
		p := plan{name: n, interval: azureMetricInterval(requested, def.intervals)}
		if !ownFilter {
			p.filter = azureSplitFilter(def.dims)
		}
		change = change || p.interval != "" || p.filter != ""
		plans = append(plans, p)
	}

	if !change {
		res, err := b.forward(ctx, t.namespace, azureMetricsQuery, prepared)
		if err != nil {
			return CallResult{Audit: audit}, err
		}
		text := azureResultText(res)
		failed := azureMetricFailed(res, text)
		// Azure's own refusal is the fallback when the definitions said
		// nothing (or could not be read): one metric at a time, each at a
		// grain the refusal lists.
		if !failed || len(names) == 0 || azureGrainRetry(text, requested) == "" {
			if failed && azureGroupNotFound(text) {
				notes = append(notes, azureGroupNotFoundNote)
			}
			return CallResult{Text: text, IsError: failed, Notes: append(notes, azureTruncatedNote(res)...), Audit: audit}, nil
		}
	}

	var items []json.RawMessage
	var failures []map[string]string
	adjusted, splitDropped := false, false
	for _, pl := range plans {
		p := make(map[string]json.RawMessage, len(prepared)+2)
		for k, v := range prepared {
			p[k] = v
		}
		p["metric-names"] = azureJSONString(pl.name)
		if pl.interval != "" {
			p["interval"] = azureJSONString(pl.interval)
			adjusted = true
		}
		if pl.filter != "" {
			p["filter"] = azureJSONString(pl.filter)
			adjusted = true
		}
		res, err := b.forward(ctx, t.namespace, azureMetricsQuery, p)
		if err != nil {
			return CallResult{Audit: audit}, err
		}
		text := azureResultText(res)
		if azureMetricFailed(res, text) {
			asked := requested
			if pl.interval != "" {
				asked = pl.interval
			}
			if iv := azureGrainRetry(text, asked); iv != "" {
				p["interval"] = azureJSONString(iv)
				adjusted = true
				if res, err = b.forward(ctx, t.namespace, azureMetricsQuery, p); err != nil {
					return CallResult{Audit: audit}, err
				}
				text = azureResultText(res)
			}
		}
		// A split Azure refuses for any other reason than the grain, or one
		// too large for the tool output, comes back unsplit: the split is
		// KubeBolt's addition and must never cost the metric itself.
		if pl.filter != "" && (azureMetricFailed(res, text) || len(text) > azureMetricSplitMax) {
			delete(p, "filter")
			if res, err = b.forward(ctx, t.namespace, azureMetricsQuery, p); err != nil {
				return CallResult{Audit: audit}, err
			}
			text = azureResultText(res)
			splitDropped = true
		}
		if azureMetricFailed(res, text) {
			failures = append(failures, map[string]string{"metric": pl.name, "error": azureClip(text, 600)})
			continue
		}
		if got, ok := azureMetricItems(text); ok {
			items = append(items, got...)
			continue
		}
		// A success in a shape this does not read goes through as it is.
		if json.Valid([]byte(text)) {
			items = append(items, json.RawMessage(text))
		} else {
			items = append(items, azureJSONString(text))
		}
	}

	merged := map[string]any{"status": 200, "message": "Success", "results": map[string]any{"results": items}}
	if len(failures) > 0 {
		merged["failed"] = failures
		if len(items) == 0 {
			merged["status"], merged["message"] = 400, "No metric could be read"
		}
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return CallResult{Audit: audit}, err
	}
	for _, f := range failures {
		if azureGroupNotFound(f["error"]) {
			notes = append(notes, azureGroupNotFoundNote)
			break
		}
	}
	if adjusted {
		notes = append(notes, azureMetricsNote)
	}
	if splitDropped {
		notes = append(notes, azureMetricsSplitDroppedNote)
	}
	out := CallResult{Text: string(raw), IsError: len(items) == 0, Notes: notes, Audit: audit}
	if len(items) > 0 {
		out.Digest = azureMetricsDigest(items, len(failures))
	}
	return out, nil
}

// forward sends one router command to the sidecar with KubeBolt's own intent.
func (b *azureBinding) forward(ctx context.Context, namespace, cmd string, params map[string]json.RawMessage) (*mcpclient.CallResult, error) {
	raw, err := json.Marshal(azureEnvelope{
		Intent:     "read-only " + cmd + " requested through KubeBolt",
		Command:    cmd,
		Parameters: params,
	})
	if err != nil {
		return nil, err
	}
	res, err := b.client.CallTool(ctx, namespace, raw)
	if err != nil {
		return nil, fmt.Errorf("azure %s: %w", cmd, err)
	}
	return res, nil
}

// metricDefs reads the resource's metric definitions, or answers from the
// cache; nil when they cannot be read — the query then goes as asked, with
// Azure's own refusal as the fallback.
func (b *azureBinding) metricDefs(ctx context.Context, t *azureTool, prepared map[string]json.RawMessage) map[string]azureMetricDef {
	if !t.allowed[azureMetricsDefs] || b.metricDefCache == nil {
		return nil
	}
	sch := t.router.commands[azureMetricsDefs]
	p := map[string]json.RawMessage{}
	var key strings.Builder
	for _, k := range azureMetricResourceKeys {
		v, ok := prepared[k]
		if !ok {
			continue
		}
		if _, declared := sch.props[k]; !declared {
			continue
		}
		p[k] = v
		key.WriteString(k + "=" + strings.ToLower(string(v)) + "\x00")
	}
	if _, ok := p["resource"]; !ok {
		return nil
	}
	now := time.Now()
	if defs, ok := b.metricDefCache.get(key.String(), now); ok {
		return defs
	}
	if _, ok := sch.props["limit"]; ok {
		p["limit"] = json.RawMessage(strconv.Itoa(azureMetricDefsLimit))
	}
	res, err := b.forward(ctx, t.namespace, azureMetricsDefs, p)
	if err != nil || res.IsError {
		return nil
	}
	defs := azureParseMetricDefs(azureResultText(res))
	if len(defs) == 0 {
		return nil
	}
	b.metricDefCache.put(key.String(), defs, now)
	return defs
}

// azureParseMetricDefs reads the definitions answer: {"results":{"results":
// [...]}} as the server answers, or {"results":[...]}.
func azureParseMetricDefs(text string) map[string]azureMetricDef {
	var body struct {
		Results json.RawMessage `json:"results"`
	}
	if json.Unmarshal([]byte(text), &body) != nil || len(body.Results) == 0 {
		return nil
	}
	list := azureResultList(body.Results)
	out := map[string]azureMetricDef{}
	for _, raw := range list {
		var d struct {
			Name             string   `json:"name"`
			AllowedIntervals []string `json:"allowedIntervals"`
			Dimensions       []string `json:"dimensions"`
		}
		if json.Unmarshal(raw, &d) != nil || d.Name == "" {
			continue
		}
		var def azureMetricDef
		for _, iv := range d.AllowedIntervals {
			if _, ok := azureISOMinutes(iv); ok {
				def.intervals = append(def.intervals, iv)
			}
		}
		for _, dim := range d.Dimensions {
			if azureDimName(dim) {
				def.dims = append(def.dims, dim)
			}
		}
		out[strings.ToLower(d.Name)] = def
	}
	return out
}

// azureResultList is the list inside a results value: the value itself, or
// its own "results".
func azureResultList(raw json.RawMessage) []json.RawMessage {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var inner struct {
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal(raw, &inner) == nil {
		return inner.Results
	}
	return nil
}

// azureMetricItems is the per-metric entries of a query answer.
func azureMetricItems(text string) ([]json.RawMessage, bool) {
	var body struct {
		Results json.RawMessage `json:"results"`
	}
	if json.Unmarshal([]byte(text), &body) != nil || len(body.Results) == 0 {
		return nil, false
	}
	list := azureResultList(body.Results)
	return list, list != nil
}

// azureMetricFailed reads a query answer as a failure: the server's error
// flag, or a status of 400 or more in its body.
func azureMetricFailed(res *mcpclient.CallResult, text string) bool {
	if res.IsError {
		return true
	}
	var body struct {
		Status int `json:"status"`
	}
	return json.Unmarshal([]byte(text), &body) == nil && body.Status >= 400
}

// azureMetricNames splits metric-names on commas, trimmed, without blanks or
// repeats, in the order asked.
func azureMetricNames(raw json.RawMessage) []string {
	s, ok := azureString(raw)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range strings.Split(s, ",") {
		n = strings.TrimSpace(n)
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	return out
}

// azureMetricInterval is the interval to query a metric at, or "" to leave
// the request as it is. With no allowed intervals known, nothing changes.
// Asked-for and offered: unchanged. Asked-for and not offered: the smallest
// offered one at least as coarse, else the coarsest offered. Not asked for:
// Azure's default (PT1M) when offered, else the finest offered.
func azureMetricInterval(requested string, allowed []string) string {
	if len(allowed) == 0 {
		return ""
	}
	type iv struct {
		iso string
		min int
	}
	var offered []iv
	for _, a := range allowed {
		if m, ok := azureISOMinutes(a); ok {
			offered = append(offered, iv{a, m})
		}
	}
	if len(offered) == 0 {
		return ""
	}
	sort.Slice(offered, func(i, j int) bool { return offered[i].min < offered[j].min })
	want := 1 // PT1M, Azure's default grain
	if requested != "" {
		m, ok := azureISOMinutes(requested)
		if !ok {
			return "" // not a duration KubeBolt reads: Azure answers for it
		}
		want = m
	}
	for _, o := range offered {
		if o.min == want {
			return "" // offered as asked (or Azure's default is): unchanged
		}
	}
	for _, o := range offered {
		if o.min >= want {
			return o.iso
		}
	}
	return offered[len(offered)-1].iso
}

// azureGrainRetry reads Azure's time-grain refusal ("Commonly allowed time
// grains: 00:30:00,01:00:00,... can not support requested time grain") and
// returns the nearest grain at least as coarse as the one asked for, in ISO
// 8601; "" when the text is not that refusal.
func azureGrainRetry(text, requested string) string {
	if !strings.Contains(text, "can not support requested time grain") {
		return ""
	}
	m := azureGrainListRE.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	var allowed []string
	for _, g := range strings.Split(m[1], ",") {
		if mins, ok := azureTimeSpanMinutes(strings.TrimSpace(g)); ok {
			allowed = append(allowed, azureISOFromMinutes(mins))
		}
	}
	if requested == "" {
		requested = "PT1M"
	}
	return azureMetricInterval(requested, allowed)
}

var azureGrainListRE = regexp.MustCompile(`allowed time grains:\s*([0-9.:,\s]+?)\s*between`)

// azureISODurationRE reads the durations Azure lists: days, hours, minutes.
var azureISODurationRE = regexp.MustCompile(`^P(?:([0-9]+)D)?(?:T(?:([0-9]+)H)?(?:([0-9]+)M)?)?$`)

func azureISOMinutes(s string) (int, bool) {
	m := azureISODurationRE.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil || (m[1] == "" && m[2] == "" && m[3] == "") {
		return 0, false
	}
	total := 0
	for i, mult := range []int{1440, 60, 1} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return 0, false
		}
		total += n * mult
	}
	return total, total > 0
}

// azureTimeSpanMinutes reads a .NET TimeSpan as Azure writes it: hh:mm:ss or
// d.hh:mm:ss.
func azureTimeSpanMinutes(s string) (int, bool) {
	days := 0
	if i := strings.Index(s, "."); i >= 0 && strings.Count(s, ":") == 2 && i < strings.Index(s, ":") {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days, s = d, s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, false
	}
	total := days*1440 + h*60 + mi
	return total, total > 0
}

func azureISOFromMinutes(m int) string {
	switch {
	case m%1440 == 0:
		return "P" + strconv.Itoa(m/1440) + "D"
	case m%60 == 0:
		return "PT" + strconv.Itoa(m/60) + "H"
	}
	return "PT" + strconv.Itoa(m) + "M"
}

// azureSplitFilter asks for every value of each dimension: "D1 eq '*' and
// D2 eq '*'"; "" with none.
func azureSplitFilter(dims []string) string {
	var parts []string
	for _, d := range dims {
		if azureDimName(d) {
			parts = append(parts, d+" eq '*'")
		}
	}
	return strings.Join(parts, " and ")
}

// azureDimName admits a dimension name that can go into an OData filter as
// it is. Dimension names come from the provider's definitions.
var azureDimNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.]{0,63}$`)

func azureDimName(s string) bool { return azureDimNameRE.MatchString(s) }

// azureCommaListKeys are string parameters the server reads as a
// comma-separated list: a model hands them over as a JSON list.
var azureCommaListKeys = map[string]bool{"metric-names": true}

// azureJoinCommaList turns a list of names into the comma-separated string
// the server takes. Left as it is — and refused by azureCheckType — unless
// every item is a non-blank string with no comma and nothing the sidecar
// would read as a file or an option.
func azureJoinCommaList(v json.RawMessage) json.RawMessage {
	var list []any
	if json.Unmarshal(v, &list) != nil || len(list) == 0 {
		return v
	}
	names := make([]string, 0, len(list))
	for _, x := range list {
		s, ok := x.(string)
		s = strings.TrimSpace(s)
		if !ok || s == "" || strings.Contains(s, ",") || azureAnyReparsed(s) {
			return v
		}
		names = append(names, s)
	}
	return azureJSONString(strings.Join(names, ","))
}
