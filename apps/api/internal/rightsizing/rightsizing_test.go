package rightsizing

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

const mi = 1024 * 1024

func wl(ns, kind, name string, cpuReq, cpuLim, memReq, memLim int64) models.WorkloadSummary {
	return models.WorkloadSummary{
		Namespace: ns, Kind: kind, Name: name,
		CPU:    models.ResourceUsage{Requested: cpuReq, Limit: cpuLim},
		Memory: models.ResourceUsage{Requested: memReq, Limit: memLim},
	}
}

// The three rules, as the Capacity screen has always applied them.
func TestEvaluateResource_Rules(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		req, lim, p95, floor int64
		want                 State
		wantSuggest          int64
	}{
		{"near limit (P95 within the floor of the limit)", 100, 500, 460, CPUAbsFloorMilli, NearLimit, 690},
		{"80% of the limit but still a floor away", 100, 500, 450, CPUAbsFloorMilli, OK, 0},
		{"over-provisioned", 1000, 0, 100, CPUAbsFloorMilli, Over, 120},
		{"over but the gap is below the floor", 60, 0, 20, CPUAbsFloorMilli, OK, 0},
		{"no specs with usage", 0, 0, 300, CPUAbsFloorMilli, NoSpecs, 0},
		{"no specs, idle", 0, 0, 5, CPUAbsFloorMilli, OK, 0},
		{"healthy", 500, 1000, 300, CPUAbsFloorMilli, OK, 0},
	} {
		f := EvaluateResource(tc.req, tc.lim, tc.p95, tc.floor, true, roundCPU)
		if f.State != tc.want || f.Suggest != tc.wantSuggest {
			t.Errorf("%s: state=%s suggest=%d, want %s %d", tc.name, f.State, f.Suggest, tc.want, tc.wantSuggest)
		}
	}
}

// Finding #74: a workload with no P95 sample at all (hasData false) is a
// workload we have never measured — not one measured at zero. No rule may fire,
// or an unsampled Deployment with a request is flagged over-provisioned and told
// to shrink to the rounding floor on no evidence whatsoever.
func TestEvaluateResource_NoDataIsNoOpinion(t *testing.T) {
	// Same inputs that, WITH data, are clearly over-provisioned (req 1000m, p95
	// 0) — but with hasData false there is nothing to act on.
	if f := EvaluateResource(1000, 0, 0, CPUAbsFloorMilli, false, roundCPU); f.State != OK || f.Suggest != 0 {
		t.Errorf("no-data over-provisioned: state=%s suggest=%d, want ok 0", f.State, f.Suggest)
	}
	// A real measured zero (hasData true) IS over-provisioned — the idle case
	// stays flagged, so the fix does not blind the engine to genuine waste.
	if f := EvaluateResource(1000, 0, 0, CPUAbsFloorMilli, true, roundCPU); f.State != Over {
		t.Errorf("measured-zero idle: state=%s, want over", f.State)
	}
}

// The web version told CPU from memory by magnitude (< 1024 = millicores): a
// suggestion of a core or more was rounded as bytes and came out as ~10
// million millicores. Each resource has its own rounding now.
func TestEvaluateResource_CPUAboveOneCoreStaysInMillicores(t *testing.T) {
	f := EvaluateResource(1000, 1000, 960, CPUAbsFloorMilli, true, roundCPU) // near limit → 960 × 1.5
	if f.State != NearLimit || f.Suggest != 1440 {
		t.Errorf("suggest = %d (%s), want 1440m", f.Suggest, f.State)
	}
	m := EvaluateResource(0, 512*mi, 480*mi, MemAbsFloorBytes, true, roundMem)
	if m.Suggest%(10*mi) != 0 || m.Suggest < 700*mi {
		t.Errorf("memory suggest = %d, want 720Mi rounded to 10Mi", m.Suggest)
	}
}

// wasteFixture is the workload set + P95 index the ordering/totals test uses.
// Every workload carries a P95 sample, so only the window gate and ordering are
// under test here.
func wasteFixture() ([]models.NamespaceWorkload, map[string]int64) {
	wls := []models.NamespaceWorkload{{Namespace: "shop", Workloads: []models.WorkloadSummary{
		wl("shop", "Deployment", "small-waste", 200, 0, 0, 0),
		wl("shop", "Deployment", "big-waste", 4000, 0, 0, 0),
		wl("shop", "Deployment", "hot", 100, 500, 0, 0),
		wl("shop", "StatefulSet", "no-specs", 0, 0, 0, 0),
		wl("shop", "Deployment", "fine", 500, 1000, 0, 0),
	}}}
	cpu := map[string]int64{
		Key("shop", "Deployment", "small-waste"): 20,
		Key("shop", "Deployment", "big-waste"):   100,
		Key("shop", "Deployment", "hot"):         470,
		Key("shop", "StatefulSet", "no-specs"):   300,
		Key("shop", "Deployment", "fine"):        300,
	}
	return wls, cpu
}

