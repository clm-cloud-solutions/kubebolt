package agent

import (
	"testing"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

func sample(name string, labels map[string]string) *agentv2.Sample {
	return &agentv2.Sample{MetricName: name, Labels: labels}
}

func TestEnforceTenantLabel_StampsWhenAbsent(t *testing.T) {
	samples := []*agentv2.Sample{
		sample("container_cpu_usage_seconds_total", map[string]string{"cluster_id": "c1"}),
		sample("node_load1", nil), // nil label map must be created
	}
	if asserted, ok := enforceTenantLabel(samples, "org-a"); !ok {
		t.Fatalf("unexpected mismatch: asserted=%q", asserted)
	}
	for i, s := range samples {
		if got := s.GetLabels()["tenant_id"]; got != "org-a" {
			t.Errorf("sample %d tenant_id = %q, want org-a", i, got)
		}
	}
}

func TestEnforceTenantLabel_PassesWhenMatching(t *testing.T) {
	samples := []*agentv2.Sample{
		sample("m", map[string]string{"tenant_id": "org-a"}),
	}
	if _, ok := enforceTenantLabel(samples, "org-a"); !ok {
		t.Fatal("matching tenant_id should pass")
	}
}

func TestEnforceTenantLabel_RejectsSpoof(t *testing.T) {
	// A batch whose first sample is honest but a later one claims another
	// org's tenant_id → spoof. Must be caught (whole batch is dropped by the
	// caller, so partial stamping of earlier samples is irrelevant).
	samples := []*agentv2.Sample{
		sample("m1", map[string]string{"tenant_id": "org-a"}),
		sample("m2", map[string]string{"tenant_id": "org-b"}),
	}
	asserted, ok := enforceTenantLabel(samples, "org-a")
	if ok {
		t.Fatal("spoofed tenant_id must be rejected")
	}
	if asserted != "org-b" {
		t.Errorf("asserted = %q, want org-b", asserted)
	}
}

func TestEnforceTenantLabel_OSSNeutralWhenNoTenant(t *testing.T) {
	// Empty tenant (auth disabled / OSS): no inspection, no stamping — a
	// sample even claiming some tenant_id is shipped untouched.
	samples := []*agentv2.Sample{
		sample("m", map[string]string{"tenant_id": "whatever"}),
		sample("m2", nil),
	}
	if _, ok := enforceTenantLabel(samples, ""); !ok {
		t.Fatal("empty tenant must short-circuit to ok")
	}
	if _, has := samples[1].GetLabels()["tenant_id"]; has {
		t.Error("must not stamp tenant_id when tenantID is empty (OSS)")
	}
}
