package notifications

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

func digestEvents(n int) []Event {
	out := make([]Event, n)
	for i := range out {
		sev := "warning"
		if i%10 == 0 {
			sev = "critical"
		}
		out[i] = Event{
			Insight:     models.Insight{Severity: sev, Title: "Rule " + sev, Resource: fmt.Sprintf("Pod/ns/p-%d", i), Namespace: "ns"},
			ClusterName: "acme-dev",
		}
	}
	return out
}

func TestEmailDigest_ListsUpToTheLimitAndSummarisesPastIt(t *testing.T) {
	n := NewEmailNotifier(EmailConfig{Host: "smtp.example.com", From: "a@example.com", To: []string{"b@example.com"}, DigestMode: DigestHourly})
	defer n.Stop()

	subject, _, _ := n.render(digestEvents(DefaultDigestListLimit))
	if !strings.HasPrefix(subject, "[KubeBolt] Digest — 20 insights") {
		t.Fatalf("20 insights: subject %q, want the card list", subject)
	}
	subject, html, text := n.render(digestEvents(DefaultDigestListLimit + 1))
	if !strings.HasPrefix(subject, "[KubeBolt] Hourly digest — 21 insights (3 critical, 18 warning)") {
		t.Fatalf("21 insights: subject %q, want the summary", subject)
	}
	if !strings.Contains(html, "Critical · 3 — listed one by one") || !strings.Contains(text, "acme-dev (21):") {
		t.Fatal("the digest summary is missing its critical list or its cluster section")
	}
}

// There is no "always list": a limit below 1 is the default, so a large digest
// is still summarised.
func TestEmailDigest_ALimitBelowOneIsTheDefault(t *testing.T) {
	for _, limit := range []int{-1, 0} {
		n := NewEmailNotifier(EmailConfig{Host: "h", From: "a@example.com", To: []string{"b@example.com"}, DigestMode: DigestDaily, DigestListLimit: limit})
		if subject, _, _ := n.render(digestEvents(DefaultDigestListLimit + 1)); !strings.HasPrefix(subject, "[KubeBolt] Daily digest — 21 insights") {
			t.Fatalf("limit %d: subject %q, want the summary past the default limit", limit, subject)
		}
		if subject, _, _ := n.render(digestEvents(DefaultDigestListLimit)); !strings.HasPrefix(subject, "[KubeBolt] Digest — 20 insights") {
			t.Fatalf("limit %d: subject %q, want the card list up to the default limit", limit, subject)
		}
		n.Stop()
	}
}

func TestEmailInstant_NeverTurnsABatchIntoADigestSummary(t *testing.T) {
	n := NewEmailNotifier(EmailConfig{Host: "h", From: "a@example.com", To: []string{"b@example.com"}})
	if n.summarisesDigest(500) {
		t.Fatal("instant mail must not use the digest summary")
	}
}

func TestEmailBurstSummary_RendersTheBurst(t *testing.T) {
	s := sampleBurst()
	s.Items[6].Insight.Title = `Zero <script>alert(1)</script> Replicas`
	subject, html, text := summaryEmail(viewFromBurst(s))
	if subject != "[KubeBolt] 570 new insights on acme-dev — 12 critical" {
		t.Fatalf("subject = %q", subject)
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("an insight title reached the HTML unescaped")
	}
	if got := strings.Count(html, "also sent on its own"); got != 4 {
		t.Fatalf("%d sent-alone marks, want 4", got)
	}
	for _, want := range []string{"+ 2 more critical", "Open the 570 insights in KubeBolt", "https://kubebolt.example.com/insights", "Why one email:"} {
		if !strings.Contains(html, want) {
			t.Fatalf("HTML is missing %q", want)
		}
	}
	if !strings.Contains(text, "By rule:") || !strings.Contains(text, "548  Service Has Zero Ready Endpoints (warning)") {
		t.Fatalf("plain text is missing the rule counts:\n%s", text)
	}

	s.Partial = true
	if subject, _, _ := summaryEmail(viewFromBurst(s)); !strings.HasSuffix(subject, "(still going)") {
		t.Fatalf("partial subject = %q", subject)
	}
}
