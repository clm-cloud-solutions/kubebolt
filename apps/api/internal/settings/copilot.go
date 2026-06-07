package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// copilotSettingsKey is the BoltDB key under the settings bucket where
// the JSON-encoded StoredCopilotSettings lives. One row per process; we
// don't yet support per-tenant Copilot overrides (out of scope for V1).
const copilotSettingsKey = "copilot"

// StoredCopilotSettings is the on-disk shape. Every field is a pointer
// so nil means "fall back to env baseline" — partial overrides are
// first-class. The PUT handler accepts a partial patch with the same
// shape, merges it onto the existing record, and writes back.
//
// Why pointers everywhere: an admin who only configures the API key via
// UI should still pick up env-driven model / provider / fallback defaults.
// A field-by-field "is this set" check is the cleanest way to express
// the override semantics without inventing sentinel values like "ENV"
// inside string fields.
type StoredCopilotSettings struct {
	Primary  *StoredProviderSettings `json:"primary,omitempty"`
	Fallback *StoredProviderSettings `json:"fallback,omitempty"`

	MaxTokens            *int     `json:"maxTokens,omitempty"`
	AutoCompact          *bool    `json:"autoCompact,omitempty"`
	SessionBudgetTokens  *int     `json:"sessionBudgetTokens,omitempty"`
	AutoCompactThreshold *float64 `json:"autoCompactThreshold,omitempty"`
	CompactModel         *string  `json:"compactModel,omitempty"`
	CompactPreserveTurns *int     `json:"compactPreserveTurns,omitempty"`
	ShowToolCalls        *bool    `json:"showToolCalls,omitempty"`
	// Sprint 1 action governance (live override of the env baseline).
	ActionsEnabled            *bool `json:"actionsEnabled,omitempty"`
	DestructiveActionsEnabled *bool `json:"destructiveActionsEnabled,omitempty"`
	// Action-progress timeout override, in milliseconds (the wire unit the
	// UI and public /copilot/config speak). Applied onto the env baseline's
	// time.Duration and floored at config.MinActionProgressTimeout.
	ActionProgressTimeoutMs *int `json:"actionProgressTimeoutMs,omitempty"`
}

// StoredProviderSettings mirrors config.ProviderConfig with optional
// fields. APIKey is encrypted at rest — the value stored here is the
// AES-GCM envelope produced by secretCrypto.encrypt, NOT the raw key.
type StoredProviderSettings struct {
	Provider      *string `json:"provider,omitempty"`
	APIKeyEncoded *string `json:"apiKeyEncoded,omitempty"` // encrypted envelope
	Model         *string `json:"model,omitempty"`
	BaseURL       *string `json:"baseURL,omitempty"`
}

// Copilot returns the live CopilotConfig: BoltDB override merged onto
// the env baseline. Caches the resolved value until InvalidateCopilot is
// called (typically on a PUT /settings/copilot). Safe for concurrent use.
//
// On decryption failure (typically a rotated JWT secret), the API key
// field falls back to the env baseline so the system stays usable even
// if the stored secret can't be unwrapped — operator is alerted via the
// /settings GET endpoint which surfaces the unreadable state explicitly.
func (r *Runtime) Copilot() config.CopilotConfig {
	r.mu.RLock()
	if r.copilotValid {
		cfg := r.copilot
		r.mu.RUnlock()
		return cfg
	}
	r.mu.RUnlock()

	// Re-resolve under the write lock. Double-check the valid bit in case
	// another goroutine raced us — first writer wins, others read the
	// resolved value without a second BoltDB hit.
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.copilotValid {
		return r.copilot
	}
	r.copilot = r.resolveCopilotLocked()
	r.copilotValid = true
	return r.copilot
}

// resolveCopilotLocked merges the BoltDB-persisted partial override onto
// the env baseline. Caller must hold r.mu (write or read). When the
// BoltDB record is absent or malformed, returns the env baseline
// unchanged.
func (r *Runtime) resolveCopilotLocked() config.CopilotConfig {
	cfg := r.envBase // value copy — safe to mutate

	raw, err := r.store.GetSetting(copilotSettingsKey)
	if err != nil {
		// "not found" is the no-override path — env baseline wins, all good.
		return cfg
	}
	var stored StoredCopilotSettings
	if err := json.Unmarshal(raw, &stored); err != nil {
		// Corrupted record — don't crash the process, just ignore and
		// continue with env. A future GET handler will see the parse fail
		// too and can surface it; for now, env is the safe fallback.
		return cfg
	}
	applyStoredCopilot(&cfg, &stored, r.crypto)
	cfg.Enabled = cfg.Primary.APIKey != ""
	return cfg
}

