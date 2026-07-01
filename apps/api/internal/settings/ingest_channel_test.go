package settings

import (
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Tests for the IngestChannel domain (spec #09 V2). Shape mirrors the
// existing Auth tests: env baseline + BoltDB override merge, validation
// per field, the pendingRestart computation against the boot snapshot
// for the three restart-required fields (AgentAuthMode, AgentTokenAudience,
// AgentRequireMTLS), and the masked-render shape.

func ingestEnvBaseline() config.IngestChannelConfig {
	return config.IngestChannelConfig{
		AgentAuthMode:                         "disabled",
		AgentTokenAudience:                    "kubebolt-backend",
		AgentRequireMTLS:                      false,
		AgentRateLimitEnabled:                 false,
		AgentRateLimitRPS:                     1000,
		AgentRateLimitBurst:                   2000,
		AgentAutoRegisterClusters:             false,
		AgentRegistryPruneHorizon:             24 * time.Hour,
		RemoteWriteEnabled:                    false,
		RemoteWriteAuthMode:                   "disabled",
		PromWriteDefaultSamplesPerSec:         10_000,
		PromWriteDefaultBurstSamples:          100_000,
		PromWriteDefaultMaxActiveSeries:       1_000_000,
		PromWriteDefaultMaxActiveSeriesGlobal: 0,
		AgentTunnelIdleTimeout:                5 * time.Minute,
	}
}

// newIngestChannelRuntime wires a Runtime with the IngestChannel env
// baseline populated. The other domains stay at zero values; they're
// not exercised in this test file.
func newIngestChannelRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt := newTestRuntime(t)
	rt.envIngestChannel = ingestEnvBaseline()
	return rt
}

func TestIngestChannel_EnvBaselineWithNoOverride(t *testing.T) {
	rt := newIngestChannelRuntime(t)
	got := rt.IngestChannel()
	want := ingestEnvBaseline()
	if got.AgentAuthMode != want.AgentAuthMode {
		t.Errorf("AgentAuthMode: got %q, want %q", got.AgentAuthMode, want.AgentAuthMode)
	}
	if got.AgentRateLimitRPS != want.AgentRateLimitRPS {
		t.Errorf("AgentRateLimitRPS: got %d, want %d", got.AgentRateLimitRPS, want.AgentRateLimitRPS)
	}
	if got.AgentRegistryPruneHorizon != want.AgentRegistryPruneHorizon {
		t.Errorf("AgentRegistryPruneHorizon: got %s, want %s", got.AgentRegistryPruneHorizon, want.AgentRegistryPruneHorizon)
	}
}

func TestIngestChannel_StoredOverrideWins(t *testing.T) {
	rt := newIngestChannelRuntime(t)
	patch := &StoredIngestChannelSettings{
		AgentAuthMode:             strPtr("enforced"),
		AgentRateLimitEnabled:     boolPtr(true),
		AgentRateLimitRPS:         intPtr(5000),
		AgentAutoRegisterClusters: boolPtr(true),
		RemoteWriteEnabled:        boolPtr(true),
	}
	if err := rt.PutIngestChannel(patch); err != nil {
		t.Fatalf("PutIngestChannel: %v", err)
	}
	got := rt.IngestChannel()
	if got.AgentAuthMode != "enforced" {
		t.Errorf("AgentAuthMode: stored override should win, got %q", got.AgentAuthMode)
	}
	if !got.AgentRateLimitEnabled {
		t.Errorf("AgentRateLimitEnabled: stored override should win, got false")
	}
	if got.AgentRateLimitRPS != 5000 {
		t.Errorf("AgentRateLimitRPS: stored override should win, got %d", got.AgentRateLimitRPS)
	}
	if !got.AgentAutoRegisterClusters {
		t.Errorf("AgentAutoRegisterClusters: stored override should win, got false")
	}
	if !got.RemoteWriteEnabled {
		t.Errorf("RemoteWriteEnabled: stored override should win, got false")
	}
	// Unspecified fields fall back to env baseline.
	if got.AgentTokenAudience != "kubebolt-backend" {
		t.Errorf("AgentTokenAudience: should fall back to env, got %q", got.AgentTokenAudience)
	}
}

func TestIngestChannel_ResetClearsOverride(t *testing.T) {
	rt := newIngestChannelRuntime(t)
	if err := rt.PutIngestChannel(&StoredIngestChannelSettings{
		AgentAuthMode: strPtr("enforced"),
	}); err != nil {
		t.Fatalf("PutIngestChannel: %v", err)
	}
	if got := rt.IngestChannel().AgentAuthMode; got != "enforced" {
		t.Fatalf("pre-reset: AgentAuthMode want enforced, got %q", got)
	}
	if err := rt.ResetIngestChannel(); err != nil {
		t.Fatalf("ResetIngestChannel: %v", err)
	}
	if got := rt.IngestChannel().AgentAuthMode; got != "disabled" {
		t.Errorf("post-reset: AgentAuthMode should fall back to env (disabled), got %q", got)
	}
}

func TestIngestChannel_InvalidationOnPut(t *testing.T) {
	rt := newIngestChannelRuntime(t)
	// Warm the cache.
	_ = rt.IngestChannel()
	// Mutate via PUT.
	if err := rt.PutIngestChannel(&StoredIngestChannelSettings{
		AgentRateLimitRPS: intPtr(9000),
	}); err != nil {
		t.Fatalf("PutIngestChannel: %v", err)
	}
	// Cache must have been invalidated; next read sees the new value.
	if got := rt.IngestChannel().AgentRateLimitRPS; got != 9000 {
		t.Errorf("AgentRateLimitRPS after PUT: want 9000, got %d", got)
	}
}

func TestIngestChannel_PendingRestart_OnlyRestartFields(t *testing.T) {
	rt := newIngestChannelRuntime(t)
	// Capture boot snapshot from the env baseline.
	rt.CaptureIngestChannelBootSnapshot()

	// Change a HOT-RELOADABLE field — pendingRestart must stay false.
	if err := rt.PutIngestChannel(&StoredIngestChannelSettings{
		AgentRateLimitRPS: intPtr(9000),
	}); err != nil {
		t.Fatalf("PutIngestChannel: %v", err)
	}
	masked, err := rt.RenderMaskedIngestChannel()
	if err != nil {
		t.Fatalf("RenderMaskedIngestChannel: %v", err)
	}
	if masked.PendingRestart {
		t.Errorf("PendingRestart: hot-reloadable change must NOT trigger restart, got true")
	}

	// Now change a RESTART-REQUIRED field — pendingRestart must flip.
	if err := rt.PutIngestChannel(&StoredIngestChannelSettings{
		AgentAuthMode: strPtr("enforced"),
	}); err != nil {
		t.Fatalf("PutIngestChannel: %v", err)
	}
	masked, err = rt.RenderMaskedIngestChannel()
	if err != nil {
		t.Fatalf("RenderMaskedIngestChannel: %v", err)
	}
	if !masked.PendingRestart {
		t.Errorf("PendingRestart: restart-required change must trigger restart, got false")
	}
	if masked.Effective.AgentAuthMode != "enforced" {
		t.Errorf("Effective.AgentAuthMode: want enforced, got %q", masked.Effective.AgentAuthMode)
	}
	if masked.BootSnapshot.AgentAuthMode != "disabled" {
		t.Errorf("BootSnapshot.AgentAuthMode: should still reflect boot (disabled), got %q", masked.BootSnapshot.AgentAuthMode)
	}
}

func TestIngestChannel_ValidationRejectsBadInput(t *testing.T) {
	rt := newIngestChannelRuntime(t)
	cases := []struct {
		name  string
		patch *StoredIngestChannelSettings
		field string
	}{
		{"bad agent auth mode", &StoredIngestChannelSettings{AgentAuthMode: strPtr("yolo")}, "agentAuthMode"},
		{"bad remote write auth mode", &StoredIngestChannelSettings{RemoteWriteAuthMode: strPtr("loose")}, "remoteWriteAuthMode"},
		{"zero rps", &StoredIngestChannelSettings{AgentRateLimitRPS: intPtr(0)}, "agentRateLimitRPS"},
		{"negative burst", &StoredIngestChannelSettings{AgentRateLimitBurst: intPtr(-1)}, "agentRateLimitBurst"},
		{"zero prune horizon", &StoredIngestChannelSettings{AgentRegistryPruneHorizonSecs: intPtr(0)}, "agentRegistryPruneHorizonSecs"},
		{"negative global series cap", &StoredIngestChannelSettings{PromWriteDefaultMaxActiveSeriesGlobal: intPtr(-5)}, "promWriteDefaultMaxActiveSeriesGlobal"},
		{"negative tunnel idle timeout", &StoredIngestChannelSettings{AgentTunnelIdleTimeoutSecs: intPtr(-1)}, "agentTunnelIdleTimeoutSecs"},
		{"token audience too long", &StoredIngestChannelSettings{AgentTokenAudience: strPtr(longString(257))}, "agentTokenAudience"},
		{"connect timeout too low", &StoredIngestChannelSettings{ConnectTimeoutSeconds: intPtr(4)}, "connectTimeoutSeconds"},
		{"connect timeout too high", &StoredIngestChannelSettings{ConnectTimeoutSeconds: intPtr(601)}, "connectTimeoutSeconds"},
		{"stuck timeout too low (non-zero)", &StoredIngestChannelSettings{StuckTimeoutSeconds: intPtr(3)}, "stuckTimeoutSeconds"},
		{"request timeout too high", &StoredIngestChannelSettings{RequestTimeoutSeconds: intPtr(9999)}, "requestTimeoutSeconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rt.PutIngestChannel(tc.patch)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected ValidationError, got %T: %v", err, err)
			}
			if ve.Field != tc.field {
				t.Errorf("Field: got %q, want %q", ve.Field, tc.field)
			}
		})
	}
}

