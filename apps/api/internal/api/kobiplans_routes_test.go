package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	bolt "go.etcd.io/bbolt"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

func routePlan(id, cluster string, created time.Time, risk kobiplan.RiskLevel) kobiplan.Plan {
	p := kobiplan.Plan{
		ID: id, OrgID: "org-1", ClusterID: cluster, Title: "Right-sizing " + id,
		Kind: kobiplan.KindRightsizing, Version: 1, Status: kobiplan.StatusDraft,
		Scope:     kobiplan.Scope{Namespaces: []string{}, Workloads: []string{}},
		CreatedAt: created, ExpiresAt: created.Add(kobiplan.PlanTTL),
		Source:  kobiplan.Source{Initiator: kobiplan.InitiatorChat, UserID: "u-1", UserName: "ana", ConversationID: "conv-1"},
		Exports: []kobiplan.Export{},
		Engine:  kobiplan.Engine{Name: "rightsizing", Percentile: 0.95, ComputedAt: created},
		Impact:  kobiplan.Impact{Estimated: kobiplan.Savings{CPUMilli: 90, MoneyUnavailable: kobiplan.MoneyNoCostData}},
		Steps: []kobiplan.Step{{
			ID: "s1", Order: 1, Action: kobiplan.ActionSetResources,
			Target:       kobiplan.Target{Type: "deployments", Namespace: "kyverno", Name: "cleanup", Owner: &kobiplan.Owner{Kind: kobiplan.OwnerHelm, Ref: "kyverno"}},
			Params:       json.RawMessage(`{"containers":[{"container":"controller","requests":{"cpu":"10m"}}]}`),
			Precondition: kobiplan.Precondition{SpecHash: "sha256:abc"},
			Risk:         risk, RiskReasons: []string{}, Status: kobiplan.StepPending,
		}},
		Excluded: []kobiplan.Exclusion{},
		Notes:    []kobiplan.Note{},
	}
	return p
}

func withPlanStore(t *testing.T, plans ...kobiplan.Plan) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "plans.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := copilot.NewBoltPlanStore(db, []byte("kobi_plans"), []byte("kobi_plans_versions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if err := store.Create(p); err != nil {
			t.Fatal(err)
		}
	}
	prev := planStorePtr.Load()
	SetKobiPlanStore(store)
	t.Cleanup(func() { planStorePtr.Store(prev) })
}

// planRequest serves one request through the plan routes with the caller's
// org and cluster scope already resolved, as the middleware leaves them.
func planRequest(t *testing.T, path string, scope ClusterScope) (int, map[string]any) {
	t.Helper()
	return planRequestWith(t, &handlers{}, path, scope)
}

