package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// An install with nothing registered — an empty kubeconfig and no agent yet —
// answers `[]`. It used to answer `null` (a nil slice), and the UI read
// `.length` on it: Home crashed on exactly the screen that should lead to the
// first cluster.
func TestListClusters_NothingRegisteredIsAnEmptyArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	empty := "apiVersion: v1\nkind: Config\nclusters: []\ncontexts: []\nusers: []\ncurrent-context: \"\"\n"
	if err := os.WriteFile(path, []byte(empty), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := cluster.NewManager(path, websocket.NewHub(), time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)

	h := &handlers{manager: m}
	rec := httptest.NewRecorder()
	h.listClusters(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("body = %s, want []", got)
	}
}
