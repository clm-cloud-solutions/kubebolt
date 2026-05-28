package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// reservedAPIPrefixes are URL path prefixes whose unmatched requests
// MUST surface as 404, NOT fall through to the SPA shell. The SPA
// shell is only appropriate for React Router client-side routes
// (e.g. `/cluster/topology`, `/admin/users`). Returning the shell for
// paths under reserved API namespaces had two bad consequences
// (discovered in session 11-A v3 2026-05-28):
//
//  1. Misleading HTTP 200 + body for security probes — a scanner
//     hitting `/api/.env` got 200 + ~580 bytes of HTML and could
//     reasonably flag the endpoint as "interesting". The body is
//     the SPA shell, not the .env file, but the 200 status invited
//     deeper inspection during pen-tests.
//  2. The reserved namespaces have their own auth middleware
//     (RequireAuth, RequireRole) that gates handlers inside them.
//     Catch-all behavior bypassed those middleware for path-probe
//     misses, returning the SPA shell publicly even when the path
//     CONCEPTUALLY belonged to an admin-only namespace.
//
// The list mirrors the prefixes mounted in router.go. Add new
// entries here whenever a new top-level API namespace is added.
var reservedAPIPrefixes = []string{"/api", "/ws", "/pf", "/health", "/metrics"}

// isReservedAPIPath returns true for paths that should NOT serve
// the SPA shell when no specific handler claimed them. Matches
// exact paths and proper subpaths (so `/api` AND `/api/anything`
// match `/api`, but `/applications` does NOT).
func isReservedAPIPath(path string) bool {
	for _, p := range reservedAPIPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// MountFrontend adds a catch-all handler to serve the embedded React frontend.
// API routes (/api/*, /ws/*, /pf/*, /health, /metrics) take priority because
// they're registered first. This handler catches everything else and serves
// static files with SPA fallback (non-file routes get index.html), EXCEPT
// paths under reservedAPIPrefixes which always 404 — see that var's doc for
// why.
func MountFrontend(r *chi.Mux, frontendFS fs.FS) {
	fileServer := http.FileServer(http.FS(frontendFS))

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		// Reserved API paths that fell through to the catch-all (no
		// specific handler claimed them) MUST 404, not serve the SPA
		// shell. See reservedAPIPrefixes doc for the security
		// rationale.
		if isReservedAPIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		urlPath := strings.TrimPrefix(r.URL.Path, "/")

		// Try to serve the exact file
		if urlPath != "" {
			if file, err := frontendFS.Open(urlPath); err == nil {
				file.Close()

				// Cache hashed assets forever (Vite adds content hash to filenames)
				if strings.HasPrefix(urlPath, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}

				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// SPA fallback: serve index.html for any route that isn't a real file
		// This lets React Router handle client-side routing
		indexFile, err := fs.ReadFile(frontendFS, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Write(indexFile)
	})

}
