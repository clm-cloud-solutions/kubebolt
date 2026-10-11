package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// The agent wizards (Finish setup, Add cluster, install, configure) issue the
// agent's token through these two handlers. A single-tenant install stamps the
// default org's NAME on every request; before sessionTenant the lookup by that
// name found nothing, auth-info listed no org and issue-token answered 404, so
// no wizard could issue a token on a stock install.

func agentWizardRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	// What ResolveTenant stamps on every request of a single-tenant install.
	return req.WithContext(auth.WithTenantID(req.Context(), auth.DefaultTenantName))
}

func TestAgentAuthInfo_SingleTenantListsTheDefaultOrg(t *testing.T) {
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its}
	wantID := fetchDefaultTenantID(t, h)

	rec := httptest.NewRecorder()
	h.handleAgentAuthInfo(rec, agentWizardRequest(http.MethodGet, "/integrations/agent/auth-info", ""))

	var out agentAuthInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(out.Tenants) != 1 || out.Tenants[0].ID != wantID {
		t.Fatalf("tenants = %+v, want only the default org %s", out.Tenants, wantID)
	}
}

func TestAgentIssueToken_SingleTenantIssuesUnderTheDefaultOrg(t *testing.T) {
	store, its, _ := newTenantsStoreWithToken(t)
	h := &handlers{tenantsStore: store, ingestTokens: its}
	wantID := fetchDefaultTenantID(t, h)

	rec := httptest.NewRecorder()
	h.handleAgentIssueToken(rec, agentWizardRequest(http.MethodPost, "/integrations/agent/issue-token", `{"label":"wizard"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var resp agentIssueTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TenantID != wantID {
		t.Fatalf("tenantId = %q, want the default org's id %q", resp.TenantID, wantID)
	}
	// The agent authenticates by looking the token's org up by id: a token
	// stored under the name would be refused at the agent's first Hello.
	tok, err := its.Lookup(context.Background(), resp.Token)
	if err != nil {
		t.Fatalf("Lookup issued token: %v", err)
	}
	if tok.TenantID != wantID {
		t.Fatalf("stored token tenant = %q, want %q", tok.TenantID, wantID)
	}
	if _, err := store.GetTenant(tok.TenantID); err != nil {
		t.Fatalf("token's org does not resolve: %v", err)
	}
}

func TestAgentWizard_MultiTenantNeverFallsBackToTheDefaultOrg(t *testing.T) {
	store, its, _ := newTenantsStoreWithToken(t)
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = true
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })
	h := &handlers{tenantsStore: store, ingestTokens: its}

	rec := httptest.NewRecorder()
	h.handleAgentAuthInfo(rec, agentWizardRequest(http.MethodGet, "/integrations/agent/auth-info", ""))
	var out agentAuthInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tenants) != 0 {
		t.Fatalf("tenants = %+v, want none for a session without its org", out.Tenants)
	}

	rec = httptest.NewRecorder()
	h.handleAgentIssueToken(rec, agentWizardRequest(http.MethodPost, "/integrations/agent/issue-token", `{}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}
