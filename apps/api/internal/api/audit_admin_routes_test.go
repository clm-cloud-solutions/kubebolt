package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// Records land under auth.DefaultTenantName, not "", because ContextTenantID
// never returns an empty string — an unauthenticated or single-tenant request
// resolves to "default". Reads go through the same helper, so the two agree;
// asserting on "" here would be testing a value the product never writes.

// mountAuditedAdmin wires a chi router whose /settings subtree is wrapped by the
// audit middleware, backed by an in-memory sink.
func mountAuditedAdmin(t *testing.T, status int) (*chi.Mux, *audit.MemoryStore) {
	t.Helper()
	sink := audit.NewMemoryStore()
	audit.SetSink(sink, func() string { return "cluster-xyz" })
	t.Cleanup(func() { audit.SetSink(nil, nil) })

	h := &handlers{}
	r := chi.NewRouter()
	r.Route("/settings", func(r chi.Router) {
		r.Use(h.auditAdminRoutes("settings", false))
		r.Get("/copilot", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Put("/copilot", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
	})
	return r, sink
}

func TestAuditAdminRoutes_RecordsMutations(t *testing.T) {
	r, sink := mountAuditedAdmin(t, http.StatusOK)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/settings/copilot", nil))

	recs, err := sink.ListOrg(auth.DefaultTenantName, 0)
	if err != nil {
		t.Fatalf("ListOrg: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d audit records, want 1", len(recs))
	}
	got := recs[0]
	if got.Action != "settings_update" {
		t.Errorf("action = %q, want settings_update", got.Action)
	}
	// The section, not the collection — this is what makes the row readable.
	if got.TargetName != "copilot" {
		t.Errorf("targetName = %q, want copilot", got.TargetName)
	}
	if got.Result != "success" {
		t.Errorf("result = %q, want success", got.Result)
	}
	// Settings belong to the install; stamping the selected cluster would
	// invent an association that reads as evidence later.
	if got.ClusterID != "" {
		t.Errorf("clusterId = %q, want empty for install-scoped settings", got.ClusterID)
	}
}

// A read must not produce a record. Otherwise every poll of the settings page
// buries the mutations the trail exists to surface.
func TestAuditAdminRoutes_IgnoresReads(t *testing.T) {
	r, sink := mountAuditedAdmin(t, http.StatusOK)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings/copilot", nil))

	if recs, _ := sink.ListOrg(auth.DefaultTenantName, 0); len(recs) != 0 {
		t.Fatalf("a GET produced %d audit records, want 0", len(recs))
	}
}

// A rejected change must not read like an applied one — the record is written
// after the handler precisely so the outcome is observed rather than assumed.
func TestAuditAdminRoutes_RecordsFailureAsError(t *testing.T) {
	r, sink := mountAuditedAdmin(t, http.StatusForbidden)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/settings/copilot", nil))

	recs, _ := sink.ListOrg(auth.DefaultTenantName, 0)
	if len(recs) != 1 {
		t.Fatalf("got %d audit records, want 1", len(recs))
	}
	if recs[0].Result != "error" {
		t.Errorf("result = %q, want error", recs[0].Result)
	}
	if recs[0].Params["status"] != http.StatusForbidden {
		t.Errorf("status param = %v, want 403", recs[0].Params["status"])
	}
}
