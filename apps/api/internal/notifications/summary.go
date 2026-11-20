package notifications

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// summaryView is what every channel's summary shows, computed once so email,
// Slack and Discord count and classify the same way. A burst covers one
// cluster; a digest summary covers every cluster in the digest, one section each.
type summaryView struct {
	Digest        DigestMode // "" for a burst summary
	Cluster       string     // the burst's cluster
	Total         int
	Counts        map[string]int // open insights by severity (critical / warning / info / other)
	Resolved      int            // resolved insights in it
	ResolvedBurst bool           // the burst is of resolutions
	Critical      []SummaryItem  // named one by one, at most maxNamedCritical
	MoreCritical  int            // critical left out of Critical
	Sections      []summarySection
	SentAlone     int
	Span          string // "within 6 seconds, 23:41:05–23:41:11 UTC"
	Partial       bool
	InsightsURL   string
	Lead          string
	Why           string
}

type summarySection struct {
	Cluster string
	Total   int
	Rules   []ruleCount
}

type ruleCount struct {
	Title      string
	Severity   string
	Resolved   bool
	Count      int
	Namespaces int       // distinct namespaces
	Top        []nsCount // the most affected, at most 3
}

type nsCount struct {
	Namespace string
	Count     int
}

// maxNamedCritical is how many critical insights a summary names one by one;
// the rest are counted and linked. Fixed: a critical is never only a number
// unless there are more than this many.
const maxNamedCritical = 10

const resolvedPrefix = "[Resolved] "

func plainTitle(e Event) string {
	return strings.TrimPrefix(e.Insight.Title, resolvedPrefix)
}

func isResolved(e Event) bool {
	return e.Insight.Resolved || strings.HasPrefix(e.Insight.Title, resolvedPrefix)
}

func normSeverity(s string) string {
	switch s = strings.ToLower(s); s {
	case "critical", "warning", "info":
		return s
	}
	return "other"
}

// countByRule groups events by rule (title) and outcome, most severe first,
// then largest first.
func countByRule(events []Event) []ruleCount {
	type key struct {
		title    string
		resolved bool
	}
	idx := map[key]int{}
	var rules []ruleCount
	nsByRule := []map[string]int{}
	for _, e := range events {
		k := key{plainTitle(e), isResolved(e)}
		i, ok := idx[k]
		if !ok {
			i = len(rules)
			idx[k] = i
			rules = append(rules, ruleCount{Title: k.title, Severity: normSeverity(e.Insight.Severity), Resolved: k.resolved})
			nsByRule = append(nsByRule, map[string]int{})
		}
		rules[i].Count++
		if sev := normSeverity(e.Insight.Severity); severityLevel(sev) > severityLevel(rules[i].Severity) {
			rules[i].Severity = sev
		}
		if ns := e.Insight.Namespace; ns != "" {
			nsByRule[i][ns]++
		}
	}
	for i := range rules {
		rules[i].Namespaces = len(nsByRule[i])
		rules[i].Top = topNamespaces(nsByRule[i], 3)
	}
	sort.SliceStable(rules, func(a, b int) bool {
		ra, rb := rules[a], rules[b]
		if ra.Resolved != rb.Resolved {
			return !ra.Resolved
		}
		if la, lb := severityLevel(ra.Severity), severityLevel(rb.Severity); la != lb {
			return la > lb
		}
		if ra.Count != rb.Count {
			return ra.Count > rb.Count
		}
		return ra.Title < rb.Title
	})
	return rules
}

func topNamespaces(ns map[string]int, n int) []nsCount {
	all := make([]nsCount, 0, len(ns))
	for k, v := range ns {
		all = append(all, nsCount{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Count != all[j].Count {
			return all[i].Count > all[j].Count
		}
		return all[i].Namespace < all[j].Namespace
	})
	if len(all) > n {
		all = all[:n]
	}
	return all
}

// tally fills the severity counts, the resolved count and the named criticals.
// In a burst of resolutions the criticals named are the resolved ones; anywhere
// else only open criticals are named.
func (v *summaryView) tally(items []SummaryItem, resolvedBurst bool) {
	v.Counts = map[string]int{}
	for _, it := range items {
		v.Total++
		if isResolved(it.Event) && !resolvedBurst {
			v.Resolved++
			continue
		}
		sev := normSeverity(it.Insight.Severity)
		v.Counts[sev]++
		if sev == "critical" {
			if len(v.Critical) < maxNamedCritical {
				v.Critical = append(v.Critical, it)
			} else {
				v.MoreCritical++
			}
		}
		if it.AlsoSentAlone {
			v.SentAlone++
		}
	}
	if resolvedBurst {
		v.Resolved = v.Total
	}
}

func insightsURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/insights"
}

// spanText says when a burst happened: "within 6 seconds, 23:41:05–23:41:11 UTC".
func spanText(first, last time.Time) string {
	first, last = first.UTC(), last.UTC()
	d := last.Sub(first)
	switch {
	case first.IsZero() || d < time.Second:
		return "at " + last.Format("15:04:05") + " UTC"
	case d < 2*time.Minute:
		return fmt.Sprintf("within %d seconds, %s–%s UTC", int(d.Round(time.Second)/time.Second), first.Format("15:04:05"), last.Format("15:04:05"))
	default:
		return fmt.Sprintf("over %d minutes, %s–%s UTC", int(d.Round(time.Minute)/time.Minute), first.Format("15:04"), last.Format("15:04"))
	}
}

