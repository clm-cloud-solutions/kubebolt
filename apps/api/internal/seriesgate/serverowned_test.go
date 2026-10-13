package seriesgate

import (
	"regexp"
	"testing"
)

// The Go check and the PromQL regex are two spellings of one set: queries
// exclude with the regex, code paths ask the function. They must agree.
func TestServerOwned_FunctionAndRegexAgree(t *testing.T) {
	re := regexp.MustCompile(`^(?:` + ServerOwnedFamiliesRegex + `)$`)
	cases := map[string]bool{
		"kubebolt_kobi_copilot_sessions_total": true,
		"kubebolt_kobi_autopilot_tokens_total": true,
		"kubebolt_kobi_sessions_total":         true,
		"kubebolt_kobi_tool_seconds_bucket":    true,
		"kubebolt_autopilot_stage_runs_total":  true,
		"kubebolt_ai_tokens_total":             true,
		"kubebolt_cluster_team_info":           true,
		"kubebolt_kobi_":                       false,
		"kubebolt_prom_write_active_series":    false,
		"kubebolt_agent_grpc_streams_total":    false,
		"kubebolt_agent_info":                  false,
		"kube_pod_info":                        false,
		"kubebolt_aircraft":                    false,
	}
	for name, want := range cases {
		if got := IsServerOwnedMetricName(name); got != want {
			t.Errorf("IsServerOwnedMetricName(%q) = %v, want %v", name, got, want)
		}
		if got := re.MatchString(name); got != want {
			t.Errorf("regex on %q = %v, want %v", name, got, want)
		}
	}
}
