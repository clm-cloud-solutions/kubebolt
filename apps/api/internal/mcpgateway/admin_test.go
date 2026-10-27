package mcpgateway

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var ve *settings.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *settings.ValidationError", err)
	}
	return ve.Field
}

func TestPutValidatesAgainstTheOfferedMode(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.AmbientAllowed = false })
	ctx := orgCtx("org-a")

	if _, err := h.gw.Put(ctx, "nope", PutRequest{Enabled: ptr(true)}, "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	cases := []struct {
		name  string
		req   PutRequest
		field string
	}{
		{"mode not offered here", PutRequest{Enabled: ptr(true), Mode: ptr("ambient"), Fields: map[string]*string{"region": ptr(regionFieldA)}}, "mode"},
		{"unknown mode", PutRequest{Mode: ptr("magic")}, "mode"},
		{"enabling without the secret", PutRequest{Enabled: ptr(true), Mode: ptr("key"), Fields: map[string]*string{"region": ptr(regionFieldA)}}, "apiKey"},
		{"value in the wrong format", PutRequest{Mode: ptr("key"), Fields: map[string]*string{"region": ptr("evil.example.com")}}, "region"},
		{"field the schema does not know", PutRequest{Mode: ptr("key"), Fields: map[string]*string{"endpoint": ptr("https://evil.example.com")}}, "endpoint"},
		{"secret sent as a field", PutRequest{Mode: ptr("key"), Fields: map[string]*string{"apiKey": ptr("x")}}, "apiKey"},
		{"control characters", PutRequest{Mode: ptr("key"), Fields: map[string]*string{"account": ptr("a\r\nX-Injected: 1")}}, "account"},
	}
	for _, c := range cases {
		if _, err := h.gw.Put(ctx, fakeID, c.req, "admin"); fieldOf(t, err) != c.field {
			t.Errorf("%s: field = %q, want %q", c.name, fieldOf(t, err), c.field)
		}
	}
	if _, ok, _ := h.st.MaskedMCPConnector(ctx, fakeID); ok {
		t.Fatal("a rejected write stored something")
	}

	// A disabled connector may be saved half filled in; values are trimmed.
	v, err := h.gw.Put(ctx, fakeID, PutRequest{Mode: ptr("key"), Fields: map[string]*string{"region": ptr("  " + regionFieldA + " ")}}, "admin")
	if err != nil {
		t.Fatalf("partial save: %v", err)
	}
	if v.Status != StatusAvailable || v.Config.Fields["region"] != regionFieldA || v.Mode != "key" {
		t.Fatalf("after partial save: status %s, fields %v, mode %q", v.Status, v.Config.Fields, v.Mode)
	}
	// Enabling it later needs the missing secret.
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Enabled: ptr(true)}, "admin"); fieldOf(t, err) != "apiKey" {
		t.Fatal("enabled an incomplete configuration")
	}
	v, err = h.gw.Put(ctx, fakeID, PutRequest{Enabled: ptr(true), Secrets: map[string]*string{"apiKey": ptr(testAPIKey)}}, "admin")
	if err != nil || v.Status != StatusEnabled || !v.Config.SecretsConfigured["apiKey"] {
		t.Fatalf("enable: %v, %+v", err, v)
	}
	// Keeping a stored secret (absent / null) counts as set.
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Secrets: map[string]*string{"apiKey": nil}, Fields: map[string]*string{"account": ptr("acct-1")}}, "admin"); err != nil {
		t.Fatalf("keep secret: %v", err)
	}
	// Clearing it on an enabled connector does not.
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Secrets: map[string]*string{"apiKey": ptr("")}}, "admin"); fieldOf(t, err) != "apiKey" {
		t.Fatal("cleared the secret of an enabled connector")
	}
}

func TestPutModeSwitchDropsTheOldModesSettings(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Fields: map[string]*string{"account": ptr("acct-1")}}, "admin"); err != nil {
		t.Fatal(err)
	}
	v, err := h.gw.Put(ctx, fakeID, PutRequest{Mode: ptr("ambient")}, "admin")
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if v.Mode != "ambient" || !reflect.DeepEqual(v.Config.Fields, map[string]string{"region": regionFieldA}) || len(v.Config.SecretsConfigured) != 0 {
		t.Fatalf("after switch: mode %q fields %v secrets %v", v.Mode, v.Config.Fields, v.Config.SecretsConfigured)
	}
	h.toolNames(ctx, mcpcatalog.ConsumerKobi)
	if r := h.fb.lastResolved(); r.Mode != "ambient" || len(r.Secrets) != 0 || r.Fields["account"] != "" {
		t.Fatalf("bound with %+v", r)
	}
}

