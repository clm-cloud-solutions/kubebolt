package copilot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// The digest is the line a tool card shows under its title once the call is
// back: what came back, counted and listed. It never says what a result
// means — "2 warnings: BackOff ×3", not "the pod is crashing" — the model does
// that in its answer, and a digest that interpreted a service would be a
// second, unreviewed source of conclusions.
//
// It is computed here from the whole result, before the executor caps it at
// 32 KB and long before a compaction stubs it, and stored with the result;
// the model never reads it. Times travel as "@" + RFC 3339 so the browser can
// show them in the operator's zone (DigestTime).

// DigestTime marks t for the browser to show in the operator's zone.
func DigestTime(t time.Time) string {
	return "@" + t.UTC().Format(time.RFC3339)
}

// digestTimeString is DigestTime for a time a tool already wrote as text;
// one it cannot read is left out.
func digestTimeString(s string) string {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return DigestTime(t)
		}
	}
	return ""
}

// digestMaxGrep is the longest log filter a digest quotes.
const digestMaxGrep = 24

// digestToolResult returns the digest of a built-in tool's result, or "" for
// a tool without one.
func digestToolResult(name string, args map[string]any, content string) string {
	var v map[string]any
	if json.Unmarshal([]byte(content), &v) != nil {
		return ""
	}
	switch name {
	case "get_events":
		return digestEvents(v)
	case "get_workload_pods":
		return digestPods(asList(v["pods"]))
	case "list_resources":
		return digestList(v, stringArg(args, "type"))
	case "get_pod_logs":
		return digestLogs(v)
	case "get_insights":
		return digestInsights(asList(v["items"]))
	case "query_metrics":
		return digestMetrics(v)
	case "get_cluster_overview":
		return digestOverview(v)
	case "get_recent_deploys":
		return digestDeploys(asList(v["deploys"]))
	case "get_resource_yaml":
		return digestYAML(v)
	case "get_resource_describe":
		return digestDescribe(str(v["describe"]))
	case "get_topology":
		return digestTopology(v)
	case "get_external_connections":
		return digestExternal(v)
	}
	return ""
}

func digestEvents(v map[string]any) string {
	items := asList(v["items"])
	if len(items) == 0 {
		return "no events"
	}
	warnings := 0
	reasons := counter{}
	for _, it := range items {
		if str(it["type"]) != "Warning" {
			continue
		}
		warnings++
		n := num(it["count"])
		if n < 1 {
			n = 1
		}
		reasons.add(str(it["reason"]), n)
	}
	out := []string{plural(len(items), "event", "events")}
	if warnings > 0 {
		out = append(out, plural(warnings, "warning", "warnings")+": "+reasons.mostFirst(3))
	}
	return strings.Join(out, " · ")
}

func digestPods(pods []map[string]any) string {
	if len(pods) == 0 {
		return "no pods"
	}
	notReady, restarts := 0, 0
	statuses := counter{}
	for _, p := range pods {
		var ready, total int
		if _, err := fmt.Sscanf(str(p["ready"]), "%d/%d", &ready, &total); err == nil && ready < total {
			notReady++
		}
		restarts += num(p["restarts"])
		if s := str(p["status"]); s != "" && s != "Running" {
			statuses.add(s, 1)
		}
	}
	out := []string{plural(len(pods), "pod", "pods")}
	if notReady > 0 {
		out = append(out, fmt.Sprintf("%d not ready", notReady))
	}
	if restarts > 0 {
		out = append(out, plural(restarts, "restart", "restarts"))
	}
	if len(statuses) > 0 {
		out = append(out, statuses.mostFirst(3))
	}
	return strings.Join(out, " · ")
}

func digestList(v map[string]any, kind string) string {
	if b, _ := v["notInstalled"].(bool); b {
		return "not installed"
	}
	if k := str(v["kind"]); k != "" {
		kind = k
	}
	items := asList(v["items"])
	total := num(v["total"])
	if total < len(items) {
		total = len(items)
	}
	if total == 0 {
		return "none"
	}
	head := fmt.Sprintf("%d %s", total, kind)
	if len(items) < total {
		head = fmt.Sprintf("%d of %d %s", len(items), total, kind)
	}
	statuses := counter{}
	for _, it := range items {
		if s := str(it["status"]); s != "" {
			statuses.add(s, 1)
		}
	}
	if len(statuses) > 1 {
		return head + " · " + statuses.mostFirst(3)
	}
	return head
}

