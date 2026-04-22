package notifications

import (
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// newInsightHelper creates a models.Insight for tests. Mirrors the shape
// the engine produces without depending on the insights package.
func newInsightHelper(severity, resource, title, message, suggestion string) models.Insight {
	now := time.Now()
	ns := ""
	cat := "workload"
	// Resource format: "Kind/namespace/name"
	// Extract namespace for the Insight field.
	parts := splitN3(resource)
	if len(parts) == 3 {
		ns = parts[1]
	}
	if len(parts) >= 1 {
		switch parts[0] {
		case "Node":
			cat = "node"
		case "PVC":
			cat = "storage"
		case "HPA":
			cat = "autoscaling"
		}
	}
	return models.Insight{
		ID:         "test-id",
		Severity:   severity,
		Category:   cat,
		Resource:   resource,
		Namespace:  ns,
		Title:      title,
		Message:    message,
		Suggestion: suggestion,
		FirstSeen:  now,
		LastSeen:   now,
	}
}

func splitN3(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s) && len(out) < 2; i++ {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func TestSeverityLevel(t *testing.T) {
	if severityLevel("critical") <= severityLevel("warning") {
		t.Error("critical > warning")
	}
	if severityLevel("warning") <= severityLevel("info") {
		t.Error("warning > info")
	}
	if severityLevel("info") <= 0 {
		t.Error("info > 0")
	}
	if severityLevel("") != 0 || severityLevel("bogus") != 0 {
		t.Error("unknown severity must be 0")
	}
}

func TestSeverityIconAndColor_Channels(t *testing.T) {
	// Slack uses hex colors
	_, slackCritical := severityIconAndColor("critical", "slack")
	if slackCritical == "" {
		t.Error("slack should return a color")
	}
	if slackCritical[0] != '#' {
		t.Errorf("slack color must be hex-prefixed, got %q", slackCritical)
	}
	// Unknown severity falls back safely
	icon, color := severityIconAndColor("nonsense", "slack")
	if icon == "" || color == "" {
		t.Errorf("fallback should still return values, got (%q, %q)", icon, color)
	}
}

func TestSlackBuildPayload_Shape(t *testing.T) {
	n := NewSlackNotifier("https://hooks.slack.com/test")
	event := Event{
		Insight: newInsightHelper("critical", "Pod/default/api", "CrashLoopBackOff", "msg", "fix it"),
		ClusterName: "prod",
		BaseURL:     "https://kb.example.com",
	}
	payload := n.buildPayload(event)

	atts, ok := payload["attachments"].([]map[string]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %+v", payload["attachments"])
	}
	if atts[0]["color"] == nil {
		t.Error("attachment should carry a color")
	}
	blocks, ok := atts[0]["blocks"].([]map[string]any)
	if !ok || len(blocks) < 3 {
		t.Errorf("expected ≥3 blocks, got %+v", atts[0]["blocks"])
	}
}

func TestDiscordBuildPayload_Shape(t *testing.T) {
	n := NewDiscordNotifier("https://discord.com/api/webhooks/x/y")
	event := Event{
		Insight: newInsightHelper("warning", "HPA/default/api", "Max replicas reached", "msg", ""),
	}
	payload := n.buildPayload(event)
	embeds, ok := payload["embeds"].([]map[string]any)
	if !ok || len(embeds) != 1 {
		t.Fatalf("expected 1 embed, got %+v", payload["embeds"])
	}
	if embeds[0]["title"] == nil {
		t.Error("embed should have title")
	}
}

func TestDiscordColorForSeverity(t *testing.T) {
	if discordColorForSeverity("critical") == 0 {
		t.Error("critical should map to non-zero color")
	}
	if discordColorForSeverity("warning") == 0 {
		t.Error("warning should map to non-zero color")
	}
	// Unknown severity — fallback acceptable (0 or non-zero), just shouldn't panic
	_ = discordColorForSeverity("bogus")
}
