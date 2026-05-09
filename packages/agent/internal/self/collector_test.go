package self

import (
	"context"
	"runtime"
	"testing"

	"github.com/kubebolt/kubebolt/packages/agent/internal/buffer"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

func TestCollector_EmitsKubeboltAgentMetrics(t *testing.T) {
	buf := buffer.New(100)
	// Push a few samples so collected_total is non-zero on first read.
	buf.Push([]*agentv2.Sample{
		{MetricName: "x"}, {MetricName: "y"}, {MetricName: "z"},
	})

	c := New(buf, "cid-1", "test-cluster", "node-a", "v1.0.0-test")
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	want := []string{
		"kubebolt_agent_samples_collected_total",
		"kubebolt_agent_samples_dropped_total",
		"kubebolt_agent_buffer_size_current",
		"kubebolt_agent_buffer_size_max",
		"kubebolt_agent_memory_bytes",
		"kubebolt_agent_goroutines",
		"kubebolt_agent_info",
	}
	got := map[string]*agentv2.Sample{}
	for _, s := range samples {
		got[s.MetricName] = s
	}

	t.Run("emits all 7 expected metric names", func(t *testing.T) {
		for _, name := range want {
			if _, ok := got[name]; !ok {
				t.Errorf("missing metric %q", name)
			}
		}
		if len(samples) != len(want) {
			t.Errorf("expected %d samples, got %d", len(want), len(samples))
		}
	})

	t.Run("buffer.Push reflected in samples_collected_total", func(t *testing.T) {
		s := got["kubebolt_agent_samples_collected_total"]
		if s == nil {
			t.Skip("metric missing — covered by previous test")
		}
		if s.Value != 3 {
			t.Errorf("samples_collected_total = %v, want 3", s.Value)
		}
	})

	t.Run("buffer_size_current matches buffer.Len after a Push", func(t *testing.T) {
		s := got["kubebolt_agent_buffer_size_current"]
		if s == nil {
			t.Skip()
		}
		if int(s.Value) != buf.Len() {
			t.Errorf("buffer_size_current = %v, want %d", s.Value, buf.Len())
		}
	})

	t.Run("buffer_size_max matches the configured capacity", func(t *testing.T) {
		s := got["kubebolt_agent_buffer_size_max"]
		if s == nil {
			t.Skip()
		}
		if int(s.Value) != 100 {
			t.Errorf("buffer_size_max = %v, want 100", s.Value)
		}
	})

	t.Run("memory_bytes is positive (process is alive)", func(t *testing.T) {
		s := got["kubebolt_agent_memory_bytes"]
		if s == nil {
			t.Skip()
		}
		if s.Value <= 0 {
			t.Errorf("memory_bytes = %v, expected positive", s.Value)
		}
	})

	t.Run("goroutines is at least 1 (the goroutine running the test)", func(t *testing.T) {
		s := got["kubebolt_agent_goroutines"]
		if s == nil {
			t.Skip()
		}
		if int(s.Value) < 1 {
			t.Errorf("goroutines = %v, want >=1", s.Value)
		}
		// Sanity: shouldn't be wildly higher than the actual count.
		if int(s.Value) > runtime.NumGoroutine()+10 {
			t.Errorf("goroutines = %v, runtime reports %d, suspiciously off", s.Value, runtime.NumGoroutine())
		}
	})

	t.Run("info gauge=1 with agent_version label", func(t *testing.T) {
		s := got["kubebolt_agent_info"]
		if s == nil {
			t.Skip()
		}
		if s.Value != 1 {
			t.Errorf("info value = %v, want 1", s.Value)
		}
		if s.Labels["agent_version"] != "v1.0.0-test" {
			t.Errorf("agent_version label = %q, want v1.0.0-test", s.Labels["agent_version"])
		}
	})

	t.Run("identity labels (cluster_id, cluster_name, node) on every sample", func(t *testing.T) {
		for _, s := range samples {
			if s.Labels["cluster_id"] != "cid-1" {
				t.Errorf("metric %s: cluster_id = %q, want cid-1", s.MetricName, s.Labels["cluster_id"])
			}
			if s.Labels["cluster_name"] != "test-cluster" {
				t.Errorf("metric %s: cluster_name = %q, want test-cluster", s.MetricName, s.Labels["cluster_name"])
			}
			if s.Labels["node"] != "node-a" {
				t.Errorf("metric %s: node = %q, want node-a", s.MetricName, s.Labels["node"])
			}
		}
	})

	t.Run("agent_version label is only on the info gauge, not on every metric", func(t *testing.T) {
		// Putting agent_version on every series would explode cardinality
		// on every release. The info pattern keeps it scoped to one series
		// while still allowing dashboards to group by version.
		for _, s := range samples {
			if s.MetricName == "kubebolt_agent_info" {
				continue
			}
			if _, has := s.Labels["agent_version"]; has {
				t.Errorf("metric %s carries agent_version label; should be on _info only", s.MetricName)
			}
		}
	})
}

func TestCollector_NoClusterName(t *testing.T) {
	buf := buffer.New(10)
	c := New(buf, "cid-1", "" /* no cluster_name */, "node-a", "v1.0.0")
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range samples {
		if _, has := s.Labels["cluster_name"]; has {
			t.Errorf("metric %s emits cluster_name when none configured", s.MetricName)
			return
		}
	}
}

func TestCollector_NameInterface(t *testing.T) {
	c := New(buffer.New(10), "cid", "name", "node", "v1")
	if c.Name() != "agent_self" {
		t.Errorf("Name() = %q, want agent_self", c.Name())
	}
}