func digestLogs(v map[string]any) string {
	returned, original := num(v["returnedLines"]), num(v["originalLines"])
	var out []string
	if grep := str(v["grep"]); grep != "" {
		// A long pattern ("error|timeout|refused|db|…") would fill the line
		// and hide the burst after it; the card's input shows it whole.
		what := fmt.Sprintf("%q", grep)
		if len(grep) > digestMaxGrep {
			what = "the filter"
		}
		out = append(out, fmt.Sprintf("%d of %d lines match %s", returned, original, what))
	} else {
		out = append(out, plural(returned, "line", "lines"))
	}
	if bursts, ok := v["bursts"].(map[string]any); ok {
		items := asList(bursts["items"])
		switch {
		case len(items) == 1:
			from, to := digestTimeString(str(items[0]["from"])), digestTimeString(str(items[0]["to"]))
			if from != "" && to != "" {
				out = append(out, "burst "+from+"–"+to)
			} else {
				out = append(out, "1 burst")
			}
		case len(items) > 1:
			out = append(out, fmt.Sprintf("%d bursts", len(items)))
		}
	}
	if denials := asList(v["awsDenials"]); len(denials) > 0 {
		out = append(out, plural(len(denials), "AWS denial", "AWS denials"))
	}
	return strings.Join(out, " · ")
}

func digestInsights(items []map[string]any) string {
	if len(items) == 0 {
		return "no insights"
	}
	bySeverity := counter{}
	for _, it := range items {
		bySeverity.add(str(it["severity"]), 1)
	}
	out := []string{plural(len(items), "insight", "insights")}
	for _, sev := range []string{"critical", "warning", "info"} {
		if n := bySeverity[sev]; n > 0 {
			out = append(out, fmt.Sprintf("%d %s", n, sev))
		}
	}
	return strings.Join(out, " · ")
}

func digestMetrics(v map[string]any) string {
	series := asList(v["series"])
	if len(series) == 0 {
		return "no data"
	}
	out := plural(len(series), "series", "series")
	if r := str(v["range"]); r != "" {
		out += " · " + r
	}
	return out
}

func digestOverview(v map[string]any) string {
	var out []string
	if nodes, ok := v["nodes"].(map[string]any); ok {
		out = append(out, fmt.Sprintf("nodes %d/%d ready", num(nodes["ready"]), num(nodes["total"])))
	}
	if pods, ok := v["pods"].(map[string]any); ok {
		live := num(pods["total"]) - num(pods["succeeded"])
		out = append(out, fmt.Sprintf("pods %d/%d ready", num(pods["ready"]), live))
	}
	return strings.Join(out, " · ")
}

func digestDeploys(deploys []map[string]any) string {
	if len(deploys) == 0 {
		return "no deploys"
	}
	latest := deploys[0]
	for _, d := range deploys[1:] {
		if str(d["deployedAt"]) > str(latest["deployedAt"]) {
			latest = d
		}
	}
	out := plural(len(deploys), "deploy", "deploys") + " · latest " + str(latest["resource"])
	if at := digestTimeString(str(latest["deployedAt"])); at != "" {
		out += " " + at
	}
	return out
}

// digestYAML: the object's kind, its phase or replicas, and its newest write
// (lastWrites comes newest first).
func digestYAML(v map[string]any) string {
	var obj map[string]any
	if yaml.Unmarshal([]byte(str(v["yaml"])), &obj) != nil {
		return ""
	}
	var out []string
	if k := str(obj["kind"]); k != "" {
		out = append(out, k)
	}
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)
	if p := str(status["phase"]); p != "" {
		out = append(out, p)
	}
	if want, ok := spec["replicas"].(float64); ok {
		out = append(out, fmt.Sprintf("replicas %d/%d ready", num(status["readyReplicas"]), int(want)))
	} else if want, ok := status["desiredNumberScheduled"].(float64); ok {
		out = append(out, fmt.Sprintf("pods %d/%d ready", num(status["numberReady"]), int(want)))
	}
	if writes := asList(v["lastWrites"]); len(writes) > 0 {
		w := "last write " + str(writes[0]["manager"])
		if str(writes[0]["subresource"]) == "status" {
			w += " (status)"
		}
		if at := digestTimeString(str(writes[0]["time"])); at != "" {
			w += " " + at
		}
		out = append(out, w)
	}
	return strings.Join(out, " · ")
}

var (
	describeStatusRE   = regexp.MustCompile(`(?m)^Status:\s+(\S.*?)\s*$`)
	describeReplicasRE = regexp.MustCompile(`(?m)^Replicas:\s+(\d+) desired \|.*?(\d+) available`)
	describeEventsRE   = regexp.MustCompile(`(?m)^Events:(.*)$`)
	describeColumnsRE  = regexp.MustCompile(`\s{2,}`)
	describeCountRE    = regexp.MustCompile(`\(x(\d+) over`)
)

