package settings

import (
	"context"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// runtimeWithEnvFallback is newTestRuntime plus a fallback in the env baseline
// (KUBEBOLT_AI_FALLBACK_*), the case where turning the fallback off in the UI
// did not stick.
func runtimeWithEnvFallback(t *testing.T) (*Runtime, *config.ProviderConfig) {
	t.Helper()
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = false // the per-org BYOK resolver
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })

	rt := newTestRuntime(t)
	envFB := &config.ProviderConfig{Provider: "openai", APIKey: "sk-env-fallback-key", Model: "gpt-5-mini"}
	rt.envBase.Fallback = envFB
	return rt, envFB
}

// Turning the fallback off over an env one stays off: across an unrelated save,
// and until a fallback setting turns it back on, which brings the env one back
// as env says it.
func TestFallbackOff_StaysOffOverTheEnvFallback(t *testing.T) {
	ctx := context.Background()
	rt, envFB := runtimeWithEnvFallback(t)

	if got := rt.Copilot(ctx).Fallback; got == nil || got.APIKey != envFB.APIKey {
		t.Fatalf("before: fallback = %+v, want the env one", got)
	}

	// What the UI's toggle sends to turn it off: an empty fallback key.
	if err := rt.PutCopilot(ctx, &StoredCopilotSettings{}, nil, strPtr("")); err != nil {
		t.Fatalf("turn off: %v", err)
	}
	if got := rt.Copilot(ctx).Fallback; got != nil {
		t.Fatalf("after turning it off: fallback = %+v, want none (the env one came back)", got)
	}

	view, err := rt.RenderMaskedCopilot(ctx)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if view.Effective.HasFallback {
		t.Error("the view says a fallback is in effect")
	}
	if !view.Stored.FallbackOff {
		t.Error("the stored view does not say the fallback was turned off")
	}
	// The fields describe what turning it back on restores, so the form starts
	// from the env provider instead of a default stored over it.
	if view.Effective.FallbackProvider != "openai" || view.Effective.FallbackModel != "gpt-5-mini" || view.Effective.FallbackAPIKeyMasked == "" {
		t.Errorf("view fallback fields = %q / %q / %q, want the env fallback's",
			view.Effective.FallbackProvider, view.Effective.FallbackModel, view.Effective.FallbackAPIKeyMasked)
	}

	// An unrelated save keeps it off.
	if err := rt.PutCopilot(ctx, &StoredCopilotSettings{MaxTokens: intPtr(8192)}, nil, nil); err != nil {
		t.Fatalf("unrelated save: %v", err)
	}
	if got := rt.Copilot(ctx).Fallback; got != nil {
		t.Fatalf("an unrelated save turned the fallback back on: %+v", got)
	}

	// Turning it on sends the fallback's provider (buildPatch always does).
	if err := rt.PutCopilot(ctx, &StoredCopilotSettings{Fallback: &StoredProviderSettings{Provider: strPtr("openai")}}, nil, nil); err != nil {
		t.Fatalf("turn on: %v", err)
	}
	got := rt.Copilot(ctx).Fallback
	if got == nil || got.APIKey != envFB.APIKey || got.Model != envFB.Model || got.Provider != "openai" {
		t.Fatalf("after turning it on: fallback = %+v, want the env one", got)
	}
	if view, _ := rt.RenderMaskedCopilot(ctx); view.Stored.FallbackOff || !view.Effective.HasFallback {
		t.Errorf("after turning it on: stored off = %v, in effect = %v", view.Stored.FallbackOff, view.Effective.HasFallback)
	}
}

