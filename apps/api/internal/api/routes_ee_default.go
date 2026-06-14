//go:build !ee

package api

import "github.com/go-chi/chi/v5"

// registerEERoutes is the OSS (community) no-op for the EE route extension
// point called from NewRouter. Enterprise builds (`-tags ee`) replace this
// with routes_ee.go, which registers edition-specific routes such as the
// internal notifications-dispatch endpoint used by Autopilot. Keeping the
// seam here means router.go stays identical between OSS and EE.
func registerEERoutes(r chi.Router, h *handlers) {}
