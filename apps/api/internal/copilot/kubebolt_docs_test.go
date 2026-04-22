package copilot

import (
	"strings"
	"testing"
)

func TestKubebolDocsTopics_NonEmpty(t *testing.T) {
	topics := KubebolDocsTopics()
	if len(topics) == 0 {
		t.Fatal("expected at least one topic")
	}
	// Sanity: list should be sorted (alphabetical).
	for i := 1; i < len(topics); i++ {
		if topics[i-1] > topics[i] {
			t.Errorf("topics not sorted: %q before %q", topics[i-1], topics[i])
		}
	}
}

func TestKubebolDocsGet_ExactKey(t *testing.T) {
	doc := KubebolDocsGet("overview")
	if doc == "" {
		t.Error("overview topic should return a doc")
	}
	if strings.HasPrefix(doc, "Unknown topic") {
		t.Errorf("overview should match, got %q", doc)
	}
}

func TestKubebolDocsGet_NormalizesCase(t *testing.T) {
	cases := []string{"OVERVIEW", "Overview", "OvErViEw"}
	base := KubebolDocsGet("overview")
	for _, c := range cases {
		if got := KubebolDocsGet(c); got != base {
			t.Errorf("case-insensitive lookup failed for %q", c)
		}
	}
}

func TestKubebolDocsGet_NormalizesSeparators(t *testing.T) {
	// "pod terminal", "pod_terminal", "pod-terminal" should all match.
	base := KubebolDocsGet("pod-terminal")
	if base == "" || strings.HasPrefix(base, "Unknown") {
		t.Fatal("pod-terminal should be a known topic")
	}
	for _, variant := range []string{"pod terminal", "pod_terminal", " pod-terminal "} {
		if got := KubebolDocsGet(variant); got != base {
			t.Errorf("separator normalization failed for %q", variant)
		}
	}
}

func TestKubebolDocsGet_FuzzyPrefix(t *testing.T) {
	// "admin" should prefix-match one of the admin-* topics.
	doc := KubebolDocsGet("admin")
	if strings.HasPrefix(doc, "Unknown topic") {
		t.Error("fuzzy prefix match failed for 'admin'")
	}
}

func TestKubebolDocsGet_Unknown(t *testing.T) {
	doc := KubebolDocsGet("totally-made-up-topic")
	if !strings.HasPrefix(doc, "Unknown topic") {
		t.Errorf("unknown topic should return fallback, got %q", snippet(doc))
	}
	// Fallback should include the full topic list for LLM recovery.
	for _, topic := range KubebolDocsTopics() {
		if !strings.Contains(doc, topic) {
			t.Errorf("fallback missing topic %q", topic)
		}
	}
}

func TestKubebolDocsGet_Empty(t *testing.T) {
	doc := KubebolDocsGet("")
	if !strings.HasPrefix(doc, "Unknown topic") {
		t.Errorf("empty topic should return fallback, got %q", snippet(doc))
	}
}

func TestKubebolDocsGet_AllTopicsResolve(t *testing.T) {
	// Every topic returned by KubebolDocsTopics() must actually resolve.
	for _, topic := range KubebolDocsTopics() {
		doc := KubebolDocsGet(topic)
		if doc == "" {
			t.Errorf("topic %q returned empty doc", topic)
		}
		if strings.HasPrefix(doc, "Unknown topic") {
			t.Errorf("topic %q flagged as unknown", topic)
		}
	}
}

func snippet(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:60]
}
