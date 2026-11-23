package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// The access token travels in Sec-WebSocket-Protocol, not in the URL, and the
// server answers with the fixed protocol: the token is never echoed back. The
// URL token still works for web bundles cached before this change.
func TestWebSocket_TokenTravelsInTheSubprotocol(t *testing.T) {
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	authCfg := config.AuthConfig{Enabled: true, JWTSecret: []byte("ws-token-test-secret-0123456789abcdef"),
		AccessTokenExpiry: 15 * time.Minute, RefreshTokenExpiry: time.Hour}
	jwtSvc := auth.NewJWTService(authCfg)
	token, err := jwtSvc.GenerateAccessToken(&auth.User{ID: "u-1", Username: "alice", Role: auth.RoleViewer})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	h := &handlers{authHandlers: auth.NewHandlers(store, jwtSvc, authCfg), wsHub: hub}
	srv := httptest.NewServer(http.HandlerFunc(h.handleWebSocket))
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	bearer := auth.WSBearerPrefix + base64.RawURLEncoding.EncodeToString([]byte(token))

	d := gorilla.Dialer{Subprotocols: []string{auth.WSProtocol, bearer}}
	conn, resp, err := d.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("upgrade with the token in the subprotocol: %v (status %v)", err, statusOf(resp))
	}
	conn.Close()
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != auth.WSProtocol {
		t.Errorf("server selected %q, want %q", got, auth.WSProtocol)
	}
	for name, vals := range resp.Header {
		for _, v := range vals {
			if strings.Contains(v, token) || strings.Contains(v, auth.WSBearerPrefix) {
				t.Errorf("the response echoes the token in %s", name)
			}
		}
	}

	if _, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil); err == nil || statusOf(resp) != http.StatusUnauthorized {
		t.Errorf("no token: err=%v status=%v, want 401", err, statusOf(resp))
	}

	legacy, _, err := gorilla.DefaultDialer.Dial(wsURL+"?token="+token, nil)
	if err != nil {
		t.Fatalf("the deprecated URL token must keep working for cached web bundles: %v", err)
	}
	legacy.Close()

	// The terminal reads the token the same way. A viewer is refused for its
	// role (403), which proves the token was read: without it the answer is 401.
	execSrv := httptest.NewServer(http.HandlerFunc(h.handleExec))
	t.Cleanup(execSrv.Close)
	execURL := "ws" + strings.TrimPrefix(execSrv.URL, "http") + "/ws/exec/default/pod-a"
	if _, resp, _ := d.Dial(execURL, nil); statusOf(resp) != http.StatusForbidden {
		t.Errorf("exec with the token in the subprotocol: status %d, want 403 (viewer, token read)", statusOf(resp))
	}
}

func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}