// With no env fallback, turning it off leaves nothing behind and the view stays
// empty, as before.
func TestFallbackOff_WithoutAnEnvFallback(t *testing.T) {
	ctx := context.Background()
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = false
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })
	rt := newTestRuntime(t)

	if err := rt.PutCopilot(ctx, &StoredCopilotSettings{}, nil, strPtr("sk-ui-fallback-key")); err != nil {
		t.Fatalf("turn on: %v", err)
	}
	if got := rt.Copilot(ctx).Fallback; got == nil || got.APIKey != "sk-ui-fallback-key" {
		t.Fatalf("fallback = %+v, want the one set in the UI", got)
	}
	if err := rt.PutCopilot(ctx, &StoredCopilotSettings{}, nil, strPtr("")); err != nil {
		t.Fatalf("turn off: %v", err)
	}
	if got := rt.Copilot(ctx).Fallback; got != nil {
		t.Fatalf("fallback = %+v, want none", got)
	}
	view, _ := rt.RenderMaskedCopilot(ctx)
	if view.Effective.HasFallback || view.Effective.FallbackProvider != "" || view.Effective.FallbackAPIKeyMasked != "" {
		t.Errorf("view = %+v, want no fallback fields", view.Effective)
	}
}

// Reset to env defaults clears the stored off with everything else.
func TestFallbackOff_ResetBringsTheEnvFallbackBack(t *testing.T) {
	ctx := context.Background()
	rt, envFB := runtimeWithEnvFallback(t)

	if err := rt.PutCopilot(ctx, &StoredCopilotSettings{}, nil, strPtr("")); err != nil {
		t.Fatalf("turn off: %v", err)
	}
	if err := rt.ResetCopilot(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := rt.Copilot(ctx).Fallback; got == nil || got.APIKey != envFB.APIKey {
		t.Fatalf("after reset: fallback = %+v, want the env one", got)
	}
}

func TestMergeCopilot_FallbackOff(t *testing.T) {
	empty, enc, model := "", "envelope", "gpt-5-mini"
	stored := StoredCopilotSettings{Fallback: &StoredProviderSettings{APIKeyEncoded: &enc, Model: &model}}

	off := mergeCopilot(stored, StoredCopilotSettings{Fallback: &StoredProviderSettings{APIKeyEncoded: &empty}})
	if off.Fallback != nil || !off.fallbackOff() {
		t.Fatalf("an empty fallback key must drop the section and store the off: %+v", off)
	}
	if got := mergeCopilot(off, StoredCopilotSettings{MaxTokens: intPtr(1)}); !got.fallbackOff() {
		t.Error("an unrelated patch dropped the off")
	}
	if got := mergeCopilot(off, StoredCopilotSettings{Fallback: &StoredProviderSettings{Model: &model}}); got.FallbackOff != nil || got.Fallback == nil {
		t.Errorf("a fallback setting must turn it back on: %+v", got)
	}
	if got := mergeCopilot(stored, StoredCopilotSettings{FallbackOff: boolPtr(true)}); got.Fallback != nil || !got.fallbackOff() {
		t.Errorf("fallbackOff:true must drop the section and store the off: %+v", got)
	}
	if got := mergeCopilot(off, StoredCopilotSettings{FallbackOff: boolPtr(false)}); got.FallbackOff != nil {
		t.Errorf("fallbackOff:false must clear the off: %+v", got)
	}
	if mask := renderStoredMask(off); !mask.FallbackOff || mask.HasFallbackOverride {
		t.Errorf("stored view = %+v, want fallbackOff and no override", mask)
	}
}

// A stored fallback override merges onto a copy of the env one: the env
// baseline is shared by every resolve and must stay what env says.
func TestApplyStoredFallback_DoesNotWriteThroughTheEnvBaseline(t *testing.T) {
	crypto, err := newSecretCrypto([]byte("a-decent-length-secret-here-32+"))
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}
	envFB := &config.ProviderConfig{Provider: "openai", APIKey: "sk-env-fallback-key", Model: "gpt-5-mini"}
	cfg := config.CopilotConfig{
		Primary:  config.ProviderConfig{Provider: "anthropic", APIKey: "sk-primary"},
		Fallback: envFB,
	}
	applyStoredCopilot(&cfg, &StoredCopilotSettings{Fallback: &StoredProviderSettings{Model: strPtr("gpt-5")}}, crypto)

	if cfg.Fallback.Model != "gpt-5" || cfg.Fallback.APIKey != envFB.APIKey {
		t.Errorf("resolved fallback = %+v, want the override over the env one", cfg.Fallback)
	}
	if envFB.Model != "gpt-5-mini" {
		t.Errorf("the env baseline was written through: model = %q", envFB.Model)
	}
}
