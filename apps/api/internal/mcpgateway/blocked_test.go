package mcpgateway

import (
	"slices"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// The blocked list is written straight to the store: the platform form only
// takes the real connectors (aws, azure, gcp), and this harness runs "fake".
func (h *harness) blockOperations(raw string) {
	h.t.Helper()
	if err := h.store.SetSetting("blocked_operations", []byte(raw)); err != nil {
		h.t.Fatal(err)
	}
}

// debt-09: an operation on the platform's list never reaches the provider,
// for Kobi or Autopilot, and reads as not sent.
func TestBlockedOperationNeverReachesTheProvider(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	h.blockOperations(`{"connectors":{"fake":["get_*"]}}`)

	for _, c := range []struct {
		consumer mcpcatalog.Consumer
		name     string
	}{{mcpcatalog.ConsumerKobi, "mcp__fake__get_thing"}, {mcpcatalog.ConsumerAutopilot, "cloud_fake_get_thing"}} {
		res := h.gw.Call(ctx, c.consumer, c.name, nil)
		if !res.IsError || !res.NotSent || !strings.Contains(res.Content, "get_thing is blocked") {
			t.Fatalf("%s: %+v, want a not-sent refusal naming the operation", c.consumer, res)
		}
	}
	if n := h.fb.calls.Load(); n != 0 {
		t.Fatalf("the provider was called %d times", n)
	}
	if !slices.ContainsFunc(h.records("org-a"), func(r audit.Record) bool { return r.Params["result"] == resultBlocked }) {
		t.Fatal("no audit record says the call was blocked")
	}

	// Taking it off the list lets the call through on the next call.
	h.blockOperations(`{"connectors":{}}`)
	if res := h.gw.Call(ctx, mcpcatalog.ConsumerKobi, "mcp__fake__get_thing", nil); res.IsError {
		t.Fatalf("after clearing the list: %+v", res)
	}
}

// An emergency switch must not open because the list cannot be read.
func TestBlockedOperationsFailClosed(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	h.blockOperations(`{not json`)

	res := h.gw.Call(ctx, mcpcatalog.ConsumerKobi, "mcp__fake__get_thing", nil)
	if !res.IsError || !strings.Contains(res.Content, "unavailable right now") {
		t.Fatalf("with an unreadable list: %+v, want the call refused", res)
	}
	if n := h.fb.calls.Load(); n != 0 {
		t.Fatalf("the provider was called %d times with an unreadable list", n)
	}
}

// settings cannot import the catalog, so the form's connector list is a
// mirror: every connector the catalog registers must be blockable the day it
// ships (GCP is listed ahead, registered for preview only).
func TestBlockableConnectorsMatchTheCatalog(t *testing.T) {
	cat, err := mcpcatalog.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range cat.List() {
		if !slices.Contains(settings.BlockableConnectors, d.ID) {
			t.Errorf("connector %q is in the catalog but cannot be blocked: add it to settings.BlockableConnectors", d.ID)
		}
	}
}
