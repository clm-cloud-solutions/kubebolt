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
// Rationale (decision 2026-05-26 after S1 multi-node kind smoke, refined
// 2026-05-27 after session 11-A GMP E2E):
//
// Mode A (DaemonSet + kubelet collectors) already produces the
// KubeBolt-named metrics the UI's curated panels consume
// (node_fs_used_bytes, node_memory_working_set_bytes, container_*,
// pod_*). Mode C running alongside Mode A duplicating those (with
// either same names → series collision, or raw Prom names → 2× storage
// for data the UI doesn't query) is wasteful — burned ~70% of Mode C's
// sample volume on data Mode A already had.
//
// The matchers below ship ONLY metrics Mode A does NOT produce and use
// **explicit metric names** rather than `{__name__=~"regex"}` selectors.
// Reason: Google Managed Prometheus rejects `=~` on the `__name__`
// label with HTTP 400 `bad_data: =~ is an unsupported matchtype for
// the __name__ label` (Cortex/Monarch query-engine limitation; the
// engine's metric-name-prefixed sharding routes queries on exact name
// match only). AMP, Azure Managed Prom, and self-managed Prom all
// accept either form, so the explicit list is universally portable.
//
// Categories covered:
//   - kube_*  KSM core (pod / deployment / statefulset / daemonset /
//             node) — drives workload-counting panels and right-sizing
//   - node_load{1,5,15}  load average — Node detail Monitor panel
//   - node_pressure_*_waiting_seconds_total  PSI — Node detail Monitor
//   - node_disk_{read,written,io_time}*  disk I/O — Node detail Monitor
//   - node_network_{receive,transmit}_errs_total  net error counters
//   - up + process_*  target health + scraper self-metrics
//
// Missing metrics (e.g. cluster doesn't run node-exporter) return empty
// matrices, NOT errors — over-listing is safe.
//
// Operators override via the env var (or the chart's
// agent.promRead.matchers value) when they want app-custom metrics or
// the wider raw Prom space for ad-hoc VM exploration.
var DefaultMatchers = []string{
	// kube-state-metrics — workload inventory + status.
	"kube_pod_info",
	"kube_pod_status_phase",
	"kube_pod_status_ready",
	"kube_pod_container_status_waiting_reason",
	"kube_pod_container_status_terminated_reason",
	"kube_pod_container_resource_requests",
	"kube_pod_container_resource_limits",
	"kube_deployment_status_replicas",
	"kube_deployment_status_replicas_available",
	"kube_deployment_status_replicas_unavailable",
	"kube_statefulset_status_replicas",
	"kube_statefulset_status_replicas_ready",
	"kube_daemonset_status_number_ready",
	"kube_daemonset_status_desired_number_scheduled",
	"kube_node_info",
	"kube_node_status_capacity",
	"kube_node_status_allocatable",
	"kube_node_status_condition",
	// node-exporter — load + PSI + disk + network errs.
	"node_load1",
	"node_load5",
	"node_load15",
	"node_pressure_cpu_waiting_seconds_total",
	"node_pressure_io_waiting_seconds_total",
	"node_pressure_memory_waiting_seconds_total",
	"node_disk_read_bytes_total",
	"node_disk_written_bytes_total",
	"node_disk_io_time_seconds_total",
	"node_network_receive_errs_total",
	"node_network_transmit_errs_total",
	// target health + self-metrics.
	"up",
	"process_resident_memory_bytes",
	"process_cpu_seconds_total",
}

// CostMatchers is the OpenCost family set, appended to the effective
// matchers when KUBEBOLT_AGENT_PROMREAD_COST=true. Kept OUT of
// DefaultMatchers on purpose: cost only exists when the customer runs
// OpenCost, so it's opt-in (chart flag agent.promRead.cost.enabled).
// Appended — never replacing the base set — so enabling cost can't
// drop the core metrics KubeBolt needs. Explicit names (no =~) for the
// same GMP portability reason as DefaultMatchers.
var CostMatchers = []string{
	"node_total_hourly_cost",
	"node_cpu_hourly_cost",
	"node_ram_hourly_cost",
	"node_gpu_hourly_cost",
	"container_cpu_allocation",
	"container_gpu_allocation",
	"container_memory_allocation_bytes",
	"pv_hourly_cost",
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
	if cfg.Enabled {
		if len(cfg.Matchers) == 0 {
			cfg.Matchers = append([]string(nil), DefaultMatchers...)
		}
		// Cost opt-in: append the OpenCost families on top of the base
		// set (defaults OR operator-supplied), deduped. A single flag
		// so activating cost never means hand-listing matchers and
		// accidentally dropping the core (the trap that motivated D4).
		if v := os.Getenv(envPrefix + "COST"); v != "" {
			costOn, err := strconv.ParseBool(v)
			if err != nil {
				return cfg, fmt.Errorf("%sCOST: %w", envPrefix, err)
			}
			if costOn {
				cfg.Matchers = appendDedup(cfg.Matchers, CostMatchers...)
			}
		}
	}

	return cfg, nil
}

// appendDedup appends add to base, skipping any value already present
// so an operator who both flips cost on AND lists a cost matcher by
// hand doesn't double-fetch it.
func appendDedup(base []string, add ...string) []string {
	seen := make(map[string]struct{}, len(base))
	for _, m := range base {
		seen[m] = struct{}{}
	}
	for _, m := range add {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		base = append(base, m)
	}
	return base
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
