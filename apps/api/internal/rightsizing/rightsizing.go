// Package rightsizing is the deterministic right-sizing engine: the rules the
// Capacity and Cost screens show and Kobi's get_right_sizing answers with.
//
// It used to live in the browser (apps/web hooks/useRightSizing.ts). Two
// readers — the screens and the tool — must give the same answer, and two
// copies in two languages drift, so the rules live here once and the web asks
// GET /right-sizing.
//
// Deterministic on purpose, not model-driven: a constant rule set is
// predictable and does not change between runs.
//
// Rules, per resource, CPU and memory evaluated independently:
//
//  1. NEAR-LIMIT  limit > 0 && P95 >= 0.8 × limit                  → critical
//  2. OVER-PROV   request > 0 && P95 < 0.5 × request &&
//     (request − P95) above an absolute floor                  → warning
//  3. NO-SPECS    request == 0 && limit == 0 && P95 above the floor → info
//
// The floors (50m CPU, 100Mi memory) stop near-idle controllers from being
// flagged on a percentage that means nothing in absolute terms.
package rightsizing

import (
	"math"
	"sort"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

const (
	CPUAbsFloorMilli = 50
	MemAbsFloorBytes = 100 * 1024 * 1024

	// Headroom over the P95 for a suggested value: enough that a small spike
	// does not OOM or throttle, not so much that the waste just moves down.
	RequestHeadroom = 1.2
	LimitHeadroom   = 1.5

	// ConfidenceDays: below two days of history the P95 has not seen two daily
	// peaks, so a quiet snapshot reads as the steady state.
	ConfidenceDays = 2.0
)

// Severity of a recommendation.
type Severity string

const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Info     Severity = "info"
)

// State of one resource.
type State string

const (
	Over      State = "over"
	NearLimit State = "near-limit"
	NoSpecs   State = "no-specs"
	OK        State = "ok"
)

// Finding is one resource of one workload. CPU in millicores, memory in bytes.
type Finding struct {
	Request int64 `json:"request"`
	Limit   int64 `json:"limit"`
	P95     int64 `json:"p95"`
	State   State `json:"state"`
	// Suggest is the new request for Over, the new limit for NearLimit, 0
	// otherwise.
	Suggest int64 `json:"suggest"`
}

