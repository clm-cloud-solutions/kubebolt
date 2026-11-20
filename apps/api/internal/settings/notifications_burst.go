package settings

import (
	"fmt"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Burst summaries: env (KUBEBOLT_NOTIFICATIONS_BURST_*) is the baseline and
// the Notifications settings move it, inside the install-wide ranges below.
// Summaries cannot be turned off: every knob has a floor.

// Install-wide ranges. Below them a summary would fire on ordinary activity or
// arrive too late to be useful; above them it would hold alerts for longer
// than anyone waits for one.
const (
	BurstThresholdFloor, BurstThresholdCeil = 2, 1000
	BurstWindowFloor, BurstWindowCeil       = 10, 3600
	BurstQuietFloor, BurstQuietCeil         = 5, 600
	BurstMaxWaitFloor, BurstMaxWaitCeil     = 30, 3600
	DigestListFloor, DigestListCeil         = 1, 500
)

// BurstLimits are the bounds the burst settings must stay within. Times in
// seconds.
type BurstLimits struct {
	ThresholdMin      int `json:"thresholdMin"`
	ThresholdMax      int `json:"thresholdMax"`
	WindowMinSeconds  int `json:"windowMinSeconds"`
	WindowMaxSeconds  int `json:"windowMaxSeconds"`
	QuietMinSeconds   int `json:"quietMinSeconds"`
	QuietMaxSeconds   int `json:"quietMaxSeconds"`
	MaxWaitMinSeconds int `json:"maxWaitMinSeconds"`
	MaxWaitMaxSeconds int `json:"maxWaitMaxSeconds"`
	ListLimitMin      int `json:"listLimitMin"`
	ListLimitMax      int `json:"listLimitMax"`
}

// GlobalBurstLimits are the install-wide ranges.
func GlobalBurstLimits() BurstLimits {
	return BurstLimits{
		ThresholdMin: BurstThresholdFloor, ThresholdMax: BurstThresholdCeil,
		WindowMinSeconds: BurstWindowFloor, WindowMaxSeconds: BurstWindowCeil,
		QuietMinSeconds: BurstQuietFloor, QuietMaxSeconds: BurstQuietCeil,
		MaxWaitMinSeconds: BurstMaxWaitFloor, MaxWaitMaxSeconds: BurstMaxWaitCeil,
		ListLimitMin: DigestListFloor, ListLimitMax: DigestListCeil,
	}
}

// Clamp moves every burst knob of cfg inside the bounds (an env value out of
// range applies at the nearest bound).
func (l BurstLimits) Clamp(cfg *config.NotificationsConfig) {
	secs := func(d time.Duration) int { return int(d / time.Second) }
	dur := func(n int) time.Duration { return time.Duration(n) * time.Second }
	cfg.BurstThreshold = clampInt(cfg.BurstThreshold, l.ThresholdMin, l.ThresholdMax)
	cfg.BurstWindow = dur(clampInt(secs(cfg.BurstWindow), l.WindowMinSeconds, l.WindowMaxSeconds))
	cfg.BurstQuiet = dur(clampInt(secs(cfg.BurstQuiet), l.QuietMinSeconds, l.QuietMaxSeconds))
	cfg.BurstMaxWait = dur(clampInt(secs(cfg.BurstMaxWait), l.MaxWaitMinSeconds, l.MaxWaitMaxSeconds))
	if cfg.BurstQuiet > cfg.BurstMaxWait {
		cfg.BurstQuiet = cfg.BurstMaxWait
	}
	cfg.DigestListLimit = clampInt(cfg.DigestListLimit, l.ListLimitMin, l.ListLimitMax)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Validate checks the fields present in b against the bounds. prefix names the
// fields the way the form does ("global.").
func (l BurstLimits) Validate(b BurstFields, prefix string) error {
	for _, c := range []struct {
		field  string
		v      *int
		lo, hi int
	}{
		{"burstThreshold", b.BurstThreshold, l.ThresholdMin, l.ThresholdMax},
		{"burstWindowSeconds", b.BurstWindowSeconds, l.WindowMinSeconds, l.WindowMaxSeconds},
		{"burstQuietSeconds", b.BurstQuietSeconds, l.QuietMinSeconds, l.QuietMaxSeconds},
		{"burstMaxWaitSeconds", b.BurstMaxWaitSeconds, l.MaxWaitMinSeconds, l.MaxWaitMaxSeconds},
		{"digestListLimit", b.DigestListLimit, l.ListLimitMin, l.ListLimitMax},
	} {
		if c.v != nil && (*c.v < c.lo || *c.v > c.hi) {
			return &ValidationError{Field: prefix + c.field, Message: fmt.Sprintf("must be between %d and %d", c.lo, c.hi)}
		}
	}
	if b.BurstQuietSeconds != nil && b.BurstMaxWaitSeconds != nil && *b.BurstQuietSeconds > *b.BurstMaxWaitSeconds {
		return &ValidationError{Field: prefix + "burstQuietSeconds", Message: "must not be longer than the maximum wait"}
	}
	return nil
}

// applyBurstFields layers stored burst knobs onto cfg. A value below its floor
// is ignored rather than applied, so a record written with "0" (once meaning
// "off" or "always list") can never turn summaries off.
func applyBurstFields(cfg *config.NotificationsConfig, b BurstFields) {
	secs := func(n int) time.Duration { return time.Duration(n) * time.Second }
	if b.BurstThreshold != nil && *b.BurstThreshold >= BurstThresholdFloor {
		cfg.BurstThreshold = *b.BurstThreshold
	}
	if b.BurstWindowSeconds != nil && *b.BurstWindowSeconds > 0 {
		cfg.BurstWindow = secs(*b.BurstWindowSeconds)
	}
	if b.BurstQuietSeconds != nil && *b.BurstQuietSeconds > 0 {
		cfg.BurstQuiet = secs(*b.BurstQuietSeconds)
	}
	if b.BurstMaxWaitSeconds != nil && *b.BurstMaxWaitSeconds > 0 {
		cfg.BurstMaxWait = secs(*b.BurstMaxWaitSeconds)
	}
	if b.DigestListLimit != nil && *b.DigestListLimit >= DigestListFloor {
		cfg.DigestListLimit = *b.DigestListLimit
	}
}
