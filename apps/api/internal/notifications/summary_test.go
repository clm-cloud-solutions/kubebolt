package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// sampleBurst is shaped like the burst that motivated summaries: a cluster's
// first evaluation with a dozen criticals among hundreds of warnings.
func sampleBurst() Summary {
	ev := func(sev, kind, ns, name, title string) Event {
		return Event{
			Insight: models.Insight{
				Severity: sev, Title: title, Namespace: ns,
				Resource: kind + "/" + ns + "/" + name,
				Message:  title + " on " + name,
			},
			ClusterName: "acme-dev",
			BaseURL:     "https://kubebolt.example.com",
		}
	}
	var items []SummaryItem
	for i := 0; i < 4; i++ {
		items = append(items, SummaryItem{Event: ev("critical", "Pod", fmt.Sprintf("app-%d", i), fmt.Sprintf("pod-%d", i), "Pod in CrashLoopBackOff"), AlsoSentAlone: true})
	}
	for i := 0; i < 6; i++ {
		items = append(items, SummaryItem{Event: ev("critical", "Deployment", fmt.Sprintf("app-%d", i), fmt.Sprintf("deploy-%d", i), "Zero Available Replicas")})
	}
	for i := 0; i < 2; i++ {
		items = append(items, SummaryItem{Event: ev("critical", "Pod", "batch", fmt.Sprintf("job-%d", i), "Image Pull Failed")})
	}
	// 548 warnings over 131 namespaces: three busy ones, the rest 4 or 3 each.
	ns := func(i int) string { return fmt.Sprintf("ns-%03d", i) }
	n := 0
	for i, c := range []int{14, 11, 9} {
		for k := 0; k < c; k++ {
			items = append(items, SummaryItem{Event: ev("warning", "Service", ns(i), fmt.Sprintf("svc-%d", n), "Service Has Zero Ready Endpoints")})
			n++
		}
	}
	for i := 3; n < 548; i = 3 + (i-2)%128 {
		items = append(items, SummaryItem{Event: ev("warning", "Service", ns(i), fmt.Sprintf("svc-%d", n), "Service Has Zero Ready Endpoints")})
		n++
	}
	for i := 0; i < 10; i++ {
		items = append(items, SummaryItem{Event: ev("info", "Namespace", ns(200+i), ns(200+i), "Namespace has running pods but no NetworkPolicy")})
	}
	first := time.Date(2026, 11, 17, 23, 41, 5, 0, time.UTC)
	return Summary{
		ClusterName: "acme-dev", BaseURL: "https://kubebolt.example.com",
		Items: items, First: first, Last: first.Add(6 * time.Second),
		Config: DefaultBurstConfig(),
	}
}

func TestViewFromBurst_CountsClassifiesAndNamesTheCriticals(t *testing.T) {
	s := sampleBurst()
	v := viewFromBurst(s)

	if v.Total != 570 {
		t.Fatalf("Total = %d, want 570", v.Total)
	}
	if v.Counts["critical"] != 12 || v.Counts["warning"] != 548 || v.Counts["info"] != 10 {
		t.Fatalf("Counts = %v, want 12 critical, 548 warning, 10 info", v.Counts)
	}
	if len(v.Critical) != maxNamedCritical || v.MoreCritical != 2 {
		t.Fatalf("named %d critical + %d more, want %d + 2", len(v.Critical), v.MoreCritical, maxNamedCritical)
	}
	if v.SentAlone != 4 {
		t.Fatalf("SentAlone = %d, want 4", v.SentAlone)
	}
	rules := v.Sections[0].Rules
	order := []string{"Zero Available Replicas", "Pod in CrashLoopBackOff", "Image Pull Failed", "Service Has Zero Ready Endpoints", "Namespace has running pods but no NetworkPolicy"}
	if len(rules) != len(order) {
		t.Fatalf("%d rules, want %d", len(rules), len(order))
	}
	for i, title := range order {
		if rules[i].Title != title {
			t.Fatalf("rule %d = %q, want %q (most severe first, then largest)", i, rules[i].Title, title)
		}
	}
	zero := rules[3]
	if zero.Count != 548 || zero.Namespaces != 131 {
		t.Fatalf("zero-endpoints rule: %d in %d namespaces, want 548 in 131", zero.Count, zero.Namespaces)
	}
	if got := zero.namespacesText(); !strings.HasPrefix(got, "in 131 namespaces · ns-000 (14) · ns-001 (11) · ns-002 (9) · +128 more") {
		t.Fatalf("namespacesText = %q", got)
	}
	for _, want := range []string{"Opened within 6 seconds, 23:41:05–23:41:11 UTC.", "The first 4 went out on their own", "so you get 5 emails instead of 570"} {
		if !strings.Contains(v.Lead, want) {
			t.Fatalf("Lead %q does not say %q", v.Lead, want)
		}
	}
	if !strings.Contains(v.Why, "5 or more insights within 60 seconds") || !strings.Contains(v.Why, "quiet for 30 seconds") {
		t.Fatalf("Why = %q, want the thresholds that made it a burst", v.Why)
	}
	if v.headline() != "570 new insights on acme-dev" || v.severityLine() != "12 critical · 548 warning · 10 info" {
		t.Fatalf("headline %q / severity line %q", v.headline(), v.severityLine())
	}
}