// Recommendation is one workload that needs a change.
type Recommendation struct {
	Namespace string   `json:"namespace"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Severity  Severity `json:"severity"`
	Reason    string   `json:"reason"`
	CPU       Finding  `json:"cpu"`
	Mem       Finding  `json:"mem"`
}

// Totals is what applying the Over recommendations would hand back.
type Totals struct {
	Count           int   `json:"count"`
	ReclaimCPUMilli int64 `json:"reclaimCpuMilli"`
	ReclaimMemBytes int64 `json:"reclaimMemBytes"`
}

// Result is the whole answer.
type Result struct {
	Recs   []Recommendation `json:"recs"`
	Totals Totals           `json:"totals"`
	// WindowDays is how much history the P95 really spans; nil when unknown.
	WindowDays *float64 `json:"windowDays,omitempty"`
	// Preliminary: WindowDays < ConfidenceDays — the figures are optimistic.
	Preliminary bool `json:"preliminary"`
}

// Key identifies a workload in the P95 indexes: namespace/kind/name.
func Key(namespace, kind, name string) string { return namespace + "/" + kind + "/" + name }

// Evaluate applies the rules to every workload. cpuP95 is in millicores and
// memP95 in bytes, keyed by Key; a workload with no sample gets P95 0.
func Evaluate(workloads []models.NamespaceWorkload, cpuP95, memP95 map[string]int64, windowDays *float64) Result {
	recs := []Recommendation{}
	for _, nsw := range workloads {
		for _, w := range nsw.Workloads {
			k := Key(w.Namespace, w.Kind, w.Name)
			cpu := EvaluateResource(w.CPU.Requested, w.CPU.Limit, cpuP95[k], CPUAbsFloorMilli, roundCPU)
			mem := EvaluateResource(w.Memory.Requested, w.Memory.Limit, memP95[k], MemAbsFloorBytes, roundMem)
			sev, ok := CombinedSeverity(cpu.State, mem.State)
			if !ok {
				continue
			}
			recs = append(recs, Recommendation{
				Namespace: w.Namespace, Kind: w.Kind, Name: w.Name,
				Severity: sev, Reason: describeReason(cpu, mem), CPU: cpu, Mem: mem,
			})
		}
	}
	order := map[Severity]int{Critical: 0, Warning: 1, Info: 2}
	sort.SliceStable(recs, func(i, j int) bool {
		if order[recs[i].Severity] != order[recs[j].Severity] {
			return order[recs[i].Severity] < order[recs[j].Severity]
		}
		if a, b := wasteMagnitude(recs[i]), wasteMagnitude(recs[j]); a != b {
			return a > b
		}
		return Key(recs[i].Namespace, recs[i].Kind, recs[i].Name) < Key(recs[j].Namespace, recs[j].Kind, recs[j].Name)
	})
	res := Result{Recs: recs, Totals: Totals{Count: len(recs)}, WindowDays: windowDays}
	for _, r := range recs {
		if r.CPU.State == Over {
			res.Totals.ReclaimCPUMilli += r.CPU.Request - r.CPU.Suggest
		}
		if r.Mem.State == Over {
			res.Totals.ReclaimMemBytes += r.Mem.Request - r.Mem.Suggest
		}
	}
	res.Preliminary = windowDays != nil && *windowDays < ConfidenceDays
	return res
}

// EvaluateResource applies the three rules to one resource. round is the
// resource's own rounding — CPU and memory are never told apart by magnitude:
// the web version did (below 1024 = millicores), so a CPU suggestion of a
// core or more was rounded as bytes and came out as ~10 million millicores.
func EvaluateResource(request, limit, p95, absFloor int64, round func(float64) int64) Finding {
	f := Finding{Request: request, Limit: limit, P95: p95, State: OK}
	switch {
	// Near-limit first: it is the urgent signal.
	case limit > 0 && float64(p95) >= 0.8*float64(limit) && p95-limit > -absFloor:
		f.State, f.Suggest = NearLimit, round(float64(p95)*LimitHeadroom)
	case request > 0 && float64(p95) < 0.5*float64(request) && request-p95 > absFloor:
		f.State, f.Suggest = Over, round(float64(p95)*RequestHeadroom)
	case request == 0 && limit == 0 && p95 > absFloor:
		f.State = NoSpecs
	}
	return f
}

// CombinedSeverity is the workload's severity from its two resources; false
// when neither needs a change.
func CombinedSeverity(cpu, mem State) (Severity, bool) {
	switch {
	case cpu == NearLimit || mem == NearLimit:
		return Critical, true
	case cpu == Over || mem == Over:
		return Warning, true
	case cpu == NoSpecs || mem == NoSpecs:
		return Info, true
	}
	return "", false
}

func describeReason(cpu, mem Finding) string {
	var parts []string
	switch cpu.State {
	case NearLimit:
		parts = append(parts, "CPU near limit")
	case Over:
		parts = append(parts, "CPU over-provisioned")
	}
	switch mem.State {
	case NearLimit:
		parts = append(parts, "Memory near limit")
	case Over:
		parts = append(parts, "Memory over-provisioned")
	}
	if len(parts) == 0 {
		var which []string
		if cpu.State == NoSpecs {
			which = append(which, "CPU")
		}
		if mem.State == NoSpecs {
			which = append(which, "memory")
		}
		if len(which) > 0 {
			parts = append(parts, "No "+strings.Join(which, " / ")+" specs defined")
		}
	}
	return strings.Join(parts, " · ")
}

// wasteMagnitude orders recommendations of the same severity: 1m of CPU
// weighs like 1Mi of memory. The ratio is arbitrary; only the order matters.
func wasteMagnitude(r Recommendation) float64 {
	var w float64
	if r.CPU.State == Over {
		w += float64(r.CPU.Request - r.CPU.P95)
	}
	if r.Mem.State == Over {
		w += float64(r.Mem.Request-r.Mem.P95) / (1024 * 1024)
	}
	return w
}

// roundCPU rounds millicores to the nearest 10m, never below 10m.
func roundCPU(v float64) int64 {
	return int64(math.Max(10, math.Round(v/10)*10))
}

// roundMem rounds bytes to the nearest 10Mi, never below 10Mi.
func roundMem(v float64) int64 {
	const tenMi = 10 * 1024 * 1024
	return int64(math.Max(tenMi, math.Round(v/tenMi)*tenMi))
}
