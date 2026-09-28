package insights

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// service-no-endpoints is the first rule instrumented with typed evidence, and
// it is first for a reason: on one production cluster it is 362 of 383 active
// insights, 352 of them named <app>-job-<hash>-ui-svc — batch-driver UI
// Services whose driver finished and whose Service nobody collected. Telling
// those apart from a genuinely broken Service needs the owner, the selector
// and the age, and none of the three survived fmt.Sprintf.

func svcWith(mutate func(*corev1.Service)) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-ui-svc", Namespace: "default",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.1",
			Selector:  map[string]string{"spark-role": "driver", "app-name": "ingest"},
		},
	}
	if mutate != nil {
		mutate(svc)
	}
	return svc
}

func evidenceByLabel(t *testing.T, ins models.Insight, label string) models.Evidence {
	t.Helper()
	for _, e := range ins.Evidence {
		if e.Label == label {
			return e
		}
	}
	t.Fatalf("no evidence labelled %q in %+v", label, ins.Evidence)
	return models.Evidence{}
}

func TestServiceNoEndpoints_EvidenceCarriesTheDecidingFacts(t *testing.T) {
	got := serviceNoEndpointsRule().Evaluate(&ClusterState{Services: []*corev1.Service{svcWith(nil)}})
	if len(got) != 1 {
		t.Fatalf("insights = %d, want 1", len(got))
	}
	ins := got[0]
	if ins.EvidenceCount != len(ins.Evidence) || ins.EvidenceCount == 0 {
		t.Fatalf("EvidenceCount=%d len=%d", ins.EvidenceCount, len(ins.Evidence))
	}

	// The owner is the deciding fact, and its ABSENCE is the finding:
	// Kubernetes collects a dependent whose owner is gone, so a Service that
	// outlives its workload almost certainly never had one. Recorded, not omitted.
	if owner := evidenceByLabel(t, ins, "Owner"); owner.Detail != "none" {
		t.Errorf("Owner = %q, want the explicit \"none\"", owner.Detail)
	}
	// The selector names what it was looking for — the durable clue once the
	// Job and the driver pod are both collected.
	if sel := evidenceByLabel(t, ins, "Selector"); !strings.Contains(sel.Detail, "spark-role=driver") {
		t.Errorf("Selector = %q", sel.Detail)
	}
	// Which branch fired, in a form something other than a human can read.
	if ep := evidenceByLabel(t, ins, "Endpoints"); !strings.Contains(ep.Detail, "matches nothing") {
		t.Errorf("Endpoints = %q", ep.Detail)
	}
}

// The age dimension the rule otherwise lacks: no endpoints for five minutes
// during a rollout and no endpoints for a month are the same finding today.
func TestServiceNoEndpoints_CreationCarriesItsOwnClock(t *testing.T) {
	got := serviceNoEndpointsRule().Evaluate(&ClusterState{Services: []*corev1.Service{svcWith(nil)}})
	created := evidenceByLabel(t, got[0], "Service created")
	if created.Kind != models.EvidenceEvent {
		t.Errorf("Kind = %q, want event — a creation is something that happened", created.Kind)
	}
	if created.At == nil {
		t.Fatal("no clock on the one fact that has one")
	}
	if want := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC); !created.At.Equal(want) {
		t.Errorf("At = %v, want %v", created.At, want)
	}
	// Detail is the AGE, not the timestamp: At already carries the exact
	// moment, and the age is what decides — five minutes during a rollout and
	// a month are the same finding without it.
	if strings.Contains(created.Detail, "2026-08-18") {
		t.Errorf("Detail repeats the timestamp At already has: %q", created.Detail)
	}
	if !strings.Contains(created.Detail, "d before this was flagged") {
		t.Errorf("Detail = %q, want a coarse age", created.Detail)
	}
	// Config facts must NOT invent a moment.
	for _, label := range []string{"Owner", "Selector", "Endpoints"} {
		if e := evidenceByLabel(t, got[0], label); e.At != nil {
			t.Errorf("%s carries a moment it does not have: %v", label, e.At)
		}
	}
}

func TestServiceNoEndpoints_OwnerIsNamedWhenPresent(t *testing.T) {
	svc := svcWith(func(s *corev1.Service) {
		s.OwnerReferences = []metav1.OwnerReference{
			{Kind: "Job", Name: "ingest-42"},
			{Kind: "Deployment", Name: "api"},
		}
	})
	got := serviceNoEndpointsRule().Evaluate(&ClusterState{Services: []*corev1.Service{svc}})
	owner := evidenceByLabel(t, got[0], "Owner")
	// Sorted, so an unchanged Service does not produce different text between
	// evaluations — Go map and slice order would otherwise look like a change.
	if owner.Detail != "Deployment/api, Job/ingest-42" {
		t.Errorf("Owner = %q", owner.Detail)
	}
}