func TestTestDraftIsMergedButNeverSaved(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	before := h.masked(ctx)

	// Keep the stored secret, change a field.
	res, err := h.gw.Test(ctx, fakeID, &Draft{Fields: map[string]*string{"region": ptr(regionFieldB)}, Secrets: map[string]*string{"apiKey": nil}})
	if err != nil || !res.OK {
		t.Fatalf("draft test: %v %+v", err, res)
	}
	if r := h.fb.lastResolved(); r.Fields["region"] != regionFieldB || r.Secrets["apiKey"] != testAPIKey {
		t.Fatalf("draft bound with region %q, stored secret kept=%v", r.Fields["region"], r.Secrets["apiKey"] == testAPIKey)
	}
	eventually(t, "the temporary binding closed", func() bool { return h.fb.binding(0).closed.Load() })

	// Clear the secret: invalid, refused before the provider and the pace.
	binds := h.fb.bindCount()
	res, err = h.gw.Test(ctx, fakeID, &Draft{Secrets: map[string]*string{"apiKey": ptr("")}})
	if err != nil || res.OK || !strings.Contains(res.Error, "API key is required") || h.fb.bindCount() != binds {
		t.Fatalf("cleared secret: %v %+v (binds %d→%d)", err, res, binds, h.fb.bindCount())
	}

	// Set a new secret.
	h.clock.Advance(testEvery)
	if res, err = h.gw.Test(ctx, fakeID, &Draft{Secrets: map[string]*string{"apiKey": ptr("sk-new-value")}}); err != nil || !res.OK {
		t.Fatalf("new secret: %v %+v", err, res)
	}
	if r := h.fb.lastResolved(); r.Secrets["apiKey"] != "sk-new-value" || r.Fields["region"] != regionFieldA {
		t.Fatalf("draft with a new secret bound with %v / %q", r.Secrets["apiKey"] == "sk-new-value", r.Fields["region"])
	}

	after := h.masked(ctx)
	if after.Fields["region"] != regionFieldA || after.VerifiedAt != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("a draft test changed the stored configuration: %+v", after)
	}
	all, _ := h.st.MCP(ctx)
	if all.Connectors[fakeID].Secrets["apiKey"] != testAPIKey {
		t.Fatal("a draft test replaced the stored secret")
	}
	if names := h.toolNames(ctx, mcpcatalog.ConsumerKobi); len(names) == 0 || h.fb.lastResolved().Fields["region"] != regionFieldA {
		t.Fatal("the cached binding does not serve the stored configuration")
	}
}

func TestTestOfTheStoredConfigRecordsTheOutcome(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)

	res, err := h.gw.Test(ctx, fakeID, nil)
	if err != nil || !res.OK || res.Tools != 2 || res.Identity != "fake-identity" {
		t.Fatalf("stored test: %v %+v", err, res)
	}
	m := h.masked(ctx)
	if m.VerifiedAt == nil || !m.VerifiedAt.Equal(h.clock.Now()) || m.VerifiedTools != 2 || m.LastError != "" {
		t.Fatalf("verification = %v/%d/%q", m.VerifiedAt, m.VerifiedTools, m.LastError)
	}
	verifiedAt := *m.VerifiedAt

	// A failing test keeps the last verification and records the error.
	h.fb.set(func(f *fakeBinder) {
		f.test = func(context.Context, *fakeBinding) (mcpcatalog.TestResult, error) {
			return mcpcatalog.TestResult{OK: false, Error: "The canary read was denied.", Hint: "Grant read access."}, nil
		}
	})
	h.clock.Advance(testEvery)
	if res, _ := h.gw.Test(ctx, fakeID, &Draft{}); res.OK { // an empty draft is the stored config
		t.Fatalf("failing test reported ok")
	}
	m = h.masked(ctx)
	if m.LastError != "The canary read was denied." || m.VerifiedAt == nil || !m.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("after failure: %v %q", m.VerifiedAt, m.LastError)
	}
	if v, _ := h.gw.Connector(ctx, fakeID); v.Status != StatusError || v.LastError != "The canary read was denied." {
		t.Fatalf("view after failure: %s %q", v.Status, v.LastError)
	}

	// A draft test, even a successful one, records nothing.
	h.fb.set(func(f *fakeBinder) { f.test = nil })
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Fields: map[string]*string{"account": ptr("acct-2")}}, "admin"); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(testEvery)
	if res, _ := h.gw.Test(ctx, fakeID, &Draft{Fields: map[string]*string{"region": ptr(regionFieldB)}}); !res.OK {
		t.Fatalf("draft: %+v", res)
	}
	if m := h.masked(ctx); m.VerifiedAt != nil || m.LastError != "" {
		t.Fatalf("a draft test was recorded: %v %q", m.VerifiedAt, m.LastError)
	}
}

func TestTestIsPacedPerOrg(t *testing.T) {
	h := newHarness(t, nil)
	a, b := orgCtx("org-a"), orgCtx("org-b")
	h.enable(a, regionFieldA)
	h.enable(b, regionFieldA)
	if _, err := h.gw.Test(a, fakeID, nil); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(2 * time.Second)
	_, err := h.gw.Test(a, fakeID, nil)
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 3*time.Second {
		t.Fatalf("second test: %v (%+v)", err, rl)
	}
	if _, err := h.gw.Test(b, fakeID, nil); err != nil {
		t.Fatalf("another org is paced too: %v", err)
	}
	h.clock.Advance(3 * time.Second)
	if _, err := h.gw.Test(a, fakeID, nil); err != nil {
		t.Fatalf("after the pace: %v", err)
	}
}

