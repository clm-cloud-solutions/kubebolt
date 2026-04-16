package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// MountFrontend adds a catch-all handler to serve the embedded React frontend.
// API routes (/api/*, /ws/*, /pf/*, /health) take priority because they're
// registered first. This handler catches everything else and serves static
// files with SPA fallback (non-file routes get index.html).
func MountFrontend(r *chi.Mux, frontendFS fs.FS) {
	fileServer := http.FileServer(http.FS(frontendFS))

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
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
