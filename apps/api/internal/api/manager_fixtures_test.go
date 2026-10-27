package api

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// newManagerOn is a real Manager over a kubeconfig whose contexts point at
// the given servers, none of them current.
func newManagerOn(t *testing.T, servers map[string]string) *cluster.Manager {
	t.Helper()
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\ncurrent-context: \"\"\nclusters:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "- name: %s\n  cluster:\n    server: %s\n", n, servers[n])
	}
	b.WriteString("contexts:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "- name: %s\n  context:\n    cluster: %s\n", n, n)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := cluster.NewManager(path, websocket.NewHub(), time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)
	m.SetConnectTimeoutProvider(func() time.Duration { return 10 * time.Second })
	m.SetCacheSyncTimeoutProvider(func() time.Duration { return 10 * time.Second })
	return m
}