func TestTestMapsBindErrorsToAdminSentences(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	h.fb.set(func(f *fakeBinder) {
		f.bindErr = errors.New("assume role: AccessDenied: not authorized; aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	})
	res, err := h.gw.Test(ctx, fakeID, nil)
	if err != nil || res.OK || !strings.Contains(res.Error, "AccessDenied") || res.Hint == "" {
		t.Fatalf("result = %v %+v", err, res)
	}
	if strings.Contains(res.Error, "wJalrXUtnFEMI") {
		t.Fatalf("a secret reached the admin message: %q", res.Error)
	}
	if res, _ := h.gw.Test(orgCtx("org-b"), fakeID, nil); res.OK || res.Error != msgNotConfigured {
		t.Fatalf("unconfigured stored test: %+v", res)
	}
}

func TestViewStatusMatrix(t *testing.T) {
	t.Run("available, enabled, paused", func(t *testing.T) {
		h := newHarness(t, nil)
		ctx := orgCtx("org-a")
		if v, _ := h.gw.Connector(ctx, fakeID); v.Status != StatusAvailable || v.Mode != "" || !v.Entitled {
			t.Fatalf("unconfigured: %+v", v)
		}
		if v := h.enable(ctx, regionFieldA); v.Status != StatusEnabled {
			t.Fatalf("enabled: %s", v.Status)
		}
		v, _ := h.gw.Put(ctx, fakeID, PutRequest{Paused: ptr(true)}, "admin")
		if v.Status != StatusPaused || !v.Paused || !v.Enabled {
			t.Fatalf("paused: %s enabled=%v paused=%v", v.Status, v.Enabled, v.Paused)
		}
		// Configured and paused reads paused whatever the enabled flag says;
		// the flags themselves tell the drawer which it is.
		v, _ = h.gw.Put(ctx, fakeID, PutRequest{Enabled: ptr(false)}, "admin")
		if v.Status != StatusPaused || v.Enabled || !v.Paused {
			t.Fatalf("disabled while paused: %s enabled=%v paused=%v", v.Status, v.Enabled, v.Paused)
		}
		v, _ = h.gw.Put(ctx, fakeID, PutRequest{Paused: ptr(false)}, "admin")
		if v.Status != StatusAvailable || v.Enabled || v.Paused {
			t.Fatalf("disabled: %s enabled=%v paused=%v", v.Status, v.Enabled, v.Paused)
		}
	})
	t.Run("error: stored lastError", func(t *testing.T) {
		h := newHarness(t, nil)
		ctx := orgCtx("org-a")
		h.enable(ctx, regionFieldA)
		h.setLastError(ctx, "The canary failed.")
		if v, _ := h.gw.Connector(ctx, fakeID); v.Status != StatusError || v.LastError != "The canary failed." {
			t.Fatalf("view: %s %q", v.Status, v.LastError)
		}
	})
	t.Run("error: secrets no longer decrypt", func(t *testing.T) {
		h := newHarness(t, nil)
		ctx := orgCtx("org-a")
		h.enable(ctx, regionFieldA)
		rotated := h.gateway(h.runtime("a-different-jwt-secret-after-rotation"), fakeDef(fakeID, h.fb))
		v, _ := rotated.Connector(ctx, fakeID)
		if v.Status != StatusError || !reflect.DeepEqual(v.Config.SecretsUnreadable, []string{"apiKey"}) || !strings.Contains(v.LastError, "Enter them again") {
			t.Fatalf("view: %s %v %q", v.Status, v.Config.SecretsUnreadable, v.LastError)
		}
		if defs, _ := rotated.ToolsFor(ctx, mcpcatalog.ConsumerKobi); len(defs) != 0 || h.fb.bindCount() != 0 {
			t.Fatal("a connector with unreadable secrets was bound")
		}
	})
	t.Run("error: current bind failure", func(t *testing.T) {
		h := newHarness(t, nil)
		ctx := orgCtx("org-a")
		h.enable(ctx, regionFieldA)
		h.fb.set(func(f *fakeBinder) { f.bindErr = context.DeadlineExceeded })
		h.toolNames(ctx, mcpcatalog.ConsumerKobi)
		if v, _ := h.gw.Connector(ctx, fakeID); v.Status != StatusError || !strings.Contains(v.LastError, "did not answer in time") {
			t.Fatalf("view: %s %q", v.Status, v.LastError)
		}
	})
	t.Run("error: mode no longer offered", func(t *testing.T) {
		h := newHarness(t, nil)
		ctx := orgCtx("org-a")
		if _, err := h.gw.Put(ctx, fakeID, PutRequest{Enabled: ptr(true), Mode: ptr("ambient"), Fields: map[string]*string{"region": ptr(regionFieldA)}}, "admin"); err != nil {
			t.Fatal(err)
		}
		h.opts.AmbientAllowed = false
		strict := h.gateway(h.st, fakeDef(fakeID, h.fb))
		v, _ := strict.Connector(ctx, fakeID)
		if v.Status != StatusError || v.LastError != msgModeNotOffered || len(v.AuthModes) != 1 || v.AuthModes[0].ID != "key" {
			t.Fatalf("view: %s %q modes %v", v.Status, v.LastError, v.AuthModes)
		}
	})
	t.Run("not offered on this install", func(t *testing.T) {
		h := newHarness(t, multiTenant(auth.PlanTeam))
		def := fakeDef("sidecar", newFakeBinder())
		def.AuthModes = def.AuthModes[1:] // only the machine-identity mode
		gw := h.gateway(h.st, def)
		v, err := gw.Connector(orgCtx("org-a"), "sidecar")
		if err != nil || v.Status != StatusAvailable || len(v.AuthModes) != 0 || !strings.Contains(v.Description, "Not available on this installation") {
			t.Fatalf("view: %v %+v", err, v)
		}
		if _, err := gw.Put(orgCtx("org-a"), "sidecar", PutRequest{Enabled: ptr(true)}, "admin"); fieldOf(t, err) != "mode" {
			t.Fatal("enabled a connector this install does not offer")
		}
	})
}

func TestViewJSONShape(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	if _, err := h.gw.Test(ctx, fakeID, nil); err != nil {
		t.Fatal(err)
	}
	h.toolNames(ctx, mcpcatalog.ConsumerKobi) // bind, so the view lists live tools
	views, err := h.gw.Connectors(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("connectors: %v %d", err, len(views))
	}
	raw, err := json.Marshal(views[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testAPIKey) {
		t.Fatal("the view carries a secret value")
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	assertKeys(t, "view", m, "id", "name", "category", "description", "tags", "docsURL", "minPlan", "entitled", "status", "enabled", "paused",
		"mode", "kobiMinRole", "authModes", "families", "never", "tools", "config", "verifiedAt", "verifiedTools", "verifiedIdentity", "setupGuide")
	assertKeys(t, "tools", m["tools"].(map[string]any), "kobi", "autopilot")
	assertKeys(t, "config", m["config"].(map[string]any), "fields", "secretsConfigured", "secretsUnreadable")
	assertKeys(t, "setupGuide", m["setupGuide"].(map[string]any), "steps")
	assertKeys(t, "setupGuide.steps[0]", m["setupGuide"].(map[string]any)["steps"].([]any)[0].(map[string]any), "title", "body", "code", "language")
	mode := m["authModes"].([]any)[0].(map[string]any)
	assertKeys(t, "authModes[0]", mode, "id", "label", "description", "selfHostedOnly", "fields")
	assertKeys(t, "authModes[0].fields[0]", mode["fields"].([]any)[0].(map[string]any), "name", "label", "secret", "required")
	assertKeys(t, "families[0]", m["families"].([]any)[0].(map[string]any), "label", "detail", "kobi", "autopilot")

	v := views[0]
	if !reflect.DeepEqual(v.Tools.Kobi, []string{"get_thing", "list_things"}) || !reflect.DeepEqual(v.Tools.Autopilot, servedToAutopilot("get_thing")) {
		t.Fatalf("tools = %+v", v.Tools)
	}
	if v.SetupGuide.Steps[0].Body != "Create it in "+regionFieldA+"." {
		t.Fatalf("guide did not get the stored fields: %q", v.SetupGuide.Steps[0].Body)
	}

	// An unconfigured connector: empty collections, never null.
	raw, _ = json.Marshal(mustView(t, h.gw, orgCtx("org-b")))
	for _, null := range []string{`"tags":null`, `"never":null`, `"kobi":null`, `"fields":null`, `"secretsUnreadable":null`, `"steps":null`, `"secretsConfigured":null`} {
		if strings.Contains(string(raw), null) {
			t.Fatalf("unconfigured view has %s: %s", null, raw)
		}
	}
}

func mustView(t *testing.T, gw *Gateway, ctx context.Context) ConnectorView {
	t.Helper()
	v, err := gw.Connector(ctx, fakeID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// assertKeys checks that an object has exactly the keys the frontend codes
// against (CONTRACT §5), no more and no fewer.
func assertKeys(t *testing.T, what string, m map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s keys = %v, want %v", what, got, want)
	}
}

// validatingBinder adds the provider-specific check a real binder (GCP)
// runs on a pasted credential.
type validatingBinder struct {
	*fakeBinder
	seen []map[string]string
}

func (v *validatingBinder) ValidateConfig(mode string, _ map[string]string, secretValues map[string]string, _ mcpcatalog.Env) error {
	v.seen = append(v.seen, secretValues)
	if key, ok := secretValues["apiKey"]; ok && !strings.HasPrefix(key, "sk-") {
		return &mcpcatalog.FieldError{Field: "apiKey", Message: "API key is not a key this connector accepts"}
	}
	return nil
}

// The generic schema cannot tell a real credential from a well-formed wrong
// one; the binder can. Save and draft test must both ask it, and it must only
// ever see the values the write introduces.
func TestBinderValidationRunsOnSaveAndDraftTest(t *testing.T) {
	h := newHarness(t, nil)
	vb := &validatingBinder{fakeBinder: h.fb}
	h.gw = h.gateway(h.st, fakeDef(fakeID, vb))
	ctx := orgCtx("org-a")

	bad := PutRequest{Enabled: ptr(true), Mode: ptr("key"), Fields: map[string]*string{"region": ptr(regionFieldA)}, Secrets: map[string]*string{"apiKey": ptr("not-a-key")}}
	if _, err := h.gw.Put(ctx, fakeID, bad, "admin"); fieldOf(t, err) != "apiKey" {
		t.Fatalf("save accepted a credential the binder refuses: %v", err)
	}
	if _, ok, _ := h.st.MaskedMCPConnector(ctx, fakeID); ok {
		t.Fatal("a refused credential was stored")
	}
	res, err := h.gw.Test(ctx, fakeID, &Draft{Mode: ptr("key"), Fields: map[string]*string{"region": ptr(regionFieldA)}, Secrets: map[string]*string{"apiKey": ptr("not-a-key")}})
	if err != nil || res.OK || h.fb.bindCount() != 0 {
		t.Fatalf("draft test reached the provider with a refused credential: %+v, %v, binds %d", res, err, h.fb.bindCount())
	}

	good := bad
	good.Secrets = map[string]*string{"apiKey": ptr(testAPIKey)}
	if _, err := h.gw.Put(ctx, fakeID, good, "admin"); err != nil {
		t.Fatalf("valid save: %v", err)
	}
	// A later write that keeps the stored key must not hand it back.
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Fields: map[string]*string{"account": ptr("acct-1")}}, "admin"); err != nil {
		t.Fatalf("keep-key save: %v", err)
	}
	if last := vb.seen[len(vb.seen)-1]; len(last) != 0 {
		t.Fatalf("the binder was handed stored secrets: %v", last)
	}
}

// nil means keep: a patch of only nulls changes nothing, so it must not write
// a record for a connector the org never configured.
func TestPutOfOnlyNullsWritesNothing(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Fields: map[string]*string{"region": nil}, Secrets: map[string]*string{"apiKey": nil}}, "admin"); err != nil {
		t.Fatalf("null-only put: %v", err)
	}
	if _, ok, _ := h.st.MaskedMCPConnector(ctx, fakeID); ok {
		t.Fatal("a patch of nulls stored a record")
	}
}

// The pace protects one provider from being hammered, not the admin from
// setting up a second connector right after the first.
func TestTestPaceIsPerConnector(t *testing.T) {
	h := newHarness(t, nil)
	h.gw = h.gateway(h.st, fakeDef(fakeID, h.fb), fakeDef("other", h.fb))
	ctx := orgCtx("org-a")
	for _, id := range []string{fakeID, "other"} {
		if _, err := h.gw.Put(ctx, id, PutRequest{
			Enabled: ptr(true), Mode: ptr("key"),
			Fields:  map[string]*string{"region": ptr(regionFieldA)},
			Secrets: map[string]*string{"apiKey": ptr(testAPIKey)},
		}, "admin"); err != nil {
			t.Fatalf("enable %s: %v", id, err)
		}
	}
	if _, err := h.gw.Test(ctx, fakeID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.gw.Test(ctx, "other", nil); err != nil {
		t.Fatalf("a second connector was paced by the first: %v", err)
	}
	var rl *RateLimitError
	if _, err := h.gw.Test(ctx, fakeID, nil); !errors.As(err, &rl) {
		t.Fatalf("the same connector was not paced: %v", err)
	}
}

// Pausing or switching off a connector nobody set up (a stale drawer, a
// direct API call) changes nothing: no record is written, so no stored-config
// test can pin an error on it and no stale pause lands the first setup paused.
func TestPauseOrDisableOfAnUnconfiguredConnectorWritesNothing(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	writes := h.cs.mcpWrites.Load()
	for _, req := range []PutRequest{{Paused: ptr(true)}, {Paused: ptr(false)}, {Enabled: ptr(false)}, {Enabled: ptr(false), Paused: ptr(true)}} {
		v, err := h.gw.Put(ctx, fakeID, req, "admin")
		if err != nil || v.Status != StatusAvailable || v.Paused || v.Enabled {
			t.Fatalf("%+v: %v status=%s paused=%v", req, err, v.Status, v.Paused)
		}
	}
	if _, ok, _ := h.st.MaskedMCPConnector(ctx, fakeID); ok {
		t.Fatal("pausing an unconfigured connector created a record")
	}
	if n := h.cs.mcpWrites.Load() - writes; n != 0 {
		t.Fatalf("settings writes = %d, want 0", n)
	}
	if res, _ := h.gw.Test(ctx, fakeID, nil); res.OK || res.Error != msgNotConfigured {
		t.Fatalf("stored test of nothing: %+v", res)
	}
	if m, ok, _ := h.st.MaskedMCPConnector(ctx, fakeID); ok || m.LastError != "" {
		t.Fatal("a stored-config test pinned an error on an unconfigured connector")
	}

	// A pause an earlier build did store must not land the first setup paused.
	if err := h.st.PutMCPConnector(ctx, fakeID, settings.MCPConnectorPatch{Paused: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if v := h.enable(ctx, regionFieldA); v.Status != StatusEnabled || v.Paused {
		t.Fatalf("first setup over a stale pause: %s paused=%v", v.Status, v.Paused)
	}
}

// Paused wins over error: an admin who pauses a failing connector sees it
// paused, with the flags the drawer needs for Resume, and the error it had.
func TestPausedConnectorWithAnErrorReadsPaused(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	h.setLastError(ctx, "The provider rejected the credentials.")
	if v, _ := h.gw.Connector(ctx, fakeID); v.Status != StatusError {
		t.Fatalf("before pausing: %s", v.Status)
	}
	v, err := h.gw.Put(ctx, fakeID, PutRequest{Paused: ptr(true)}, "admin")
	if err != nil || v.Status != StatusPaused || !v.Paused || !v.Enabled || v.LastError != "The provider rejected the credentials." {
		t.Fatalf("after pause: %v status=%s paused=%v enabled=%v lastError=%q", err, v.Status, v.Paused, v.Enabled, v.LastError)
	}
	v, _ = h.gw.Put(ctx, fakeID, PutRequest{Paused: ptr(false)}, "admin")
	if v.Status != StatusError || v.Paused {
		t.Fatalf("after resume: %s paused=%v", v.Status, v.Paused)
	}
}

// A passing test that found something to fix (the AWS root user) keeps
// saying so after the drawer closes; a clean pass clears it, and so does a
// new configuration.
func TestTestWarningAndIdentityAreStoredWithTheVerification(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	h.enable(ctx, regionFieldA)
	const warning = "These are the account's root user credentials."
	pass := func(warn string) func(context.Context, *fakeBinding) (mcpcatalog.TestResult, error) {
		return func(context.Context, *fakeBinding) (mcpcatalog.TestResult, error) {
			return mcpcatalog.TestResult{OK: true, Tools: 2, Identity: "arn:aws:iam::123456789012:root", Warning: warn}, nil
		}
	}
	h.fb.set(func(f *fakeBinder) { f.test = pass(warning) })
	if res, err := h.gw.Test(ctx, fakeID, nil); err != nil || !res.OK || res.Warning != warning {
		t.Fatalf("test: %v %+v", err, res)
	}
	v, _ := h.gw.Connector(ctx, fakeID)
	if v.VerifiedWarning != warning || v.VerifiedIdentity != "arn:aws:iam::123456789012:root" || v.Status != StatusEnabled {
		t.Fatalf("view after a pass with a warning: %s %q %q", v.Status, v.VerifiedWarning, v.VerifiedIdentity)
	}

	// A failure keeps what the last pass said.
	h.fb.set(func(f *fakeBinder) {
		f.test = func(context.Context, *fakeBinding) (mcpcatalog.TestResult, error) {
			return mcpcatalog.TestResult{Error: "The canary read was denied.", Warning: "ignored on a failure"}, nil
		}
	})
	h.clock.Advance(testEvery)
	if res, _ := h.gw.Test(ctx, fakeID, nil); res.OK || res.Warning != "" {
		t.Fatalf("failed test: %+v", res)
	}
	if v, _ := h.gw.Connector(ctx, fakeID); v.VerifiedWarning != warning || v.LastError == "" {
		t.Fatalf("after a failure: %q %q", v.VerifiedWarning, v.LastError)
	}

	h.fb.set(func(f *fakeBinder) { f.test = pass("") })
	h.clock.Advance(testEvery)
	if _, err := h.gw.Test(ctx, fakeID, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := h.gw.Connector(ctx, fakeID); v.VerifiedWarning != "" || v.VerifiedIdentity == "" || v.LastError != "" {
		t.Fatalf("a clean pass kept the warning: %q %q %q", v.VerifiedWarning, v.VerifiedIdentity, v.LastError)
	}

	h.fb.set(func(f *fakeBinder) { f.test = pass(warning) })
	h.clock.Advance(testEvery)
	if _, err := h.gw.Test(ctx, fakeID, nil); err != nil {
		t.Fatal(err)
	}
	v, _ = h.gw.Put(ctx, fakeID, PutRequest{Fields: map[string]*string{"region": ptr(regionFieldB)}}, "admin")
	if v.VerifiedWarning != "" || v.VerifiedIdentity != "" || v.VerifiedAt != nil {
		t.Fatalf("a new configuration kept the old verification: %q %q %v", v.VerifiedWarning, v.VerifiedIdentity, v.VerifiedAt)
	}
}

// The AWS external ID is KubeBolt's to choose, once per org: it is generated
// the first time the admin sees the connector, stored where the binder reads
// it, never replaced, never accepted from the admin — and generating it does
// not make the connector configured.
func TestGeneratedFieldIsCreatedOnceAndNeverSet(t *testing.T) {
	h := newHarness(t, nil)
	h.gw = h.gateway(h.st, generatedDef(h.fb))
	ctx := orgCtx("org-a")

	v, err := h.gw.Connector(ctx, fakeID)
	if err != nil {
		t.Fatal(err)
	}
	id := v.Config.Fields["externalId"]
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Fatalf("generated value = %q, want 64 hex characters", id)
	}
	if v.Status != StatusAvailable || v.Mode != "" || v.Enabled || v.Paused {
		t.Fatalf("generating made the connector configured: %s mode=%q", v.Status, v.Mode)
	}
	if res, _ := h.gw.Test(ctx, fakeID, nil); res.Error != msgNotConfigured {
		t.Fatalf("stored test of a connector holding only a generated value: %+v", res)
	}
	if views, _ := h.gw.Connectors(ctx); views[0].Config.Fields["externalId"] != id {
		t.Fatal("the generated value changed on the next view")
	}
	if other, _ := h.gw.Connector(orgCtx("org-b"), fakeID); other.Config.Fields["externalId"] == id {
		t.Fatal("two orgs share a generated value")
	}

	// Neither a save nor a draft may carry it.
	for _, req := range []PutRequest{
		{Mode: ptr("key"), Fields: map[string]*string{"externalId": ptr("chosen-by-the-customer-01")}},
		{Mode: ptr("key"), Fields: map[string]*string{"externalId": ptr("")}},
		{Secrets: map[string]*string{"externalId": ptr("chosen-by-the-customer-01")}},
	} {
		_, err := h.gw.Put(ctx, fakeID, req, "admin")
		var ve *settings.ValidationError
		if !errors.As(err, &ve) || ve.Field != "externalId" || !strings.Contains(ve.Message, "generated by KubeBolt; it cannot be set") {
			t.Fatalf("put %+v: %v", req, err)
		}
	}
	if _, err := h.gw.Test(ctx, fakeID, &Draft{Mode: ptr("key"), Fields: map[string]*string{"externalId": ptr("chosen-by-the-customer-01")}}); fieldOf(t, err) != "externalId" {
		t.Fatal("a draft carrying the generated value was tested")
	}
	if h.fb.bindCount() != 0 {
		t.Fatal("a refused request reached the provider")
	}

	// A save that keeps it (null) is fine, and the binder receives it.
	h.enable(ctx, regionFieldA)
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Fields: map[string]*string{"externalId": nil, "account": ptr("acct-1")}}, "admin"); err != nil {
		t.Fatalf("keep: %v", err)
	}
	h.toolNames(ctx, mcpcatalog.ConsumerKobi)
	if got := h.fb.lastResolved().Fields["externalId"]; got != id {
		t.Fatalf("the binder got externalId %q", got)
	}
	// A mode that does not use it leaves it alone, and switching back finds it.
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Mode: ptr("ambient")}, "admin"); err != nil {
		t.Fatal(err)
	}
	h.toolNames(ctx, mcpcatalog.ConsumerKobi)
	if got := h.fb.lastResolved().Fields["externalId"]; got != "" {
		t.Fatalf("a mode without the field was handed it: %q", got)
	}
	if got := h.masked(ctx).Fields["externalId"]; got != id {
		t.Fatalf("switching modes dropped the generated value: %q", got)
	}
	v, err = h.gw.Put(ctx, fakeID, PutRequest{Mode: ptr("key"), Secrets: map[string]*string{"apiKey": ptr(testAPIKey)}}, "admin")
	if err != nil || v.Config.Fields["externalId"] != id {
		t.Fatalf("after switching modes and back: %v %q", err, v.Config.Fields["externalId"])
	}
}

// Two replicas over one store. Replica 2 served a Kobi turn before replica 1
// generated the value, so its settings cache says nothing is stored. Its
// admin view must still show the stored value — not a fresh one, not none —
// and accept a save and a draft test that rely on it.
func TestGeneratedValueOnAReplicaWithAStaleCache(t *testing.T) {
	h := newHarness(t, nil)
	def := generatedDef(h.fb)
	gw1 := h.gateway(h.st, def)
	st2 := h.runtime(testJWTKey)
	gw2 := h.gateway(st2, def)
	ctx := orgCtx("org-a")

	if _, err := st2.MCP(ctx); err != nil {
		t.Fatal(err)
	}
	v1, err := gw1.Connector(ctx, fakeID)
	if err != nil {
		t.Fatal(err)
	}
	id := v1.Config.Fields["externalId"]
	if id == "" {
		t.Fatal("replica 1 generated nothing")
	}
	if v2, err := gw2.Connector(ctx, fakeID); err != nil || v2.Config.Fields["externalId"] != id {
		t.Fatalf("replica 2 shows externalId %q, stored %q (err %v)", v2.Config.Fields["externalId"], id, err)
	}
	if res, err := gw2.Test(ctx, fakeID, &Draft{Mode: ptr("key"), Fields: map[string]*string{"region": ptr(regionFieldA)}, Secrets: map[string]*string{"apiKey": ptr(testAPIKey)}}); err != nil || !res.OK {
		t.Fatalf("draft test on replica 2: %v %+v", err, res)
	}
	if got := h.fb.lastResolved().Fields["externalId"]; got != id {
		t.Fatalf("replica 2 tested with externalId %q", got)
	}
	v2, err := gw2.Put(ctx, fakeID, PutRequest{
		Enabled: ptr(true),
		Mode:    ptr("key"),
		Fields:  map[string]*string{"region": ptr(regionFieldA)},
		Secrets: map[string]*string{"apiKey": ptr(testAPIKey)},
	}, "admin")
	if err != nil || v2.Status != StatusEnabled || v2.Config.Fields["externalId"] != id {
		t.Fatalf("save on replica 2: %v %s %q", err, v2.Status, v2.Config.Fields["externalId"])
	}
}

// Removing a connector forgets how it was set up and keeps what KubeBolt
// generated for the org: the customer's trust policy names the external ID,
// and re-adding the connector must find the same one. What is left is not a
// configuration.
func TestDeleteKeepsTheGeneratedValue(t *testing.T) {
	h := newHarness(t, nil)
	h.gw = h.gateway(h.st, generatedDef(h.fb))
	ctx := orgCtx("org-a")
	v, err := h.gw.Connector(ctx, fakeID)
	if err != nil {
		t.Fatal(err)
	}
	id := v.Config.Fields["externalId"]
	h.enable(ctx, regionFieldA)
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Paused: ptr(true), KobiMinRole: ptr("admin"), Fields: map[string]*string{"account": ptr("acct-1")}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{Paused: ptr(false)}, "admin"); err != nil {
		t.Fatal(err)
	}
	if names := h.toolNames(ctx, mcpcatalog.ConsumerKobi); len(names) == 0 {
		t.Fatal("no tools before the delete")
	}
	if res, err := h.gw.Test(ctx, fakeID, nil); err != nil || !res.OK {
		t.Fatalf("test: %v %+v", err, res)
	}

	if err := h.gw.Delete(ctx, fakeID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the binding closed on delete", func() bool { return h.fb.binding(0).closed.Load() })
	v, err = h.gw.Connector(ctx, fakeID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Config.Fields, map[string]string{"externalId": id}) {
		t.Fatalf("after delete the fields are %v, want only the generated %q", v.Config.Fields, id)
	}
	if v.Status != StatusAvailable || v.Mode != "" || v.Enabled || v.Paused || v.KobiMinRole != string(DefaultKobiMinRole) ||
		len(v.Config.SecretsConfigured) != 0 || v.VerifiedAt != nil || v.VerifiedIdentity != "" || v.LastError != "" {
		t.Fatalf("the delete left a configuration behind: %+v", v)
	}
	if names := h.toolNames(ctx, mcpcatalog.ConsumerKobi); len(names) != 0 {
		t.Fatalf("tools after delete = %v", names)
	}
	h.clock.Advance(testEvery)
	if res, _ := h.gw.Test(ctx, fakeID, nil); res.Error != msgNotConfigured {
		t.Fatalf("stored test after delete: %+v", res)
	}
	if v = h.enable(ctx, regionFieldB); v.Config.Fields["externalId"] != id {
		t.Fatalf("re-adding the connector issued externalId %q, the trust policy names %q", v.Config.Fields["externalId"], id)
	}
}

// ConnectorToolNames is what a model's read path (get_external_connections)
// asks: it names the tools without generating a value, rendering a guide or
// writing the org's settings — a write there would change the connector's
// fingerprint and rebuild its live binding from a read.
func TestConnectorToolNamesWritesNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.gw = h.gateway(h.st, generatedDef(h.fb))
	ctx := orgCtx("org-a")
	// Stored before KubeBolt generated values (or behind the gateway's back).
	if err := h.st.PutMCPConnector(ctx, fakeID, settings.MCPConnectorPatch{
		Enabled: ptr(true),
		Mode:    ptr("key"),
		Fields:  map[string]*string{"region": ptr(regionFieldA)},
		Secrets: map[string]*string{"apiKey": ptr(testAPIKey)},
	}); err != nil {
		t.Fatal(err)
	}
	writes := h.cs.mcpWrites.Load()
	names, err := h.gw.ConnectorToolNames(ctx, fakeID)
	if err != nil {
		t.Fatal(err)
	}
	if names.Kobi == nil || names.Autopilot == nil {
		t.Fatalf("tool lists must be non-nil: %+v", names)
	}
	if got := h.cs.mcpWrites.Load(); got != writes {
		t.Fatalf("a read wrote the settings %d time(s)", got-writes)
	}
	if got := h.masked(ctx).Fields["externalId"]; got != "" {
		t.Fatalf("a read generated externalId %q", got)
	}
	if _, err := h.gw.ConnectorToolNames(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown connector: %v", err)
	}
	if _, err := h.gw.ConnectorToolNames(context.Background(), fakeID); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("no org: %v", err)
	}

	// With a live binding for the stored configuration it names what that
	// binding serves each consumer, as the admin view does.
	h.gw = h.gateway(h.st, fakeDef(fakeID, h.fb))
	h.toolNames(ctx, mcpcatalog.ConsumerKobi)
	names, err = h.gw.ConnectorToolNames(ctx, fakeID)
	if err != nil || strings.Join(names.Kobi, ",") != "get_thing,list_things" || strings.Join(names.Autopilot, ",") != "get_thing" {
		t.Fatalf("names from the live binding: %v %+v", err, names)
	}
	view, _ := h.gw.Connector(ctx, fakeID)
	if !reflect.DeepEqual(view.Tools.Kobi, names.Kobi) || !reflect.DeepEqual(view.Tools.Autopilot, servedToAutopilot(names.Autopilot...)) {
		t.Fatalf("the admin view names %+v, the read path %+v", view.Tools, names)
	}
}

