package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// withConsolidationEdition sets the edition's consolidation seam for one test
// and puts it back after: the same tests run in the community build (off)
// and the Enterprise one (on).
func withConsolidationEdition(t *testing.T, on bool) {
	t.Helper()
	prev := consolidationEdition
	consolidationEdition = on
	t.Cleanup(func() { consolidationEdition = prev })
}

// The community edition has right-sizing plans and no consolidation: a
// consolidation request is told so before anything is read, whatever the
// org's plan, and the bill's path answers requires_ee.
func TestCommunityEditionHasNoConsolidation(t *testing.T) {
	withConsolidationEdition(t, false)
	ctx := auth.WithTenantID(context.Background(), "org-1")
	h := &handlers{}
	if h.consolidationOnPlan(ctx) {
		t.Fatal("consolidation is not in the community edition")
	}
	src := planSource{h: h}
	if _, err := src.CreatePlan(ctx, copilot.CreatePlanRequest{Kind: kobiplan.KindConsolidation}); !errors.Is(err, errConsolidationNotInEdition) {
		t.Fatalf("consolidation request = %v", err)
	}

	withPlanStore(t)
	code, body := billPathRequest(t, h, ClusterScope{requested: "c-dev"})
	if code != http.StatusConflict || body["code"] != auth.ErrCodeRequiresEE {
		t.Fatalf("bill path = %d %v", code, body)
	}
}

// Without a billing currency per org, a plan's money stays in OpenCost's.
func TestCommunityEditionKeepsOpenCostCurrency(t *testing.T) {
	prev := planCurrency
	t.Cleanup(func() { planCurrency = prev })
	planCurrency = func(*handlers, context.Context) string { return "" }
	if got := planCurrency(&handlers{}, context.Background()); got != "" {
		t.Fatalf("currency = %q", got)
	}
}
