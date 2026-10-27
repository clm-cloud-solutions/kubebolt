package settings

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// The escalation model set in the UI: nil keeps the env baseline, "" turns
// escalation off (env included), a model overrides the env one's model and
// keeps its provider and key. The env baseline is never written through.
func TestApplyStoredEscalation(t *testing.T) {
	str := func(s string) *string { return &s }
	envEsc := &config.ProviderConfig{Provider: "anthropic", APIKey: "k-esc", Model: "claude-sonnet-5-5"}

	cfg := config.CopilotConfig{Escalation: envEsc}
	applyStoredEscalation(&cfg, nil)
	if cfg.Escalation != envEsc {
		t.Error("nil must keep the env baseline")
	}

	cfg = config.CopilotConfig{Escalation: envEsc}
	applyStoredEscalation(&cfg, str("  "))
	if cfg.Escalation != nil {
		t.Error(`"" must turn escalation off`)
	}

	cfg = config.CopilotConfig{Escalation: envEsc}
	applyStoredEscalation(&cfg, str("claude-opus-5-5"))
	if cfg.Escalation.Model != "claude-opus-5-5" || cfg.Escalation.APIKey != "k-esc" || cfg.Escalation.Provider != "anthropic" {
		t.Errorf("override lost the env provider/key: %+v", cfg.Escalation)
	}
	if envEsc.Model != "claude-sonnet-5-5" {
		t.Error("the env baseline was written through")
	}

	cfg = config.CopilotConfig{}
	applyStoredEscalation(&cfg, str("claude-sonnet-5-5"))
	if cfg.Escalation == nil || cfg.Escalation.Model != "claude-sonnet-5-5" || cfg.Escalation.APIKey != "" {
		t.Errorf("model-only escalation: %+v", cfg.Escalation)
	}
}

// A PUT carrying only escalationModel merges onto what is stored; "" is kept
// as an explicit off, not dropped.
func TestMergeCopilot_EscalationModel(t *testing.T) {
	m, off := "claude-sonnet-5-5", ""
	base := StoredCopilotSettings{EscalationModel: &m}
	if got := mergeCopilot(base, StoredCopilotSettings{}); got.EscalationModel == nil || *got.EscalationModel != m {
		t.Error("an unrelated patch dropped the stored escalation model")
	}
	if got := mergeCopilot(base, StoredCopilotSettings{EscalationModel: &off}); got.EscalationModel == nil || *got.EscalationModel != "" {
		t.Error(`"" must be stored as an explicit off`)
	}
	if mask := renderStoredMask(base); mask.OtherFields == nil || mask.OtherFields.EscalationModel == nil {
		t.Error("the stored view hides the escalation model")
	}
}
