package rightsizing

import "testing"

// The spec's example: a 3-replica Deployment whose totals are 1500m / 3Gi
// requested with a P95 of 300m / 750Mi. The engine suggests 360m / 900Mi for
// the whole workload; each pod's container gets 120m / 300Mi.
func TestPerPod_DividesTheEngineTotalsByThePods(t *testing.T) {
	cpu := EvaluateResource(1500, 3000, 300, CPUAbsFloorMilli, true, roundCPU)
	mem := EvaluateResource(3*1024*mi, 6*1024*mi, 750*mi, MemAbsFloorBytes, true, roundMem)
	if cpu.State != Over || mem.State != Over {
		t.Fatalf("setup: states cpu=%s mem=%s, want over/over", cpu.State, mem.State)
	}
	if cpu.Suggest != 360 || mem.Suggest != 900*mi {
		t.Fatalf("setup: suggestions cpu=%d mem=%d", cpu.Suggest, mem.Suggest)
	}

	got, skip := PerPod(Recommendation{CPU: cpu, Mem: mem}, 3, 1, true)
	if skip != "" {
		t.Fatalf("skip = %q, want none", skip)
	}
	if got.CPU == nil || *got.CPU != (PerPodValue{Field: FieldRequests, Value: 120, Current: 500}) {
		t.Fatalf("cpu = %+v, want requests 120m (from 500m)", got.CPU)
	}
	if got.Mem == nil || *got.Mem != (PerPodValue{Field: FieldRequests, Value: 300 * mi, Current: 1024 * mi}) {
		t.Fatalf("mem = %+v, want requests 300Mi (from 1Gi)", got.Mem)
	}
}

// Writing the total as one pod's value would give each replica the whole
// workload's suggestion: the bug the conversion exists to prevent.
func TestPerPod_NeverWritesTheTotalAsAPodValue(t *testing.T) {
	cpu := EvaluateResource(1500, 0, 300, CPUAbsFloorMilli, true, roundCPU)
	got, _ := PerPod(Recommendation{CPU: cpu}, 3, 1, true)
	if got.CPU == nil || got.CPU.Value == cpu.Suggest {
		t.Fatalf("cpu = %+v: the per-pod value must not be the workload total %d", got.CPU, cpu.Suggest)
	}
}

func TestPerPod_NearLimitRaisesTheLimit(t *testing.T) {
	// Two pods, 400m limit each (800m total), using 780m together: within the
	// engine's 50m floor of the limit, so the near-limit rule fires.
	cpu := EvaluateResource(400, 800, 780, CPUAbsFloorMilli, true, roundCPU)
	if cpu.State != NearLimit {
		t.Fatalf("setup: state %s, want near-limit", cpu.State)
	}
	got, skip := PerPod(Recommendation{CPU: cpu}, 2, 1, true)
	if skip != "" {
		t.Fatalf("skip = %q", skip)
	}
	// 780 × 1.5 = 1170 total → 585 per pod → rounded to 590m (nearest 10m).
	if got.CPU == nil || *got.CPU != (PerPodValue{Field: FieldLimits, Value: 590, Current: 400}) {
		t.Fatalf("cpu = %+v, want limits 590m (from 400m)", got.CPU)
	}
	if got.Mem != nil {
		t.Fatalf("mem = %+v, want no change", got.Mem)
	}
}

func TestPerPod_BothDimensionsInOneChange(t *testing.T) {
	cpu := EvaluateResource(1000, 0, 100, CPUAbsFloorMilli, true, roundCPU)           // over
	mem := EvaluateResource(512*mi, 600*mi, 580*mi, MemAbsFloorBytes, true, roundMem) // near limit
	got, skip := PerPod(Recommendation{CPU: cpu, Mem: mem}, 1, 1, true)
	if skip != "" || got.CPU == nil || got.Mem == nil {
		t.Fatalf("got %+v skip %q, want a change in both", got, skip)
	}
	if got.CPU.Field != FieldRequests || got.Mem.Field != FieldLimits {
		t.Fatalf("fields cpu=%s mem=%s, want requests/limits", got.CPU.Field, got.Mem.Field)
	}
}

func TestPerPod_Skips(t *testing.T) {
	over := EvaluateResource(1500, 0, 300, CPUAbsFloorMilli, true, roundCPU)
	cases := []struct {
		name       string
		rec        Recommendation
		pods       int
		containers int
		stable     bool
		want       PerPodSkip
	}{
		{"two containers", Recommendation{CPU: over}, 3, 2, true, SkipMultiContainer},
		{"no containers known", Recommendation{CPU: over}, 3, 0, true, SkipMultiContainer},
		{"pods changed in the window", Recommendation{CPU: over}, 3, 1, false, SkipPodsVaried},
		{"scaled to zero", Recommendation{CPU: over}, 0, 1, true, SkipPodsVaried},
		{"no specs", Recommendation{
			CPU: EvaluateResource(0, 0, 400, CPUAbsFloorMilli, true, roundCPU),
			Mem: EvaluateResource(0, 0, 400*mi, MemAbsFloorBytes, true, roundMem),
		}, 2, 1, true, SkipNoEngineValue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, skip := PerPod(c.rec, c.pods, c.containers, c.stable)
			if skip != c.want {
				t.Fatalf("skip = %q, want %q", skip, c.want)
			}
			if got.CPU != nil || got.Mem != nil {
				t.Fatalf("a skipped recommendation carried values: %+v", got)
			}
		})
	}
}

// Ten pods at 10m each, using 40m together: over-provisioned on the totals,
// but each pod's value rounds back to the 10m floor it already has.
func TestPerPod_RoundingThatDoesNotMoveIsNoChange(t *testing.T) {
	cpu := EvaluateResource(100, 0, 40, CPUAbsFloorMilli, true, roundCPU)
	if cpu.State != Over {
		t.Fatalf("setup: state %s, want over", cpu.State)
	}
	got, skip := PerPod(Recommendation{CPU: cpu}, 10, 1, true)
	if skip != SkipNoEngineValue || got.CPU != nil {
		t.Fatalf("got %+v skip %q, want no change (no_engine_value)", got, skip)
	}
}
