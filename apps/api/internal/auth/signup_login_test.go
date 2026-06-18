package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// newSignupHandlers builds an org/team-wired Handlers with a real JWT service,
// the way main.go does for the public signup/login routes.
func newSignupHandlers(t *testing.T) *Handlers {
	t.Helper()
	s := newTestStore(t)
	tenants, err := NewTenantsStore(s.DB())
	if err != nil {
		t.Fatalf("NewTenantsStore: %v", err)
	}
	teams, err := NewTeamStore(s.DB())
	if err != nil {
		t.Fatalf("NewTeamStore: %v", err)
	}
	dt := ensureFixtureDefaultTenant(t, tenants)
	team, err := teams.EnsureDefaultTeam(context.Background(), dt.ID)
	if err != nil {
		t.Fatalf("EnsureDefaultTeam: %v", err)
	}
	cfg := config.AuthConfig{
		Enabled:            true,
		JWTSecret:          []byte("test-secret-test-secret-test-sec"),
		AccessTokenExpiry:  15 * time.Minute,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	h := NewHandlers(s, NewJWTService(cfg), cfg)
	h.SetOrgTeamContext(teams, tenants, dt.ID, team.ID)
	return h
}

func postJSON(t *testing.T, h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// --- Signup (OSS-safe gate + validation) ---

func TestSignup_RequiresEEWhenSingleOrg(t *testing.T) {
	withMultiTenant(t, false) // OSS / single-org
	h := newSignupHandlers(t)

	rec := postJSON(t, h.Signup, "/auth/signup",
		`{"orgName":"Acme","email":"x@acme.io","name":"X","password":"supersecret"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "requires_ee" {
		t.Fatalf("code = %q, want requires_ee — body=%s", body["code"], rec.Body.String())
	}
}

func TestSignup_Validation(t *testing.T) {
	withMultiTenant(t, true)
	h := newSignupHandlers(t)

	cases := map[string]string{
		"missing org":    `{"orgName":"","email":"a@b.io","name":"A","password":"supersecret"}`,
		"bad email":      `{"orgName":"Acme","email":"not-an-email","name":"A","password":"supersecret"}`,
		"short password": `{"orgName":"Acme","email":"a@b.io","name":"A","password":"short"}`,
		"malformed body": `{not json`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postJSON(t, h.Signup, "/auth/signup", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 — body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// --- Login (username path is OSS-safe) ---

func TestLogin_UsernamePathStillWorks(t *testing.T) {
	withMultiTenant(t, false)
	h := newSignupHandlers(t)

	// Create a username-only user directly (no email containing "@").
	if _, err := h.store.CreateUser(context.Background(), "alice", "", "Alice", "supersecret", RoleEditor); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rec := postJSON(t, h.Login, "/auth/login",
		`{"username":"alice","password":"supersecret"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("username login status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.User.Username != "alice" {
		t.Fatalf("user = %#v", resp.User)
	}
}
