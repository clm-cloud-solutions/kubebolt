package api

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestParseQuotaError(t *testing.T) {
	msg := `pods "demo-web" is forbidden: exceeded quota: demo-tight, requested: limits.cpu=200m, used: limits.cpu=1050m, limited: limits.cpu=1200m`
	q := parseQuotaError(msg)
	if q == nil {
		t.Fatal("expected a quotaDetail, got nil")
	}
	if q.Name != "demo-tight" {
		t.Errorf("name = %q, want demo-tight", q.Name)
	}
	if q.Requested != "limits.cpu=200m" {
		t.Errorf("requested = %q", q.Requested)
	}
	if q.Used != "limits.cpu=1050m" {
		t.Errorf("used = %q", q.Used)
	}
	if q.Limited != "limits.cpu=1200m" {
		t.Errorf("limited = %q", q.Limited)
	}
}

func TestParseQuotaError_NotQuota(t *testing.T) {
	if q := parseQuotaError("some other forbidden message"); q != nil {
		t.Errorf("expected nil for non-quota message, got %+v", q)
	}
}

func TestHumanizeMutationError(t *testing.T) {
	// Quota forbidden → structured detail + clean headline.
	quotaErr := apierrors.NewForbidden(
		schema.GroupResource{Resource: "pods"}, "demo-web",
		errors.New("exceeded quota: demo-tight, requested: limits.cpu=200m, used: limits.cpu=1050m, limited: limits.cpu=1200m"),
	)
	msg, q := humanizeMutationError(quotaErr)
	if q == nil || q.Name != "demo-tight" {
		t.Fatalf("expected quota detail demo-tight, got %+v", q)
	}
	if msg != `ResourceQuota "demo-tight" exceeded` {
		t.Errorf("message = %q", msg)
	}

	// NotFound → friendly line, no quota.
	nfMsg, nfQ := humanizeMutationError(apierrors.NewNotFound(schema.GroupResource{Resource: "deployments"}, "gone"))
	if nfQ != nil {
		t.Errorf("not-found should have no quota detail")
	}
	if nfMsg == "" {
		t.Error("not-found should produce a message")
	}

	// nil → empty.
	if m, _ := humanizeMutationError(nil); m != "" {
		t.Errorf("nil err should give empty message, got %q", m)
	}
}

func TestPodCreateDeniedByRBAC(t *testing.T) {
	// RBAC denial of the dry-run pod create — inconclusive, must NOT false-reject.
	rbac := apierrors.NewForbidden(
		schema.GroupResource{Resource: "pods"}, "",
		errors.New(`User "system:serviceaccount:demo:kobi" cannot create resource "pods" in API group "" in the namespace "demo"`),
	)
	if !podCreateDeniedByRBAC(rbac) {
		t.Error("expected RBAC pod-create denial to be detected")
	}
	// Quota forbidden is a REAL rejection — must NOT be treated as RBAC.
	quota := apierrors.NewForbidden(
		schema.GroupResource{Resource: "pods"}, "x",
		errors.New("exceeded quota: demo-tight, requested: limits.cpu=200m, used: limits.cpu=1050m, limited: limits.cpu=1200m"),
	)
	if podCreateDeniedByRBAC(quota) {
		t.Error("quota rejection must NOT be classified as RBAC")
	}
	if podCreateDeniedByRBAC(nil) {
		t.Error("nil err is not an RBAC denial")
	}
}

func TestDryRunAll(t *testing.T) {
	if got := dryRunAll(true); len(got) != 1 || got[0] != "All" {
		t.Errorf("dryRunAll(true) = %v, want [All]", got)
	}
	if got := dryRunAll(false); got != nil {
		t.Errorf("dryRunAll(false) = %v, want nil", got)
	}
}

// keep metav1 import used in case future cases need it
var _ = metav1.PatchOptions{}
