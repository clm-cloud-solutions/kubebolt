package integrations

import (
	"context"
	"strings"
	"testing"
)

func TestPrometheusReadProvider_Meta(t *testing.T) {
	p := NewPrometheusRead(nil, nil)
	got := p.Meta()
	if got.ID != PrometheusReadID {
		t.Errorf("ID = %q, want %q", got.ID, PrometheusReadID)
	}
	if got.Name != PrometheusReadName {
		t.Errorf("Name = %q, want %q", got.Name, PrometheusReadName)
	}
	if !strings.Contains(got.Description, "Mode C") {
		t.Errorf("Description should mention Mode C, got %q", got.Description)
	}
	if got.DocsURL == "" {
		t.Errorf("DocsURL must be set so the card's 'Learn more' link works")
	}
	wantCaps := map[string]bool{"metrics.scraped": true, "metrics.historical": true}
	for _, c := range got.Capabilities {
		delete(wantCaps, c)
	}
	if len(wantCaps) != 0 {
		t.Errorf("missing capabilities: %v", wantCaps)
	}
	// Static metadata only — Detect populates these.
	if got.Status != "" {
		t.Errorf("Meta() must not set Status (Detect's job), got %q", got.Status)
	}
}

func TestPrometheusReadProvider_Detect(t *testing.T) {
	const activeClusterID = "active-cluster-uid"

	makeProbe := func(active bool, err error) promreadActiveProbeFn {
		return func(_ context.Context, _ string) (bool, error) {
			return active, err
		}
	}

	tests := []struct {
		name               string
		clusterID          string
		probe              promreadActiveProbeFn
		wantStatus         Status
		wantMessageContain string
	}{
		{
			name:               "no probe wired → Unknown with reason",
			clusterID:          activeClusterID,
			probe:              nil,
			wantStatus:         StatusUnknown,
			wantMessageContain: "vm probe not configured",
		},
		{
			name:               "no active cluster UID → Unknown (cannot scope query)",
			clusterID:          "",
			probe:              makeProbe(true, nil),
			wantStatus:         StatusUnknown,
			wantMessageContain: "active cluster UID not yet resolved",
		},
		{
			name:               "probe errors → Unknown (transient VM blip, don't downgrade)",
			clusterID:          activeClusterID,
			probe:              makeProbe(false, &sentinelError{msg: "vm: i/o timeout"}),
			wantStatus:         StatusUnknown,
			wantMessageContain: "vm probe failed",
		},
		{
			name:               "no leader gauge → NotInstalled with helm hint",
			clusterID:          activeClusterID,
			probe:              makeProbe(false, nil),
			wantStatus:         StatusNotInstalled,
			wantMessageContain: "agent.promRead.enabled=true",
		},
		{
			name:               "leader gauge present → Installed",
			clusterID:          activeClusterID,
			probe:              makeProbe(true, nil),
			wantStatus:         StatusInstalled,
			wantMessageContain: "Promread leader is active",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPrometheusRead(
				func() string { return tc.clusterID },
				tc.probe,
			)
			got, err := p.Detect(context.Background(), nil)
			if err != nil {
				t.Fatalf("Detect() returned error: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Health == nil {
				t.Fatalf("Health is nil — Detect must always populate it (reason/hint comes from here)")
			}
			if !strings.Contains(got.Health.Message, tc.wantMessageContain) {
				t.Errorf("Health.Message = %q, want substring %q", got.Health.Message, tc.wantMessageContain)
			}
		})
	}
}

func TestPrometheusReadProvider_Detect_NilClusterClosure(t *testing.T) {
	// Constructor must tolerate a nil currentCluster (tests routinely
	// pass nil to exercise the no-UID branch). Without the nil guard
	// the call would panic before Detect even gets to the Unknown
	// branch.
	p := NewPrometheusRead(nil, func(_ context.Context, _ string) (bool, error) {
		return true, nil
	})
	got, err := p.Detect(context.Background(), nil)
	if err != nil {
		t.Fatalf("Detect() returned error: %v", err)
	}
	if got.Status != StatusUnknown {
		t.Errorf("nil currentCluster should produce StatusUnknown (no UID to scope), got %q", got.Status)
	}
}
