package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// IngestChannelConfig bundles the env-driven defaults for everything
// in the "kubebolt-agent ↔ kubebolt" communication plane, plus the
// Prom remote_write receiver knobs. Spec #09 V2 surfaces this struct
// through the Settings UI (Ingest tab) so operators can change auth
// posture, rate limits, auto-registration, and tunnel timeouts without
// redeploying.
//
// Field categories:
//
//   - Channel security (restart-required):
//       AgentAuthMode, AgentTokenAudience, AgentRequireMTLS
//     These wire into the gRPC server's auth interceptor at boot. Hot
//     reload would race with in-flight RPCs and the mounted server
//     chain, so changes via UI set pendingRestart=true and apply on
//     next process boot. Same pattern as V1 Auth domain.
//
//   - Rate limiting / auto-register / tunnels / receiver (hot-reload):
//     Read at request time (or next ticker tick) by their consumers
//     via runtime.IngestChannel(). Operators can flip toggles without
//     redeploy; effective on the next refill/registration/request.
//
// Defaults are intentionally permissive ("disabled" for auth, off for
// rate limit, off for autoregister, off for remote_write) so a fresh
// install with zero env config produces a runnable system the operator
// can lock down via UI after first login.
type IngestChannelConfig struct {
	// Channel security — gRPC agent ingest.
	AgentAuthMode      string // disabled | permissive | enforced
	AgentTokenAudience string
	AgentRequireMTLS   bool

	// Rate limiting (fleet-wide; per-tenant overrides are separate).
	AgentRateLimitEnabled bool
	AgentRateLimitRPS     int
	AgentRateLimitBurst   int

	// Cluster auto-registration for agent-proxy peers.
	AgentAutoRegisterClusters bool
	AgentRegistryPruneHorizon time.Duration

	// Prom remote_write receiver.
	RemoteWriteEnabled                    bool
	RemoteWriteAuthMode                   string // disabled | permissive | enforced
	PromWriteDefaultSamplesPerSec         int
	PromWriteDefaultBurstSamples          int
	PromWriteDefaultMaxActiveSeries       int
	PromWriteDefaultMaxActiveSeriesGlobal int // 0 = disabled

	// SPDY tunnels via agent-proxy.
	AgentTunnelIdleTimeout time.Duration

	// Agent-proxy resilience timeouts (seconds) — so one stuck/dead agent can't
	// hang the API. Hot-reload: read live by the manager's connect deadline (per
	// cold connect), the stuck-agent watchdog (per tick; 0 disables), and the
	// per-request proxy timeout. Env baselines are Go durations
	// (KUBEBOLT_AGENT_PROXY_{CONNECT,STUCK,REQUEST}_TIMEOUT), stored as whole seconds.
	ConnectTimeoutSeconds int
	StuckTimeoutSeconds   int
	RequestTimeoutSeconds int
}

// Default values used when the corresponding env var is unset or
// unparseable. Exposed as constants for tests + handler-side rendering.
const (
	DefaultAgentAuthMode             = "disabled"
	DefaultAgentTokenAudience        = "kubebolt-backend"
	DefaultAgentRequireMTLS          = false
	DefaultAgentRateLimitEnabled     = false
	DefaultAgentRateLimitRPS         = 1000
	DefaultAgentRateLimitBurst       = 2000
	DefaultAgentAutoRegisterClusters = false
	DefaultAgentRegistryPruneHorizon = 24 * time.Hour
	DefaultRemoteWriteEnabled        = false
	DefaultRemoteWriteAuthMode       = "disabled"
	DefaultAgentTunnelIdleTimeout    = 5 * time.Minute

	// Agent-proxy resilience baselines + floors. Stuck accepts 0 (disabled);
	// connect/request are floored so a fat-fingered tiny value can't wedge the proxy.
	DefaultConnectTimeoutSeconds = 25
	DefaultStuckTimeoutSeconds   = 45
	DefaultRequestTimeoutSeconds = 30
	MinAgentProxyTimeoutSeconds  = 5
	MaxAgentProxyTimeoutSeconds  = 600
)