// servedToAutopilot is what the admin view shows for Autopilot's tools in
// this edition: the names when it serves Autopilot, none when it does not.
func servedToAutopilot(names ...string) []string {
	if !autopilotServed {
		return []string{}
	}
	return names
}

// An edition without Autopilot shows no Autopilot tools, reach or families in
// the admin view; what Kobi reads is unchanged.
func TestTheViewWithoutAutopilot(t *testing.T) {
	cat, err := mcpcatalog.New()
	if err != nil {
		t.Fatal(err)
	}
	aws, _ := cat.Get("aws")
	v := withoutAutopilot(ConnectorView{
		Tools:    ToolNames{Kobi: []string{"call"}, Autopilot: []string{"call"}},
		Reach:    reachView(aws),
		Families: nonNil(aws.Families),
	})
	if len(v.Tools.Autopilot) != 0 || v.Tools.Autopilot == nil || !reflect.DeepEqual(v.Tools.Kobi, []string{"call"}) {
		t.Errorf("tools = %+v", v.Tools)
	}
	if v.Reach.Autopilot != 0 || v.Reach.Kobi == 0 {
		t.Errorf("reach = %+v", v.Reach)
	}
	kobi := 0
	for _, f := range v.Families {
		if f.Autopilot {
			t.Errorf("family %q still marked for Autopilot", f.Label)
		}
		if f.Kobi {
			kobi++
		}
	}
	if kobi == 0 {
		t.Error("no family left for Kobi")
	}
	for _, f := range aws.Families {
		if f.Autopilot {
			return // the catalog's own table is untouched
		}
	}
	t.Error("withoutAutopilot changed the catalog's family table")
}

