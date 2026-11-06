package settings

import (
	"context"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// With the multi-tenant seam on, the platform-managed config shares the off:
// turning the platform's fallback off over an env one stays off for the
// platform view and for every org that layers onto it.
func TestFallbackOff_PlatformManaged(t *testing.T) {
	ctx := context.Background()
	rt, envFB := runtimeWithEnvFallback(t)
	auth.MultiTenantEnabled = true // runtimeWithEnvFallback restores it

	if got := rt.Copilot(ctx).Fallback; got == nil || got.APIKey != envFB.APIKey {
		t.Fatalf("before: org fallback = %+v, want the env one", got)
	}
	if err := rt.PutCopilotPlatform(&StoredCopilotSettings{}, nil, strPtr("")); err != nil {
		t.Fatalf("turn off: %v", err)
	}
	if got := rt.CopilotPlatform().Fallback; got != nil {
		t.Errorf("platform fallback = %+v, want none", got)
	}
	if got := rt.Copilot(ctx).Fallback; got != nil {
		t.Errorf("org fallback = %+v, want none", got)
	}
	view, err := rt.RenderMaskedCopilotPlatform()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if view.Effective.HasFallback || !view.Stored.FallbackOff || view.Effective.FallbackProvider != "openai" {
		t.Errorf("platform view: in effect = %v, stored off = %v, provider = %q",
			view.Effective.HasFallback, view.Stored.FallbackOff, view.Effective.FallbackProvider)
	}

	if err := rt.PutCopilotPlatform(&StoredCopilotSettings{Fallback: &StoredProviderSettings{Provider: strPtr("openai")}}, nil, nil); err != nil {
		t.Fatalf("turn on: %v", err)
	}
	if got := rt.Copilot(ctx).Fallback; got == nil || got.APIKey != envFB.APIKey {
		t.Errorf("after turning it on: org fallback = %+v, want the env one", got)
	}
}
