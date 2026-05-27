package promread

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultMatchers is the surgical matcher set the Reader falls back to
// when KUBEBOLT_AGENT_PROMREAD_MATCHERS is empty AND ENABLED=true.
//
// Rationale (decision 2026-05-26 after S1 multi-node kind smoke):
// Mode A (DaemonSet + kubelet collectors) already produces the
// KubeBolt-named metrics the UI's curated panels consume
// (node_fs_used_bytes, node_memory_working_set_bytes, container_*,
// pod_*). Mode C running alongside Mode A duplicating those (with
// either same names → series collision, or raw Prom names → 2× storage
// for data the UI doesn't query) is wasteful — burned ~70% of Mode C's
// sample volume on data Mode A already had.
//
// These matchers ship ONLY the metrics Mode A does NOT produce:
//
//   - kube_*        full kube-state-metrics surface (pod/deployment/
//                   statefulset/daemonset/service/node), drives most
//                   workload-counting panels
//   - node_load.*   load average (1/5/15) for the Node detail panel
//   - node_pressure.*  PSI (CPU/IO/memory waiting) for the Node detail
//                   panel — kernel-level stress signal Mode A can't
//                   synthesize
//   - node_disk_.*  full disk I/O detail (read/write bytes, queue,
//                   latency) — Mode A only emits filesystem capacity
//   - node_network_.*_errs_.* network error counters, early-warning
//                   signal for NetworkPolicy / driver issues
//   - up, process_* target health (scrape success per job) +
//                   process self-metrics
//
// Operators override via the env var (or the chart's
// agent.promRead.matchers value) when they want app-custom metrics or
// the wider raw Prom space for ad-hoc VM exploration.
var DefaultMatchers = []string{
	`{__name__=~"kube_.*"}`,
	`{__name__=~"node_load.*|node_pressure_.*"}`,
	`{__name__=~"node_disk_.*|node_network_.*_errs_.*"}`,
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
		AwsRegion:         os.Getenv(envPrefix + "AWS_REGION"),
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
