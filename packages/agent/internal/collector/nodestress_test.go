package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestNodeStress_Collect_Happy verifies the parser against canonical
// /proc/loadavg and /proc/pressure/* fixtures from a real Linux system
// (kernel ≥ 4.20, PSI enabled).
func TestNodeStress_Collect_Happy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "loadavg"),
		[]byte("1.23 0.55 0.27 2/512 9876\n"), 0644); err != nil {
		t.Fatalf("write loadavg: %v", err)
	}
	pressureDir := filepath.Join(root, "pressure")
	if err := os.Mkdir(pressureDir, 0755); err != nil {
		t.Fatalf("mkdir pressure: %v", err)
	}
	// cpu only has "some" — kernel doesn't emit "full" for CPU.
	if err := os.WriteFile(filepath.Join(pressureDir, "cpu"),
		[]byte("some avg10=0.10 avg60=0.20 avg300=0.30 total=4500000\n"), 0644); err != nil {
		t.Fatalf("write pressure/cpu: %v", err)
	}
	// memory + io have both "some" and "full" lines — we only consume "some".
	for _, r := range []string{"memory", "io"} {
		body := "some avg10=0.00 avg60=0.00 avg300=0.00 total=12000000\n" +
			"full avg10=0.00 avg60=0.00 avg300=0.00 total=8000000\n"
		if err := os.WriteFile(filepath.Join(pressureDir, r), []byte(body), 0644); err != nil {
			t.Fatalf("write pressure/%s: %v", r, err)
		}
	}

	c := NewNodeStress("cl1", "my-cluster", "node-a", "t1", root)
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	want := map[string]float64{
		"node_load1":                                4.5,    // sentinel — overwritten below
		"node_load5":                                4.5,    // ditto
		"node_load15":                               4.5,    // ditto
		"node_pressure_cpu_waiting_seconds_total":    4.5,   // 4_500_000 µs
		"node_pressure_memory_waiting_seconds_total": 12.0,  // 12_000_000 µs
		"node_pressure_io_waiting_seconds_total":     12.0,
	}
	want["node_load1"] = 1.23
	want["node_load5"] = 0.55
	want["node_load15"] = 0.27

	got := map[string]float64{}
	for _, s := range samples {
		got[s.MetricName] = s.Value
		// Spot-check labels — every sample must carry node + cluster_id.
		if s.Labels["node"] != "node-a" {
			t.Errorf("sample %q missing node label: %v", s.MetricName, s.Labels)
		}
		if s.Labels["cluster_id"] != "cl1" {
			t.Errorf("sample %q missing cluster_id label: %v", s.MetricName, s.Labels)
		}
		if s.Labels["cluster_name"] != "my-cluster" {
			t.Errorf("sample %q missing cluster_name label: %v", s.MetricName, s.Labels)
		}
		if s.Labels["tenant_id"] != "t1" {
			t.Errorf("sample %q missing tenant_id label: %v", s.MetricName, s.Labels)
		}
		// source label is REQUIRED — the coverage probe filters by it
		// to distinguish OUR samples from real node-exporter. Without
		// it the kubebolt-node-stress chip falsely lights up on every
		// cluster that has any node-exporter source.
		if s.Labels["source"] != "kubebolt-agent" {
			t.Errorf("sample %q missing source=kubebolt-agent label: %v", s.MetricName, s.Labels)
		}
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("missing sample %q", name)
			continue
		}
		if g != w {
			t.Errorf("sample %q = %v, want %v", name, g, w)
		}
	}
}

// TestNodeStress_OlderKernel_NoPSI verifies graceful degradation: when
// /proc/pressure doesn't exist (kernel < 4.20), load averages still
// emit and Collect returns no error.
func TestNodeStress_OlderKernel_NoPSI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "loadavg"),
		[]byte("0.10 0.20 0.30 1/100 42\n"), 0644); err != nil {
		t.Fatalf("write loadavg: %v", err)
	}
	// Intentionally NO pressure/ directory — simulates kernel < 4.20.

	c := NewNodeStress("cl1", "", "node-a", "", root)
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect should not error when PSI is just missing: %v", err)
	}
	// Only 3 load samples; no PSI.
	if len(samples) != 3 {
		t.Errorf("len(samples) = %d, want 3 (only loadavg, PSI absent)", len(samples))
	}
	for _, s := range samples {
		if s.MetricName != "node_load1" && s.MetricName != "node_load5" && s.MetricName != "node_load15" {
			t.Errorf("unexpected metric %q when PSI absent", s.MetricName)
		}
		// cluster_name + tenant_id empty in this fixture → should NOT appear
		// in the label map (labels are skipped when empty per nodeLabels).
		if _, has := s.Labels["cluster_name"]; has {
			t.Errorf("empty cluster_name should NOT appear as label, got %v", s.Labels)
		}
		if _, has := s.Labels["tenant_id"]; has {
			t.Errorf("empty tenant_id should NOT appear as label, got %v", s.Labels)
		}
	}
}

// TestNodeStress_DisabledByOption is a silent no-op: when
// WithDeferNodeStress(true) is passed, Collect returns zero samples
// and no error even with a fully-populated /proc fixture. This is the
// path operators take when kube-prom-stack node-exporter is already
// shipping these metrics, to avoid double-counting.
func TestNodeStress_DisabledByOption(t *testing.T) {
	root := t.TempDir()
	// Populate fixtures that WOULD produce samples if collector ran.
	if err := os.WriteFile(filepath.Join(root, "loadavg"),
		[]byte("1.0 2.0 3.0 1/100 42\n"), 0644); err != nil {
		t.Fatalf("write loadavg: %v", err)
	}

	c := NewNodeStress("cl1", "", "node-a", "", root, WithDeferNodeStress(true))
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Errorf("disabled collector must not error, got %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("disabled collector must emit 0 samples, got %d", len(samples))
	}
}

// TestNodeStress_MalformedLoadavg ensures parse errors surface as an
// error from Collect (so the agent's caller logs it) but don't panic.
func TestNodeStress_MalformedLoadavg(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "loadavg"),
		[]byte("garbage not_a_number 0.27 1/100 42\n"), 0644); err != nil {
		t.Fatalf("write loadavg: %v", err)
	}
	c := NewNodeStress("cl1", "", "node-a", "", root)
	samples, err := c.Collect(context.Background())
	if err == nil {
		t.Errorf("expected error for malformed loadavg, got nil")
	}
	// No load samples (parse failed), no PSI samples (no pressure dir).
	if len(samples) != 0 {
		t.Errorf("expected 0 samples on parse failure, got %d", len(samples))
	}
}
