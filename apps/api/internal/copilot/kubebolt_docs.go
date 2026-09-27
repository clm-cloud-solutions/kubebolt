package copilot

import (
	"fmt"
	"sort"
	"strings"
)

// kubebolt_docs is a terse product knowledge base the copilot can query via
// the get_kubebolt_docs tool. Entries are hand-curated — short enough to
// burn few tokens when returned, specific enough to actually answer the
// user. Prefer action-oriented wording ("click X", "press Cmd+Y") over
// marketing.
//
// EDITION SEAM: the map itself is edition-specific and lives in
// kubebolt_docs_default.go (`//go:build !ee`, the open-source edition). The
// Enterprise build supplies its own kubebolt_docs_ee.go (`//go:build ee`)
// describing the Cloud / Enterprise surface (Autopilot, billing, plans,
// credits, teams). This file — the lookup, the aliases, the fuzzy matching —
// is shared byte-identical between editions. Both maps must define every
// alias target below; TestKubebolDocsAliases_ResolveToRealTopics enforces it
// in each build.
//
// When adding topics: keep each entry under ~500 chars, lowercase keys,
// kebab-case multi-word keys. New topics are picked up automatically by
// the get_kubebolt_docs tool (topic list is derived from the map). Never
// hardcode prices — they drift.

// kubeboltDocsAliases maps the topic names the LLM plausibly GUESSES to the
// real keys. Aliases are invisible in the topic list — the canonical names
// live there — but they catch pattern-following guesses: in vivo the model
// asked for "admin-billing" (extrapolating the admin-* family) and got
// "Unknown topic", then improvised an answer about plans.
var kubeboltDocsAliases = map[string]string{
	"admin-billing":   "plans-billing",
	"billing-plans":   "plans-billing",
	"pricing":         "plans-billing",
	"subscription":    "plans-billing",
	"connect-cluster": "add-cluster",
	"cluster-connect": "add-cluster",
	"onboarding":      "add-cluster",
	"kobi":            "copilot",
	"admin-access":    "admin-users",
	"editions":        "distribution",
	"enterprise":      "distribution",
}

// KubebolDocsTopics returns the list of known topic keys for the tool
// description — lets the LLM discover available topics without a round-trip.
func KubebolDocsTopics() []string {
	topics := make([]string, 0, len(kubebolt_docs))
	for k := range kubebolt_docs {
		topics = append(topics, k)
	}
	sort.Strings(topics)
	return topics
}

// KubebolDocsGet returns the doc for a topic. When the topic is unknown,
// returns a short message plus the list of valid topics so the LLM can
// retry. Fuzzy matching is intentionally forgiving — fold case, normalize
// whitespace and underscores, and try prefix matches. Keeps the tool
// useful even when the LLM guesses a slightly-off key.
func KubebolDocsGet(topic string) string {
	key := normalizeDocKey(topic)
	if key == "" {
		return kubebolDocsUnknown("", KubebolDocsTopics())
	}
	if doc, ok := kubebolt_docs[key]; ok {
		return doc
	}
	if canonical, ok := kubeboltDocsAliases[key]; ok {
		return kubebolt_docs[canonical]
	}
	// Prefix fallback
	for k, doc := range kubebolt_docs {
		if strings.HasPrefix(k, key) || strings.HasPrefix(key, k) {
			return doc
		}
	}
	// Substring fallback
	for k, doc := range kubebolt_docs {
		if strings.Contains(k, key) {
			return doc
		}
	}
	return kubebolDocsUnknown(topic, KubebolDocsTopics())
}

func normalizeDocKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

func kubebolDocsUnknown(topic string, topics []string) string {
	prefix := "Unknown topic"
	if topic != "" {
		prefix = fmt.Sprintf("Unknown topic %q", topic)
	}
	return fmt.Sprintf("%s. Available topics: %s", prefix, strings.Join(topics, ", "))
}