// Map iteration order is random in Go. Evidence whose text changes between
// evaluations of an unchanged object is noise that reads like a change.
func TestServiceNoEndpoints_RenderingIsDeterministic(t *testing.T) {
	svc := svcWith(func(s *corev1.Service) {
		s.Spec.Selector = map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"}
		s.Labels = map[string]string{"z": "1", "y": "2", "x": "3"}
	})
	first := serviceNoEndpointsRule().Evaluate(&ClusterState{Services: []*corev1.Service{svc}})[0]
	for i := 0; i < 20; i++ {
		again := serviceNoEndpointsRule().Evaluate(&ClusterState{Services: []*corev1.Service{svc}})[0]
		for _, label := range []string{"Selector", "Service labels"} {
			if a, b := evidenceByLabel(t, first, label), evidenceByLabel(t, again, label); a.Detail != b.Detail {
				t.Fatalf("%s is not deterministic: %q vs %q", label, a.Detail, b.Detail)
			}
		}
	}
}

// The episode row carries this for every occurrence, so a Service with forty
// labels must not put a paragraph in a field meant to be read at a glance.
func TestServiceNoEndpoints_MapsAreBounded(t *testing.T) {
	many := map[string]string{}
	for i := 0; i < 30; i++ {
		many[string(rune('a'+i))+"key"] = "value"
	}
	svc := svcWith(func(s *corev1.Service) { s.Labels = many })
	got := serviceNoEndpointsRule().Evaluate(&ClusterState{Services: []*corev1.Service{svc}})[0]
	labels := evidenceByLabel(t, got, "Service labels")
	if !strings.Contains(labels.Detail, "more)") {
		t.Errorf("30 labels rendered whole: %q", labels.Detail)
	}
	if len(labels.Detail) > 200 {
		t.Errorf("Detail is %d chars; it is read at a glance", len(labels.Detail))
	}
}

// Kubernetes' own bookkeeping is on everything and therefore distinguishes
// nothing — it would crowd out the operator labels that are the actual clue.
func TestServiceNoEndpoints_DropsBookkeepingLabels(t *testing.T) {
	svc := svcWith(func(s *corev1.Service) {
		s.Labels = map[string]string{
			"app.kubernetes.io/managed-by":  "Helm",
			"helm.sh/chart":                 "x-1.0",
			"sparkoperator.k8s.io/app-name": "ingest-sigfe",
		}
	})
	got := serviceNoEndpointsRule().Evaluate(&ClusterState{Services: []*corev1.Service{svc}})[0]
	labels := evidenceByLabel(t, got, "Service labels")
	if !strings.Contains(labels.Detail, "sparkoperator.k8s.io/app-name=ingest-sigfe") {
		t.Errorf("dropped the label that identifies the creator: %q", labels.Detail)
	}
	for _, noise := range []string{"managed-by", "helm.sh/chart"} {
		if strings.Contains(labels.Detail, noise) {
			t.Errorf("kept bookkeeping label %q: %s", noise, labels.Detail)
		}
	}
}

// The other branch: pods exist, none pass readiness. A different operator
// response, and the evidence has to say which one this is.
func TestServiceNoEndpoints_NotReadyBranchIsDistinguishable(t *testing.T) {
	notReady := false
	state := &ClusterState{
		Services: []*corev1.Service{svcWith(nil)},
		EndpointSlices: []*discoveryv1.EndpointSlice{{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Labels:    map[string]string{"kubernetes.io/service-name": "api-ui-svc"},
			},
			Endpoints: []discoveryv1.Endpoint{
				{Conditions: discoveryv1.EndpointConditions{Ready: &notReady}},
				{Conditions: discoveryv1.EndpointConditions{Ready: &notReady}},
			},
		}},
	}
	got := serviceNoEndpointsRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("insights = %d, want 1", len(got))
	}
	ep := evidenceByLabel(t, got[0], "Endpoints")
	if !strings.Contains(ep.Detail, "2") || !strings.Contains(ep.Detail, "none ready") {
		t.Errorf("Endpoints = %q, want the two-not-ready shape", ep.Detail)
	}
	if strings.Contains(ep.Detail, "matches nothing") {
		t.Error("reported the empty-selector branch for a Service that HAS backends")
	}
}

// The exemptions the rule already had must keep short-circuiting BEFORE any
// evidence is built — they are the shapes that legitimately have no endpoints.
func TestServiceNoEndpoints_ExemptionsStillFireNothing(t *testing.T) {
	cases := map[string]*corev1.Service{
		"ExternalName": svcWith(func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeExternalName }),
		"headless":     svcWith(func(s *corev1.Service) { s.Spec.ClusterIP = corev1.ClusterIPNone }),
		"selectorless": svcWith(func(s *corev1.Service) { s.Spec.Selector = nil }),
	}
	for name, svc := range cases {
		if got := serviceNoEndpointsRule().Evaluate(&ClusterState{Services: []*corev1.Service{svc}}); len(got) != 0 {
			t.Errorf("%s produced %d insights, want 0", name, len(got))
		}
	}
}

func TestHumanAge(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:    "moments",
		7 * time.Minute:     "7m",
		5 * time.Hour:       "5h",
		47 * time.Hour:      "47h",
		34 * 24 * time.Hour: "34d",
	}
	for d, want := range cases {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%v) = %q, want %q", d, got, want)
		}
	}
}