// applyStoredCopilot merges a non-nil StoredCopilotSettings onto an
// existing CopilotConfig. Exported indirectly through resolveCopilotLocked;
// kept as a free function so tests can exercise the merge logic without
// a Runtime instance.
func applyStoredCopilot(cfg *config.CopilotConfig, stored *StoredCopilotSettings, crypto *secretCrypto) {
	if stored.Primary != nil {
		applyStoredProvider(&cfg.Primary, stored.Primary, crypto)
	}
	if stored.Fallback != nil {
		// Initialize a fallback config if the override sets one and env
		// didn't. If both env and override exist, override merges onto env.
		if cfg.Fallback == nil {
			fb := config.ProviderConfig{}
			cfg.Fallback = &fb
		}
		applyStoredProvider(cfg.Fallback, stored.Fallback, crypto)
		// Drop the fallback only when the stored override never had an
		// encrypted key (legitimate "no fallback configured" state). If
		// the stored key EXISTS but decryption failed (JWT secret was
		// rotated mid-life), keep cfg.Fallback visible — secretsReadable
		// flips false elsewhere so the UI can show the "re-enter key"
		// prompt. Previously this branch dropped the fallback in BOTH
		// cases, making a rotated install look identical to a fresh one
		// and the operator's config silently disappeared.
		storedHadKey := stored.Fallback.APIKeyEncoded != nil && *stored.Fallback.APIKeyEncoded != ""
		if cfg.Fallback.APIKey == "" && !storedHadKey {
			cfg.Fallback = nil
		}
	}
	if stored.MaxTokens != nil {
		cfg.MaxTokens = *stored.MaxTokens
	}
	if stored.AutoCompact != nil {
		cfg.AutoCompact = *stored.AutoCompact
	}
	if stored.SessionBudgetTokens != nil {
		cfg.SessionBudgetTokens = *stored.SessionBudgetTokens
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
		// Same floor the env path enforces — a fat-fingered tiny value must
		// not declare every rollout stalled before the first poll lands.
		if ms < config.MinActionProgressTimeout {
			ms = config.MinActionProgressTimeout
		}
		cfg.ActionProgressTimeout = ms
	}
}

// applyStoredProvider merges a stored provider override onto a baseline.
// The API key is decrypted on the way through; an unreadable envelope
// leaves the baseline key untouched (env-fallback for that field only).
func applyStoredProvider(cfg *config.ProviderConfig, stored *StoredProviderSettings, crypto *secretCrypto) {
	if stored.Provider != nil {
		cfg.Provider = *stored.Provider
	}
	if stored.APIKeyEncoded != nil && *stored.APIKeyEncoded != "" {
		pt, err := crypto.decrypt(*stored.APIKeyEncoded)
		if err == nil {
			cfg.APIKey = pt
		}
		// On decrypt failure we keep cfg.APIKey as whatever env said.
		// The settings GET handler reports the unreadable state.
	}
	if stored.Model != nil {
		cfg.Model = *stored.Model
	}
	if stored.BaseURL != nil {
		cfg.BaseURL = *stored.BaseURL
	}
}

