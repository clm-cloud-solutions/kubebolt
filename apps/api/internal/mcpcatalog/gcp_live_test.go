package mcpcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
)

// TestGCPLiveCatalog builds the binding's tool set from Google's REAL
// endpoints. tools/list is public, so it needs no credentials: the signer
// gets a dummy token that is never sent, because listing is unsigned by
// design. It reports what each consumer would get and fails when the
// allowlist and Google's catalog have drifted apart.
//
//	KUBEBOLT_MCP_LIVE_GCP=1 go test -run TestGCPLiveCatalog -v ./internal/mcpcatalog/
//
// With KUBEBOLT_MCP_LIVE_GCP_UPDATE=1 it also rewrites the offline snapshot
// (testdata/gcp_tools_snapshot.json) the other tests bind against.
func TestGCPLiveCatalog(t *testing.T) {
	if os.Getenv("KUBEBOLT_MCP_LIVE_GCP") != "1" {
		t.Skip("set KUBEBOLT_MCP_LIVE_GCP=1 to list Google's live MCP catalogs")
	}
	b := gcpBinder{tokenSource: gcpStaticTokens, logger: gcpTestLogger(t)}
	start := time.Now()
	bound, err := b.Bind(t.Context(), gcpTestResolved("kubebolt-live-check"), Env{})
	if err != nil {
		t.Fatalf("bind against the live endpoints: %v", err)
	}
	// The gateway gives a bind 10 s (mcpgateway.DefaultBindTimeout).
	t.Logf("bind listed %d endpoints in %s", len(gcpServices), time.Since(start).Round(time.Millisecond))
	bd := bound.(*gcpBinding)
	defer bd.Close(t.Context())
	if len(bd.missing) > 0 {
		t.Errorf("endpoints that could not be listed: %v", bd.missing)
	}

	for _, c := range []Consumer{ConsumerKobi, ConsumerAutopilot} {
		tools := bd.Tools(c)
		total := 0
		for _, s := range tools {
			n := gcpDefinitionSize(c, s)
			total += n
			t.Logf("%-9s %-50s %5d bytes", c, ToolName(c, gcpConnectorID, s.Name), n)
		}
		t.Logf("%s: %d tools, %d bytes of definitions", c, len(tools), total)
		if got, want := gcpSpecNames(tools), gcpToolNames(c); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: live set differs from the allowlist\n got: %v\nwant: %v", c, got, want)
		}
		if total > gcpKobiBudgetBytes {
			t.Errorf("%s: %d bytes of definitions, over the %d budget", c, total, gcpKobiBudgetBytes)
		}
	}

	live := map[string][]wire.Tool{}
	for _, s := range gcpServices {
		tools, err := bd.endpoints[s.id].lister.ListTools(t.Context())
		if err != nil {
			t.Fatalf("list %s: %v", s.id, err)
		}
		live[s.id] = tools
		names := map[string]wire.Tool{}
		for _, lt := range tools {
			names[lt.Name] = lt
		}
		for name := range s.tools {
			lt, ok := names[name]
			switch {
			case !ok:
				t.Errorf("drift: %s/%s is allowlisted but Google no longer serves it", s.id, name)
			case !lt.ReadOnly():
				t.Errorf("drift: %s/%s is allowlisted but is no longer annotated read-only", s.id, name)
			}
		}
		var fresh []string
		for _, lt := range tools {
			if _, listed := s.tools[lt.Name]; !listed && lt.ReadOnly() && gcpDenied(s.id, lt.Name) == "" {
				fresh = append(fresh, lt.Name)
			}
		}
		sort.Strings(fresh)
		t.Logf("%s: %d tools served, not offered (read-only, review before adding): %v", s.id, len(tools), fresh)
	}

	if os.Getenv("KUBEBOLT_MCP_LIVE_GCP_UPDATE") == "1" {
		b, err := json.MarshalIndent(gcpSnapshotOf(live), "", " ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("testdata", "gcp_tools_snapshot.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(b)+1)
	}
}

// gcpSnapshotOf reduces live catalogs to what the offline tests need: the
// allowlisted tools in full (without outputSchema), and every tool KubeBolt
// must refuse — writes, denied data-plane reads — as a stub, so the offline
// bind still has something to filter out.
func gcpSnapshotOf(live map[string][]wire.Tool) gcpSnapshot {
	snap := gcpSnapshot{
		Note: "Google Cloud MCP tools/list, reduced: allowlisted tools in full without outputSchema; refused tools as stubs. " +
			"Regenerate with KUBEBOLT_MCP_LIVE_GCP=1 KUBEBOLT_MCP_LIVE_GCP_UPDATE=1 go test -run TestGCPLiveCatalog ./internal/mcpcatalog/",
		Endpoints: map[string][]wire.Tool{},
	}
	for _, s := range gcpServices {
		var out []wire.Tool
		for _, lt := range live[s.id] {
			lt.OutputSchema = nil
			lt.Title = ""
			switch {
			case s.tools[lt.Name].kobi || s.tools[lt.Name].autopilot:
				if s.tools[lt.Name].desc != "" {
					// Replaced by KubeBolt's text; a head of Google's is
					// enough to prove the replacement (the full ones
					// reach 40 KB).
					lt.Description = gcpCapText(lt.Description, 2000)
				}
				out = append(out, lt)
			case !lt.ReadOnly() || gcpDenied(s.id, lt.Name) != "":
				out = append(out, wire.Tool{
					Name:        lt.Name,
					Description: gcpCapText(gcpCleanText(lt.Description), 120),
					InputSchema: map[string]any{"type": "object"},
					Annotations: lt.Annotations,
				})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		snap.Endpoints[s.id] = out
	}
	return snap
}