func TestEvaluate_OrderAndTotals(t *testing.T) {
	wls, cpu := wasteFixture()
	days := 3.0 // past the default minimum history, so the engine operates
	res := Evaluate(wls, cpu, nil, &days, 0)
	var got []string
	for _, r := range res.Recs {
		got = append(got, r.Name)
	}
	want := []string{"hot", "big-waste", "small-waste", "no-specs"} // critical, then warnings by waste, then info
	if len(got) != len(want) {
		t.Fatalf("recs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// big-waste 4000 → suggest 120 (reclaim 3880); small-waste 200 → 20 (reclaim 180)
	if res.Totals.Count != 4 || res.Totals.ReclaimCPUMilli != 3880+180 {
		t.Errorf("totals = %+v", res.Totals)
	}
	if res.InsufficientHistory {
		t.Error("3 days of history is enough — must not read as insufficient")
	}
}

// Finding #74: below the minimum history (48h by default) — or with no samples at all —
// right-sizing emits NO recommendations and flags InsufficientHistory, instead
// of recommending shrinks built on a quiet snapshot of a barely-run cluster.
func TestEvaluate_InsufficientHistoryGate(t *testing.T) {
	wls, cpu := wasteFixture() // these WOULD produce 4 recs with enough history
	for _, tc := range []struct {
		name string
		days *float64
	}{
		{"half a day", f64(0.5)},
		{"just under the bar", f64(1.9)},
		{"no window at all", nil},
	} {
		res := Evaluate(wls, cpu, nil, tc.days, 0)
		if len(res.Recs) != 0 || res.Totals.Count != 0 {
			t.Errorf("%s: got %d recs, want 0 (still collecting)", tc.name, len(res.Recs))
		}
		if !res.InsufficientHistory {
			t.Errorf("%s: InsufficientHistory=false, want true", tc.name)
		}
	}
	// At exactly the minimum the engine operates (the gate is a strict <).
	at := 2.0
	if Evaluate(wls, cpu, nil, &at, 0).InsufficientHistory {
		t.Error("exactly the default minimum must operate, not collect")
	}
}

// Finding #74: with enough cluster history, a workload still absent from the
// P95 index (just deployed, never sampled) gets no recommendation — while an
// idle workload genuinely measured at zero stays flagged over-provisioned.
func TestEvaluate_UnsampledWorkloadIsNotRecommended(t *testing.T) {
	wls := []models.NamespaceWorkload{{Namespace: "shop", Workloads: []models.WorkloadSummary{
		wl("shop", "Deployment", "just-deployed", 1000, 0, 0, 0), // absent from cpu → no sample
		wl("shop", "Deployment", "truly-idle", 1000, 0, 0, 0),    // present at 0 → measured idle
	}}}
	cpu := map[string]int64{
		Key("shop", "Deployment", "truly-idle"): 0, // a real measured zero
	}
	days := 3.0
	res := Evaluate(wls, cpu, nil, &days, 0)
	var got []string
	for _, r := range res.Recs {
		got = append(got, r.Name)
	}
	if len(got) != 1 || got[0] != "truly-idle" {
		t.Fatalf("recs = %v, want only [truly-idle] (the unsampled one is no opinion)", got)
	}
}

func f64(v float64) *float64 { return &v }

// The platform may lower the minimum history so a trial cluster shows
// recommendations on its first day (Settings › General). Below the default two
// days they carry LowConfidence; the gate still holds below the lowered bar.
func TestEvaluate_LoweredMinimumHistory(t *testing.T) {
	wls, cpu := wasteFixture()

	at44h := 44.0 / 24
	res := Evaluate(wls, cpu, nil, &at44h, 36)
	if res.InsufficientHistory || len(res.Recs) == 0 {
		t.Fatalf("44h against a 36h minimum must operate: insufficient=%v recs=%d", res.InsufficientHistory, len(res.Recs))
	}
	if !res.LowConfidence {
		t.Error("recommendations from under 48h of history must say they are low-confidence")
	}
	if res.MinHistoryHours != 36 {
		t.Errorf("MinHistoryHours = %d, want 36", res.MinHistoryHours)
	}

	at30h := 30.0 / 24
	if res := Evaluate(wls, cpu, nil, &at30h, 36); !res.InsufficientHistory || len(res.Recs) != 0 {
		t.Errorf("30h against a 36h minimum must still collect: insufficient=%v recs=%d", res.InsufficientHistory, len(res.Recs))
	}

	if res := Evaluate(wls, cpu, nil, &at44h, 0); !res.InsufficientHistory || res.MinHistoryHours != DefaultMinHistoryHours {
		t.Errorf("unset minimum must be the default 48h: insufficient=%v min=%d", res.InsufficientHistory, res.MinHistoryHours)
	}

	at3d := 3.0
	if res := Evaluate(wls, cpu, nil, &at3d, 36); res.LowConfidence {
		t.Error("three days of history is not low-confidence")
	}
}

func TestClampMinHistoryHours(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 48}, {-5, 48}, {6, MinMinHistoryHours}, {12, 12}, {36, 36}, {500, MaxMinHistoryHours},
	} {
		if got := ClampMinHistoryHours(tc.in); got != tc.want {
			t.Errorf("ClampMinHistoryHours(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