// LoadIngestChannelConfig reads the ingest-channel env vars and returns
// the resolved IngestChannelConfig. Centralizes reads that used to live
// in main.go, rate_limiter.go, authenticator_factory.go, tls_config.go,
// tunnel.go, and prom_write.go — V2 makes the settings runtime the
// single read site for these values, so consumers no longer touch
// os.Getenv directly. The function logs a WARN per malformed value but
// always returns a usable config (defaults fill the gaps).
func LoadIngestChannelConfig() IngestChannelConfig {
	cfg := IngestChannelConfig{
		AgentAuthMode:                         DefaultAgentAuthMode,
		AgentTokenAudience:                    DefaultAgentTokenAudience,
		AgentRequireMTLS:                      DefaultAgentRequireMTLS,
		AgentRateLimitEnabled:                 DefaultAgentRateLimitEnabled,
		AgentRateLimitRPS:                     DefaultAgentRateLimitRPS,
		AgentRateLimitBurst:                   DefaultAgentRateLimitBurst,
		AgentAutoRegisterClusters:             DefaultAgentAutoRegisterClusters,
		AgentRegistryPruneHorizon:             DefaultAgentRegistryPruneHorizon,
		RemoteWriteEnabled:                    DefaultRemoteWriteEnabled,
		RemoteWriteAuthMode:                   DefaultRemoteWriteAuthMode,
		PromWriteDefaultSamplesPerSec:         DefaultPromWriteSamplesPerSec,
		PromWriteDefaultBurstSamples:          DefaultPromWriteBurstSamples,
		PromWriteDefaultMaxActiveSeries:       DefaultPromWriteMaxActiveSeries,
		PromWriteDefaultMaxActiveSeriesGlobal: 0,
		AgentTunnelIdleTimeout:                DefaultAgentTunnelIdleTimeout,
		ConnectTimeoutSeconds:                 DefaultConnectTimeoutSeconds,
		StuckTimeoutSeconds:                   DefaultStuckTimeoutSeconds,
		RequestTimeoutSeconds:                 DefaultRequestTimeoutSeconds,
	}

	if v := os.Getenv("KUBEBOLT_AGENT_AUTH_MODE"); v != "" {
		if isValidAuthMode(v) {
			cfg.AgentAuthMode = v
		} else {
			log.Printf("WARN config: KUBEBOLT_AGENT_AUTH_MODE=%q is not one of disabled|permissive|enforced — using default %s", v, DefaultAgentAuthMode)
		}
	}
	if v := os.Getenv("KUBEBOLT_AGENT_TOKEN_AUDIENCE"); v != "" {
		cfg.AgentTokenAudience = v
	}
	cfg.AgentRequireMTLS = os.Getenv("KUBEBOLT_AGENT_REQUIRE_MTLS") == "true"

	cfg.AgentRateLimitEnabled = os.Getenv("KUBEBOLT_AGENT_RATE_LIMIT_ENABLED") == "true"
	if v := readPositiveIntEnv("KUBEBOLT_AGENT_RATE_LIMIT_RPS", cfg.AgentRateLimitRPS); v != 0 {
		cfg.AgentRateLimitRPS = v
	}
	if v := readPositiveIntEnv("KUBEBOLT_AGENT_RATE_LIMIT_BURST", cfg.AgentRateLimitBurst); v != 0 {
		cfg.AgentRateLimitBurst = v
	}

	cfg.AgentAutoRegisterClusters = parseAutoRegisterEnv(os.Getenv("KUBEBOLT_AGENT_AUTOREGISTER_CLUSTERS"))
	if v := os.Getenv("KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Printf("WARN config: KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON=%q is not a valid Go duration — using default %s", v, DefaultAgentRegistryPruneHorizon)
		} else if d <= 0 {
			log.Printf("WARN config: KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON=%s must be > 0 — using default %s", d, DefaultAgentRegistryPruneHorizon)
		} else {
			cfg.AgentRegistryPruneHorizon = d
		}
	}

	cfg.RemoteWriteEnabled = parseBoolEnv(os.Getenv("KUBEBOLT_REMOTE_WRITE_ENABLED"))
	if v := os.Getenv("KUBEBOLT_REMOTE_WRITE_AUTH_MODE"); v != "" {
		if isValidAuthMode(v) {
			cfg.RemoteWriteAuthMode = v
		} else {
			log.Printf("WARN config: KUBEBOLT_REMOTE_WRITE_AUTH_MODE=%q is not one of disabled|permissive|enforced — using default %s", v, DefaultRemoteWriteAuthMode)
		}
	}

	if v := readPositiveIntEnv("KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC", cfg.PromWriteDefaultSamplesPerSec); v != 0 {
		cfg.PromWriteDefaultSamplesPerSec = v
	}
	if v := readPositiveIntEnv("KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES", cfg.PromWriteDefaultBurstSamples); v != 0 {
		cfg.PromWriteDefaultBurstSamples = v
	}
	if v := readPositiveIntEnv("KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES", cfg.PromWriteDefaultMaxActiveSeries); v != 0 {
		cfg.PromWriteDefaultMaxActiveSeries = v
	}
	// Global cap: 0 / empty = disabled (per-tenant caps only). We accept
	// 0 explicitly here, unlike readPositiveIntEnv which treats 0 as
	// "fall back to default" — there is no default for the global cap,
	// 0 IS the intended off-state.
	if raw := os.Getenv("KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES_GLOBAL"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			log.Printf("WARN config: KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES_GLOBAL=%q must be a non-negative integer — using 0 (disabled)", raw)
		} else {
			cfg.PromWriteDefaultMaxActiveSeriesGlobal = v
		}
	}

	if v := os.Getenv("KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Printf("WARN config: KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT=%q is not a valid Go duration — using default %s", v, DefaultAgentTunnelIdleTimeout)
		} else if d < 0 {
			log.Printf("WARN config: KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT=%s must be >= 0 (0 disables watchdog) — using default %s", d, DefaultAgentTunnelIdleTimeout)
		} else {
			cfg.AgentTunnelIdleTimeout = d
		}
	}

	// Agent-proxy resilience timeouts: env vars are Go durations ("25s") for
	// backward-compat with how they first shipped; stored as whole seconds.
	// Connect/request are floored; stuck accepts 0 (disabled) or a floored value.
	cfg.ConnectTimeoutSeconds = agentProxyEnvSeconds("KUBEBOLT_AGENT_PROXY_CONNECT_TIMEOUT", cfg.ConnectTimeoutSeconds, false)
	cfg.StuckTimeoutSeconds = agentProxyEnvSeconds("KUBEBOLT_AGENT_PROXY_STUCK_TIMEOUT", cfg.StuckTimeoutSeconds, true)
	cfg.RequestTimeoutSeconds = agentProxyEnvSeconds("KUBEBOLT_AGENT_PROXY_REQUEST_TIMEOUT", cfg.RequestTimeoutSeconds, false)

	return cfg
}

