package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

func issueTokenFixture(t *testing.T) (*handlers, *auth.BoltIngestTokenStore) {
	t.Helper()
	store, its, _ := newTenantsStoreWithToken(t)
	return &handlers{tenantsStore: store, ingestTokens: its}, its
}

func issueTokenResponse(t *testing.T, h *handlers, body string) agentIssueTokenResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleAgentIssueToken(rec, agentWizardRequest(http.MethodPost, "/integrations/agent/issue-token", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp agentIssueTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// The wizards' copy path (onboarding, add cluster) issues a token that stops
// working if no agent uses it within a week, and says so in the response so
// the wizard can show it.
func TestIssueToken_TheCopiedTokenHasAUseByDate(t *testing.T) {
	h, its := issueTokenFixture(t)
	before := time.Now()
	resp := issueTokenResponse(t, h, `{"label":"first-cluster"}`)
	tok, err := its.Lookup(context.Background(), resp.Token)
	if err != nil {
		t.Fatal(err)
	}
	if tok.UnusedExpiresAt == nil {
		t.Fatal("the copied token has no use-by date: an abandoned onboarding leaves a credential that works forever")
	}
	if d := tok.UnusedExpiresAt.Sub(before); d < wizardTokenUseBy-time.Minute || d > wizardTokenUseBy+time.Minute {
		t.Fatalf("use-by in %s, want ~%s", d, wizardTokenUseBy)
	}
	if tok.ExpiresAt != nil {
		t.Fatalf("hard expiry %v set without ttlSeconds", tok.ExpiresAt)
	}
	if got, err := time.Parse(time.RFC3339, resp.UnusedExpiresAt); err != nil || !got.Equal(tok.UnusedExpiresAt.Truncate(time.Second)) {
		t.Fatalf("response unusedExpiresAt = %q, want the token's %v", resp.UnusedExpiresAt, tok.UnusedExpiresAt)
	}
}

// ttlSeconds was declared on the request and ignored.
func TestIssueToken_HonoursTTLSeconds(t *testing.T) {
	h, its := issueTokenFixture(t)
	before := time.Now()
	resp := issueTokenResponse(t, h, `{"label":"x","ttlSeconds":3600}`)
	tok, err := its.Lookup(context.Background(), resp.Token)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ExpiresAt == nil {
		t.Fatal("ttlSeconds ignored: no expiry")
	}
	if d := tok.ExpiresAt.Sub(before); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("expires in %s, want ~1h", d)
	}
}

// A token the API writes straight into the connected cluster's Secret has no
// use-by date: the agent may read it only at its next restart.
func TestIssueWizardToken_AMaterializedTokenHasNoUseByDate(t *testing.T) {
	h, _ := issueTokenFixture(t)
	_, tok, err := h.issueWizardToken(context.Background(), "org-1", "", "configure", "admin", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if tok.UnusedExpiresAt != nil {
		t.Fatalf("materialized token got a use-by date %v", tok.UnusedExpiresAt)
	}
}
