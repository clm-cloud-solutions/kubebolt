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

// The insight tick decodes only the releases HelmStatusNeedsAttention accepts.
// Every rule that reads HelmReleases, given a release in any
// status Helm writes, must report only on statuses the predicate accepts —
// otherwise it would never see the release in production.
func TestHelmStatusNeedsAttention_CoversEveryHelmRule(t *testing.T) {
	statuses := []string{
		"unknown", "deployed", "uninstalled", "superseded", "failed",
		"uninstalling", "pending-install", "pending-upgrade", "pending-rollback",
	}
	long := time.Now().Add(-time.Hour)
	for _, rule := range AllRules() {
		for _, st := range statuses {
			rel := helm.Release{Name: "r", Namespace: "ns", Status: st, Updated: long, Chart: "c", ChartVersion: "1"}
			if got := rule.Evaluate(&ClusterState{HelmReleases: []helm.Release{rel}}); len(got) > 0 && !HelmStatusNeedsAttention(st) {
				t.Errorf("rule %s reports on status %q, which HelmStatusNeedsAttention leaves undecoded", rule.ID, st)
			}
		}
	}
	for _, st := range []string{"failed", "FAILED", "pending-install", "pending-upgrade", "pending-rollback"} {
		if !HelmStatusNeedsAttention(st) {
			t.Errorf("HelmStatusNeedsAttention(%q) = false", st)
		}
	}
	for _, st := range []string{"deployed", "superseded", "uninstalled", "uninstalling", ""} {
		if HelmStatusNeedsAttention(st) {
			t.Errorf("HelmStatusNeedsAttention(%q) = true: the tick would decode releases no rule reads", st)
		}
	}
}