// digestDescribe reads kubectl describe's text: the object's top-level
// Status, a Deployment's available replicas, and its Events table counted
// like get_events ("(x12 over 5m)" is twelve).
func digestDescribe(text string) string {
	var out []string
	if m := describeStatusRE.FindStringSubmatch(text); m != nil {
		out = append(out, "status "+m[1])
	}
	if m := describeReplicasRE.FindStringSubmatch(text); m != nil {
		out = append(out, fmt.Sprintf("replicas %s/%s available", m[2], m[1]))
	}
	loc := describeEventsRE.FindStringSubmatchIndex(text)
	if loc == nil {
		return strings.Join(out, " · ")
	}
	if strings.TrimSpace(text[loc[2]:loc[3]]) == "<none>" {
		return strings.Join(append(out, "no events"), " · ")
	}
	events, warnings := 0, 0
	reasons := counter{}
	for _, line := range strings.Split(text[loc[1]:], "\n")[1:] {
		if strings.TrimSpace(line) == "" || !strings.HasPrefix(line, " ") {
			break
		}
		cols := describeColumnsRE.Split(strings.TrimSpace(line), -1)
		if len(cols) < 3 || cols[0] == "Type" || strings.HasPrefix(cols[0], "--") {
			continue
		}
		events++
		if cols[0] != "Warning" {
			continue
		}
		warnings++
		n := 1
		if m := describeCountRE.FindStringSubmatch(cols[2]); m != nil {
			fmt.Sscanf(m[1], "%d", &n)
		}
		reasons.add(cols[1], n)
	}
	if events == 0 {
		return strings.Join(append(out, "no events"), " · ")
	}
	out = append(out, plural(events, "event", "events"))
	if warnings > 0 {
		out = append(out, plural(warnings, "warning", "warnings")+": "+reasons.mostFirst(3))
	}
	return strings.Join(out, " · ")
}

// digestTopology counts the graph's resources by kind, most first.
func digestTopology(v map[string]any) string {
	nodes := asList(v["nodes"])
	if len(nodes) == 0 {
		return "no resources"
	}
	kinds := counter{}
	for _, n := range nodes {
		kinds.add(str(n["type"]), 1)
	}
	edges, _ := v["edges"].([]any)
	return fmt.Sprintf("%s · %s · %s",
		plural(len(nodes), "resource", "resources"), plural(len(edges), "link", "links"), kinds.mostFirst(3))
}

// digestExternal: where the cluster's traffic went (busiest first, cluster
// plumbing counted apart), the drops among it, the hosts the workload's
// configuration names, its entry points and the window read.
func digestExternal(v map[string]any) string {
	var out []string
	sourcePresent, _ := v["sourcePresent"].(bool)
	uidKnown, _ := v["clusterUIDKnown"].(bool)
	switch {
	case !sourcePresent:
		out = append(out, "no flow data")
	case !uidKnown:
		out = append(out, "cluster identity not read yet")
	default:
		dests := asList(v["destinations"])
		total := len(dests) + num(v["omittedDestinations"])
		var names []string
		infra, dropped := 0, 0
		for _, d := range dests {
			if strings.Contains(str(d["verdict"]), "dropped") {
				dropped++
			}
			if b, _ := d["infrastructure"].(bool); b {
				infra++
				continue
			}
			name := str(d["fqdn"])
			if name == "" {
				name = str(d["ip"])
			}
			names = append(names, name)
		}
		switch {
		case total == 0:
			out = append(out, "no destinations")
		case len(names) == 0:
			out = append(out, plural(total, "destination", "destinations"))
		default:
			head := plural(total, "destination", "destinations") + ": " + strings.Join(names[:min(len(names), 3)], ", ")
			if more := total - infra - min(len(names), 3); more > 0 {
				head += fmt.Sprintf(", +%d more", more)
			}
			out = append(out, head)
		}
		if infra > 0 {
			out = append(out, fmt.Sprintf("%d cluster infrastructure", infra))
		}
		if dropped > 0 {
			out = append(out, fmt.Sprintf("%d dropped", dropped))
		}
	}
	hosts := 0
	for _, h := range asList(v["dependencyHosts"]) {
		if str(h["host"]) != "" {
			hosts++
		}
	}
	if hosts > 0 {
		out = append(out, plural(hosts, "host", "hosts")+" in its configuration")
	}
	if entry := len(asList(v["entry"])) + num(v["omittedEntry"]); entry > 0 {
		out = append(out, plural(entry, "entry point", "entry points"))
	}
	if w := num(v["windowMinutes"]); w > 0 && sourcePresent && uidKnown {
		if w%60 == 0 {
			out = append(out, fmt.Sprintf("%dh", w/60))
		} else {
			out = append(out, fmt.Sprintf("%dm", w))
		}
	}
	return strings.Join(out, " · ")
}

// ----- small helpers -----

// counter counts names and lists them most frequent first, a name seen more
// than once with its count: "BackOff ×3, Unhealthy".
type counter map[string]int

func (c counter) add(name string, n int) {
	if name == "" {
		name = "(none)"
	}
	c[name] += n
}

func (c counter) mostFirst(max int) string {
	names := make([]string, 0, len(c))
	for n := range c {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if c[names[i]] != c[names[j]] {
			return c[names[i]] > c[names[j]]
		}
		return names[i] < names[j]
	})
	out := make([]string, 0, max+1)
	for i, n := range names {
		if i == max {
			out = append(out, fmt.Sprintf("+%d more", len(names)-max))
			break
		}
		if c[n] > 1 {
			out = append(out, fmt.Sprintf("%s ×%d", n, c[n]))
		} else {
			out = append(out, n)
		}
	}
	return strings.Join(out, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func asList(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
