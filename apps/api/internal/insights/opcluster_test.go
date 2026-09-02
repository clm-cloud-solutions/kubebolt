package insights

import (
	"strconv"
	"testing"
	"time"
)

// The clusterer is a product promise: L1, zero LLM, reproducible arithmetic.
// These tests pin the three deterministic steps — session windowing, seed
// precedence, blast counting — against synthetic bursts, including the shape
// of the Dipres escalation the whole program exists to answer.

func opEp(id, cluster, rule, resource string, at time.Time, status, kind string, dur time.Duration) Episode {
	ep := Episode{
		ID: id, ClusterID: cluster, RuleID: rule, Resource: resource,
		Status: status, FirstSeen: at, LastSeen: at.Add(dur),
	}
	if status == EpisodeResolved {
		end := at.Add(dur)
		ep.ResolvedAt = &end
		ep.ResolutionKind = kind
	}
	return ep
}

func TestClusterEpisodes_SessionWindowAndMinBurst(t *testing.T) {
	t0 := time.Date(2026, 8, 25, 5, 51, 0, 0, time.UTC)

	var eps []Episode
	// Burst A: 6 episodes trickling in 2-minute steps (each within SeedGap
	// of the previous — a node rotation drips, it doesn't detonate).
	for i := 0; i < 6; i++ {
		eps = append(eps, opEp(strconv.Itoa(i), "cl-1", "zero-replicas", "Deploy/ns/a"+strconv.Itoa(i),
			t0.Add(time.Duration(i)*2*time.Minute), EpisodeResolved, ResolutionAutoRecovered, 10*time.Minute))
	}
	// A lone episode 30 minutes later: outside the gap, and alone it is
	// below MinBurst — no operational episode for it.
	eps = append(eps, opEp("lone", "cl-1", "crash-loop", "Pod/ns/x", t0.Add(40*time.Minute), EpisodeFiring, "", time.Hour))

	ops := ClusterEpisodes("org-1", eps)
	if len(ops) != 1 {
		t.Fatalf("ops = %d (%+v), want 1 — the lone episode must not group", len(ops), ops)
	}
	if len(ops[0].MemberIDs) != 6 {
		t.Fatalf("members = %d, want 6", len(ops[0].MemberIDs))
	}
}

func TestClusterEpisodes_SeedPrecedence(t *testing.T) {
	t0 := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	mk := func(seedRule string) []Episode {
		eps := []Episode{opEp("seed", "cl-1", seedRule, "Node/_/n1", t0, EpisodeResolved, ResolutionAutoRecovered, 5*time.Minute)}
		for i := 0; i < 5; i++ {
			eps = append(eps, opEp("m"+strconv.Itoa(i), "cl-1", "readiness-probe-failing", "Deploy/ns/w"+strconv.Itoa(i),
				t0.Add(time.Minute), EpisodeResolved, ResolutionAutoRecovered, 5*time.Minute))
		}
		return eps
	}

	// A rotation seed wins even though rollout rules dominate by count.
	ops := ClusterEpisodes("org-1", mk("node-not-ready"))
	if len(ops) != 1 || ops[0].Kind != OpKindNodeRotation {
		t.Fatalf("kind = %+v, want node_rotation", ops)
	}
	if len(ops[0].SeedIDs) != 1 || ops[0].SeedIDs[0] != "seed" {
		t.Fatalf("seeds = %v, want [seed]", ops[0].SeedIDs)
	}

	ops = ClusterEpisodes("org-1", mk("evicted-pods"))
	if ops[0].Kind != OpKindNodePressure {
		t.Fatalf("kind = %s, want node_pressure", ops[0].Kind)
	}

	// No structural seed, but 5 distinct workloads on rollout rules.
	ops = ClusterEpisodes("org-1", mk("policy-orphan"))
	if ops[0].Kind != OpKindMassRollout {
		t.Fatalf("kind = %s, want mass_rollout", ops[0].Kind)
	}

	// A burst of pure expectation noise admits it doesn't know.
	var noise []Episode
	for i := 0; i < 6; i++ {
		noise = append(noise, opEp("n"+strconv.Itoa(i), "cl-1", "policy-orphan", "Namespace/ns"+strconv.Itoa(i)+"/ns"+strconv.Itoa(i),
			t0.Add(time.Duration(i)*time.Minute), EpisodeFiring, "", time.Hour))
	}
	ops = ClusterEpisodes("org-1", noise)
	if ops[0].Kind != OpKindUnknownBurst {
		t.Fatalf("kind = %s, want unknown_burst", ops[0].Kind)
	}
}

