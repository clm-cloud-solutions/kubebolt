package settings

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Platform-managed (install-global) Copilot config.
//
// When the multi-tenant seam is on, the AI is platform-managed: the provider,
// model, key and cost/context tunables are install-global (set by a platform
// admin) — per-org records can't override the model. Only the per-org
// governance + display fields (actions on/off, tool-call display, action-
// progress timeout) stay org-editable. This file is the install-global half;
// the per-org half stays in copilot.go. The resolver (resolveCopilotLocked)
// layers them. When the seam is off (single-tenant), this config is unused —
// the per-org record owns everything (BYOK).
const copilotPlatformSettingsKey = "copilot_platform"

// copilotPlatformFieldClass: which StoredCopilotSettings fields the platform
// owns (provider + cost/context) vs the org owns (governance + display). The
// two appliers below project a stored record onto each class.

// applyCopilotPlatformFields applies the PLATFORM-managed subset (provider +
// cost/context tunables) of a stored Copilot override onto cfg.
func applyCopilotPlatformFields(cfg *config.CopilotConfig, stored *StoredCopilotSettings, crypto *secretCrypto) {
	if stored.Primary != nil {
		applyStoredProvider(&cfg.Primary, stored.Primary, crypto)
	}
	if stored.Fallback != nil {
		if cfg.Fallback == nil {
			fb := config.ProviderConfig{}
			cfg.Fallback = &fb
		}
		applyStoredProvider(cfg.Fallback, stored.Fallback, crypto)
		storedHadKey := stored.Fallback.APIKeyEncoded != nil && *stored.Fallback.APIKeyEncoded != ""
		if cfg.Fallback.APIKey == "" && !storedHadKey {
			cfg.Fallback = nil
		}
	}
	if stored.MaxTokens != nil {
		cfg.MaxTokens = *stored.MaxTokens
	}
	if stored.SessionBudgetTokens != nil {
		cfg.SessionBudgetTokens = *stored.SessionBudgetTokens
	}
	if stored.AutoCompact != nil {
		cfg.AutoCompact = *stored.AutoCompact
	}
	if stored.AutoCompactThreshold != nil {
		cfg.AutoCompactThreshold = *stored.AutoCompactThreshold
	}
	if stored.CompactModel != nil {
		cfg.CompactModel = *stored.CompactModel
	}
	if stored.CompactPreserveTurns != nil {
		cfg.CompactPreserveTurns = *stored.CompactPreserveTurns
	}
	if stored.MaxRounds != nil {
		cfg.MaxRounds = config.ClampMaxRounds(*stored.MaxRounds)
	}
}

// applyCopilotOrgFields applies the per-ORG subset (governance + display) — the
// only Copilot fields an org may set when the platform owns the provider.
func applyCopilotOrgFields(cfg *config.CopilotConfig, stored *StoredCopilotSettings) {
	if stored.ShowToolCalls != nil {
		cfg.ShowToolCalls = *stored.ShowToolCalls
	}
	if stored.ActionsEnabled != nil {
		cfg.ActionsEnabled = *stored.ActionsEnabled
	}
	if stored.DestructiveActionsEnabled != nil {
		cfg.DestructiveActionsEnabled = *stored.DestructiveActionsEnabled
	}
	if stored.ActionProgressTimeoutMs != nil && *stored.ActionProgressTimeoutMs > 0 {
		ms := time.Duration(*stored.ActionProgressTimeoutMs) * time.Millisecond
		if ms < config.MinActionProgressTimeout {
			ms = config.MinActionProgressTimeout
		}
		cfg.ActionProgressTimeout = ms
	}
}

// applyCopilotPlatformOverride layers the install-global PLATFORM Copilot
// override (provider + cost) onto cfg. Read on-demand from the SettingStore
// (like GeneralGlobal) — not cached separately; the per-org cache amortizes it.
func (r *Runtime) applyCopilotPlatformOverride(cfg *config.CopilotConfig) {
	raw, err := r.store.GetSetting(copilotPlatformSettingsKey)
	if err != nil {
		return
	}
	var stored StoredCopilotSettings
	if json.Unmarshal(raw, &stored) == nil {
		applyCopilotPlatformFields(cfg, &stored, r.crypto)
	}
}

// CopilotPlatform returns the resolved platform-managed Copilot config: the env
// baseline with ONLY the install-global PLATFORM override applied (provider +
// cost). For the platform-admin GET/render — no request org. Not cached.
func (r *Runtime) CopilotPlatform() config.CopilotConfig {
	cfg := r.envBase
	r.applyCopilotPlatformOverride(&cfg)
	cfg.Enabled = cfg.Primary.APIKey != ""
	return cfg
}

// invalidateCopilotAllOrgs clears every org's cached Copilot config — used when
// the install-global PLATFORM Copilot override changes, since every org's
// resolved config layers onto it.
func (r *Runtime) invalidateCopilotAllOrgs() {
	r.mu.Lock()
	r.copilotValid = map[string]bool{}
	r.mu.Unlock()
}

