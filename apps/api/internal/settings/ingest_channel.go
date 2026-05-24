package settings

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Spec #09 V2 — IngestChannel covers everything in the
// kubebolt-agent ↔ kubebolt communication plane (auth modes, rate
// limits, auto-registration, tunnel timeouts, Prom remote_write
// receiver). Three of its fields require a restart to apply
// (AgentAuthMode, AgentTokenAudience, AgentRequireMTLS) because they
// wire into the gRPC server's auth interceptor at boot; the rest are
// hot-reloadable and consumers read them via runtime.IngestChannel()
// per request / per tick.
//
// Restart-required handling mirrors V1 Auth: PUT writes to BoltDB and
// returns 200 with pendingRestart=true; the UI surfaces a banner; on
// next process boot the new values seed the wired-once subsystems.

const ingestChannelSettingsKey = "ingest_channel"

// StoredIngestChannelSettings is the on-disk shape for the
// IngestChannel domain. Every field is a pointer so nil means "fall
// back to env baseline" — same partial-override semantics as the
// other Settings domains.
//
// Durations are stored as seconds (int) rather than Go duration
// strings because the UI's input is a number-of-seconds field;
// round-tripping through time.ParseDuration adds a parse step at
// every read with no operator-visible benefit.
type StoredIngestChannelSettings struct {
	// Channel security (restart-required).
	AgentAuthMode      *string `json:"agentAuthMode,omitempty"`
	AgentTokenAudience *string `json:"agentTokenAudience,omitempty"`
	AgentRequireMTLS   *bool   `json:"agentRequireMTLS,omitempty"`

	// Rate limiting (fleet-wide).
	AgentRateLimitEnabled *bool `json:"agentRateLimitEnabled,omitempty"`
	AgentRateLimitRPS     *int  `json:"agentRateLimitRPS,omitempty"`
	AgentRateLimitBurst   *int  `json:"agentRateLimitBurst,omitempty"`

	// Cluster auto-registration.
	AgentAutoRegisterClusters     *bool `json:"agentAutoRegisterClusters,omitempty"`
	AgentRegistryPruneHorizonSecs *int  `json:"agentRegistryPruneHorizonSecs,omitempty"`

	// Prom remote_write receiver.
	RemoteWriteEnabled                    *bool   `json:"remoteWriteEnabled,omitempty"`
	RemoteWriteAuthMode                   *string `json:"remoteWriteAuthMode,omitempty"`
	PromWriteDefaultSamplesPerSec         *int    `json:"promWriteDefaultSamplesPerSec,omitempty"`
	PromWriteDefaultBurstSamples          *int    `json:"promWriteDefaultBurstSamples,omitempty"`
	PromWriteDefaultMaxActiveSeries       *int    `json:"promWriteDefaultMaxActiveSeries,omitempty"`
	PromWriteDefaultMaxActiveSeriesGlobal *int    `json:"promWriteDefaultMaxActiveSeriesGlobal,omitempty"`

	// SPDY tunnels.
	AgentTunnelIdleTimeoutSecs *int `json:"agentTunnelIdleTimeoutSecs,omitempty"`
}