// secondsText: "30 seconds", "60 seconds", "5 minutes" (settings are in
// seconds, so up to two minutes it says seconds).
func secondsText(d time.Duration) string {
	if d >= 2*time.Minute && d%time.Minute == 0 {
		if m := int(d / time.Minute); m == 1 {
			return "1 minute"
		} else {
			return fmt.Sprintf("%d minutes", m)
		}
	}
	return fmt.Sprintf("%d seconds", int(d/time.Second))
}

// viewFromBurst builds the view of one cluster's burst summary.
func viewFromBurst(s Summary) summaryView {
	v := summaryView{
		Cluster:       s.ClusterName,
		ResolvedBurst: s.Resolved,
		Span:          spanText(s.First, s.Last),
		Partial:       s.Partial,
		InsightsURL:   insightsURL(s.BaseURL),
	}
	v.tally(s.Items, s.Resolved)
	events := make([]Event, len(s.Items))
	for i, it := range s.Items {
		events[i] = it.Event
	}
	v.Sections = []summarySection{{Cluster: s.ClusterName, Total: len(events), Rules: countByRule(events)}}

	cfg := s.Config
	verb := "opened"
	if s.Resolved {
		verb = "resolved"
	}
	lead := strings.ToUpper(verb[:1]) + verb[1:] + " " + v.Span + "."
	if k := v.SentAlone; k > 0 {
		lead += fmt.Sprintf(" The first %d went out on their own before the burst was clear; this email counts all %d, so you get %d emails instead of %d.",
			k, v.Total, k+1, v.Total)
	} else {
		lead += fmt.Sprintf(" This email counts all %d.", v.Total)
	}
	v.Lead = lead
	if s.Partial {
		v.Why = fmt.Sprintf("%s is still sending insights: this summary covers %s, and the next one follows when the cluster has been quiet for %s, or after another %s. Change these numbers in Settings › Notifications.",
			s.ClusterName, secondsText(cfg.MaxWait), secondsText(cfg.Quiet), secondsText(cfg.MaxWait))
	} else {
		v.Why = fmt.Sprintf("%s %s %d or more insights within %s, so KubeBolt held the rest and summarised them once the cluster had been quiet for %s. Change these numbers in Settings › Notifications.",
			s.ClusterName, verb, cfg.Threshold, secondsText(cfg.Window), secondsText(cfg.Quiet))
	}
	return v
}

// viewFromDigest builds the view of a digest too long to list.
func viewFromDigest(events []Event, mode DigestMode, baseURL string, listLimit int) summaryView {
	v := summaryView{Digest: mode}
	items := make([]SummaryItem, len(events))
	byCluster := map[string][]Event{}
	var clusters []string
	for i, e := range events {
		items[i] = SummaryItem{Event: e}
		if _, ok := byCluster[e.ClusterName]; !ok {
			clusters = append(clusters, e.ClusterName)
		}
		byCluster[e.ClusterName] = append(byCluster[e.ClusterName], e)
		if v.InsightsURL == "" && e.BaseURL != "" {
			v.InsightsURL = insightsURL(e.BaseURL)
		}
	}
	if v.InsightsURL == "" {
		v.InsightsURL = insightsURL(baseURL)
	}
	v.tally(items, false)
	sort.SliceStable(clusters, func(i, j int) bool { return len(byCluster[clusters[i]]) > len(byCluster[clusters[j]]) })
	for _, c := range clusters {
		v.Sections = append(v.Sections, summarySection{Cluster: c, Total: len(byCluster[c]), Rules: countByRule(byCluster[c])})
	}
	word := "clusters"
	if len(clusters) == 1 {
		word = "cluster"
	}
	v.Lead = fmt.Sprintf("Across %d %s. More than %d, so they are counted by rule instead of listed one by one.", len(clusters), word, listLimit)
	v.Why = fmt.Sprintf("A digest lists insights one by one up to %d; above that it counts them by rule and still names every critical, up to %d. Change this in Settings › Notifications.",
		listLimit, maxNamedCritical)
	return v
}

// severityLine renders "12 critical · 576 warning · 10 info", most severe first.
func (v summaryView) severityLine() string {
	var parts []string
	for _, sev := range []string{"critical", "warning", "info", "other"} {
		if n := v.Counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
		}
	}
	if v.Resolved > 0 && !v.ResolvedBurst {
		parts = append(parts, fmt.Sprintf("%d resolved", v.Resolved))
	}
	return strings.Join(parts, " · ")
}

// headline: "598 new insights on acme-dev", "540 insights resolved on
// acme-dev", "601 insights in the last hour".
func (v summaryView) headline() string {
	switch {
	case v.Digest == DigestDaily:
		return fmt.Sprintf("%d insights in the last day", v.Total)
	case v.Digest != "":
		return fmt.Sprintf("%d insights in the last hour", v.Total)
	case v.ResolvedBurst:
		return fmt.Sprintf("%d insights resolved on %s", v.Total, v.Cluster)
	default:
		return fmt.Sprintf("%d new insights on %s", v.Total, v.Cluster)
	}
}

// namespacesText: "in 131 namespaces · accounts-api (14) · billing-api (11) · +129 more".
func (r ruleCount) namespacesText() string {
	if r.Namespaces == 0 {
		return ""
	}
	word := "namespaces"
	if r.Namespaces == 1 {
		word = "namespace"
	}
	parts := []string{fmt.Sprintf("in %d %s", r.Namespaces, word)}
	for _, n := range r.Top {
		if n.Count > 1 {
			parts = append(parts, fmt.Sprintf("%s (%d)", n.Namespace, n.Count))
		} else {
			parts = append(parts, n.Namespace)
		}
	}
	if rest := r.Namespaces - len(r.Top); rest > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", rest))
	}
	return strings.Join(parts, " · ")
}
