package config

import (
	"encoding/base64"
	"net/url"
	"os"
	"strings"
)

// DefaultMetricsStorageURL is where KubeBolt looks for VictoriaMetrics when
// KUBEBOLT_METRICS_STORAGE_URL is not set: one running on its standard port
// next to a laptop install. The Helm chart and Docker Compose always set it.
const DefaultMetricsStorageURL = "http://localhost:8428"

// MetricsStorageURL is the VictoriaMetrics base URL, without a trailing
// slash, that every reader and writer of history uses, and whether
// KUBEBOLT_METRICS_STORAGE_URL set it (false: the default).
func MetricsStorageURL() (string, bool) {
	if u := strings.TrimRight(os.Getenv("KUBEBOLT_METRICS_STORAGE_URL"), "/"); u != "" {
		return u, true
	}
	return DefaultMetricsStorageURL, false
}

// MetricsStorageAuthorization is the Authorization header for
// VictoriaMetrics, or "" when it needs none: a bearer token
// (KUBEBOLT_METRICS_STORAGE_TOKEN) wins over basic auth
// (KUBEBOLT_METRICS_STORAGE_USERNAME / _PASSWORD). Credentials written in the
// URL itself (https://user:pass@host) keep working without these.
func MetricsStorageAuthorization() string {
	if t := strings.TrimSpace(os.Getenv("KUBEBOLT_METRICS_STORAGE_TOKEN")); t != "" {
		return "Bearer " + t
	}
	user, pass := os.Getenv("KUBEBOLT_METRICS_STORAGE_USERNAME"), os.Getenv("KUBEBOLT_METRICS_STORAGE_PASSWORD")
	if user == "" && pass == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// RedactedURL is u with any password replaced by "xxxxx", for logs.
func RedactedURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return "(unparseable URL)"
	}
	return parsed.Redacted()
}
