package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// A container that restarted since the step was applied counts; one that
// restarted before does not. OOMKilled is said either way it shows.
func TestPodTroubleSince(t *testing.T) {
	applied := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	term := func(reason string, at time.Time) *corev1.ContainerStateTerminated {
		return &corev1.ContainerStateTerminated{Reason: reason, FinishedAt: metav1.NewTime(at)}
	}
	pod := func(statuses ...corev1.ContainerStatus) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: statuses}}
	}
	before := pod(corev1.ContainerStatus{LastTerminationState: corev1.ContainerState{Terminated: term("OOMKilled", applied.Add(-time.Hour))}})
	if oom, n := podTroubleSince([]*corev1.Pod{before}, applied); oom || n != 0 {
		t.Fatalf("before: oom=%v restarts=%d", oom, n)
	}
	after := pod(
		corev1.ContainerStatus{LastTerminationState: corev1.ContainerState{Terminated: term("OOMKilled", applied.Add(time.Hour))}},
		corev1.ContainerStatus{LastTerminationState: corev1.ContainerState{Terminated: term("Error", applied.Add(2*time.Hour))}},
	)
	if oom, n := podTroubleSince([]*corev1.Pod{before, after}, applied); !oom || n != 2 {
		t.Fatalf("after: oom=%v restarts=%d", oom, n)
	}
}

func TestPromWindow(t *testing.T) {
	cases := map[time.Duration]string{
		7 * 24 * time.Hour: "[604800s:1h]",
		24 * time.Hour:     "[86400s:1h]",
		10 * time.Minute:   "[600s:1m]",
		time.Minute:        "[300s:1m]", // never under five minutes
	}
	for d, want := range cases {
		if got := promWindow(d); got != want {
			t.Errorf("%s = %s, want %s", d, got, want)
		}
	}
}

// The workload's cost after is the same allocation the baseline averages.
func TestWorkloadAllocationExpr(t *testing.T) {
	tg := kobiplan.Target{Type: "deployments", Namespace: "kube-system", Name: "coredns"}
	q := workloadAllocationExpr(tg)
	if !strings.Contains(q, `container_cpu_allocation{namespace="kube-system",pod=~"coredns-[a-z0-9]{6,12}-[a-z0-9]{5}"}`) || strings.Contains(q, "avg_over_time") {
		t.Fatalf("expr = %s", q)
	}
	if !strings.Contains(workloadAllocationQuery(tg), workloadAllocationExpr(tg)[:40]) {
		t.Fatal("the baseline and the cost after read different allocations")
	}
}

// Verify now needs the plan's cluster: without it, it says so.
func TestVerifyPlan_ClusterNotConnected(t *testing.T) {
	now := time.Now().UTC()
	p := routePlan("pln_a", "c-dev", now, kobiplan.RiskLow)
	p.RecordExport(kobiplan.ExportCommands, "ana", now, nil, nil)
	withPlanStore(t, p)
	code, body, _ := planPostRequest(t, "/plans/pln_a/verify", (&handlers{}).handleVerifyPlan, "/plans/{id}/verify")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "not connected") {
		t.Fatalf("%d %s", code, body)
	}
}

// A draft past its expiry is marked expired by the sweep's step, with or
// without its cluster.
func TestVerifyPlan_ExpiresAnUnexportedDraft(t *testing.T) {
	now := time.Now().UTC()
	withPlanStore(t, routePlan("pln_old", "c-dev", now.Add(-8*24*time.Hour), kobiplan.RiskLow))
	got, err := (&handlers{}).verifyPlan(t.Context(), kobiPlanStore(), "org-1", nil, "pln_old", now)
	if err != nil || got.Status != kobiplan.StatusExpired || got.ExpiredReason != kobiplan.ExpiredUnexported {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// planPostRequest serves one POST through a plan route with the caller's org
// and an open cluster scope, as the middleware leaves them.
func planPostRequest(t *testing.T, path string, handler http.HandlerFunc, pattern string) (int, string, planView) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithTenantID(req.Context(), "org-1")
			ctx = context.WithValue(ctx, clusterScopeKey, ClusterScope{})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Post(pattern, handler)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	var out planView
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, rec.Body.String(), out
}
