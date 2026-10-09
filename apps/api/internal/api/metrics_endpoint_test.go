package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// /metrics carries every org's id, every cluster's UID and the orgs' AI usage.
// A self-hosted install may scrape it; a multi-tenant one must not serve it —
// the API pushes its registry into VictoriaMetrics itself.
func TestMetricsEndpoint_NotServedMultiTenant(t *testing.T) {
	for _, tc := range []struct {
		multiTenant bool
		want        int
	}{{false, http.StatusOK}, {true, http.StatusNotFound}} {
		withMultiTenant(t, tc.multiTenant)
		router := NewRouter(newManagerOn(t, map[string]string{}), nil, nil, config.CopilotConfig{}, nil, nil, nil, nil, nil, nil, "",
			nil, nil, "", nil, nil, nil, NewPromWriteMetrics(prometheus.NewRegistry()), nil, nil, nil, nil, nil, nil, nil, nil, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != tc.want {
			t.Errorf("multiTenant=%v: GET /metrics = %d, want %d", tc.multiTenant, rec.Code, tc.want)
		}
	}
}
