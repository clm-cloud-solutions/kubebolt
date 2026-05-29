package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// TestHandleListActions_EndToEnd exercises the Sprint 1 audit endpoint
// through the HTTP handler: a wired store → GET /admin/actions → JSON.
func TestHandleListActions_EndToEnd(t *testing.T) {
	store := audit.NewMemoryStore()
	_ = store.Append(&audit.Record{
		ID: "1", Timestamp: time.Now().Add(-time.Minute), Source: "copilot_proposal",
		Action: "scale_workload", TargetType: "deployments", TargetName: "api",
		Result: "success", OriginatingInsightID: "occ-7",
	})
	_ = store.Append(&audit.Record{
		ID: "2", Timestamp: time.Now(), Source: "ui",
		Action: "restart_workload", TargetType: "deployments", TargetName: "web", Result: "success",
	})
	SetAuditStore(store, func() string { return "cluster-1" })
	t.Cleanup(func() { SetAuditStore(nil, nil) })

	h := &handlers{}
	rec := httptest.NewRecorder()
	h.handleListActions(rec, httptest.NewRequest(http.MethodGet, "/admin/actions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Items []audit.Record `json:"items"`
		Total int            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 || len(body.Items) != 2 {
		t.Fatalf("total/items = %d/%d, want 2/2", body.Total, len(body.Items))
	}
	// Newest first.
	if body.Items[0].ID != "2" {
		t.Fatalf("expected newest record first, got id=%s", body.Items[0].ID)
	}
	// Provenance round-trips through the endpoint (Sprint 0 → Sprint 1 link).
	if body.Items[1].OriginatingInsightID != "occ-7" {
		t.Fatalf("originating insight id lost: %+v", body.Items[1])
	}
}

// TestHandleListActions_NoStore returns an empty list (not an error) when no
// store is wired — the auth-disabled / no-BoltDB path.
func TestHandleListActions_NoStore(t *testing.T) {
	SetAuditStore(nil, nil)
	h := &handlers{}
	rec := httptest.NewRecorder()
	h.handleListActions(rec, httptest.NewRequest(http.MethodGet, "/admin/actions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestCopilotDestructiveBlocked verifies the Sprint 1 server-side guard: only
// copilot_proposal-sourced requests are blocked when destructive ops are off;
// UI requests and non-destructive contexts pass.
func TestCopilotDestructiveBlocked(t *testing.T) {
	cases := []struct {
		name        string
		destructive bool
		source      string
		wantBlocked bool
	}{
		{"destructive-on, kobi", true, "copilot_proposal", false},
		{"destructive-off, kobi", false, "copilot_proposal", true},
		{"destructive-off, ui", false, "ui", false},
		{"destructive-off, no-header", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &handlers{copilotConfig: config.CopilotConfig{DestructiveActionsEnabled: tc.destructive}}
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			if tc.source != "" {
				req.Header.Set("X-KubeBolt-Action-Source", tc.source)
			}
			if got := h.copilotDestructiveBlocked(req); got != tc.wantBlocked {
				t.Fatalf("copilotDestructiveBlocked = %v, want %v", got, tc.wantBlocked)
			}
		})
	}
}
