package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// GET /copilot/config tells the usage views which unit to show AI spend in.
// Credits when the platform runs the AI (SaaS): a customer never sees the LLM
// cost behind a credit. USD when the org brings its own key (self-hosted EE,
// OSS): the cost is their own provider bill, and hiding it helps nobody.
func TestCopilotConfig_SpendUnitFollowsWhoPaysTheProvider(t *testing.T) {
	for _, tc := range []struct {
		multiTenant bool
		want        string
	}{
		{multiTenant: true, want: "credits"},
		{multiTenant: false, want: "usd"},
	} {
		prev := auth.MultiTenantEnabled
		auth.MultiTenantEnabled = tc.multiTenant
		h := &handlers{copilotConfig: config.CopilotConfig{}}
		rec := httptest.NewRecorder()
		h.HandleCopilotConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/copilot/config", nil))
		auth.MultiTenantEnabled = prev

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body)
		}
		if got := body["spendUnit"]; got != tc.want {
			t.Errorf("multiTenant=%v: spendUnit = %v, want %q", tc.multiTenant, got, tc.want)
		}
	}
}