func TestIngestChannel_AcceptsZeroTunnelIdleTimeoutAsDisable(t *testing.T) {
	// Idle timeout 0 disables the watchdog — must be accepted as a
	// meaningful explicit value, not rejected as "<= 0".
	rt := newIngestChannelRuntime(t)
	if err := rt.PutIngestChannel(&StoredIngestChannelSettings{
		AgentTunnelIdleTimeoutSecs: intPtr(0),
	}); err != nil {
		t.Fatalf("PutIngestChannel: %v", err)
	}
	if got := rt.IngestChannel().AgentTunnelIdleTimeout; got != 0 {
		t.Errorf("AgentTunnelIdleTimeout: want 0 (disabled), got %s", got)
	}
}

func TestIngestChannel_AcceptsZeroGlobalSeriesCapAsDisable(t *testing.T) {
	// Global series cap 0 = disabled (per-tenant caps only).
	rt := newIngestChannelRuntime(t)
	// First set to non-zero to prove the diff path works.
	if err := rt.PutIngestChannel(&StoredIngestChannelSettings{
		PromWriteDefaultMaxActiveSeriesGlobal: intPtr(500_000),
	}); err != nil {
		t.Fatalf("set non-zero: %v", err)
	}
	if got := rt.IngestChannel().PromWriteDefaultMaxActiveSeriesGlobal; got != 500_000 {
		t.Fatalf("intermediate: got %d", got)
	}
	// Now reset to 0 explicitly.
	if err := rt.PutIngestChannel(&StoredIngestChannelSettings{
		PromWriteDefaultMaxActiveSeriesGlobal: intPtr(0),
	}); err != nil {
		t.Fatalf("set zero: %v", err)
	}
	if got := rt.IngestChannel().PromWriteDefaultMaxActiveSeriesGlobal; got != 0 {
		t.Errorf("PromWriteDefaultMaxActiveSeriesGlobal: want 0, got %d", got)
	}
}

func TestIngestChannel_AgentProxyTimeoutsResolveAndDisable(t *testing.T) {
	rt := newIngestChannelRuntime(t)
	// Override all three; stuck=0 must be accepted as "disable the watchdog".
	if err := rt.PutIngestChannel(&StoredIngestChannelSettings{
		ConnectTimeoutSeconds: intPtr(40),
		StuckTimeoutSeconds:   intPtr(0),
		RequestTimeoutSeconds: intPtr(50),
	}); err != nil {
		t.Fatalf("PutIngestChannel: %v", err)
	}
	got := rt.IngestChannel()
	if got.ConnectTimeoutSeconds != 40 {
		t.Errorf("ConnectTimeoutSeconds: want 40, got %d", got.ConnectTimeoutSeconds)
	}
	if got.StuckTimeoutSeconds != 0 {
		t.Errorf("StuckTimeoutSeconds: want 0 (disabled), got %d", got.StuckTimeoutSeconds)
	}
	if got.RequestTimeoutSeconds != 50 {
		t.Errorf("RequestTimeoutSeconds: want 50, got %d", got.RequestTimeoutSeconds)
	}
}

func longString(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'x'
	}
	return string(out)
}
