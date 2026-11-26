package rightsizing

// PerPod is where a recommendation becomes something a step can write.
//
// The engine works on workload totals: requests, limits and the P95 are summed
// over every replica and every container (cluster/connector.go
// buildNamespaceWorkloads, the P95 query in api/rightsizing.go). set_resources
// writes a value per container. Writing the total as if it were one pod's value
// would hand a 3-replica Deployment three times what it needs, so the conversion
// lives here, next to the rules, and not in a plan or in Kobi (Kobi Plans spec,
// «Del total del motor al valor por contenedor»).

// PerPodSkip says why a recommendation cannot become a per-container value.
// The values are the plan's exclusion reasons, so a plan carries them as they
// are.
type PerPodSkip string

const (
	// SkipMultiContainer: the pod template has more than one container. The
	// engine sums them, so any split between containers would be invented.
	// Lifted when the engine measures the P95 per container.
	SkipMultiContainer PerPodSkip = "multi_container"
	// SkipPodsVaried: the number of pods changed during the window (or is zero
	// now). The P95 of the sum then mixes moments with different replica
	// counts, and dividing it by today's count gives a false number.
	SkipPodsVaried PerPodSkip = "pods_varied"
	// SkipNoEngineValue: neither resource has a value to write. No-specs, or
	// the per-pod value rounds to what the pod already has.
	SkipNoEngineValue PerPodSkip = "no_engine_value"
)

// Fields a PerPodValue writes in a container's resources.
const (
	FieldRequests = "requests"
	FieldLimits   = "limits"
)

// PerPodValue is one resource's new value for the single container of each
// pod.
type PerPodValue struct {
	// Field is FieldRequests for an over-provisioned resource and FieldLimits
	// for one near its limit.
	Field string
	// Value is millicores for CPU and bytes for memory, rounded like the
	// engine rounds (10m, 10Mi, never below either).
	Value int64
	// Current is the pod's value today in the same field: the workload total
	// divided by the pods.
	Current int64
}

// PerPodChange is a recommendation turned into per-pod values. A nil resource
// has nothing to change.
type PerPodChange struct {
	CPU *PerPodValue
	Mem *PerPodValue
}

// PerPod turns one recommendation into the value for one pod's container.
//
//   - Only single-container workloads (containers == 1).
//   - Only when the pod count did not change in the window (podsStableInWindow)
//     and is above zero.
//   - Value = the engine's suggestion ÷ pods, rounded with the engine's
//     rounding. Over changes requests; NearLimit changes limits; a workload
//     with both gets both.
//   - A resource whose per-pod value would not move in the right direction
//     (a request that does not go down, a limit that does not go up) is left
//     alone: rounding to 10m on a small pod can land on today's value.
//
// The second return value is empty when there is something to write.
func PerPod(rec Recommendation, pods, containers int, podsStableInWindow bool) (PerPodChange, PerPodSkip) {
	if containers != 1 {
		return PerPodChange{}, SkipMultiContainer
	}
	if pods <= 0 || !podsStableInWindow {
		return PerPodChange{}, SkipPodsVaried
	}
	change := PerPodChange{
		CPU: perPodValue(rec.CPU, pods, roundCPU),
		Mem: perPodValue(rec.Mem, pods, roundMem),
	}
	if change.CPU == nil && change.Mem == nil {
		return PerPodChange{}, SkipNoEngineValue
	}
	return change, ""
}

func perPodValue(f Finding, pods int, round func(float64) int64) *PerPodValue {
	n := float64(pods)
	switch f.State {
	case Over:
		current := float64(f.Request) / n
		v := round(float64(f.Suggest) / n)
		if float64(v) >= current {
			return nil
		}
		return &PerPodValue{Field: FieldRequests, Value: v, Current: int64(current)}
	case NearLimit:
		current := float64(f.Limit) / n
		v := round(float64(f.Suggest) / n)
		if float64(v) <= current {
			return nil
		}
		return &PerPodValue{Field: FieldLimits, Value: v, Current: int64(current)}
	}
	return nil
}
