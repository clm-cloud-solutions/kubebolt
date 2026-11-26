package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// What of Kobi Plans the edition has (P22, 26-nov). The community edition has
// what an organization on Free has in KubeBolt Cloud, without its limits:
// right-sizing plans end to end (created by Kobi, exported, revised,
// verified, their impact measured). Consolidation plans and the bill's path,
// the step that lowers the bill, come with KubeBolt Cloud and Enterprise: the
// Enterprise build turns them on (kobiplans_edition_ee.go). The cut is here,
// on the server; the web reads its own flag (eeConsolidationPlans in
// ee/registry.tsx) only to word it.
var consolidationEdition = false

// planCurrency is the currency a plan's money is shown in; empty keeps
// OpenCost's. The community edition has no billing currency per org.
var planCurrency = func(h *handlers, ctx context.Context) string { return "" }

// errConsolidationNotInEdition: asked for a consolidation plan where the
// edition has none.
var errConsolidationNotInEdition = errors.New("consolidation plans come with KubeBolt Cloud and Enterprise; right-sizing plans are available in this edition")

// respondConsolidationNotInEdition answers a consolidation-only route where
// the edition has none, the way every edition-gated route does.
func respondConsolidationNotInEdition(w http.ResponseWriter, what string) {
	respondJSON(w, http.StatusConflict, map[string]string{
		"error": what + " comes with KubeBolt Cloud and Enterprise",
		"code":  auth.ErrCodeRequiresEE,
	})
}
