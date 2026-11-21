package promread

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The agent chart refuses two sources of OpenCost's cost (finding #101):
// promRead.cost and the agent's OpenCost exporter both on stored every cost
// family twice and doubled every cost total, silently — kind-kubebolt-dev
// showed $1,485/mo for $743/mo. Renders the chart with helm; skipped where
// helm is not installed.
func TestChartRefusesTwoSourcesOfCost(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed")
	}
	chart := chartWithoutDependencies(t)
	promRead := []string{"--set", "agent.promRead.enabled=true", "--set", "agent.promRead.url=http://prom:9090"}
	cost := []string{"--set", "agent.promRead.cost.enabled=true"}
	exporter := []string{"--set", "collectors.exporters.opencost=http://opencost:9003/metrics"}
	bundled := []string{"--set", "opencost.enabled=true"}
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name   string
		args   []string
		refuse bool
	}{
		{"promRead cost alone", join(promRead, cost), false},
		{"exporter alone", exporter, false},
		{"bundled OpenCost alone", bundled, false},
		{"promRead without cost, exporter", join(promRead, exporter), false},
		{"cost flag with promRead off, exporter", join(cost, exporter), false},
		{"promRead cost and exporter", join(promRead, cost, exporter), true},
		{"promRead cost and bundled OpenCost", join(promRead, cost, bundled), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"template", "t", chart, "--set", "backendUrl=kubebolt:9090"}, c.args...)
			out, err := exec.Command(helm, args...).CombinedOutput()
			refused := err != nil && strings.Contains(string(out), "pick one source of cost")
			if refused != c.refuse {
				t.Fatalf("refused = %v, want %v; helm said:\n%s", refused, c.refuse, lastLines(string(out), 5))
			}
			if !c.refuse && err != nil {
				t.Fatalf("helm template failed: %v\n%s", err, lastLines(string(out), 5))
			}
		})
	}
}

// chartWithoutDependencies copies the agent chart to a temp dir without its
// OpenCost sub-chart dependency, so helm renders it offline: the guard lives
// in the agent's own templates.
func chartWithoutDependencies(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "..", "..", "deploy", "helm", "kubebolt-agent")
	dst := filepath.Join(t.TempDir(), "kubebolt-agent")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("copy the agent chart: %v", err)
	}
	_ = os.Remove(filepath.Join(dst, "Chart.lock"))
	_ = os.RemoveAll(filepath.Join(dst, "charts"))
	chartYAML := filepath.Join(dst, "Chart.yaml")
	raw, err := os.ReadFile(chartYAML)
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	// Drop the top-level dependencies: block (its key and the indented or
	// list lines under it).
	var kept []string
	inDeps := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "dependencies:") {
			inDeps = true
			continue
		}
		if inDeps && (line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "-")) {
			continue
		}
		inDeps = false
		kept = append(kept, line)
	}
	if err := os.WriteFile(chartYAML, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("write Chart.yaml: %v", err)
	}
	return dst
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
