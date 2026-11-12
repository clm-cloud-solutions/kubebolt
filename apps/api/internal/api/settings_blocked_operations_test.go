package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// debt-09: the platform tab's API — read the list with the connectors this
// install has, block an operation, refuse a malformed one naming its field.

func blockedOpsHandlers(t *testing.T) *handlers {
	t.Helper()
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rt, err := settings.NewRuntime(store, store, config.CopilotConfig{},
		config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{},
		config.IngestChannelConfig{}, []byte("test-jwt-secret-32-bytes-padding-"))
	if err != nil {
		t.Fatalf("settings.NewRuntime: %v", err)
	}
	cat, err := mcpcatalog.New()
	if err != nil {
		t.Fatal(err)
	}
	return &handlers{settingsRuntime: rt, mcpCatalog: cat}
}

func blockedOpsRequest(t *testing.T, h *handlers, method, body string) (*httptest.ResponseRecorder, blockedOperationsView) {
	t.Helper()
	req := httptest.NewRequest(method, "/admin/settings/blocked-operations", strings.NewReader(body))
	rec := httptest.NewRecorder()
	if method == http.MethodGet {
		h.handleGetSettingsBlockedOperations(rec, req)
	} else {
		h.handlePutSettingsBlockedOperations(rec, req)
	}
	var v blockedOperationsView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return rec, v
}

func TestBlockedOperationsSettings_ReadBlockRefuse(t *testing.T) {
	h := blockedOpsHandlers(t)

	rec, v := blockedOpsRequest(t, h, http.MethodGet, "")
	if rec.Code != http.StatusOK || len(v.Connectors) != 0 {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body.String())
	}
	if len(v.Available) == 0 || v.Available[0].ID != "aws" {
		t.Fatalf("available = %+v, want the install's connectors, AWS first", v.Available)
	}

	rec, v = blockedOpsRequest(t, h, http.MethodPut, `{"connectors":{"aws":["S3.GetObject","kms.*"]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(v.Connectors["aws"], ","); got != "kms.*,s3.getobject" {
		t.Fatalf("aws = %q", got)
	}
	if blocked, err := h.settingsRuntime.OperationBlocked("aws", "s3.getobject"); !blocked || err != nil {
		t.Fatalf("OperationBlocked after PUT = %v, %v", blocked, err)
	}

	rec, _ = blockedOpsRequest(t, h, http.MethodPut, `{"connectors":{"aws":["s3 get"]}}`)
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusBadRequest || body["field"] != "connectors.aws" {
		t.Fatalf("malformed entry = %d %s, want 400 on connectors.aws", rec.Code, rec.Body.String())
	}
}