// PutCopilot validates and persists a partial Copilot settings patch.
// The patch shape uses pointers so callers can express "unchanged" by
// omitting the field. API keys go through crypto.encrypt on the way in
// so callers pass plaintext (never the envelope) and the store sees
// only the wrapped form.
//
// Empty plaintext for an API key means "clear it" (revert that field to
// env baseline) — caller distinguishes "unchanged" (nil pointer) from
// "clear" (non-nil pointer to empty string).
//
// Validation:
//   - Provider, if set, must be one of the registered providers
//   - MaxTokens, if set, must be > 0
//   - AutoCompactThreshold, if set, must be in (0, 1)
//   - SessionBudgetTokens, if set, must be >= 0
//   - CompactPreserveTurns, if set, must be >= 0
//
// Returns ValidationError on bad input — the handler maps that to 400.
func (r *Runtime) PutCopilot(patch *StoredCopilotSettings, plaintextAPIKey, plaintextFallbackAPIKey *string) error {
	if err := validateCopilotPatch(patch); err != nil {
		return err
	}
	// Encrypt the API keys if the caller is updating them. nil pointer =
	// don't touch the field; non-nil with empty string = explicit clear
	// (used by the UI's "disable fallback" toggle — we set the encoded
	// envelope to the literal empty string, which mergeProvider treats
	// as "drop this override" and applyStoredCopilot then drops the
	// whole fallback section since cfg.Fallback.APIKey ends up empty).
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

	// Read existing record (if any) so we MERGE the patch instead of
	// overwriting unrelated fields. Pointer-field semantics: nil in the
	// patch leaves the existing value alone.
	existing, _ := r.loadCopilot()
	merged := mergeCopilot(existing, *patch)

	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := r.store.SetSetting(copilotSettingsKey, encoded); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	r.InvalidateCopilot()
	return nil
}

