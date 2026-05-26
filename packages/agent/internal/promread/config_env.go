package promread

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultMatchers is the matcher set the Reader falls back to when
// KUBEBOLT_AGENT_PROMREAD_MATCHERS is empty AND ENABLED=true. Pulls
// the KSM + cAdvisor + node-exporter + uptime families that Mode A's
// scrape sidecar collects, so a customer flipping from Mode A to
// Mode C gets a comparable starting set without picking matchers
// from scratch. Operators override via the env var (or the chart's
// agent.promRead.matchers value) when they want a tighter or wider
// selection.
//
// node_* coverage is included because the Node detail Monitor tab
// (Load Average + PSI) and the node-exporter coverage chip both
// depend on it; the S1 kind smoke surfaced their absence when the
// defaults skipped node_* — leaving them out was a worse default
// for Mode C than the small cardinality cost (~50 series per node).
var DefaultMatchers = []string{
	`{__name__=~"kube_pod_.*"}`,
	`{__name__=~"container_(cpu|memory|fs|network)_.*"}`,
	`{__name__=~"node_.*"}`,
	`{__name__=~"up|process_.*"}`,
}

// envPrefix is shared by all promread env vars. Kept in one place so
// the env-template docs (.env.example × 4 per feedback_document_env_vars)
// and the chart values can grep against a single constant.
const envPrefix = "KUBEBOLT_AGENT_PROMREAD_"

// LoadConfigFromEnv builds a Config from KUBEBOLT_AGENT_PROMREAD_*
// env vars. Cluster + tenant identifiers (ClusterID / ClusterName /
// TenantID) are left zero — the agent's main.go fills those from
// the already-resolved cluster/tenant globals. Returns an error when
// any individual env value is malformed (bad bool, bad duration);
// missing env vars are fine and fall back to defaults at Validate /
// applyDefaults time.
func LoadConfigFromEnv() (Config, error) {
	cfg := Config{}

	if v := os.Getenv(envPrefix + "ENABLED"); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("%sENABLED: %w", envPrefix, err)
		}
		cfg.Enabled = parsed
	}

	cfg.URL = os.Getenv(envPrefix + "URL")

	cfg.Auth = AuthConfig{
		Mode:              AuthMode(os.Getenv(envPrefix + "AUTH_MODE")),
		BasicAuthUsername: os.Getenv(envPrefix + "BASIC_AUTH_USERNAME"),
		BasicAuthPassword: os.Getenv(envPrefix + "BASIC_AUTH_PASSWORD"),
		BearerToken:       os.Getenv(envPrefix + "BEARER_TOKEN"),
	}

	for _, f := range []struct {
		key  string
		dest *time.Duration
	}{
		{"POLL_INTERVAL", &cfg.PollInterval},
		{"STEP", &cfg.Step},
		{"LOOKBACK", &cfg.Lookback},
	} {
		if v := os.Getenv(envPrefix + f.key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return cfg, fmt.Errorf("%s%s: %w", envPrefix, f.key, err)
			}
			*f.dest = d
		}
	}

	cfg.Matchers = parseMatchers(os.Getenv(envPrefix + "MATCHERS"))
	if cfg.Enabled && len(cfg.Matchers) == 0 {
		cfg.Matchers = append([]string(nil), DefaultMatchers...)
	}

	return cfg, nil
}

// parseMatchers splits the env value on newlines, trims whitespace
// per entry, and drops empties. Newlines work because env values can
// span lines (helm chart renders the values.yaml `matchers` list
// joined with "\n") and PromQL selectors don't legitimately contain
// raw newlines.
func parseMatchers(raw string) []string {
	if raw == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}
