//go:build !ee

package copilot

import (
	"strings"
	"testing"
)

// The OSS knowledge base must describe the OSS build. These guard the two
// ways it drifted before: Cloud-only features described as available, and
// menu paths that do not exist in the OSS sidebar.

// Cloud / Enterprise-only topics stay answerable (the model asks for them)
// but must say they are not part of this edition and where to find them.
func TestKubebolDocsOSS_CloudOnlyTopicsSaySo(t *testing.T) {
	for _, topic := range []string{"autopilot", "plans-billing", "credits", "teams-orgs", "environments"} {
		doc := KubebolDocsGet(topic)
		if strings.HasPrefix(doc, "Unknown topic") {
			t.Fatalf("topic %q must exist in the OSS map", topic)
		}
		if !strings.Contains(doc, "KubeBolt Cloud") || !strings.Contains(doc, "https://kubebolt.io") {
			t.Errorf("topic %q must name KubeBolt Cloud and https://kubebolt.io:\n%s", topic, doc)
		}
	}
	for _, topic := range []string{"autopilot", "plans-billing", "credits"} {
		if doc := KubebolDocsGet(topic); !strings.Contains(doc, "not available in the open-source edition") &&
			!strings.Contains(doc, "do not exist in the open-source edition") {
			t.Errorf("topic %q must say it is not available in the open-source edition:\n%s", topic, doc)
		}
	}
}

// Menus that exist only in the Enterprise build, or that were renamed.
func TestKubebolDocsOSS_NoStaleMenuPaths(t *testing.T) {
	stale := []string{
		"AI & Autopilot",
		"Billing →",
		"Plan & usage",
		"Usage & Credits",
		"Administration → Copilot",
		"Settings → AI",
		"Administration → Clusters",
	}
	for topic, doc := range kubebolt_docs {
		for _, s := range stale {
			if strings.Contains(doc, s) {
				t.Errorf("topic %q references %q, which is not an OSS menu", topic, s)
			}
		}
	}
}

// The admin hubs the OSS sidebar actually renders (Sidebar.tsx adminItems).
func TestKubebolDocsOSS_NavigationListsRealAdminHubs(t *testing.T) {
	nav := KubebolDocsGet("navigation")
	for _, hub := range []string{"Access", "Agents & Ingest", "AI (Kobi)", "Insights", "System", "API Tokens"} {
		if !strings.Contains(nav, hub) {
			t.Errorf("navigation topic missing admin hub %q", hub)
		}
	}
}

// openai.go / anthropic.go POST to the configured base URL verbatim, so the
// docs must show a full endpoint, never a bare /v1 root.
func TestKubebolDocsOSS_BaseURLIsFullEndpoint(t *testing.T) {
	doc := KubebolDocsGet("ai-config")
	if !strings.Contains(doc, "/chat/completions") {
		t.Errorf("ai-config must show a full endpoint example:\n%s", doc)
	}
	if strings.Contains(doc, "custom") && strings.Contains(doc, "anthropic|openai|custom") {
		t.Errorf("ai-config must not offer a 'custom' provider:\n%s", doc)
	}
}
