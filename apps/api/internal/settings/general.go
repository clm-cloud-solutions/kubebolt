package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// General is a MIXED domain (Track D — per-tenant settings). It splits across
// two stores by field class:
//
//   - ORG fields — per-org, in the OrgSettingStore under generalOrgKey:
//     DisplayName, DefaultRefreshIntervalSeconds, ProdNamespacePattern. Each org
//     configures its own; ProdNamespacePattern is read per request from the
//     resolved org. DisplayName is per-org branding (plan-gated editable later;
//     read-only in the UI for now).
//   - PLATFORM fields — install-global, in the SettingStore under
//     generalGlobalKey: UpdateCheckEnabled (the background GitHub poll) and
//     CacheSyncTimeoutSeconds (applied at cluster connect, no request org). One
//     value per install — these have no request context to scope by.
const (
	generalOrgKey    = "general"        // per-org ORG fields (OrgSettingStore)
	generalGlobalKey = "general_global" // install-global PLATFORM fields (SettingStore)
)

// StoredGeneralSettings is the on-disk shape for the General domain.
// Pointer fields so nil means "fall back to env baseline" — same
// partial-override semantics as the other Settings domains.
type StoredGeneralSettings struct {
	DisplayName                   *string `json:"displayName,omitempty"`
	DefaultRefreshIntervalSeconds *int    `json:"defaultRefreshIntervalSeconds,omitempty"`
	// ProdNamespacePattern is a regex (RE2 syntax) that classifies a
	// namespace as "production" for the Secret Reveal role-escalation
	// rule. Validated at PUT — invalid regex returns 400.
	ProdNamespacePattern *string `json:"prodNamespacePattern,omitempty"`
	// UpdateCheckEnabled toggles the GitHub releases poller that
	// drives the "new version available" chip. PLATFORM (install-global).
	UpdateCheckEnabled *bool `json:"updateCheckEnabled,omitempty"`
	// CacheSyncTimeoutSeconds is the cold-connect informer cache-sync
	// deadline. PLATFORM (install-global) — applies on the next cluster
	// connect, no request org.
	CacheSyncTimeoutSeconds *int `json:"cacheSyncTimeoutSeconds,omitempty"`
}

// orgFields / platformFields project a stored record onto the two field classes
// so PUT can route each to the right store and the masked view can recombine.
func (s StoredGeneralSettings) orgFields() StoredGeneralSettings {
	return StoredGeneralSettings{
		DisplayName:                   s.DisplayName,
		DefaultRefreshIntervalSeconds: s.DefaultRefreshIntervalSeconds,
		ProdNamespacePattern:          s.ProdNamespacePattern,
	}
}

func (s StoredGeneralSettings) platformFields() StoredGeneralSettings {
	return StoredGeneralSettings{
		UpdateCheckEnabled:      s.UpdateCheckEnabled,
		CacheSyncTimeoutSeconds: s.CacheSyncTimeoutSeconds,
	}
}

func (s StoredGeneralSettings) hasOrgFields() bool {
	return s.DisplayName != nil || s.DefaultRefreshIntervalSeconds != nil || s.ProdNamespacePattern != nil
}

func (s StoredGeneralSettings) hasPlatformFields() bool {
	return s.UpdateCheckEnabled != nil || s.CacheSyncTimeoutSeconds != nil
}