func planRequestWith(t *testing.T, h *handlers, path string, scope ClusterScope) (int, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithTenantID(req.Context(), "org-1")
			ctx = context.WithValue(ctx, clusterScopeKey, scope)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Get("/plans", h.handleListPlans)
	r.Get("/plans/{id}", h.handleGetPlan)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func narrowedTo(ids ...string) ClusterScope {
	s := ClusterScope{narrowed: true, allowed: map[string]bool{}}
	for _, id := range ids {
		s.allowed[id] = true
	}
	return s
}

func TestGetPlan(t *testing.T) {
	now := time.Now().UTC()
	withPlanStore(t,
		routePlan("pln_a", "c-dev", now.Add(-time.Hour), kobiplan.RiskMedium),
		routePlan("pln_old", "c-dev", now.Add(-8*24*time.Hour), kobiplan.RiskLow),
	)

	code, body := planRequest(t, "/plans/pln_a", ClusterScope{})
	if code != http.StatusOK || body["id"] != "pln_a" || body["effectiveStatus"] != "draft" || body["maxRisk"] != "medium" {
		t.Fatalf("GET pln_a = %d %v", code, body)
	}
	if src := body["source"].(map[string]any); src["userName"] != "ana" {
		t.Errorf("source = %v, want the creator's name", src)
	}

	// A draft past its expiry reads expired before the sweep marks it.
	_, old := planRequest(t, "/plans/pln_old", ClusterScope{})
	if old["status"] != "draft" || old["effectiveStatus"] != "expired" || old["effectiveExpiredReason"] != "unexported" {
		t.Errorf("an expired draft = status %v effective %v %v", old["status"], old["effectiveStatus"], old["effectiveExpiredReason"])
	}

	if code, _ := planRequest(t, "/plans/pln_nope", ClusterScope{}); code != http.StatusNotFound {
		t.Errorf("an unknown plan = %d, want 404", code)
	}
	// A plan of a cluster the caller may not read is not found, not forbidden.
	if code, _ := planRequest(t, "/plans/pln_a", narrowedTo("c-other")); code != http.StatusNotFound {
		t.Errorf("another team's plan = %d, want 404", code)
	}
	if code, _ := planRequest(t, "/plans/pln_a", narrowedTo("c-dev")); code != http.StatusOK {
		t.Errorf("an entitled plan = %d, want 200", code)
	}
}

func TestListPlans(t *testing.T) {
	now := time.Now().UTC()
	lab := routePlan("pln_lab", "c-lab", now.Add(-time.Hour), kobiplan.RiskHigh)
	lab.Steps[0].Status = kobiplan.StepApplied
	withPlanStore(t,
		routePlan("pln_dev", "c-dev", now.Add(-2*time.Hour), kobiplan.RiskLow),
		lab,
	)
	ids := func(body map[string]any) []string {
		var out []string
		for _, p := range body["plans"].([]any) {
			out = append(out, p.(map[string]any)["id"].(string))
		}
		return out
	}

	_, all := planRequest(t, "/plans", ClusterScope{})
	if got := ids(all); len(got) != 2 {
		t.Fatalf("all plans = %v", got)
	}
	first := all["plans"].([]any)[0].(map[string]any)
	if first["stepCount"] != float64(1) || first["createdBy"] != "ana" || first["maxRisk"] == nil {
		t.Errorf("summary = %v", first)
	}
	// The list tells a plan under way from one untouched.
	applied := map[string]any{}
	for _, p := range all["plans"].([]any) {
		applied[p.(map[string]any)["id"].(string)] = p.(map[string]any)["appliedCount"]
	}
	if applied["pln_lab"] != float64(1) || applied["pln_dev"] != float64(0) {
		t.Errorf("applied counts = %v", applied)
	}

	if got := ids(func() map[string]any {
		_, b := planRequest(t, "/plans?cluster=c-lab", ClusterScope{requested: "c-lab"})
		return b
	}()); len(got) != 1 || got[0] != "pln_lab" {
		t.Errorf("?cluster=c-lab = %v", got)
	}
	if got := ids(func() map[string]any { _, b := planRequest(t, "/plans", narrowedTo("c-dev")); return b }()); len(got) != 1 || got[0] != "pln_dev" {
		t.Errorf("a narrowed caller = %v, want only c-dev's", got)
	}
	if _, b := planRequest(t, "/plans", narrowedTo()); len(b["plans"].([]any)) != 0 {
		t.Errorf("a caller entitled to nothing listed %v", b["plans"])
	}
	if code, _ := planRequest(t, "/plans?status=bogus", ClusterScope{}); code != http.StatusBadRequest {
		t.Errorf("an unknown status = %d, want 400", code)
	}
}

// A plan names its author by the display name they have today; a person no
// longer in the store keeps the username the plan recorded.
func TestPlanAuthorIsTheDisplayName(t *testing.T) {
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	u, err := store.CreateUser(context.Background(), "ana", "ana@example.com", "Ana Pérez", "password-123", auth.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.AuthConfig{Enabled: true, JWTSecret: []byte("plan-author-test-secret-32-bytes!!"), AccessTokenExpiry: time.Minute, RefreshTokenExpiry: time.Hour}
	h := &handlers{authHandlers: auth.NewHandlers(store, auth.NewJWTService(cfg), cfg)}

	now := time.Now().UTC()
	mine := routePlan("pln_ana", "c-dev", now, kobiplan.RiskLow)
	mine.Source.UserID = u.ID
	gone := routePlan("pln_gone", "c-dev", now.Add(-time.Minute), kobiplan.RiskLow)
	gone.Source.UserID, gone.Source.UserName = "u-removed", "old-user"
	withPlanStore(t, mine, gone)

	if _, body := planRequestWith(t, h, "/plans/pln_ana", ClusterScope{}); body["createdBy"] != "Ana Pérez" {
		t.Fatalf("detail createdBy = %v, want the display name", body["createdBy"])
	}
	_, list := planRequestWith(t, h, "/plans", ClusterScope{})
	by := map[string]any{}
	for _, p := range list["plans"].([]any) {
		row := p.(map[string]any)
		by[row["id"].(string)] = row["createdBy"]
	}
	if by["pln_ana"] != "Ana Pérez" || by["pln_gone"] != "old-user" {
		t.Fatalf("list createdBy = %v", by)
	}
}
