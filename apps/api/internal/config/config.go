package config

import (
	"os"
	"strings"
)

// Config holds all application configuration.
type Config struct {
	Kubeconfig      string
	Port            int
	MetricInterval  int
	InsightInterval int
	CORSOrigins     []string
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	c := &Config{
		Port:            8080,
		MetricInterval:  30,
		InsightInterval: 60,
		CORSOrigins:     []string{"http://localhost:3000", "http://localhost:5173"},
	}
	// KUBEBOLT_CORS_ORIGINS (comma-separated) overrides the localhost defaults.
	// Required when the UI is served from a different origin than the API — e.g.
	// a Vercel-hosted SPA at https://app.kubebolt.io (plus preview URLs like
	// https://*.vercel.app) calling this API cross-origin. go-chi/cors supports
	// wildcard patterns and reflects the matched origin, which is correct with
	// AllowCredentials.
	if origins := splitTrim(os.Getenv("KUBEBOLT_CORS_ORIGINS")); len(origins) > 0 {
		c.CORSOrigins = origins
	}
	return c
}

// splitTrim splits a comma-separated list, trimming whitespace and dropping
// empty entries. Returns nil for an empty/whitespace input.
func splitTrim(v string) []string {
	out := make([]string, 0)
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