// PutCopilotPlatform validates and persists the install-global PLATFORM Copilot
// override (provider + cost). Platform-admin only (gated at the route). API keys
// are encrypted at rest via the same crypto layer as the per-org config. A
// change invalidates EVERY org's Copilot cache (orgs layer onto it).
func (r *Runtime) PutCopilotPlatform(patch *StoredCopilotSettings, plaintextAPIKey, plaintextFallbackAPIKey *string) error {
	if err := validateCopilotPatch(patch); err != nil {
		return err
	}
	if plaintextAPIKey != nil {
		if patch.Primary == nil {
			patch.Primary = &StoredProviderSettings{}
		}
		if *plaintextAPIKey == "" {
			empty := ""
			patch.Primary.APIKeyEncoded = &empty
		} else {
			enc, err := r.crypto.encrypt(*plaintextAPIKey)
			if err != nil {
				return fmt.Errorf("encrypt primary key: %w", err)
			}
			patch.Primary.APIKeyEncoded = &enc
		}
	}
	if plaintextFallbackAPIKey != nil {
		if patch.Fallback == nil {
			patch.Fallback = &StoredProviderSettings{}
		}
		if *plaintextFallbackAPIKey == "" {
			empty := ""
			patch.Fallback.APIKeyEncoded = &empty
		} else {
			enc, err := r.crypto.encrypt(*plaintextFallbackAPIKey)
			if err != nil {
				return fmt.Errorf("encrypt fallback key: %w", err)
			}
			patch.Fallback.APIKeyEncoded = &enc
		}
	}

	existing, _ := r.loadCopilotPlatform()
	merged := mergeCopilot(existing, *patch)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := r.store.SetSetting(copilotPlatformSettingsKey, encoded); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	r.invalidateCopilotAllOrgs()
	return nil
}

// loadCopilotPlatform reads the install-global PLATFORM Copilot record (no env
// merge). Used by PutCopilotPlatform for the merge and by the masked GET view.
func (r *Runtime) loadCopilotPlatform() (StoredCopilotSettings, error) {
	raw, err := r.store.GetSetting(copilotPlatformSettingsKey)
	if err != nil {
		return StoredCopilotSettings{}, nil
	}
	var out StoredCopilotSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return StoredCopilotSettings{}, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// ResetCopilotPlatform clears the install-global PLATFORM override so the managed
// config falls back to the env baseline for every field.
func (r *Runtime) ResetCopilotPlatform() error {
	if err := r.store.SetSetting(copilotPlatformSettingsKey, []byte("null")); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	r.invalidateCopilotAllOrgs()
	return nil
}

// GetCopilotPlatform returns the install-global stored record + env baseline +
// a secrets-readable probe, mirroring GetCopilot for the platform render.
func (r *Runtime) GetCopilotPlatform() (stored StoredCopilotSettings, baseline config.CopilotConfig, secretReadable bool, err error) {
	stored, err = r.loadCopilotPlatform()
	if err != nil {
		return StoredCopilotSettings{}, r.envBase, false, err
	}
	secretReadable = true
	if stored.Primary != nil && stored.Primary.APIKeyEncoded != nil && *stored.Primary.APIKeyEncoded != "" {
		if _, decErr := r.crypto.decrypt(*stored.Primary.APIKeyEncoded); decErr != nil {
			secretReadable = false
		}
	}
	if stored.Fallback != nil && stored.Fallback.APIKeyEncoded != nil && *stored.Fallback.APIKeyEncoded != "" {
		if _, decErr := r.crypto.decrypt(*stored.Fallback.APIKeyEncoded); decErr != nil {
			secretReadable = false
		}
	}
	return stored, r.envBase, secretReadable, nil
}

// RenderMaskedCopilotPlatform builds the GET response for the platform-admin AI
// config form: the effective managed config (env + platform override) with
// secrets masked, plus the stored override layer. Reuses the MaskedCopilot shape.
func (r *Runtime) RenderMaskedCopilotPlatform() (MaskedCopilot, error) {
	stored, _, secretsReadable, err := r.GetCopilotPlatform()
	if err != nil {
		return MaskedCopilot{}, err
	}
	effective := r.CopilotPlatform()
	out := MaskedCopilot{
		Effective: MaskedEffectiveCopilot{
			Enabled:              effective.Enabled,
			Provider:             effective.Primary.Provider,
			Model:                effective.Primary.Model,
			APIKeyMasked:         maskSecret(effective.Primary.APIKey),
			BaseURL:              effective.Primary.BaseURL,
			MaxTokens:            effective.MaxTokens,
			AutoCompact:          effective.AutoCompact,
			MaxRounds:            effective.MaxRounds,
			SessionBudgetTokens:  effective.SessionBudgetTokens,
			AutoCompactThreshold: effective.AutoCompactThreshold,
			CompactModel:         effective.CompactModel,
			CompactPreserveTurns: effective.CompactPreserveTurns,
		},
		Stored:          renderStoredMask(stored),
		SecretsReadable: secretsReadable,
	}
	if effective.Fallback != nil {
		out.Effective.HasFallback = true
		out.Effective.FallbackProvider = effective.Fallback.Provider
		out.Effective.FallbackModel = effective.Fallback.Model
		out.Effective.FallbackAPIKeyMasked = maskSecret(effective.Fallback.APIKey)
		out.Effective.FallbackBaseURL = effective.Fallback.BaseURL
	}
	return out, nil
}
