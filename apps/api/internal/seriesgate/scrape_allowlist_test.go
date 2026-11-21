package seriesgate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/packages/shared/costmetrics"
)

// The agent chart's scrape allowlist is the one copy of the core/custom
// boundary that is not Go: it cannot import costmetrics, so this reads it.
// Without the cost families, scrape mode dropped the volume, spot,
// load-balancer and egress costs (finding #101).
func TestScrapeKeepRegexKeepsTheCostFamilies(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "deploy", "helm", "kubebolt-agent", "values.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the agent chart's values: %v", err)
	}
	keep := scrapeKeepRegex(t, string(raw))
	re := regexp.MustCompile("^(?:" + keep + ")$") // Prometheus relabel regexes are anchored
	for _, name := range costmetrics.Families {
		if !re.MatchString(name) {
			t.Errorf("scrape.metricRelabelConfigs keep regex drops the cost family %s", name)
		}
	}
	// And it stays a boundary: what OpenCost publishes and nothing reads, and a
	// customer's own app metrics, are still dropped.
	for _, name := range []string{"kubecost_http_requests_total", "deployment_match_labels", "pod_info", "gitlab_sql_duration_seconds_bucket"} {
		if re.MatchString(name) {
			t.Errorf("scrape.metricRelabelConfigs keep regex now keeps %s", name)
		}
	}
}

// scrapeKeepRegex returns the regex of the `action: keep` rule under
// scrape.metricRelabelConfigs.
func scrapeKeepRegex(t *testing.T, values string) string {
	t.Helper()
	i := strings.Index(values, "metricRelabelConfigs:")
	if i < 0 {
		t.Fatal("no metricRelabelConfigs in the agent chart's values")
	}
	rest := values[i:]
	k := strings.Index(rest, "action: keep")
	if k < 0 {
		t.Fatal("no keep rule under metricRelabelConfigs")
	}
	m := regexp.MustCompile(`(?m)^\s*regex:\s*'([^']*)'`).FindStringSubmatch(rest[k:])
	if m == nil {
		t.Fatal("the keep rule has no single-quoted regex")
	}
	return m[1]
}
