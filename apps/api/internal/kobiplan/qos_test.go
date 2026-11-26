package kobiplan

import (
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/rightsizing"
)

func TestQoSClass(t *testing.T) {
	cases := map[string]struct {
		spec []string // cpuReq, memReq, cpuLim, memLim per container
		want string
	}{
		"requests equal limits":         {[]string{"100m", "50Mi", "100m", "50Mi"}, QoSGuaranteed},
		"limits only (requests follow)": {[]string{"", "", "100m", "50Mi"}, QoSGuaranteed},
		"a limit above its request":     {[]string{"100m", "50Mi", "140m", "50Mi"}, QoSBurstable},
		"no memory limit":               {[]string{"100m", "50Mi", "100m", ""}, QoSBurstable},
		"nothing set":                   {[]string{"", "", "", ""}, QoSBestEffort},
	}
	for name, c := range cases {
		if got := QoSClass(specOf(container("c", c.spec[0], c.spec[1], c.spec[2], c.spec[3]))); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
	// One Burstable container makes the pod Burstable.
	if got := QoSClass(specOf(container("a", "100m", "50Mi", "100m", "50Mi"), container("b", "10m", "", "", ""))); got != QoSBurstable {
		t.Errorf("mixed = %s", got)
	}
}

// kindnet (25-nov, lab): Guaranteed at 100m/100m; raising its CPU limit to
// 140m alone makes it Burstable. The request that keeps the class is 140m.
func TestQoSChange_RaisingALimit(t *testing.T) {
	spec := specOf(container("kindnet-cni", "100m", "50Mi", "100m", "50Mi"))
	lim := "140m"
	q := qosChange(spec, ContainerResources{Container: "kindnet-cni", Limits: &Resources{CPU: &lim}})
	if q == nil || q.Before != QoSGuaranteed || q.After != QoSBurstable || q.KeepWith == nil || *q.KeepWith.CPU != "140m" {
		t.Fatalf("change = %+v", q)
	}
	// Raising both keeps the class: no change to say.
	if q := qosChange(spec, ContainerResources{Container: "kindnet-cni", Requests: &Resources{CPU: &lim}, Limits: &Resources{CPU: &lim}}); q != nil {
		t.Fatalf("both raised = %+v", q)
	}
}

// The builder flags it: high risk, the reason, and the class change in the
// step's evidence.
func TestBuildRightsizing_LeavingGuaranteedIsHighRisk(t *testing.T) {
	in := fixture()
	in.Scope = Scope{Workloads: []string{"payments/worker"}}
	k := rightsizing.Key("payments", "Deployment", "worker")
	w := in.Workloads[k]
	w.Template = specOf(container("worker", "400m", "128Mi", "400m", "128Mi"))
	in.Workloads[k] = w
	p, _ := BuildRightsizing(in)
	s := stepByName(p, "worker")
	if s == nil || s.Risk != RiskHigh || !contains(s.RiskReasons, RiskReasonQoSChange) {
		t.Fatalf("step = %+v", s)
	}
	if !strings.Contains(string(s.Evidence), `"qos":{"before":"Guaranteed","after":"Burstable","keepWith":{"cpu":`) {
		t.Fatalf("evidence = %s", s.Evidence)
	}
	// A pod that is not Guaranteed has no class to lose.
	w.Template = specOf(container("worker", "200m", "", "400m", ""))
	in.Workloads[k] = w
	p, _ = BuildRightsizing(in)
	if s := stepByName(p, "worker"); contains(s.RiskReasons, RiskReasonQoSChange) {
		t.Fatalf("a Burstable pod got qos_change: %v", s.RiskReasons)
	}
}
