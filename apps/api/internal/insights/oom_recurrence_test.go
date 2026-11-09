package insights

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// oomPod is a pod whose container was last OOMKilled at `killed` and runs
// again, Ready, since `started`.
func oomPod(name string, killed, started time.Time) *corev1.Pod {
	p := pod("prod", name)
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "app",
		Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(killed),
		}},
	}}
	return p
}

func evalOOM(r Rule, pods ...*corev1.Pod) []models.Insight {
	return r.Evaluate(&ClusterState{Pods: pods})
}

// One kill: the replacement run clears it after readyGrace, as before.
func TestOOMRecurrence_SingleKillClearsAfterReadyGrace(t *testing.T) {
	now := time.Now()
	r := oomKilledRule()
	if got := evalOOM(r, oomPod("api", now.Add(-time.Minute), now.Add(-time.Minute))); len(got) != 1 {
		t.Fatalf("a kill one minute ago is not on: %+v", got)
	}
	if got := evalOOM(r, oomPod("api", now.Add(-10*time.Minute), now.Add(-10*time.Minute))); len(got) != 0 {
		t.Fatalf("a single kill held past readyGrace: %+v", got)
	}
}

// Two kills six hours apart (the engine saw the first while the container ran
// after it): the run after the second has to outlast six hours. An hour in,
// the insight is still open and says why.
func TestOOMRecurrence_SecondKillHoldsUntilTheRunOutlastsTheInterval(t *testing.T) {
	now := time.Now()
	first := now.Add(-7 * time.Hour)
	second := now.Add(-time.Hour)
	r := oomKilledRule()

	// Seen running after the first kill, long past readyGrace: closed, as today.
	if got := evalOOM(r, oomPod("api", first, first.Add(time.Second))); len(got) != 0 {
		t.Fatalf("first kill held: %+v", got)
	}
	got := evalOOM(r, oomPod("api", second, second.Add(time.Second)))
	if len(got) != 1 {
		t.Fatalf("a recurring OOM one hour into a run that used to last six closed: %+v", got)
	}
	if !strings.Contains(got[0].Message, "also OOMKilled 6h before that") {
		t.Errorf("message does not say why it stays open: %q", got[0].Message)
	}
	prev := evidenceByLabel(t, got[0], "Previous OOM kill")
	if prev.At == nil || !prev.At.Equal(first) || prev.Detail != "6h before the last one" {
		t.Errorf("previous kill evidence: %+v", prev)
	}
}

// The same pair of kills, later: the run now outlasts the interval, and the
// insight clears on that evidence.
func TestOOMRecurrence_ClearsOnceTheRunOutlastsTheInterval(t *testing.T) {
	now := time.Now()
	first := now.Add(-20 * time.Hour)
	second := now.Add(-14 * time.Hour) // 6 h apart; the run since has lasted 14 h
	r := oomKilledRule()
	evalOOM(r, oomPod("api", first, first.Add(time.Second)))
	if got := evalOOM(r, oomPod("api", second, second.Add(time.Second))); len(got) != 0 {
		t.Fatalf("a run that outlasted the interval kept the insight: %+v", got)
	}
}

// Kills a month apart: the hold stops at maxOOMRecurrenceGrace.
func TestOOMRecurrence_HoldIsCappedAtADay(t *testing.T) {
	now := time.Now()
	first := now.Add(-30 * 24 * time.Hour)
	r := oomKilledRule()
	evalOOM(r, oomPod("api", first, first.Add(time.Second)))

	if got := evalOOM(r, oomPod("api", now.Add(-23*time.Hour), now.Add(-23*time.Hour))); len(got) != 1 {
		t.Fatalf("23 h into the run, under the cap, closed: %+v", got)
	}
	r2 := oomKilledRule()
	evalOOM(r2, oomPod("api", first, first.Add(time.Second)))
	if got := evalOOM(r2, oomPod("api", now.Add(-25*time.Hour), now.Add(-25*time.Hour))); len(got) != 0 {
		t.Fatalf("25 h into the run, past the cap, still open: %+v", got)
	}
}

// Kills closer together than readyGrace never shorten it.
func TestOOMRecurrence_NeverShorterThanReadyGrace(t *testing.T) {
	now := time.Now()
	r := oomKilledRule()
	evalOOM(r, oomPod("api", now.Add(-90*time.Second), now.Add(-89*time.Second)))
	if got := evalOOM(r, oomPod("api", now.Add(-60*time.Second), now.Add(-60*time.Second))); len(got) != 1 {
		t.Fatalf("a kill a minute ago closed: %+v", got)
	}
}

// A replaced pod starts with no history: the rolled-out fix clears at once,
// and the memory of the old pod is dropped.
func TestOOMRecurrence_ReplacedPodForgetsTheHistory(t *testing.T) {
	now := time.Now()
	first, second := now.Add(-7*time.Hour), now.Add(-time.Hour)
	r := oomKilledRule()
	evalOOM(r, oomPod("api-old", first, first.Add(time.Second)))
	evalOOM(r, oomPod("api-old", second, second.Add(time.Second)))

	fresh := pod("prod", "api-new")
	fresh.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))}},
	}}
	if got := evalOOM(r, fresh); len(got) != 0 {
		t.Fatalf("a replacement pod with no OOM on record fired: %+v", got)
	}
	// Back with the old pod's last kill (say the informer missed a beat): the
	// first kill is forgotten, so readyGrace applies again.
	if got := evalOOM(r, oomPod("api-old", second, second.Add(time.Second))); len(got) != 0 {
		t.Fatalf("history survived the pod leaving the cluster: %+v", got)
	}
}

// Re-evaluating the same kill keeps the same answer: the second sighting of
// the last termination is not a new kill.
func TestOOMRecurrence_SameKillSeenTwiceIsOneKill(t *testing.T) {
	now := time.Now()
	killed := now.Add(-10 * time.Minute)
	r := oomKilledRule()
	for i := 0; i < 3; i++ {
		if got := evalOOM(r, oomPod("api", killed, killed.Add(time.Second))); len(got) != 0 {
			t.Fatalf("evaluation %d: one kill seen again became a recurrence: %+v", i, got)
		}
	}
}