// TestClusterEpisodes_DipresShape — the 25-ago escalation as arithmetic: a
// node rotation knocks 46 deployments to zero replicas across two clusters;
// 45 auto-recover in ~49 min, one stays down 9h. The report's sentence is a
// count over this output.
func TestClusterEpisodes_DipresShape(t *testing.T) {
	t0 := time.Date(2026, 8, 25, 5, 51, 0, 0, time.UTC)

	eps := []Episode{opEp("rot", "cl-a", "node-not-ready", "Node/_/gke-pool-1", t0, EpisodeResolved, ResolutionAutoRecovered, 20*time.Minute)}
	for i := 0; i < 45; i++ {
		cl := "cl-a"
		if i%2 == 0 {
			cl = "cl-b" // the case crossed two clusters
		}
		eps = append(eps, opEp("d"+strconv.Itoa(i), cl, "zero-replicas", "Deploy/ns/svc"+strconv.Itoa(i),
			t0.Add(time.Duration(i%4)*time.Minute), EpisodeResolved, ResolutionAutoRecovered, 49*time.Minute))
	}
	eps = append(eps, opEp("stuck", "cl-a", "zero-replicas", "Deploy/ns/the-one",
		t0.Add(2*time.Minute), EpisodeFiring, "", 9*time.Hour))

	ops := ClusterEpisodes("org-1", eps)
	if len(ops) != 1 {
		t.Fatalf("ops = %d, want 1 — one rotation, not 47 findings", len(ops))
	}
	op := ops[0]
	if op.Kind != OpKindNodeRotation {
		t.Fatalf("kind = %s", op.Kind)
	}
	if op.Blast.Affected != 47 || op.Blast.AutoRecovered != 46 || op.Blast.StillFiring != 1 {
		t.Fatalf("blast = %+v, want affected 47 / autoRecovered 46 / stillFiring 1", op.Blast)
	}
	if op.Blast.WorstResource != "Deploy/ns/the-one" || op.Blast.WorstSeconds != int64((9*time.Hour).Seconds()) {
		t.Fatalf("worst = %s/%ds, want the 9h straggler", op.Blast.WorstResource, op.Blast.WorstSeconds)
	}
	if len(op.Clusters) != 2 {
		t.Fatalf("clusters = %v, want both (cross-cluster from day one)", op.Clusters)
	}
}

// Deterministic ids: recomputing the same window mints the SAME id, so
// downstream references survive a recompute.
func TestClusterEpisodes_DeterministicID(t *testing.T) {
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	var eps []Episode
	for i := 0; i < 6; i++ {
		eps = append(eps, opEp("e"+strconv.Itoa(i), "cl-1", "evicted-pods", "Pod/ns/p"+strconv.Itoa(i),
			t0.Add(time.Duration(i)*time.Minute), EpisodeResolved, ResolutionAutoRecovered, 5*time.Minute))
	}
	a := ClusterEpisodes("org-1", eps)
	b := ClusterEpisodes("org-1", eps)
	if a[0].ID != b[0].ID {
		t.Fatalf("recompute minted a new id: %s vs %s", a[0].ID, b[0].ID)
	}
	if c := ClusterEpisodes("org-2", eps); c[0].ID == a[0].ID {
		t.Fatal("two orgs share an operational id")
	}
}
