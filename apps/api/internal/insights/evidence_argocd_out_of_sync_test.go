package insights

import "testing"

// argocd-out-of-sync: sync and health as Argo CD reports them, with the
// revision it synced to.
func TestArgoOutOfSync_EvidenceCarriesSyncHealthAndRevision(t *testing.T) {
	app := map[string]interface{}{
		"name": "shop", "namespace": "argocd", "syncStatus": "OutOfSync", "healthStatus": "Degraded",
		"revision": "3f9c2ab1d0e4f5a6b7c8", "project": "default",
	}
	got := argoOutOfSyncRule().Evaluate(&ClusterState{ArgoApps: []map[string]interface{}{app}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "Sync").Detail; d != "OutOfSync at revision 3f9c2ab1d0e4" {
		t.Errorf("sync: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Health").Detail; d != "Degraded" {
		t.Errorf("health: %q", d)
	}
}
