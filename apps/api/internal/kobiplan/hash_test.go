package kobiplan

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func podSpec(cpuReq string, image string, containers ...string) corev1.PodSpec {
	var spec corev1.PodSpec
	for _, name := range containers {
		spec.Containers = append(spec.Containers, corev1.Container{
			Name: name, Image: image,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpuReq), corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
		})
	}
	return spec
}

func TestSpecHash_IsCanonical(t *testing.T) {
	a := podSpec("1000m", "api:1.0", "api", "sidecar")
	b := podSpec("1", "api:1.0", "sidecar", "api") // same CPU spelled another way, containers swapped
	if SpecHash(a) != SpecHash(b) {
		t.Fatal("the same template hashed differently")
	}
	if !strings.HasPrefix(SpecHash(a), "sha256:") {
		t.Fatalf("hash %q has no sha256: prefix", SpecHash(a))
	}
}

func TestSpecHash_MovesWhenTheTemplateChanges(t *testing.T) {
	base := SpecHash(podSpec("500m", "api:1.0", "api"))
	for name, changed := range map[string]corev1.PodSpec{
		"image":     podSpec("500m", "api:1.1", "api"),
		"resources": podSpec("400m", "api:1.0", "api"),
		"container": podSpec("500m", "api:1.0", "api", "proxy"),
	} {
		if SpecHash(changed) == base {
			t.Errorf("changing the %s left the hash as it was", name)
		}
	}
	// Labels, env or probes are not the step's business.
	other := podSpec("500m", "api:1.0", "api")
	other.Containers[0].Env = []corev1.EnvVar{{Name: "X", Value: "1"}}
	if SpecHash(other) != base {
		t.Error("an env change moved the hash")
	}
}

func TestContentHash_IgnoresMetadata(t *testing.T) {
	p := examplePlan()
	base := ContentHash(p)

	p.Version = 3
	p.Status = StatusVerified
	p.ID = "pln_other"
	p.CreatedAt = p.CreatedAt.Add(48 * 3600e9)
	p.Exports = []Export{{Format: ExportCommands, At: t0}}
	p.Notes = []Note{{Version: 2, At: t0, Text: "rejected 80m"}}
	p.Baseline = &Baseline{WindowDays: 7}
	p.Impact.Observed = &Observed{WindowDays: 7}
	p.Impact.Estimated.Money.Monthly = 99 // prices moved; the proposal did not
	p.Engine.ComputedAt = p.Engine.ComputedAt.Add(3600e9)
	p.Steps[0].Status = StepVerified
	p.Steps[0].Provenance = "another wording"
	p.ContentHash = "sha256:anything"
	if ContentHash(p) != base {
		t.Fatal("metadata moved the content hash")
	}
}

func TestContentHash_MovesWithTheProposal(t *testing.T) {
	base := ContentHash(examplePlan())
	cases := map[string]func(*Plan){
		"a value": func(p *Plan) {
			p.Steps[0].Params = json.RawMessage(`{"containers":[{"container":"api","requests":{"cpu":"150m","memory":"300Mi"}}]}`)
		},
		"the target":   func(p *Plan) { p.Steps[0].Target.Name = "worker" },
		"the risk":     func(p *Plan) { p.Steps[0].Risk = RiskMedium },
		"an exclusion": func(p *Plan) { p.Excluded = nil },
		"the owner":    func(p *Plan) { p.Steps[0].Target.Owner = &Owner{Kind: OwnerHelm, Ref: "shop"} },
	}
	for name, mutate := range cases {
		p := examplePlan()
		mutate(&p)
		if ContentHash(p) == base {
			t.Errorf("changing %s left the content hash as it was", name)
		}
	}
}

// The same params written with other spacing or key order are the same
// proposal.
func TestContentHash_ParamsSpelling(t *testing.T) {
	a := examplePlan()
	b := examplePlan()
	b.Steps[0].Params = json.RawMessage(`{ "containers": [ { "requests": { "memory": "300Mi", "cpu": "120m" }, "container": "api" } ] }`)
	if ContentHash(a) != ContentHash(b) {
		t.Fatal("respelled params moved the content hash")
	}
}
