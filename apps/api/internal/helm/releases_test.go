package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// encodeRelease mimics Helm 3's secrets driver: json → gzip → base64.
func encodeRelease(t *testing.T, jsonBody string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(jsonBody)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	gz.Close()
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

func releaseSecret(t *testing.T, ns, name string, rev int, status, chartVer, jsonBody string) corev1.Secret {
	return corev1.Secret{
		Data: map[string][]byte{"release": encodeRelease(t, jsonBody)},
	}
}

func TestDecodeReleases_LatestRevisionPerRelease(t *testing.T) {
	// Two revisions of "kubebolt" + one of "agent". List view should return
	// the latest revision per release name.
	v1 := `{"name":"kubebolt","namespace":"kb","version":1,"info":{"status":"superseded","last_deployed":"2026-05-01T00:00:00Z"},"chart":{"metadata":{"name":"kubebolt","version":"1.13.0","appVersion":"1.13.0"}}}`
	v2 := `{"name":"kubebolt","namespace":"kb","version":2,"info":{"status":"deployed","last_deployed":"2026-05-20T00:00:00Z","description":"Upgrade complete"},"chart":{"metadata":{"name":"kubebolt","version":"1.14.0","appVersion":"1.14.0"}}}`
	agent := `{"name":"agent","namespace":"kb","version":1,"info":{"status":"deployed"},"chart":{"metadata":{"name":"kubebolt-agent","version":"1.1.0"}}}`

	secrets := []corev1.Secret{
		releaseSecret(t, "kb", "kubebolt", 1, "superseded", "1.13.0", v1),
		releaseSecret(t, "kb", "kubebolt", 2, "deployed", "1.14.0", v2),
		releaseSecret(t, "kb", "agent", 1, "deployed", "1.1.0", agent),
	}

	got := DecodeReleases(secrets)
	if len(got) != 2 {
		t.Fatalf("expected 2 releases (latest per name), got %d", len(got))
	}
	// Sorted by ns then name → agent first, kubebolt second.
	var kb *Release
	for i := range got {
		if got[i].Name == "kubebolt" {
			kb = &got[i]
		}
	}
	if kb == nil {
		t.Fatal("kubebolt release missing")
	}
	if kb.Revision != 2 || kb.Status != "deployed" || kb.ChartVersion != "1.14.0" {
		t.Fatalf("expected latest revision 2/deployed/1.14.0, got %d/%s/%s", kb.Revision, kb.Status, kb.ChartVersion)
	}
	if !kb.Updated.Equal(time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected Updated: %v", kb.Updated)
	}
}

func TestDecodeReleaseDetail_ValuesManifestHistory(t *testing.T) {
	v1 := `{"name":"kubebolt","namespace":"kb","version":1,"info":{"status":"superseded","last_deployed":"2026-05-01T00:00:00Z"},"chart":{"metadata":{"name":"kubebolt","version":"1.13.0"}}}`
	v2 := `{"name":"kubebolt","namespace":"kb","version":2,"manifest":"apiVersion: v1\nkind: Service","config":{"replicas":3},"info":{"status":"deployed","notes":"NOTES.txt body","last_deployed":"2026-05-20T00:00:00Z"},"chart":{"metadata":{"name":"kubebolt","version":"1.14.0","dependencies":[{"name":"victoria-metrics","version":"0.1.0"}]}}}`

	secrets := []corev1.Secret{
		releaseSecret(t, "kb", "kubebolt", 1, "superseded", "1.13.0", v1),
		releaseSecret(t, "kb", "kubebolt", 2, "deployed", "1.14.0", v2),
		// A different release that must NOT leak into the detail.
		releaseSecret(t, "kb", "agent", 1, "deployed", "1.1.0", `{"name":"agent","namespace":"kb","version":1,"info":{"status":"deployed"},"chart":{"metadata":{"name":"kubebolt-agent","version":"1.1.0"}}}`),
	}

	d, err := DecodeReleaseDetail("kb", "kubebolt", secrets)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.Revision != 2 || d.Manifest == "" || d.Notes != "NOTES.txt body" {
		t.Fatalf("detail not from latest revision: %+v", d)
	}
	if d.Values["replicas"] != float64(3) {
		t.Fatalf("values not decoded: %+v", d.Values)
	}
	if len(d.History) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(d.History))
	}
	if d.History[0].Revision != 2 {
		t.Fatalf("history should be newest-first, got rev %d first", d.History[0].Revision)
	}
	if len(d.Dependencies) != 1 || d.Dependencies[0].Name != "victoria-metrics" {
		t.Fatalf("dependencies not decoded: %+v", d.Dependencies)
	}

	if _, err := DecodeReleaseDetail("kb", "does-not-exist", secrets); err == nil {
		t.Fatal("expected not-found error for missing release")
	}
}
