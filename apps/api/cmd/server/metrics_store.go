package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// resolveMetricsStore picks the metrics store (VictoriaMetrics) and says so
// once at boot, so an install without one knows it keeps no history. The URL
// is where the readers and the agent ingest go. active says whether the
// background jobs that need the store run (the API's self-push and the
// active-series cap): always when KUBEBOLT_METRICS_STORAGE_URL is set, and
// for the default only when a VictoriaMetrics answers there at boot.
func resolveMetricsStore(ctx context.Context) (url string, active bool) {
	url, explicit := config.MetricsStorageURL()
	switch {
	case explicit:
		slog.Info("metrics store", slog.String("url", config.RedactedURL(url)),
			slog.String("source", "KUBEBOLT_METRICS_STORAGE_URL"))
		return url, true
	case metricsStoreAnswers(ctx, url):
		slog.Info("metrics store", slog.String("url", url),
			slog.String("source", "default; set KUBEBOLT_METRICS_STORAGE_URL to use another"))
		return url, true
	default:
		slog.Info("history off: no VictoriaMetrics at " + url + "; start one there or set " +
			"KUBEBOLT_METRICS_STORAGE_URL, then restart. Live CPU and memory still come from metrics-server")
		return url, false
	}
}

// metricsStoreAnswers reports whether a VictoriaMetrics answers its health
// check at url within two seconds.
func metricsStoreAnswers(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/health", nil)
	if err != nil {
		return false
	}
	if auth := config.MetricsStorageAuthorization(); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
