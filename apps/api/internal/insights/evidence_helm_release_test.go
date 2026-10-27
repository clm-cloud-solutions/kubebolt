package insights

import (
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
)

// helm-release-failed and helm-release-hook-pending: the release as Helm
// recorded it, and when it last changed — a release stuck for minutes and one
// stuck for a month are different findings.
func TestHelmRelease_EvidenceCarriesReleaseAndWhen(t *testing.T) {
	updated := time.Now().Add(-3 * time.Hour)
	failed := helm.Release{Name: "shop", Namespace: "shop", Revision: 7, Status: "failed", Chart: "shop", ChartVersion: "1.4.0",
		Updated: updated, Description: "Upgrade \"shop\" failed: timed out waiting for the condition"}
	got := helmReleaseFailedRule().Evaluate(&ClusterState{HelmReleases: []helm.Release{failed}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "Release").Detail; d != "chart shop 1.4.0, revision 7, status failed" {
		t.Errorf("release: %q", d)
	}
	if e := evidenceByLabel(t, got[0], "Last change"); !strings.HasPrefix(e.Detail, "Upgrade \"shop\" failed") || !strings.HasSuffix(e.Detail, "3h before this was flagged") || e.At == nil {
		t.Errorf("last change: %+v", e)
	}

	pending := failed
	pending.Status, pending.Description = "pending-upgrade", ""
	got = helmReleaseHookPendingRule().Evaluate(&ClusterState{HelmReleases: []helm.Release{pending}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if e := evidenceByLabel(t, got[0], "Pending since"); e.Detail != "3h before this was flagged" {
		t.Errorf("pending since: %+v", e)
	}
}
