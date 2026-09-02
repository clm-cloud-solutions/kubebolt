package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
)

// falcoTokens is the smallest IngestTokenStore that satisfies the door.
type falcoTokens struct{ tok *auth.IngestToken }

func (f *falcoTokens) Issue(context.Context, string, string, string, string, string, *time.Duration) (string, *auth.IngestToken, error) {
	return "", nil, nil
}
func (f *falcoTokens) Revoke(context.Context, string, string) error { return nil }
func (f *falcoTokens) Rotate(context.Context, string, string, string) (string, *auth.IngestToken, error) {
	return "", nil, nil
}
func (f *falcoTokens) Lookup(context.Context, string) (*auth.IngestToken, error) { return f.tok, nil }
func (f *falcoTokens) MarkUsed(context.Context, string, string, time.Time) error { return nil }
func (f *falcoTokens) ListByTenant(context.Context, string) ([]auth.IngestToken, error) {
	return nil, nil
}

// memEvents records what actually reached the store, so a test can assert that
// a rejected request wrote NOTHING — a 403 that still persisted the event would
// be the worst of both.
type memEvents struct{ appended []findings.EventRecord }

func (m *memEvents) Append(rec *findings.EventRecord) error {
	m.appended = append(m.appended, *rec)
	return nil
}
func (m *memEvents) ListEvents(findings.EventQuery) ([]findings.EventRecord, error) {
	return m.appended, nil
}
func (m *memEvents) PruneEvents(time.Time) (int, error)            { return 0, nil }
func (m *memEvents) PruneEventsOrg(string, time.Time) (int, error) { return 0, nil }

func falcoPost(t *testing.T, tokenCluster, body string) (*httptest.ResponseRecorder, *memEvents) {
	t.Helper()
	events := &memEvents{}
	h := &handlers{
		eventStore:   events,
		ingestTokens: &falcoTokens{tok: &auth.IngestToken{ID: "t1", TenantID: "org-a", ClusterID: tokenCluster}},
		tenantsStore: &fakeTenantStore{tenant: &auth.Tenant{ID: "org-a"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/falco", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer kb_whatever")
	rr := httptest.NewRecorder()
	h.handleFalcoIngest(rr, r)
	return rr, events
}

const falcoBody = `{"rule":"Read sensitive file untrusted","priority":"Warning",` +
	`"output":"Sensitive file opened","output_fields":{"k8s.ns.name":"prod","k8s.pod.name":"web-1"}}`

// Falco has no handshake: the token is the only identity a pushed event carries.
// An unscoped one produces alerts nobody can route to a cluster.
func TestFalcoIngest_RejectsAnUnscopedToken(t *testing.T) {
	rr, events := falcoPost(t, "", falcoBody)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — an unscoped token cannot attribute its events", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "cluster-scoped") {
		t.Errorf("the error must say what to do, got %q", rr.Body.String())
	}
	if len(events.appended) != 0 {
		t.Errorf("a rejected request stored %d events, want 0", len(events.appended))
	}
}

func TestFalcoIngest_AcceptsAScopedToken(t *testing.T) {
	rr, events := falcoPost(t, "cluster-a", falcoBody)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body=%s)", rr.Code, rr.Body)
	}
	if len(events.appended) != 1 {
		t.Fatalf("stored %d events, want 1", len(events.appended))
	}
	// Identity comes from the TOKEN, never the payload — the anti-spoof stance.
	if got := events.appended[0].ClusterID; got != "cluster-a" {
		t.Errorf("ClusterID = %q, want cluster-a", got)
	}
	if got := events.appended[0].TenantID; got != "org-a" {
		t.Errorf("TenantID = %q, want org-a", got)
	}
}

// Mirrors Sec #13 on the agent door. This does NOT stop an attacker — holding
// cluster A's token they would assert A — it catches pasting one cluster's token
// into another cluster's Falco values, which otherwise files B's events under A
// in silence.
func TestFalcoIngest_RejectsAContradictingClusterAssertion(t *testing.T) {
	body := `{"rule":"r","priority":"Warning","output":"o","output_fields":{"cluster_id":"cluster-b"}}`
	rr, events := falcoPost(t, "cluster-a", body)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 on a cluster the token does not allow", rr.Code)
	}
	if len(events.appended) != 0 {
		t.Errorf("a refused assertion stored %d events, want 0", len(events.appended))
	}
}

// The assertion is optional and agreeing is fine — an install that has not set
// falcosidekick's customfields must keep working, same as an agent with an empty
// ClusterHint.
func TestFalcoIngest_AgreeingOrAbsentAssertionIsAccepted(t *testing.T) {
	agree := `{"rule":"r","priority":"Warning","output":"o","output_fields":{"cluster_id":"cluster-a"}}`
	if rr, _ := falcoPost(t, "cluster-a", agree); rr.Code != http.StatusAccepted {
		t.Errorf("matching assertion: status = %d, want 202", rr.Code)
	}
	if rr, _ := falcoPost(t, "cluster-a", falcoBody); rr.Code != http.StatusAccepted {
		t.Errorf("absent assertion: status = %d, want 202", rr.Code)
	}
}

// fakeTenantStore answers GetTenant with a fixed tenant. EE declares it next to
// the agent-ingest-gate capping tests (plan machinery OSS does not carry); the
// Falco ingest only needs the one method.
type fakeTenantStore struct {
	auth.TenantStore
	tenant *auth.Tenant
}

func (f *fakeTenantStore) GetTenant(string) (*auth.Tenant, error) { return f.tenant, nil }