// agentProxyEnvSeconds parses a Go-duration env var into whole seconds, keeping
// the fallback on parse error or an out-of-range value. allowZero lets the stuck
// watchdog be disabled with "0"; otherwise the value is floored at
// MinAgentProxyTimeoutSeconds. All values are capped at MaxAgentProxyTimeoutSeconds.
func agentProxyEnvSeconds(key string, fallback int, allowZero bool) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Printf("WARN config: %s=%q is not a valid Go duration — using default %ds", key, v, fallback)
		return fallback
	}
	n := int(d.Seconds())
	if n == 0 {
		if allowZero {
			return 0
		}
		return fallback
	}
	if n < MinAgentProxyTimeoutSeconds || n > MaxAgentProxyTimeoutSeconds {
		log.Printf("WARN config: %s=%s out of range [%d,%d]s — using default %ds", key, d, MinAgentProxyTimeoutSeconds, MaxAgentProxyTimeoutSeconds, fallback)
		return fallback
	}
	return n
}

// isValidAuthMode returns true for one of the three three-tier values
// the agent + remote_write interceptors accept. Centralized so the
// three sites (env parse, BoltDB validation, settings handler) agree
// on the canonical set.
func isValidAuthMode(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "disabled", "permissive", "enforced":
		return true
	}
	return false
}

// parseAutoRegisterEnv interprets KUBEBOLT_AGENT_AUTOREGISTER_CLUSTERS.
// Mirrors main.go's parseAutoRegisterFlag so behavior is identical
// before and after the V2 centralization: empty/false/0/no = off,
// true/1/yes = on, anything else logs a WARN and defaults to off.
func parseAutoRegisterEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "false", "0", "no", "off":
		return false
	case "true", "1", "yes", "on":
		return true
	}
	log.Printf("WARN config: KUBEBOLT_AGENT_AUTOREGISTER_CLUSTERS=%q is not a recognized boolean — defaulting to false", v)
	return false
}

// parseBoolEnv is the lenient bool parser used by the remote_write
// gate. Matches the cmd/server/main.go convention to keep behavior
// identical pre- and post-V2.
func parseBoolEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