func TestKobiMinRoleInTheView(t *testing.T) {
	h := newHarness(t, nil)
	ctx := orgCtx("org-a")
	if v, _ := h.gw.Connector(ctx, fakeID); v.KobiMinRole != string(DefaultKobiMinRole) || DefaultKobiMinRole != auth.RoleEditor {
		t.Fatalf("default floor = %q", v.KobiMinRole)
	}
	if _, err := h.gw.Put(ctx, fakeID, PutRequest{KobiMinRole: ptr("owner")}, "admin"); fieldOf(t, err) != "kobiMinRole" {
		t.Fatal("an unknown role was accepted")
	}
	h.enable(ctx, regionFieldA)
	if _, err := h.gw.Test(ctx, fakeID, nil); err != nil {
		t.Fatal(err)
	}
	verified := h.masked(ctx).VerifiedAt
	v, err := h.gw.Put(ctx, fakeID, PutRequest{KobiMinRole: ptr("admin")}, "admin")
	if err != nil || v.KobiMinRole != "admin" || v.VerifiedAt == nil || !v.VerifiedAt.Equal(*verified) {
		t.Fatalf("floor change: %v %q verified=%v", err, v.KobiMinRole, v.VerifiedAt)
	}
	if v, _ = h.gw.Put(ctx, fakeID, PutRequest{KobiMinRole: ptr("")}, "admin"); v.KobiMinRole != "editor" {
		t.Fatalf("\"\" did not restore the default: %q", v.KobiMinRole)
	}
	if !(PutRequest{Fields: map[string]*string{"region": nil}}).ChangesNothing() || (PutRequest{KobiMinRole: ptr("viewer")}).ChangesNothing() {
		t.Fatal("ChangesNothing disagrees with keep/clear/set")
	}
}

// The connector view carries the binder's reach for both consumers, and
// none for a binder that cannot say it.
func TestReachViewFromTheBinder(t *testing.T) {
	cat, err := mcpcatalog.New()
	if err != nil {
		t.Fatal(err)
	}
	aws, _ := cat.Get("aws")
	r := reachView(aws)
	if r == nil || r.Unit != "AWS services" || r.Autopilot == 0 || r.Autopilot > r.Kobi {
		t.Errorf("aws reach = %+v", r)
	}
	if r := reachView(&mcpcatalog.ConnectorDef{ID: "x"}); r != nil {
		t.Errorf("a binder without reach got %+v", r)
	}
}
