package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// The burst knobs are stored in seconds, applied as durations, rendered back
// for the form and merged field by field like the rest of the global block.
func TestNotifications_BurstSettingsPersistApplyAndRender(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()

	patch := &StoredNotificationsSettings{Global: &StoredNotificationsGlobal{BurstFields: BurstFields{
		BurstThreshold:      intPtr(8),
		BurstWindowSeconds:  intPtr(120),
		BurstQuietSeconds:   intPtr(45),
		BurstMaxWaitSeconds: intPtr(600),
		DigestListLimit:     intPtr(50),
	}}}
	if err := rt.PutNotifications(ctx, patch, nil, nil, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	got := rt.Notifications(ctx)
	if got.BurstThreshold != 8 || got.BurstWindow != 2*time.Minute || got.BurstQuiet != 45*time.Second ||
		got.BurstMaxWait != 10*time.Minute || got.DigestListLimit != 50 {
		t.Fatalf("applied = threshold %d, window %v, quiet %v, max wait %v, list limit %d",
			got.BurstThreshold, got.BurstWindow, got.BurstQuiet, got.BurstMaxWait, got.DigestListLimit)
	}

	// A later patch that only moves the threshold keeps the other knobs.
	if err := rt.PutNotifications(ctx, &StoredNotificationsSettings{Global: &StoredNotificationsGlobal{
		BurstFields: BurstFields{BurstThreshold: intPtr(12)},
	}}, nil, nil, nil); err != nil {
		t.Fatalf("put threshold: %v", err)
	}
	if got := rt.Notifications(ctx); got.BurstThreshold != 12 || got.BurstQuiet != 45*time.Second {
		t.Fatalf("after the second patch: threshold %d, quiet %v; want 12 and the stored 45s", got.BurstThreshold, got.BurstQuiet)
	}

	masked, err := rt.RenderMaskedNotifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if masked.Effective.BurstQuietSeconds != 45 || masked.Effective.DigestListLimit != 50 {
		t.Fatalf("rendered quiet %d / list limit %d, want 45 / 50", masked.Effective.BurstQuietSeconds, masked.Effective.DigestListLimit)
	}
	if masked.BurstLimits == nil || *masked.BurstLimits != GlobalBurstLimits() {
		t.Fatalf("burstLimits = %+v, want the install-wide ranges", masked.BurstLimits)
	}
}

func TestNotifications_BurstSettingsValidation(t *testing.T) {
	cases := []struct {
		name      string
		b         BurstFields
		wantField string
	}{
		{"threshold 0 does not turn summaries off", BurstFields{BurstThreshold: intPtr(0)}, "global.burstThreshold"},
		{"threshold 1 is not a burst", BurstFields{BurstThreshold: intPtr(1)}, "global.burstThreshold"},
		{"threshold above the ceiling", BurstFields{BurstThreshold: intPtr(1001)}, "global.burstThreshold"},
		{"window too short", BurstFields{BurstWindowSeconds: intPtr(5)}, "global.burstWindowSeconds"},
		{"quiet too long", BurstFields{BurstQuietSeconds: intPtr(601)}, "global.burstQuietSeconds"},
		{"max wait too short", BurstFields{BurstMaxWaitSeconds: intPtr(10)}, "global.burstMaxWaitSeconds"},
		{"list limit 0 does not always list", BurstFields{DigestListLimit: intPtr(0)}, "global.digestListLimit"},
		{"quiet longer than max wait", BurstFields{BurstQuietSeconds: intPtr(300), BurstMaxWaitSeconds: intPtr(60)}, "global.burstQuietSeconds"},
		{"the defaults", BurstFields{BurstThreshold: intPtr(5), BurstWindowSeconds: intPtr(60), BurstQuietSeconds: intPtr(30), BurstMaxWaitSeconds: intPtr(300), DigestListLimit: intPtr(20)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateNotificationsPatch(&StoredNotificationsSettings{Global: &StoredNotificationsGlobal{BurstFields: c.b}})
			if c.wantField == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != c.wantField {
				t.Fatalf("err = %v, want a validation error on %s", err, c.wantField)
			}
		})
	}
}

// A stored 0 (once "off" for the threshold, "always list" for the digest) is
// ignored on read, so no record can turn summaries off.
func TestNotifications_AStoredOffValueIsIgnored(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	if err := rt.orgStore.SetOrgSetting(ctx, notificationsSettingsKey,
		[]byte(`{"global":{"burstThreshold":0,"digestListLimit":0,"burstQuietSeconds":45}}`)); err != nil {
		t.Fatal(err)
	}
	rt.InvalidateNotifications(ctx)
	got := rt.Notifications(ctx)
	if got.BurstThreshold != config.DefaultBurstThreshold || got.DigestListLimit != config.DefaultDigestListLimit {
		t.Fatalf("stored off values applied: threshold %d, list %d", got.BurstThreshold, got.DigestListLimit)
	}
	if got.BurstQuiet != 45*time.Second {
		t.Fatalf("the valid knob next to them was dropped: quiet %v", got.BurstQuiet)
	}
}

// An env value out of range applies at the nearest bound; nothing unset reads
// as the most aggressive floor.
func TestBurstLimits_ClampKeepsTheConfigInRange(t *testing.T) {
	cfg := config.NotificationsConfig{BurstThreshold: 5000, BurstWindow: time.Second, BurstQuiet: 20 * time.Minute, BurstMaxWait: 2 * time.Minute}.WithBurstDefaults()
	GlobalBurstLimits().Clamp(&cfg)
	if cfg.BurstThreshold != BurstThresholdCeil || cfg.BurstWindow != BurstWindowFloor*time.Second ||
		cfg.BurstQuiet != 2*time.Minute || cfg.DigestListLimit != config.DefaultDigestListLimit {
		t.Fatalf("clamped = threshold %d, window %v, quiet %v, list %d", cfg.BurstThreshold, cfg.BurstWindow, cfg.BurstQuiet, cfg.DigestListLimit)
	}
}