func TestViewFromBurst_ABurstOfResolutionsNamesTheResolvedCriticals(t *testing.T) {
	s := sampleBurst()
	s.Resolved = true
	for i := range s.Items {
		s.Items[i].Insight.Title = resolvedPrefix + s.Items[i].Insight.Title
		s.Items[i].Insight.Resolved = true
	}
	v := viewFromBurst(s)
	if v.headline() != "570 insights resolved on acme-dev" {
		t.Fatalf("headline = %q", v.headline())
	}
	if len(v.Critical) != maxNamedCritical {
		t.Fatalf("named %d resolved criticals, want %d", len(v.Critical), maxNamedCritical)
	}
	if strings.HasPrefix(v.Sections[0].Rules[0].Title, resolvedPrefix) {
		t.Fatal("rule titles keep the [Resolved] prefix")
	}
	if !strings.HasPrefix(v.Lead, "Resolved within 6 seconds") {
		t.Fatalf("Lead = %q", v.Lead)
	}
}

func TestViewFromBurst_APartialSummarySaysTheBurstContinues(t *testing.T) {
	s := sampleBurst()
	s.Partial = true
	v := viewFromBurst(s)
	if !strings.Contains(v.Why, "still sending insights") {
		t.Fatalf("Why = %q", v.Why)
	}
}

func TestSpanText(t *testing.T) {
	at := time.Date(2026, 11, 17, 23, 41, 5, 0, time.UTC)
	cases := []struct {
		first, last time.Time
		want        string
	}{
		{at, at, "at 23:41:05 UTC"},
		{at, at.Add(6 * time.Second), "within 6 seconds, 23:41:05–23:41:11 UTC"},
		{at, at.Add(4*time.Minute + 10*time.Second), "over 4 minutes, 23:41–23:45 UTC"},
	}
	for _, c := range cases {
		if got := spanText(c.first, c.last); got != c.want {
			t.Errorf("spanText = %q, want %q", got, c.want)
		}
	}
}

// --- chat channels --------------------------------------------------------

type webhookRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (w *webhookRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("webhook body is not JSON: %v", err)
		}
		w.mu.Lock()
		w.bodies = append(w.bodies, body)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (w *webhookRecorder) posts() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]any(nil), w.bodies...)
}

func TestSlackSummary_OneMessageForTheWholeBurst(t *testing.T) {
	rec := &webhookRecorder{}
	srv := rec.server(t)
	n := NewSlackNotifier(srv.URL)

	s := sampleBurst()
	s.Items[5].Insight.Title = "Zero <Available> Replicas & co"
	if err := n.SendSummary(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	posts := rec.posts()
	if len(posts) != 1 {
		t.Fatalf("%d posts, want 1", len(posts))
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(posts[0])
	text := buf.String()
	if !strings.Contains(text, "570 new insights on acme-dev") {
		t.Fatalf("the Slack summary does not count the burst: %s", text)
	}
	if !strings.Contains(text, "Zero &lt;Available&gt; Replicas &amp; co") {
		t.Fatal("Slack control characters in a title are not escaped")
	}
	if !strings.Contains(text, "and 2 more critical") || !strings.Contains(text, "also sent on its own") {
		t.Fatalf("missing the overflow line or the sent-alone mark: %s", text)
	}
}

func TestDiscordSummary_StaysWithinTheEmbedLimits(t *testing.T) {
	rec := &webhookRecorder{}
	srv := rec.server(t)
	n := NewDiscordNotifier(srv.URL)
	s := sampleBurst()
	// Many distinct rules, to push the description past Discord's limit.
	for i := 0; i < 400; i++ {
		s.Items = append(s.Items, SummaryItem{Event: Event{
			Insight:     models.Insight{Severity: "warning", Title: fmt.Sprintf("Rule number %03d with a long descriptive title", i), Namespace: "x", Resource: "Pod/x/y"},
			ClusterName: "acme-dev",
		}})
	}
	if err := n.SendSummary(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	posts := rec.posts()
	if len(posts) != 1 {
		t.Fatalf("%d posts, want 1", len(posts))
	}
	embed := posts[0]["embeds"].([]any)[0].(map[string]any)
	if d := embed["description"].(string); len(d) > 4096 {
		t.Fatalf("description is %d bytes, Discord rejects more than 4096", len(d))
	}
	if title := embed["title"].(string); !strings.Contains(title, "970 new insights on acme-dev") {
		t.Fatalf("title = %q", title)
	}
}
