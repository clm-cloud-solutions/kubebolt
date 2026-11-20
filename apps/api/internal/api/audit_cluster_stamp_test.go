package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// The audit trail resolved its cluster with context.Background(), which names
// the install's default cluster rather than the one the request acted on. The
// resolver now receives the request's context. These tests wire a resolver
// that answers only from the context it is given: a value that reaches the
// record proves the request's context got there.

type stampClusterKey struct{}

func withStampCluster(r *http.Request, cluster string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), stampClusterKey{}, cluster))
}

func newStampSink(t *testing.T) *audit.MemoryStore {
	t.Helper()
	sink := audit.NewMemoryStore()
	audit.SetSink(sink, func(ctx context.Context) string {
		v, _ := ctx.Value(stampClusterKey{}).(string)
		return v
	})
	t.Cleanup(func() { audit.SetSink(nil, nil) })
	return sink
}

func onlyRecord(t *testing.T, sink *audit.MemoryStore) audit.Record {
	t.Helper()
	recs, err := sink.ListOrg(auth.DefaultTenantName, 0)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records: %d, err %v", len(recs), err)
	}
	return recs[0]
}

func TestAuditMutation_StampsTheClusterTheRequestResolved(t *testing.T) {
	sink := newStampSink(t)
	r := withStampCluster(httptest.NewRequest(http.MethodPost, "/api/v1/resources/deployments/shop/web/restart", nil), "uid-org-b")

	auditMutation(r, "restart", "deployments", "shop", "web", nil, nil)

	if got := onlyRecord(t, sink).ClusterID; got != "uid-org-b" {
		t.Errorf("clusterId = %q, want uid-org-b (the request's cluster)", got)
	}
}

func TestAuditMutation_RequestWithoutAClusterRecordsNone(t *testing.T) {
	sink := newStampSink(t)

	auditMutation(httptest.NewRequest(http.MethodPost, "/x", nil), "restart", "deployments", "shop", "web", nil, errors.New("boom"))

	if got := onlyRecord(t, sink).ClusterID; got != "" {
		t.Errorf("clusterId = %q, want empty", got)
	}
}

// A mutation whose subject is a named cluster (a mute) records that cluster,
// not the one the person had selected.
func TestAuditMutationOn_RecordsTheClusterItActsOn(t *testing.T) {
	sink := newStampSink(t)
	r := withStampCluster(httptest.NewRequest(http.MethodPut, "/x", nil), "uid-selected")

	auditMutationOn(r, "uid-named", "mute", "insight-mute", "", "rule resource", nil, nil)

	if got := onlyRecord(t, sink).ClusterID; got != "uid-named" {
		t.Errorf("clusterId = %q, want uid-named", got)
	}
}

// A change the request cannot tie to a cluster (an unmute by id) records none
// rather than the selected one.
func TestAuditMutationOn_EmptyMeansNoCluster(t *testing.T) {
	sink := newStampSink(t)
	r := withStampCluster(httptest.NewRequest(http.MethodPost, "/x", nil), "uid-selected")

	auditMutationOn(r, "", "unmute", "insight-mute", "", "mute-1", nil, nil)

	if got := onlyRecord(t, sink).ClusterID; got != "" {
		t.Errorf("clusterId = %q, want empty", got)
	}
}

func TestAuditAdminRoutes_ClusterScopedStampsTheRequestsCluster(t *testing.T) {
	sink := newStampSink(t)
	h := &handlers{}
	r := chi.NewRouter()
	r.Route("/integrations", func(r chi.Router) {
		r.Use(h.auditAdminRoutes("integration", true))
		r.Post("/{id}/install", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	r.ServeHTTP(httptest.NewRecorder(), withStampCluster(httptest.NewRequest(http.MethodPost, "/integrations/trivy/install", nil), "uid-org-b"))

	if got := onlyRecord(t, sink).ClusterID; got != "uid-org-b" {
		t.Errorf("clusterId = %q, want uid-org-b", got)
	}
}

// An access the handler could not tie to a cluster records none. It used to
// fall back to the background resolver: the install's default cluster.
func TestAccessRecord_WithoutAResolvedClusterRecordsNone(t *testing.T) {
	newStampSink(t)
	actor := accessActor{UserID: "u1", Username: "alice", Role: "admin", TenantID: "org-a"}

	if got := actor.record("pod_file_read", "pods", "shop", "web-0", nil).ClusterID; got != "" {
		t.Errorf("clusterId = %q, want empty", got)
	}
	if got := actor.withCluster("uid-reached").record("pod_file_read", "pods", "shop", "web-0", nil).ClusterID; got != "uid-reached" {
		t.Errorf("clusterId = %q, want uid-reached", got)
	}
}
