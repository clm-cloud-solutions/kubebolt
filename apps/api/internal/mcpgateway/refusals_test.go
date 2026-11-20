package mcpgateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
)

// debt-10: what the gateway refuses before the provider is counted per org —
// the shape of a prompt injection is one org's assistant asking, again and
// again, for what it may not do. A provider's own denial (a role without the
// permission) is the connector failing, already alerted on elsewhere.
func TestRefusalsAreCountedPerOrgBeforeTheProvider(t *testing.T) {
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = true
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })

	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	kobi := string(mcpcatalog.ConsumerKobi)

	// A tool the org's set does not list.
	if res := h.gw.Call(ctx, mcpcatalog.ConsumerKobi, "mcp__fake__delete_everything", nil); !res.IsError {
		t.Fatalf("unlisted tool was not refused: %+v", res)
	}
	// The binding refuses the input itself; then the provider denies.
	for _, r := range []mcpcatalog.CallResult{
		{IsError: true, Own: true, Text: "Refused: command is not in the allowlist."},
		{IsError: true, Text: "AccessDenied: not authorized"},
	} {
		h.fb.set(func(f *fakeBinder) {
			f.call = func(context.Context, *fakeBinding, mcpcatalog.Consumer, string, json.RawMessage) (mcpcatalog.CallResult, error) {
				return r, nil
			}
		})
		h.gw.Call(ctx, mcpcatalog.ConsumerKobi, "mcp__fake__get_thing", nil)
	}

	if got := testutil.ToFloat64(h.gw.m.refusals.WithLabelValues("org-a", kobi, "fake", refusalToolNotAllowed)); got != 1 {
		t.Errorf("tool_not_allowed for org-a = %v, want 1", got)
	}
	if got := testutil.ToFloat64(h.gw.m.refusals.WithLabelValues("org-a", kobi, "fake", refusalInput)); got != 1 {
		t.Errorf("input_refused for org-a = %v, want 1 (the provider's AccessDenied is not a refusal)", got)
	}
}

func TestRefusalsCarryNoTenantOnASingleTenantInstall(t *testing.T) {
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = false
	t.Cleanup(func() { auth.MultiTenantEnabled = prev })

	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	h.gw.Call(ctx, mcpcatalog.ConsumerKobi, "mcp__fake__delete_everything", nil)
	if got := testutil.ToFloat64(h.gw.m.refusals.WithLabelValues("", string(mcpcatalog.ConsumerKobi), "fake", refusalToolNotAllowed)); got != 1 {
		t.Errorf("single-tenant refusal = %v, want 1 under an empty tenant", got)
	}
}

// Found live: Prometheus' increase() reads the first sample of a new series
// as nothing, so five refusals on a connector that had none yet fired no
// alert. A single-tenant install starts every refusal series at 0; a
// multi-tenant one cannot name its orgs and reads increase_pure.
func TestRefusalSeriesStartAtZeroOnASingleTenantInstall(t *testing.T) {
	h := newHarness(t, nil)
	// Two consumers × (the fake connector's two reasons + an unknown name).
	if n := testutil.CollectAndCount(h.gw.m.refusals); n != 6 {
		t.Fatalf("refusal series before any call = %d, want 6", n)
	}
	for _, consumer := range []mcpcatalog.Consumer{mcpcatalog.ConsumerKobi, mcpcatalog.ConsumerAutopilot} {
		for _, labels := range [][2]string{{"fake", refusalToolNotAllowed}, {"fake", refusalInput}, {"unknown", refusalToolNotAllowed}} {
			if got := testutil.ToFloat64(h.gw.m.refusals.WithLabelValues("", string(consumer), labels[0], labels[1])); got != 0 {
				t.Errorf("%s %v = %v, want 0", consumer, labels, got)
			}
		}
	}

	multi := newHarness(t, func(o *Options) { o.MultiTenant = true })
	if n := testutil.CollectAndCount(multi.gw.m.refusals); n != 0 {
		t.Errorf("multi-tenant refusal series before any call = %d, want none", n)
	}
}
