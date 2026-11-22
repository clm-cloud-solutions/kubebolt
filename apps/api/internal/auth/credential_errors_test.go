package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A token store that does not answer is not a token that is bad. A Postgres
// restart used to answer every API key with 401 "invalid or expired token":
// the caller (Autopilot, a script, an MCP client) read its key as rejected.

func TestIsCredentialRejection(t *testing.T) {
	for _, err := range []error{
		ErrTokenMalformed, ErrTokenNotFound, ErrTokenRevoked, ErrTokenExpired,
		ErrTenantDisabled, ErrTenantNotFound, ErrTokenEdgeBlocked, errAPITokensUnavailable,
		ErrMissingToken, ErrTokenInvalid, ErrTLSRequired, ErrUnknownMode,
		fmt.Errorf("lookup: %w", ErrTokenRevoked),
	} {
		if !IsCredentialRejection(err) {
			t.Errorf("%v must read as a rejection of the credential", err)
		}
	}
	for _, err := range []error{
		errors.New("dial tcp 10.0.0.5:5432: connect: connection refused"),
		context.DeadlineExceeded,
		nil,
	} {
		if IsCredentialRejection(err) {
			t.Errorf("%v is a failure to check the credential, not a rejection", err)
		}
	}
}

// storeDown answers every lookup with a connection error.
type storeDown struct{ *APITokenStore }

func (storeDown) Lookup(context.Context, string) (*APIToken, error) {
	return nil, errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")
}

func TestRequireAuth_APIToken_StoreDownIs503(t *testing.T) {
	s := newTestAPIStore(t)
	plaintext, _, err := s.Issue(context.Background(), TokenTypeAPIKey, RoleViewer, []string{ScopeAll}, "ci", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

	down := &Handlers{authEnabled: true}
	down.SetAPITokenStore(storeDown{s})
	rr := httptest.NewRecorder()
	down.RequireAuth(next).ServeHTTP(rr, bearerReq(plaintext, ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("store down: status %d, want 503 (body %s)", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("a 503 for an unavailable store must say when to retry")
	}
	if reached {
		t.Error("the request reached the handler without a verified token")
	}

	// A key the store does not know is still a 401.
	up := &Handlers{authEnabled: true}
	up.SetAPITokenStore(s)
	rr = httptest.NewRecorder()
	up.RequireAuth(next).ServeHTTP(rr, bearerReq(APIKeyTokenPrefix+"0000000000000000000000000000000000000000", ""))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unknown key: status %d, want 401", rr.Code)
	}
}
