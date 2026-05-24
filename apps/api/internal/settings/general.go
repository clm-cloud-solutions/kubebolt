package settings

import (
	"encoding/json"
	"fmt"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

const generalSettingsKey = "general"

// StoredGeneralSettings is the on-disk shape for the General domain.
// Pointer fields so nil means "fall back to env baseline" — same
// partial-override semantics as the other Settings domains.
type StoredGeneralSettings struct {
	DisplayName                   *string `json:"displayName,omitempty"`
	DefaultRefreshIntervalSeconds *int    `json:"defaultRefreshIntervalSeconds,omitempty"`
}

// General returns the resolved GeneralConfig (env + BoltDB override).
// Cached until InvalidateGeneral.
func (r *Runtime) General() config.GeneralConfig {
	r.mu.RLock()
	if r.generalValid {
		cfg := r.general
		r.mu.RUnlock()
		return cfg
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generalValid {
		return r.general
	}
	r.general = r.resolveGeneralLocked()
	r.generalValid = true
	return r.general
}

func (r *Runtime) InvalidateGeneral() {
	r.mu.Lock()
	r.generalValid = false
	r.mu.Unlock()
}

func (r *Runtime) resolveGeneralLocked() config.GeneralConfig {
	cfg := r.envGeneral

	raw, err := r.store.GetSetting(generalSettingsKey)
	if err != nil {
		return cfg
	}
	var stored StoredGeneralSettings
	if err := json.Unmarshal(raw, &stored); err != nil {
		return cfg
	}
	applyStoredGeneral(&cfg, &stored)
	return cfg
}

func applyStoredGeneral(cfg *config.GeneralConfig, stored *StoredGeneralSettings) {
	if stored.DisplayName != nil {
		cfg.DisplayName = *stored.DisplayName
	}
	if stored.DefaultRefreshIntervalSeconds != nil {
		cfg.DefaultRefreshIntervalSeconds = *stored.DefaultRefreshIntervalSeconds
	}
}

// PutGeneral validates and persists a partial General settings patch.
func (r *Runtime) PutGeneral(patch *StoredGeneralSettings) error {
	if err := validateGeneralPatch(patch); err != nil {
		return err
	}
	existing, _ := r.loadGeneral()
	merged := mergeGeneral(existing, *patch)

	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := r.store.SetSetting(generalSettingsKey, encoded); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	r.InvalidateGeneral()
	return nil
}

func (r *Runtime) loadGeneral() (StoredGeneralSettings, error) {
	raw, err := r.store.GetSetting(generalSettingsKey)
	if err != nil {
		return StoredGeneralSettings{}, nil
	}
	var out StoredGeneralSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return StoredGeneralSettings{}, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

func (r *Runtime) ResetGeneral() error {
	if err := r.store.SetSetting(generalSettingsKey, []byte("null")); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	r.InvalidateGeneral()
	return nil
}

// MaskedGeneral is the GET response shape. No secrets to mask — every
// field is shown verbatim. Mirrors the "effective + stored" layout
// the other Settings domains use so the UI can render source badges.
type MaskedGeneral struct {
	Effective MaskedEffectiveGeneral `json:"effective"`
	Stored    MaskedStoredGeneral    `json:"stored"`
}

type MaskedEffectiveGeneral struct {
	DisplayName                   string `json:"displayName"`
	DefaultRefreshIntervalSeconds int    `json:"defaultRefreshIntervalSeconds"`
}

type MaskedStoredGeneral struct {
	HasOverride                   bool    `json:"hasOverride"`
	DisplayName                   *string `json:"displayName,omitempty"`
	DefaultRefreshIntervalSeconds *int    `json:"defaultRefreshIntervalSeconds,omitempty"`
}

func (r *Runtime) RenderMaskedGeneral() (MaskedGeneral, error) {
	stored, err := r.loadGeneral()
	if err != nil {
		return MaskedGeneral{}, err
	}
	resolved := r.General()
	out := MaskedGeneral{
		Effective: MaskedEffectiveGeneral{
			DisplayName:                   resolved.DisplayName,
			DefaultRefreshIntervalSeconds: resolved.DefaultRefreshIntervalSeconds,
		},
		Stored: MaskedStoredGeneral{
			HasOverride:                   stored.DisplayName != nil || stored.DefaultRefreshIntervalSeconds != nil,
			DisplayName:                   stored.DisplayName,
			DefaultRefreshIntervalSeconds: stored.DefaultRefreshIntervalSeconds,
		},
	}
	return out, nil
}

// ─── Validation + merge ───────────────────────────────────────────────

func validateGeneralPatch(p *StoredGeneralSettings) error {
	if p == nil {
		return &ValidationError{Field: "patch", Message: "patch is required"}
	}
	if p.DisplayName != nil {
		// Trim to 64 chars max — the topbar's render width assumes
		// "reasonable instance label", not "operator pastes a paragraph".
		if len(*p.DisplayName) > 64 {
			return &ValidationError{Field: "displayName", Message: "must be 64 characters or fewer"}
		}
	}
	if p.DefaultRefreshIntervalSeconds != nil {
		ok := false
		for _, v := range config.ValidRefreshIntervalSeconds {
			if *p.DefaultRefreshIntervalSeconds == v {
				ok = true
				break
			}
		}
		if !ok {
			return &ValidationError{Field: "defaultRefreshIntervalSeconds", Message: "must be one of 5, 10, 15, 30, 60, 120"}
		}
	}
	return nil
}

func mergeGeneral(base, patch StoredGeneralSettings) StoredGeneralSettings {
	out := base
	if patch.DisplayName != nil {
		out.DisplayName = patch.DisplayName
	}
	if patch.DefaultRefreshIntervalSeconds != nil {
		out.DefaultRefreshIntervalSeconds = patch.DefaultRefreshIntervalSeconds
	}
	return out
}