// loadCopilot reads the existing BoltDB record without merging onto env.
// Used internally by PutCopilot for merge, and exported via GetCopilot
// for the read-side handler to render the masked view.
func (r *Runtime) loadCopilot() (StoredCopilotSettings, error) {
	raw, err := r.store.GetSetting(copilotSettingsKey)
	if err != nil {
		// Not-found is normal (no overrides yet) — return zero value.
		return StoredCopilotSettings{}, nil
	}
	var out StoredCopilotSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return StoredCopilotSettings{}, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// GetCopilot returns the BoltDB-persisted record (unmasked secrets
// decrypted in-memory) AND the env baseline, so the handler can render
// a comparison view. The decrypted API keys are NOT included in the
// response shape; callers use MaskedCopilot for that.
func (r *Runtime) GetCopilot() (stored StoredCopilotSettings, baseline config.CopilotConfig, secretReadable bool, err error) {
	stored, err = r.loadCopilot()
	if err != nil {
		return StoredCopilotSettings{}, r.envBase, false, err
	}
	// Probe whether the encrypted secrets are readable with the current
	// crypto key. If not, the UI shows a "re-enter your key" prompt.
	// Probe BOTH primary and fallback — a rotated JWT secret invalidates
	// every encrypted blob, and the prior code only checked Primary so
	// Fallback failures slipped through silently.
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

// MaskedCopilot renders the response shape for GET /settings/copilot.
// Secrets are NEVER round-tripped to the UI — only a masked preview is
// returned. The `secretReadable` flag flips the UI into "re-enter your
// key" mode when a rotated JWT secret made the stored value unusable.
type MaskedCopilot struct {
	// Effective is the resolved live config (envBase + stored merged).
	// Useful for the UI to show "this is what's in effect right now"
	// alongside the per-field source.
	Effective MaskedEffectiveCopilot `json:"effective"`

	// Stored is the BoltDB override layer with secrets masked. Pointer
	// fields mirror the on-disk shape so the UI can render per-field
	// "configured here" vs "inherits from env" decisions.
	Stored MaskedStoredCopilot `json:"stored"`

	// SecretsReadable is false when the encrypted API key can't be
	// decrypted with the current JWT secret (typically after rotation).
	// UI uses this to render a "re-enter" prompt instead of a stale
	// masked preview.
	SecretsReadable bool `json:"secretsReadable"`
}

type MaskedEffectiveCopilot struct {
	Enabled              bool   `json:"enabled"`
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	APIKeyMasked         string `json:"apiKeyMasked"`
	BaseURL              string `json:"baseURL,omitempty"`
	HasFallback          bool   `json:"hasFallback"`
	FallbackProvider     string `json:"fallbackProvider,omitempty"`
	FallbackModel        string `json:"fallbackModel,omitempty"`
	FallbackAPIKeyMasked string `json:"fallbackApiKeyMasked,omitempty"`
	FallbackBaseURL      string `json:"fallbackBaseURL,omitempty"`
	MaxTokens            int    `json:"maxTokens"`
	AutoCompact          bool   `json:"autoCompact"`
	ShowToolCalls        bool   `json:"showToolCalls"`
	ActionsEnabled            bool `json:"actionsEnabled"`
	DestructiveActionsEnabled bool `json:"destructiveActionsEnabled"`
	// Action-progress timeout in effect, milliseconds (UI converts to seconds).
	ActionProgressTimeoutMs int `json:"actionProgressTimeoutMs,omitempty"`
	// Auto-compact tunables. Surfaced in the API so the UI can display
	// "what's in effect" without a second round-trip to the env-baseline
	// endpoint. Nil pointers when the field is unset; the resolver
	// guarantees the live config has them filled, but JSON
	// omitempty avoids leaking zero values that the operator would
	// misread as "compaction will trigger at 0% full".
	SessionBudgetTokens  int     `json:"sessionBudgetTokens,omitempty"`
	AutoCompactThreshold float64 `json:"autoCompactThreshold,omitempty"`
	CompactModel         string  `json:"compactModel,omitempty"`
	CompactPreserveTurns int     `json:"compactPreserveTurns,omitempty"`
}

type MaskedStoredCopilot struct {
	HasPrimaryOverride  bool                    `json:"hasPrimaryOverride"`
	HasFallbackOverride bool                    `json:"hasFallbackOverride"`
	Primary             *MaskedStoredProvider   `json:"primary,omitempty"`
	Fallback            *MaskedStoredProvider   `json:"fallback,omitempty"`
	OtherFields         *MaskedStoredOtherCopilot `json:"otherFields,omitempty"`
}

type MaskedStoredProvider struct {
	Provider          *string `json:"provider,omitempty"`
	APIKeyMasked      string  `json:"apiKeyMasked,omitempty"`
	APIKeyConfigured  bool    `json:"apiKeyConfigured"`
	Model             *string `json:"model,omitempty"`
	BaseURL           *string `json:"baseURL,omitempty"`
}

type MaskedStoredOtherCopilot struct {
	MaxTokens            *int     `json:"maxTokens,omitempty"`
	AutoCompact          *bool    `json:"autoCompact,omitempty"`
	SessionBudgetTokens  *int     `json:"sessionBudgetTokens,omitempty"`
	AutoCompactThreshold *float64 `json:"autoCompactThreshold,omitempty"`
	CompactModel         *string  `json:"compactModel,omitempty"`
	CompactPreserveTurns *int     `json:"compactPreserveTurns,omitempty"`
	ShowToolCalls        *bool    `json:"showToolCalls,omitempty"`
	ActionsEnabled            *bool `json:"actionsEnabled,omitempty"`
	DestructiveActionsEnabled *bool `json:"destructiveActionsEnabled,omitempty"`
	ActionProgressTimeoutMs   *int  `json:"actionProgressTimeoutMs,omitempty"`
}

// RenderMaskedCopilot builds the GET response from a stored record + env
// baseline + crypto. Secrets are masked using the prefix+tail convention
// from crypto.go's maskSecret.
func (r *Runtime) RenderMaskedCopilot() (MaskedCopilot, error) {
	stored, _, secretsReadable, err := r.GetCopilot()
	if err != nil {
		return MaskedCopilot{}, err
	}
	effective := r.Copilot() // forces a fresh resolve via the cache path

	out := MaskedCopilot{
		Effective: MaskedEffectiveCopilot{
			Enabled:              effective.Enabled,
			Provider:             effective.Primary.Provider,
			Model:                effective.Primary.Model,
			APIKeyMasked:         maskSecret(effective.Primary.APIKey),
			BaseURL:              effective.Primary.BaseURL,
			MaxTokens:            effective.MaxTokens,
			AutoCompact:          effective.AutoCompact,
			ShowToolCalls:        effective.ShowToolCalls,
			ActionsEnabled:            effective.ActionsEnabled,
			DestructiveActionsEnabled: effective.DestructiveActionsEnabled,
			ActionProgressTimeoutMs:   int(effective.ActionProgressTimeout.Milliseconds()),
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

func renderStoredMask(s StoredCopilotSettings) MaskedStoredCopilot {
	out := MaskedStoredCopilot{}
	if s.Primary != nil {
		out.HasPrimaryOverride = true
		out.Primary = &MaskedStoredProvider{
			Provider: s.Primary.Provider,
			Model:    s.Primary.Model,
			BaseURL:  s.Primary.BaseURL,
		}
		if s.Primary.APIKeyEncoded != nil && *s.Primary.APIKeyEncoded != "" {
			out.Primary.APIKeyConfigured = true
			// Mask is computed against a placeholder — we cannot decrypt
			// here without exposing the cleartext to JSON encoding. The
			// effective mask above already shows the right preview if
			// decryption succeeded; UI uses APIKeyConfigured as the
			// "is something set here" signal.
			out.Primary.APIKeyMasked = "***configured***"
		}
	}
	if s.Fallback != nil {
		out.HasFallbackOverride = true
		out.Fallback = &MaskedStoredProvider{
			Provider: s.Fallback.Provider,
			Model:    s.Fallback.Model,
			BaseURL:  s.Fallback.BaseURL,
		}
		if s.Fallback.APIKeyEncoded != nil && *s.Fallback.APIKeyEncoded != "" {
			out.Fallback.APIKeyConfigured = true
			out.Fallback.APIKeyMasked = "***configured***"
		}
	}
	if s.MaxTokens != nil || s.AutoCompact != nil || s.SessionBudgetTokens != nil ||
		s.AutoCompactThreshold != nil || s.CompactModel != nil ||
		s.CompactPreserveTurns != nil || s.ShowToolCalls != nil ||
		s.ActionsEnabled != nil || s.DestructiveActionsEnabled != nil ||
		s.ActionProgressTimeoutMs != nil {
		out.OtherFields = &MaskedStoredOtherCopilot{
			MaxTokens:            s.MaxTokens,
			AutoCompact:          s.AutoCompact,
			SessionBudgetTokens:  s.SessionBudgetTokens,
			AutoCompactThreshold: s.AutoCompactThreshold,
			CompactModel:         s.CompactModel,
			CompactPreserveTurns: s.CompactPreserveTurns,
			ShowToolCalls:        s.ShowToolCalls,
			ActionsEnabled:            s.ActionsEnabled,
			DestructiveActionsEnabled: s.DestructiveActionsEnabled,
			ActionProgressTimeoutMs:   s.ActionProgressTimeoutMs,
		}
	}
	return out
}

// ResetCopilot clears the BoltDB override entirely so the system falls
// back to the env baseline for every field. Used by POST /settings/reset
// when the admin chooses "reset to env defaults" on the Copilot tab.
func (r *Runtime) ResetCopilot() error {
	if err := r.store.SetSetting(copilotSettingsKey, []byte("null")); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	r.InvalidateCopilot()
	return nil
}

// ─── Validation ──────────────────────────────────────────────────────

// ValidationError carries per-field issues so the handler can surface
// them inline on the form. Multiple errors per field aren't supported —
// first failure per field wins, which is fine for the shapes we accept.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// IsValidation reports whether err is a settings ValidationError.
func IsValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

func validateCopilotPatch(p *StoredCopilotSettings) error {
	if p == nil {
		return &ValidationError{Field: "patch", Message: "patch is required"}
	}
	if p.Primary != nil && p.Primary.Provider != nil {
		if err := validateProviderName(*p.Primary.Provider); err != nil {
			return &ValidationError{Field: "primary.provider", Message: err.Error()}
		}
	}
	if p.Fallback != nil && p.Fallback.Provider != nil {
		if err := validateProviderName(*p.Fallback.Provider); err != nil {
			return &ValidationError{Field: "fallback.provider", Message: err.Error()}
		}
	}
	if p.MaxTokens != nil && *p.MaxTokens <= 0 {
		return &ValidationError{Field: "maxTokens", Message: "must be > 0"}
	}
	if p.AutoCompactThreshold != nil {
		t := *p.AutoCompactThreshold
		if !(t > 0 && t < 1) {
			return &ValidationError{Field: "autoCompactThreshold", Message: "must be in (0, 1)"}
		}
	}
	if p.SessionBudgetTokens != nil && *p.SessionBudgetTokens < 0 {
		return &ValidationError{Field: "sessionBudgetTokens", Message: "must be >= 0"}
	}
	if p.CompactPreserveTurns != nil && *p.CompactPreserveTurns < 0 {
		return &ValidationError{Field: "compactPreserveTurns", Message: "must be >= 0"}
	}
	// Milliseconds; must be positive. Values below the floor are accepted here
	// and clamped to config.MinActionProgressTimeout in applyStoredCopilot.
	if p.ActionProgressTimeoutMs != nil && *p.ActionProgressTimeoutMs <= 0 {
		return &ValidationError{Field: "actionProgressTimeoutMs", Message: "must be > 0"}
	}
	return nil
}

var knownProviders = map[string]struct{}{
	"anthropic": {},
	"openai":    {},
}

func validateProviderName(p string) error {
	if p == "" {
		return errors.New("provider name cannot be empty")
	}
	if _, ok := knownProviders[strings.ToLower(p)]; !ok {
		return fmt.Errorf("unknown provider %q (must be one of: anthropic, openai)", p)
	}
	return nil
}

// ─── Merge helper ────────────────────────────────────────────────────

// mergeCopilot applies patch onto base. Both are typed pointer-shaped
// records; the result is what we persist. Fields with nil pointer in
// patch retain their value from base; fields with non-nil pointer in
// patch overwrite. Same semantics for nested providers.
//
// Special: APIKeyEncoded is already an encrypted envelope at this layer
// (PutCopilot encrypts plaintext before calling). Empty envelope means
// "clear this override" — when we see Primary.APIKeyEncoded set to ""
// we drop the override so the field falls back to env.
func mergeCopilot(base, patch StoredCopilotSettings) StoredCopilotSettings {
	out := base
	if patch.Primary != nil {
		if out.Primary == nil {
			out.Primary = &StoredProviderSettings{}
		}
		mergeProvider(out.Primary, patch.Primary)
	}
	if patch.Fallback != nil {
		// Same drop-on-empty-key semantics as Primary. This is what
		// the UI's "Enable" toggle uses: when the operator flips
		// fallback off, the frontend sends plaintextFallbackAPIKey=""
		// → PutCopilot encodes that as APIKeyEncoded=&"" → we drop the
		// whole Fallback section here so provider/model don't survive
		// as ghost fields that re-enable themselves on next reload.
		if patch.Fallback.APIKeyEncoded != nil && *patch.Fallback.APIKeyEncoded == "" {
			out.Fallback = nil
		} else {
			if out.Fallback == nil {
				out.Fallback = &StoredProviderSettings{}
			}
			mergeProvider(out.Fallback, patch.Fallback)
		}
	}
	if patch.MaxTokens != nil {
		out.MaxTokens = patch.MaxTokens
	}
	if patch.AutoCompact != nil {
		out.AutoCompact = patch.AutoCompact
	}
	if patch.SessionBudgetTokens != nil {
		out.SessionBudgetTokens = patch.SessionBudgetTokens
	}
	if patch.AutoCompactThreshold != nil {
		out.AutoCompactThreshold = patch.AutoCompactThreshold
	}
	if patch.CompactModel != nil {
		out.CompactModel = patch.CompactModel
	}
	if patch.CompactPreserveTurns != nil {
		out.CompactPreserveTurns = patch.CompactPreserveTurns
	}
	if patch.ShowToolCalls != nil {
		out.ShowToolCalls = patch.ShowToolCalls
	}
	if patch.ActionsEnabled != nil {
		out.ActionsEnabled = patch.ActionsEnabled
	}
	if patch.DestructiveActionsEnabled != nil {
		out.DestructiveActionsEnabled = patch.DestructiveActionsEnabled
	}
	if patch.ActionProgressTimeoutMs != nil {
		out.ActionProgressTimeoutMs = patch.ActionProgressTimeoutMs
	}
	return out
}

func mergeProvider(out, patch *StoredProviderSettings) {
	if patch.Provider != nil {
		out.Provider = patch.Provider
	}
	if patch.APIKeyEncoded != nil {
		// Empty envelope clears the override.
		if *patch.APIKeyEncoded == "" {
			out.APIKeyEncoded = nil
		} else {
			out.APIKeyEncoded = patch.APIKeyEncoded
		}
	}
	if patch.Model != nil {
		out.Model = patch.Model
	}
	if patch.BaseURL != nil {
		out.BaseURL = patch.BaseURL
	}
}