// General returns the resolved GeneralConfig for the request's org: the env
// baseline, the install-global PLATFORM override, then the per-org ORG override
// on top. Cached per org until InvalidateGeneral. OSS resolves every request to
// the single default tenant → one cache entry, identical to the old behavior.
func (r *Runtime) General(ctx context.Context) config.GeneralConfig {
	org := orgKey(ctx)
	r.mu.RLock()
	if r.generalValid[org] {
		cfg := r.generalByOrg[org]
		r.mu.RUnlock()
		return cfg
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generalValid[org] {
		return r.generalByOrg[org]
	}
	cfg := r.resolveGeneralLocked(ctx)
	r.generalByOrg[org] = cfg
	r.generalValid[org] = true
	return cfg
}

// GeneralGlobal returns the env baseline with ONLY the PLATFORM override applied
// (UpdateCheckEnabled, CacheSyncTimeoutSeconds). For background / boot consumers
// that have no request org — the update-check poller and the cluster manager's
// cache-sync deadline. Not cached (called rarely).
func (r *Runtime) GeneralGlobal() config.GeneralConfig {
	cfg := r.envGeneral
	r.applyGeneralGlobalOverride(&cfg)
	return cfg
}

// InvalidateGeneral marks one org's cached General config stale (org-level PUT /
// reset). The platform-field PUT invalidates ALL orgs instead (see PutGeneral).
func (r *Runtime) InvalidateGeneral(ctx context.Context) {
	org := orgKey(ctx)
	r.mu.Lock()
	delete(r.generalValid, org)
	r.mu.Unlock()
}

// invalidateGeneralAllOrgs clears every org's cached General config — used when
// an install-global PLATFORM field changes, since every org's resolved config
// layers onto it.
func (r *Runtime) invalidateGeneralAllOrgs() {
	r.mu.Lock()
	r.generalValid = map[string]bool{}
	r.mu.Unlock()
}

func (r *Runtime) resolveGeneralLocked(ctx context.Context) config.GeneralConfig {
	cfg := r.envGeneral
	r.applyGeneralGlobalOverride(&cfg)
	// Per-org ORG override on top.
	if raw, err := r.orgStore.GetOrgSetting(ctx, generalOrgKey); err == nil {
		var stored StoredGeneralSettings
		if json.Unmarshal(raw, &stored) == nil {
			org := stored.orgFields()
			applyStoredGeneral(&cfg, &org)
		}
	}
	return cfg
}

// applyGeneralGlobalOverride layers the install-global PLATFORM override onto cfg.
func (r *Runtime) applyGeneralGlobalOverride(cfg *config.GeneralConfig) {
	raw, err := r.store.GetSetting(generalGlobalKey)
	if err != nil {
		return
	}
	var stored StoredGeneralSettings
	if json.Unmarshal(raw, &stored) == nil {
		p := stored.platformFields()
		applyStoredGeneral(cfg, &p)
	}
}

func applyStoredGeneral(cfg *config.GeneralConfig, stored *StoredGeneralSettings) {
	if stored.DisplayName != nil {
		cfg.DisplayName = *stored.DisplayName
	}
	if stored.DefaultRefreshIntervalSeconds != nil {
		cfg.DefaultRefreshIntervalSeconds = *stored.DefaultRefreshIntervalSeconds
	}
	if stored.ProdNamespacePattern != nil {
		cfg.ProdNamespacePattern = *stored.ProdNamespacePattern
	}
	if stored.UpdateCheckEnabled != nil {
		cfg.UpdateCheckEnabled = *stored.UpdateCheckEnabled
	}
	if stored.CacheSyncTimeoutSeconds != nil {
		cfg.CacheSyncTimeoutSeconds = *stored.CacheSyncTimeoutSeconds
	}
}

// PutGeneral validates and persists a partial General settings patch, routing
// ORG fields to the per-org store and PLATFORM fields to the install-global
// store. Org-admin only (gated at the route).
func (r *Runtime) PutGeneral(ctx context.Context, patch *StoredGeneralSettings) error {
	if err := validateGeneralPatch(patch); err != nil {
		return err
	}
	if patch.hasOrgFields() {
		existing := r.loadGeneralOrg(ctx)
		merged := mergeGeneral(existing, patch.orgFields())
		encoded, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		if err := r.orgStore.SetOrgSetting(ctx, generalOrgKey, encoded); err != nil {
			return fmt.Errorf("persist org: %w", err)
		}
	}
	if patch.hasPlatformFields() {
		existing := r.loadGeneralGlobal()
		merged := mergeGeneral(existing, patch.platformFields())
		encoded, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		if err := r.store.SetSetting(generalGlobalKey, encoded); err != nil {
			return fmt.Errorf("persist platform: %w", err)
		}
	}
	// A platform change affects every org's resolved config; an org-only change
	// affects just this org.
	if patch.hasPlatformFields() {
		r.invalidateGeneralAllOrgs()
	} else {
		r.InvalidateGeneral(ctx)
	}
	return nil
}

// loadGeneralOrg reads the per-org ORG override record (no env merge).
func (r *Runtime) loadGeneralOrg(ctx context.Context) StoredGeneralSettings {
	raw, err := r.orgStore.GetOrgSetting(ctx, generalOrgKey)
	if err != nil {
		return StoredGeneralSettings{}
	}
	var out StoredGeneralSettings
	if json.Unmarshal(raw, &out) != nil {
		return StoredGeneralSettings{}
	}
	return out.orgFields()
}

// loadGeneralGlobal reads the install-global PLATFORM override record.
func (r *Runtime) loadGeneralGlobal() StoredGeneralSettings {
	raw, err := r.store.GetSetting(generalGlobalKey)
	if err != nil {
		return StoredGeneralSettings{}
	}
	var out StoredGeneralSettings
	if json.Unmarshal(raw, &out) != nil {
		return StoredGeneralSettings{}
	}
	return out.platformFields()
}

// loadGeneral returns the combined stored override (per-org ORG fields +
// install-global PLATFORM fields) for the masked GET view.
func (r *Runtime) loadGeneral(ctx context.Context) StoredGeneralSettings {
	org := r.loadGeneralOrg(ctx)
	plat := r.loadGeneralGlobal()
	org.UpdateCheckEnabled = plat.UpdateCheckEnabled
	org.CacheSyncTimeoutSeconds = plat.CacheSyncTimeoutSeconds
	return org
}

// ResetGeneral clears the caller-org's ORG override so its fields fall back to
// the env/platform baseline. The install-global PLATFORM fields are unaffected.
func (r *Runtime) ResetGeneral(ctx context.Context) error {
	if err := r.orgStore.SetOrgSetting(ctx, generalOrgKey, []byte("null")); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	r.InvalidateGeneral(ctx)
	return nil
}

// MigrateLegacyGeneral is a one-time, best-effort upgrade shim. Before General
// split per-org, a single global "general" record (all 5 fields) lived in the
// SettingStore. Copy its ORG fields into the org in ctx (the default tenant) and
// its PLATFORM fields into the new global key, so an upgrading install keeps its
// values. No-op on fresh installs / once migrated.
func (r *Runtime) MigrateLegacyGeneral(ctx context.Context) bool {
	// If the new keys already exist, nothing to migrate.
	if _, err := r.orgStore.GetOrgSetting(ctx, generalOrgKey); err == nil {
		return false
	}
	legacy, err := r.store.GetSetting(generalOrgKey) // old global "general"
	if err != nil || len(legacy) == 0 || string(legacy) == "null" {
		return false
	}
	var stored StoredGeneralSettings
	if json.Unmarshal(legacy, &stored) != nil {
		return false
	}
	migrated := false
	if org := stored.orgFields(); org.hasOrgFields() {
		if enc, e := json.Marshal(org); e == nil {
			if r.orgStore.SetOrgSetting(ctx, generalOrgKey, enc) == nil {
				migrated = true
			}
		}
	}
	if plat := stored.platformFields(); plat.hasPlatformFields() {
		if _, e := r.store.GetSetting(generalGlobalKey); e != nil { // don't clobber
			if enc, e2 := json.Marshal(plat); e2 == nil {
				_ = r.store.SetSetting(generalGlobalKey, enc)
			}
		}
	}
	if migrated {
		r.InvalidateGeneral(ctx)
		r.invalidateGeneralAllOrgs()
	}
	return migrated
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
	ProdNamespacePattern          string `json:"prodNamespacePattern"`
	UpdateCheckEnabled            bool   `json:"updateCheckEnabled"`
	CacheSyncTimeoutSeconds       int    `json:"cacheSyncTimeoutSeconds"`
}

type MaskedStoredGeneral struct {
	HasOverride                   bool    `json:"hasOverride"`
	DisplayName                   *string `json:"displayName,omitempty"`
	DefaultRefreshIntervalSeconds *int    `json:"defaultRefreshIntervalSeconds,omitempty"`
	ProdNamespacePattern          *string `json:"prodNamespacePattern,omitempty"`
	UpdateCheckEnabled            *bool   `json:"updateCheckEnabled,omitempty"`
	CacheSyncTimeoutSeconds       *int    `json:"cacheSyncTimeoutSeconds,omitempty"`
}

func (r *Runtime) RenderMaskedGeneral(ctx context.Context) (MaskedGeneral, error) {
	stored := r.loadGeneral(ctx)
	resolved := r.General(ctx)
	out := MaskedGeneral{
		Effective: MaskedEffectiveGeneral{
			DisplayName:                   resolved.DisplayName,
			DefaultRefreshIntervalSeconds: resolved.DefaultRefreshIntervalSeconds,
			ProdNamespacePattern:          resolved.ProdNamespacePattern,
			UpdateCheckEnabled:            resolved.UpdateCheckEnabled,
			CacheSyncTimeoutSeconds:       resolved.CacheSyncTimeoutSeconds,
		},
		Stored: MaskedStoredGeneral{
			HasOverride: stored.DisplayName != nil ||
				stored.DefaultRefreshIntervalSeconds != nil ||
				stored.ProdNamespacePattern != nil ||
				stored.UpdateCheckEnabled != nil ||
				stored.CacheSyncTimeoutSeconds != nil,
			DisplayName:                   stored.DisplayName,
			DefaultRefreshIntervalSeconds: stored.DefaultRefreshIntervalSeconds,
			ProdNamespacePattern:          stored.ProdNamespacePattern,
			UpdateCheckEnabled:            stored.UpdateCheckEnabled,
			CacheSyncTimeoutSeconds:       stored.CacheSyncTimeoutSeconds,
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
	if p.ProdNamespacePattern != nil {
		// Empty string is intentional ("fall back to default pattern")
		// and bypasses compile check.
		if *p.ProdNamespacePattern != "" {
			if len(*p.ProdNamespacePattern) > 512 {
				return &ValidationError{Field: "prodNamespacePattern", Message: "must be 512 characters or fewer"}
			}
			if _, err := regexp.Compile(*p.ProdNamespacePattern); err != nil {
				return &ValidationError{
					Field:   "prodNamespacePattern",
					Message: "regex does not compile: " + err.Error(),
				}
			}
		}
	}
	if p.CacheSyncTimeoutSeconds != nil {
		if *p.CacheSyncTimeoutSeconds < config.MinCacheSyncTimeoutSeconds || *p.CacheSyncTimeoutSeconds > 600 {
			return &ValidationError{Field: "cacheSyncTimeoutSeconds", Message: "must be between 5 and 600 seconds"}
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
	if patch.ProdNamespacePattern != nil {
		out.ProdNamespacePattern = patch.ProdNamespacePattern
	}
	if patch.UpdateCheckEnabled != nil {
		out.UpdateCheckEnabled = patch.UpdateCheckEnabled
	}
	if patch.CacheSyncTimeoutSeconds != nil {
		out.CacheSyncTimeoutSeconds = patch.CacheSyncTimeoutSeconds
	}
	return out
}