// IngestChannel returns the resolved IngestChannelConfig (env +
// BoltDB override). Cached until InvalidateIngestChannel.
//
// Note: like Auth(), this is the "what would the channel look like if
// we restarted right now" config — NOT what the running interceptor is
// using for the restart-required fields. Compare against
// IngestChannelBootSnapshot() to compute pendingRestart.
func (r *Runtime) IngestChannel() config.IngestChannelConfig {
	r.mu.RLock()
	if r.ingestChannelValid {
		cfg := r.ingestChannel
		r.mu.RUnlock()
		return cfg
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ingestChannelValid {
		return r.ingestChannel
	}
	r.ingestChannel = r.resolveIngestChannelLocked()
	r.ingestChannelValid = true
	return r.ingestChannel
}

func (r *Runtime) InvalidateIngestChannel() {
	r.mu.Lock()
	r.ingestChannelValid = false
	r.mu.Unlock()
}

// CaptureIngestChannelBootSnapshot records the resolved IngestChannel
// config as the "this is what the running process was built from"
// baseline. Called once at boot AFTER the gRPC server has been wired
// with the resolved channel values. pendingRestart is computed by
// diffing the live resolved IngestChannel() against this snapshot,
// limited to the restart-required subset.
func (r *Runtime) CaptureIngestChannelBootSnapshot() {
	resolved := r.IngestChannel()
	r.mu.Lock()
	r.ingestChannelBootSnapshot = resolved
	r.ingestChannelBootCaptured = true
	r.mu.Unlock()
}

// IngestChannelBootSnapshot returns the boot-time resolved config that
// the live gRPC interceptor was wired from.
func (r *Runtime) IngestChannelBootSnapshot() (config.IngestChannelConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ingestChannelBootSnapshot, r.ingestChannelBootCaptured
}

func (r *Runtime) resolveIngestChannelLocked() config.IngestChannelConfig {
	cfg := r.envIngestChannel

	raw, err := r.store.GetSetting(ingestChannelSettingsKey)
	if err != nil {
		return cfg
	}
	var stored StoredIngestChannelSettings
	if err := json.Unmarshal(raw, &stored); err != nil {
		return cfg
	}
	applyStoredIngestChannel(&cfg, &stored)
	return cfg
}

func applyStoredIngestChannel(cfg *config.IngestChannelConfig, stored *StoredIngestChannelSettings) {
	if stored.AgentAuthMode != nil {
		cfg.AgentAuthMode = *stored.AgentAuthMode
	}
	if stored.AgentTokenAudience != nil {
		cfg.AgentTokenAudience = *stored.AgentTokenAudience
	}
	if stored.AgentRequireMTLS != nil {
		cfg.AgentRequireMTLS = *stored.AgentRequireMTLS
	}
	if stored.AgentRateLimitEnabled != nil {
		cfg.AgentRateLimitEnabled = *stored.AgentRateLimitEnabled
	}
	if stored.AgentRateLimitRPS != nil && *stored.AgentRateLimitRPS > 0 {
		cfg.AgentRateLimitRPS = *stored.AgentRateLimitRPS
	}
	if stored.AgentRateLimitBurst != nil && *stored.AgentRateLimitBurst > 0 {
		cfg.AgentRateLimitBurst = *stored.AgentRateLimitBurst
	}
	if stored.AgentAutoRegisterClusters != nil {
		cfg.AgentAutoRegisterClusters = *stored.AgentAutoRegisterClusters
	}
	if stored.AgentRegistryPruneHorizonSecs != nil && *stored.AgentRegistryPruneHorizonSecs > 0 {
		cfg.AgentRegistryPruneHorizon = time.Duration(*stored.AgentRegistryPruneHorizonSecs) * time.Second
	}
	if stored.RemoteWriteEnabled != nil {
		cfg.RemoteWriteEnabled = *stored.RemoteWriteEnabled
	}
	if stored.RemoteWriteAuthMode != nil {
		cfg.RemoteWriteAuthMode = *stored.RemoteWriteAuthMode
	}
	if stored.PromWriteDefaultSamplesPerSec != nil && *stored.PromWriteDefaultSamplesPerSec > 0 {
		cfg.PromWriteDefaultSamplesPerSec = *stored.PromWriteDefaultSamplesPerSec
	}
	if stored.PromWriteDefaultBurstSamples != nil && *stored.PromWriteDefaultBurstSamples > 0 {
		cfg.PromWriteDefaultBurstSamples = *stored.PromWriteDefaultBurstSamples
	}
	if stored.PromWriteDefaultMaxActiveSeries != nil && *stored.PromWriteDefaultMaxActiveSeries > 0 {
		cfg.PromWriteDefaultMaxActiveSeries = *stored.PromWriteDefaultMaxActiveSeries
	}
	// Global cap accepts 0 as a meaningful value (disabled). Negative
	// is rejected by validation; non-nil means "use what was stored,
	// including 0 to explicitly disable".
	if stored.PromWriteDefaultMaxActiveSeriesGlobal != nil && *stored.PromWriteDefaultMaxActiveSeriesGlobal >= 0 {
		cfg.PromWriteDefaultMaxActiveSeriesGlobal = *stored.PromWriteDefaultMaxActiveSeriesGlobal
	}
	// Idle timeout: 0 IS a valid value (disables the watchdog). We
	// distinguish nil ("inherit env") from 0 ("explicitly disabled").
	if stored.AgentTunnelIdleTimeoutSecs != nil && *stored.AgentTunnelIdleTimeoutSecs >= 0 {
		cfg.AgentTunnelIdleTimeout = time.Duration(*stored.AgentTunnelIdleTimeoutSecs) * time.Second
	}
}

// PutIngestChannel validates and persists a partial IngestChannel patch.
// Restart-required fields (auth mode, token audience, mTLS) update
// BoltDB but don't change the running interceptor — pendingRestart is
// surfaced in MaskedIngestChannel; the rest are hot-reloadable and
// effective on the next read by the relevant consumer.
func (r *Runtime) PutIngestChannel(patch *StoredIngestChannelSettings) error {
	if err := validateIngestChannelPatch(patch); err != nil {
		return err
	}
	existing, _ := r.loadIngestChannel()
	merged := mergeIngestChannel(existing, *patch)

	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := r.store.SetSetting(ingestChannelSettingsKey, encoded); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	r.InvalidateIngestChannel()
	return nil
}

func (r *Runtime) ResetIngestChannel() error {
	if err := r.store.SetSetting(ingestChannelSettingsKey, []byte("null")); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	r.InvalidateIngestChannel()
	return nil
}

func (r *Runtime) loadIngestChannel() (StoredIngestChannelSettings, error) {
	raw, err := r.store.GetSetting(ingestChannelSettingsKey)
	if err != nil {
		return StoredIngestChannelSettings{}, nil
	}
	var out StoredIngestChannelSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return StoredIngestChannelSettings{}, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// ─── Masked render ────────────────────────────────────────────────────

// MaskedIngestChannel is the GET response shape. Same effective/
// bootSnapshot/stored/pendingRestart shape as MaskedAuth so the UI can
// reuse the side-by-side comparison component.
type MaskedIngestChannel struct {
	Effective      MaskedEffectiveIngestChannel `json:"effective"`
	BootSnapshot   MaskedEffectiveIngestChannel `json:"bootSnapshot"`
	Stored         MaskedStoredIngestChannel    `json:"stored"`
	PendingRestart bool                         `json:"pendingRestart"`
}

type MaskedEffectiveIngestChannel struct {
	// Channel security.
	AgentAuthMode      string `json:"agentAuthMode"`
	AgentTokenAudience string `json:"agentTokenAudience"`
	AgentRequireMTLS   bool   `json:"agentRequireMTLS"`
	// Rate limiting.
	AgentRateLimitEnabled bool `json:"agentRateLimitEnabled"`
	AgentRateLimitRPS     int  `json:"agentRateLimitRPS"`
	AgentRateLimitBurst   int  `json:"agentRateLimitBurst"`
	// Cluster auto-registration.
	AgentAutoRegisterClusters     bool `json:"agentAutoRegisterClusters"`
	AgentRegistryPruneHorizonSecs int  `json:"agentRegistryPruneHorizonSecs"`
	// Prom remote_write.
	RemoteWriteEnabled                    bool   `json:"remoteWriteEnabled"`
	RemoteWriteAuthMode                   string `json:"remoteWriteAuthMode"`
	PromWriteDefaultSamplesPerSec         int    `json:"promWriteDefaultSamplesPerSec"`
	PromWriteDefaultBurstSamples          int    `json:"promWriteDefaultBurstSamples"`
	PromWriteDefaultMaxActiveSeries       int    `json:"promWriteDefaultMaxActiveSeries"`
	PromWriteDefaultMaxActiveSeriesGlobal int    `json:"promWriteDefaultMaxActiveSeriesGlobal"`
	// Tunnels.
	AgentTunnelIdleTimeoutSecs int `json:"agentTunnelIdleTimeoutSecs"`
}

type MaskedStoredIngestChannel struct {
	HasOverride                           bool    `json:"hasOverride"`
	AgentAuthMode                         *string `json:"agentAuthMode,omitempty"`
	AgentTokenAudience                    *string `json:"agentTokenAudience,omitempty"`
	AgentRequireMTLS                      *bool   `json:"agentRequireMTLS,omitempty"`
	AgentRateLimitEnabled                 *bool   `json:"agentRateLimitEnabled,omitempty"`
	AgentRateLimitRPS                     *int    `json:"agentRateLimitRPS,omitempty"`
	AgentRateLimitBurst                   *int    `json:"agentRateLimitBurst,omitempty"`
	AgentAutoRegisterClusters             *bool   `json:"agentAutoRegisterClusters,omitempty"`
	AgentRegistryPruneHorizonSecs         *int    `json:"agentRegistryPruneHorizonSecs,omitempty"`
	RemoteWriteEnabled                    *bool   `json:"remoteWriteEnabled,omitempty"`
	RemoteWriteAuthMode                   *string `json:"remoteWriteAuthMode,omitempty"`
	PromWriteDefaultSamplesPerSec         *int    `json:"promWriteDefaultSamplesPerSec,omitempty"`
	PromWriteDefaultBurstSamples          *int    `json:"promWriteDefaultBurstSamples,omitempty"`
	PromWriteDefaultMaxActiveSeries       *int    `json:"promWriteDefaultMaxActiveSeries,omitempty"`
	PromWriteDefaultMaxActiveSeriesGlobal *int    `json:"promWriteDefaultMaxActiveSeriesGlobal,omitempty"`
	AgentTunnelIdleTimeoutSecs            *int    `json:"agentTunnelIdleTimeoutSecs,omitempty"`
}

func (r *Runtime) RenderMaskedIngestChannel() (MaskedIngestChannel, error) {
	stored, err := r.loadIngestChannel()
	if err != nil {
		return MaskedIngestChannel{}, err
	}
	resolved := r.IngestChannel()
	boot, captured := r.IngestChannelBootSnapshot()
	if !captured {
		boot = r.envIngestChannel
	}
	pending := !sameIngestChannelRestartFields(resolved, boot)

	out := MaskedIngestChannel{
		Effective:      renderEffectiveIngestChannel(resolved),
		BootSnapshot:   renderEffectiveIngestChannel(boot),
		Stored:         renderStoredIngestChannelMask(stored),
		PendingRestart: pending,
	}
	return out, nil
}

func renderEffectiveIngestChannel(cfg config.IngestChannelConfig) MaskedEffectiveIngestChannel {
	return MaskedEffectiveIngestChannel{
		AgentAuthMode:                         cfg.AgentAuthMode,
		AgentTokenAudience:                    cfg.AgentTokenAudience,
		AgentRequireMTLS:                      cfg.AgentRequireMTLS,
		AgentRateLimitEnabled:                 cfg.AgentRateLimitEnabled,
		AgentRateLimitRPS:                     cfg.AgentRateLimitRPS,
		AgentRateLimitBurst:                   cfg.AgentRateLimitBurst,
		AgentAutoRegisterClusters:             cfg.AgentAutoRegisterClusters,
		AgentRegistryPruneHorizonSecs:         int(cfg.AgentRegistryPruneHorizon / time.Second),
		RemoteWriteEnabled:                    cfg.RemoteWriteEnabled,
		RemoteWriteAuthMode:                   cfg.RemoteWriteAuthMode,
		PromWriteDefaultSamplesPerSec:         cfg.PromWriteDefaultSamplesPerSec,
		PromWriteDefaultBurstSamples:          cfg.PromWriteDefaultBurstSamples,
		PromWriteDefaultMaxActiveSeries:       cfg.PromWriteDefaultMaxActiveSeries,
		PromWriteDefaultMaxActiveSeriesGlobal: cfg.PromWriteDefaultMaxActiveSeriesGlobal,
		AgentTunnelIdleTimeoutSecs:            int(cfg.AgentTunnelIdleTimeout / time.Second),
	}
}

func renderStoredIngestChannelMask(s StoredIngestChannelSettings) MaskedStoredIngestChannel {
	out := MaskedStoredIngestChannel{
		AgentAuthMode:                         s.AgentAuthMode,
		AgentTokenAudience:                    s.AgentTokenAudience,
		AgentRequireMTLS:                      s.AgentRequireMTLS,
		AgentRateLimitEnabled:                 s.AgentRateLimitEnabled,
		AgentRateLimitRPS:                     s.AgentRateLimitRPS,
		AgentRateLimitBurst:                   s.AgentRateLimitBurst,
		AgentAutoRegisterClusters:             s.AgentAutoRegisterClusters,
		AgentRegistryPruneHorizonSecs:         s.AgentRegistryPruneHorizonSecs,
		RemoteWriteEnabled:                    s.RemoteWriteEnabled,
		RemoteWriteAuthMode:                   s.RemoteWriteAuthMode,
		PromWriteDefaultSamplesPerSec:         s.PromWriteDefaultSamplesPerSec,
		PromWriteDefaultBurstSamples:          s.PromWriteDefaultBurstSamples,
		PromWriteDefaultMaxActiveSeries:       s.PromWriteDefaultMaxActiveSeries,
		PromWriteDefaultMaxActiveSeriesGlobal: s.PromWriteDefaultMaxActiveSeriesGlobal,
		AgentTunnelIdleTimeoutSecs:            s.AgentTunnelIdleTimeoutSecs,
	}
	out.HasOverride = s.AgentAuthMode != nil ||
		s.AgentTokenAudience != nil ||
		s.AgentRequireMTLS != nil ||
		s.AgentRateLimitEnabled != nil ||
		s.AgentRateLimitRPS != nil ||
		s.AgentRateLimitBurst != nil ||
		s.AgentAutoRegisterClusters != nil ||
		s.AgentRegistryPruneHorizonSecs != nil ||
		s.RemoteWriteEnabled != nil ||
		s.RemoteWriteAuthMode != nil ||
		s.PromWriteDefaultSamplesPerSec != nil ||
		s.PromWriteDefaultBurstSamples != nil ||
		s.PromWriteDefaultMaxActiveSeries != nil ||
		s.PromWriteDefaultMaxActiveSeriesGlobal != nil ||
		s.AgentTunnelIdleTimeoutSecs != nil
	return out
}

// sameIngestChannelRestartFields compares only the restart-required
// subset (AgentAuthMode, AgentTokenAudience, AgentRequireMTLS). Other
// fields are hot-reloadable, so a diff on them must NOT trigger the
// pendingRestart banner.
func sameIngestChannelRestartFields(a, b config.IngestChannelConfig) bool {
	return a.AgentAuthMode == b.AgentAuthMode &&
		a.AgentTokenAudience == b.AgentTokenAudience &&
		a.AgentRequireMTLS == b.AgentRequireMTLS
}

// ─── Validation + merge ──────────────────────────────────────────────

func validateIngestChannelPatch(p *StoredIngestChannelSettings) error {
	if p == nil {
		return &ValidationError{Field: "patch", Message: "patch is required"}
	}
	if p.AgentAuthMode != nil && !isValidThreeTierMode(*p.AgentAuthMode) {
		return &ValidationError{Field: "agentAuthMode", Message: "must be one of disabled, permissive, enforced"}
	}
	if p.RemoteWriteAuthMode != nil && !isValidThreeTierMode(*p.RemoteWriteAuthMode) {
		return &ValidationError{Field: "remoteWriteAuthMode", Message: "must be one of disabled, permissive, enforced"}
	}
	if p.AgentTokenAudience != nil {
		// Audience is a stringly-typed identifier; no whitespace allowed
		// (the K8s TokenReview expects a single token). Cap length at
		// 256 to defend against accidental paste of an entire URL.
		if len(*p.AgentTokenAudience) > 256 {
			return &ValidationError{Field: "agentTokenAudience", Message: "must be 256 characters or fewer"}
		}
	}
	if p.AgentRateLimitRPS != nil && *p.AgentRateLimitRPS <= 0 {
		return &ValidationError{Field: "agentRateLimitRPS", Message: "must be > 0"}
	}
	if p.AgentRateLimitBurst != nil && *p.AgentRateLimitBurst <= 0 {
		return &ValidationError{Field: "agentRateLimitBurst", Message: "must be > 0"}
	}
	if p.AgentRegistryPruneHorizonSecs != nil && *p.AgentRegistryPruneHorizonSecs <= 0 {
		return &ValidationError{Field: "agentRegistryPruneHorizonSecs", Message: "must be > 0"}
	}
	if p.PromWriteDefaultSamplesPerSec != nil && *p.PromWriteDefaultSamplesPerSec <= 0 {
		return &ValidationError{Field: "promWriteDefaultSamplesPerSec", Message: "must be > 0"}
	}
	if p.PromWriteDefaultBurstSamples != nil && *p.PromWriteDefaultBurstSamples <= 0 {
		return &ValidationError{Field: "promWriteDefaultBurstSamples", Message: "must be > 0"}
	}
	if p.PromWriteDefaultMaxActiveSeries != nil && *p.PromWriteDefaultMaxActiveSeries <= 0 {
		return &ValidationError{Field: "promWriteDefaultMaxActiveSeries", Message: "must be > 0"}
	}
	if p.PromWriteDefaultMaxActiveSeriesGlobal != nil && *p.PromWriteDefaultMaxActiveSeriesGlobal < 0 {
		return &ValidationError{Field: "promWriteDefaultMaxActiveSeriesGlobal", Message: "must be >= 0 (0 disables the global cap)"}
	}
	if p.AgentTunnelIdleTimeoutSecs != nil && *p.AgentTunnelIdleTimeoutSecs < 0 {
		return &ValidationError{Field: "agentTunnelIdleTimeoutSecs", Message: "must be >= 0 (0 disables the watchdog)"}
	}
	return nil
}

func isValidThreeTierMode(s string) bool {
	switch s {
	case "disabled", "permissive", "enforced":
		return true
	}
	return false
}

func mergeIngestChannel(base, patch StoredIngestChannelSettings) StoredIngestChannelSettings {
	out := base
	if patch.AgentAuthMode != nil {
		out.AgentAuthMode = patch.AgentAuthMode
	}
	if patch.AgentTokenAudience != nil {
		out.AgentTokenAudience = patch.AgentTokenAudience
	}
	if patch.AgentRequireMTLS != nil {
		out.AgentRequireMTLS = patch.AgentRequireMTLS
	}
	if patch.AgentRateLimitEnabled != nil {
		out.AgentRateLimitEnabled = patch.AgentRateLimitEnabled
	}
	if patch.AgentRateLimitRPS != nil {
		out.AgentRateLimitRPS = patch.AgentRateLimitRPS
	}
	if patch.AgentRateLimitBurst != nil {
		out.AgentRateLimitBurst = patch.AgentRateLimitBurst
	}
	if patch.AgentAutoRegisterClusters != nil {
		out.AgentAutoRegisterClusters = patch.AgentAutoRegisterClusters
	}
	if patch.AgentRegistryPruneHorizonSecs != nil {
		out.AgentRegistryPruneHorizonSecs = patch.AgentRegistryPruneHorizonSecs
	}
	if patch.RemoteWriteEnabled != nil {
		out.RemoteWriteEnabled = patch.RemoteWriteEnabled
	}
	if patch.RemoteWriteAuthMode != nil {
		out.RemoteWriteAuthMode = patch.RemoteWriteAuthMode
	}
	if patch.PromWriteDefaultSamplesPerSec != nil {
		out.PromWriteDefaultSamplesPerSec = patch.PromWriteDefaultSamplesPerSec
	}
	if patch.PromWriteDefaultBurstSamples != nil {
		out.PromWriteDefaultBurstSamples = patch.PromWriteDefaultBurstSamples
	}
	if patch.PromWriteDefaultMaxActiveSeries != nil {
		out.PromWriteDefaultMaxActiveSeries = patch.PromWriteDefaultMaxActiveSeries
	}
	if patch.PromWriteDefaultMaxActiveSeriesGlobal != nil {
		out.PromWriteDefaultMaxActiveSeriesGlobal = patch.PromWriteDefaultMaxActiveSeriesGlobal
	}
	if patch.AgentTunnelIdleTimeoutSecs != nil {
		out.AgentTunnelIdleTimeoutSecs = patch.AgentTunnelIdleTimeoutSecs
	}
	return out
}
