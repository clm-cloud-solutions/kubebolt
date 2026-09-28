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
		f := EvaluateResource(tc.req, tc.lim, tc.p95, tc.floor, roundCPU)
		if f.State != tc.want || f.Suggest != tc.wantSuggest {
			t.Errorf("%s: state=%s suggest=%d, want %s %d", tc.name, f.State, f.Suggest, tc.want, tc.wantSuggest)
		}
	}
}

// The web version told CPU from memory by magnitude (< 1024 = millicores): a
// suggestion of a core or more was rounded as bytes and came out as ~10
// million millicores. Each resource has its own rounding now.
func TestEvaluateResource_CPUAboveOneCoreStaysInMillicores(t *testing.T) {
	f := EvaluateResource(1000, 1000, 960, CPUAbsFloorMilli, roundCPU) // near limit → 960 × 1.5
	if f.State != NearLimit || f.Suggest != 1440 {
		t.Errorf("suggest = %d (%s), want 1440m", f.Suggest, f.State)
	}
	m := EvaluateResource(0, 512*mi, 480*mi, MemAbsFloorBytes, roundMem)
	if m.Suggest%(10*mi) != 0 || m.Suggest < 700*mi {
		t.Errorf("memory suggest = %d, want 720Mi rounded to 10Mi", m.Suggest)
	}
}

func TestEvaluate_OrderTotalsAndPreliminary(t *testing.T) {
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
	days := 1.5
	res := Evaluate(wls, cpu, nil, &days)
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
	if !res.Preliminary {
		t.Error("1.5 days of history must read as preliminary")
	}
	if Evaluate(wls, cpu, nil, nil).Preliminary {
		t.Error("an unknown window must not cry preliminary")
	}
}
