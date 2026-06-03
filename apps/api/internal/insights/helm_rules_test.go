package insights

import (
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
)

func TestHelmReleaseFailedRule(t *testing.T) {
	rule := helmReleaseFailedRule()
	state := &ClusterState{
		HelmReleases: []helm.Release{
			{Name: "ok", Namespace: "a", Status: "deployed", Chart: "x"},
			{Name: "broken", Namespace: "a", Status: "failed", Chart: "x", ChartVersion: "1.0.0", Description: "upgrade failed"},
		},
	}
	got := rule.Evaluate(state)
	if len(got) != 1 || got[0].Resource != "HelmRelease/a/broken" {
		t.Fatalf("expected only the failed release flagged, got %+v", got)
	}
	if got[0].Severity != "critical" {
		t.Fatalf("expected critical severity, got %s", got[0].Severity)
	}
}

func TestHelmReleaseHookPendingRule(t *testing.T) {
	rule := helmReleaseHookPendingRule()
	state := &ClusterState{
		HelmReleases: []helm.Release{
			// pending but recent → not flagged
			{Name: "fresh", Namespace: "a", Status: "pending-upgrade", Updated: time.Now().Add(-1 * time.Minute)},
			// pending and stale → flagged
			{Name: "wedged", Namespace: "a", Status: "pending-install", Updated: time.Now().Add(-10 * time.Minute)},
			// deployed → not flagged
			{Name: "ok", Namespace: "a", Status: "deployed", Updated: time.Now().Add(-1 * time.Hour)},
		},
	}
	got := rule.Evaluate(state)
	if len(got) != 1 || got[0].Resource != "HelmRelease/a/wedged" {
		t.Fatalf("expected only the stale-pending release flagged, got %+v", got)
	}
}
