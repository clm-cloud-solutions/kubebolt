package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

// Tests for the per-org MCP connector settings: secrets
// encrypted at rest and decrypted only in memory, keep/clear/set patch
// semantics, per-org isolation, and a read-modify-write that neither drops a
// concurrent writer's connector nor overwrites what it could not read.

const (
	testExternalID = "ext-id-7f3a9c2e-plaintext-canary"
	testCredJSON   = `{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----canary"}`
)

func mustPutMCP(t *testing.T, rt *Runtime, ctx context.Context, id string, p MCPConnectorPatch) {
	t.Helper()
	if err := rt.PutMCPConnector(ctx, id, p); err != nil {
		t.Fatalf("put %s: %v", id, err)
	}
}

func mustMCP(t *testing.T, rt *Runtime, ctx context.Context) ResolvedMCP {
	t.Helper()
	m, err := rt.MCP(ctx)
	if err != nil {
		t.Fatalf("mcp: %v", err)
	}
	return m
}

func awsPatch() MCPConnectorPatch {
	return MCPConnectorPatch{
		Enabled: boolPtr(true),
		Mode:    strPtr("assume-role"),
		Fields: map[string]*string{
			"roleArn": strPtr("arn:aws:iam::123456789012:role/KubeBoltRead"),
			"region":  strPtr("us-east-1"),
		},
		Secrets:   map[string]*string{"externalId": strPtr(testExternalID)},
		UpdatedBy: "admin",
	}
}

func TestMCP_EmptyOrgHasNoConnectors(t *testing.T) {
	rt := newTestRuntime(t)
	m := mustMCP(t, rt, context.Background())
	if m.Connectors == nil || len(m.Connectors) != 0 {
		t.Fatalf("want an empty non-nil map, got %#v", m.Connectors)
	}
	masked, ok, err := rt.MaskedMCPConnector(context.Background(), "aws")
	if err != nil || ok {
		t.Fatalf("unconfigured connector: ok=%v err=%v", ok, err)
	}
	if masked.Fields == nil || masked.SecretsConfigured == nil || masked.SecretsUnreadable == nil {
		t.Errorf("unconfigured view must carry empty, non-nil collections: %#v", masked)
	}
}

func TestMCP_SecretsEncryptedAtRest(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())
	mustPutMCP(t, rt, ctx, "gcp", MCPConnectorPatch{
		Mode:    strPtr("service-account"),
		Fields:  map[string]*string{"projectId": strPtr("acme-prod")},
		Secrets: map[string]*string{"credentialJSON": strPtr(testCredJSON)},
	})

	raw, err := rt.orgStore.GetOrgSetting(ctx, mcpSettingsKey)
	if err != nil {
		t.Fatalf("raw read: %v", err)
	}
	for _, plaintext := range []string{testExternalID, "canary", "BEGIN PRIVATE KEY"} {
		if strings.Contains(string(raw), plaintext) {
			t.Errorf("plaintext %q leaked into the stored record", plaintext)
		}
	}
	var stored StoredMCPSettings
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if enc := stored.Connectors["aws"].SecretsEncoded["externalId"]; !strings.HasPrefix(enc, secretEnvelopePrefix) {
		t.Errorf("stored secret is not a %q envelope: %q", secretEnvelopePrefix, enc)
	}
	// Non-secret fields are stored as given — they are what the setup guide prints.
	if got := stored.Connectors["aws"].Fields["region"]; got != "us-east-1" {
		t.Errorf("stored region = %q", got)
	}

	// Resolved: decrypted in memory.
	m := mustMCP(t, rt, ctx)
	if got := m.Connectors["aws"].Secrets["externalId"]; got != testExternalID {
		t.Errorf("resolved externalId = %q", got)
	}
	if got := m.Connectors["gcp"].Secrets["credentialJSON"]; got != testCredJSON {
		t.Errorf("resolved credentialJSON = %q", got)
	}
	aws := m.Connectors["aws"]
	if !aws.Enabled || aws.Mode != "assume-role" || aws.UpdatedBy != "admin" || aws.UpdatedAt.IsZero() {
		t.Errorf("resolved aws metadata: %+v", aws.Fields)
	}
}

// The resolved struct carries plaintext credentials; neither an accidental JSON
// encode of it nor the admin view may reveal them.
func TestMCP_SecretsNeverLeaveThroughViews(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())

	m := mustMCP(t, rt, ctx)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal resolved: %v", err)
	}
	if strings.Contains(string(b), testExternalID) {
		t.Error("json encoding of ResolvedMCP carries a secret")
	}

	masked, ok, err := rt.MaskedMCPConnector(ctx, "aws")
	if err != nil || !ok {
		t.Fatalf("masked: ok=%v err=%v", ok, err)
	}
	b, _ = json.Marshal(masked)
	if strings.Contains(string(b), testExternalID) || strings.Contains(string(b), "ext-") || strings.Contains(string(b), "canary") {
		t.Errorf("masked view reveals secret material: %s", b)
	}
	if !masked.SecretsConfigured["externalId"] {
		t.Errorf("masked view should report externalId as configured: %s", b)
	}
	if masked.Fields["roleArn"] == "" || masked.Mode != "assume-role" || !masked.Enabled {
		t.Errorf("masked view lost non-secret config: %s", b)
	}
}

// json:"-" only guards encoders. A resolved config printed by fmt or slog —
// a debug line, an error message — must not carry the plaintext either.
func TestMCP_ResolvedNeverPrintsSecrets(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())
	m := mustMCP(t, rt, ctx)
	c := m.Connectors["aws"]

	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("resolved", "mcp", m, "connector", c)
	outputs := map[string]string{
		"%v mcp":    fmt.Sprintf("%v", m),
		"%+v mcp":   fmt.Sprintf("%+v", m),
		"%#v mcp":   fmt.Sprintf("%#v", m),
		"%v conn":   fmt.Sprintf("%v", c),
		"%+v *conn": fmt.Sprintf("%+v", &c),
		"%#v conn":  fmt.Sprintf("%#v", c),
		"%s conn":   fmt.Sprintf("%s", c),
		"%d conn":   fmt.Sprintf("%d", c),
		"slog text": logged.String(),
	}
	for how, out := range outputs {
		// Never print `out` itself: on failure it holds the canary.
		if strings.Contains(out, testExternalID) {
			t.Errorf("%s prints the plaintext secret", how)
		}
	}
	if s := c.String(); !strings.Contains(s, "externalId") || !strings.Contains(s, "KubeBoltRead") {
		t.Errorf("String must still name the secret and show the fields: %s", s)
	}
}

// Only a miss of THIS key is "nothing configured"; any other error — even one
// that happens to end in "not found" — must stay an error.
func TestIsOrgSettingMiss(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("setting %q not found", mcpSettingsKey), true},
		{fmt.Errorf("setting %q not found", "copilot"), false},
		{errors.New(`role "kubebolt_app" not found`), false},
		{errors.New("not found"), false},
		{errors.New("connection reset by peer"), false},
		{nil, false},
	} {
		if got := isOrgSettingMiss(tc.err, mcpSettingsKey); got != tc.want {
			t.Errorf("isOrgSettingMiss(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestMCP_PatchKeepClearSet(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())

	// Keep: no secrets map, and an explicit nil entry.
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{Paused: boolPtr(true)})
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{Secrets: map[string]*string{"externalId": nil}, Fields: map[string]*string{"region": nil}})
	aws := mustMCP(t, rt, ctx).Connectors["aws"]
	if aws.Secrets["externalId"] != testExternalID {
		t.Errorf("keep: externalId = %q", aws.Secrets["externalId"])
	}
	if aws.Fields["region"] != "us-east-1" {
		t.Errorf("keep: region = %q", aws.Fields["region"])
	}
	if !aws.Paused || !aws.Enabled || aws.Mode != "assume-role" {
		t.Errorf("keep: flags/mode changed: paused=%v enabled=%v mode=%q", aws.Paused, aws.Enabled, aws.Mode)
	}

	// Set.
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{
		Secrets: map[string]*string{"externalId": strPtr("rotated-external-id")},
		Fields:  map[string]*string{"region": strPtr("eu-west-1")},
	})
	aws = mustMCP(t, rt, ctx).Connectors["aws"]
	if aws.Secrets["externalId"] != "rotated-external-id" || aws.Fields["region"] != "eu-west-1" {
		t.Errorf("set: secret=%q region=%q", aws.Secrets["externalId"], aws.Fields["region"])
	}

	// Clear: "" removes the secret and deletes the field key.
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{
		Secrets: map[string]*string{"externalId": strPtr("")},
		Fields:  map[string]*string{"region": strPtr("")},
	})
	aws = mustMCP(t, rt, ctx).Connectors["aws"]
	if _, ok := aws.Secrets["externalId"]; ok {
		t.Error("clear: externalId still present")
	}
	if _, ok := aws.Fields["region"]; ok {
		t.Error("clear: region key still present")
	}
	if aws.Fields["roleArn"] == "" {
		t.Error("clear of one field removed another")
	}
	masked, _, _ := rt.MaskedMCPConnector(ctx, "aws")
	if masked.SecretsConfigured["externalId"] {
		t.Error("masked view still reports a cleared secret")
	}
	raw, _ := rt.orgStore.GetOrgSetting(ctx, mcpSettingsKey)
	if strings.Contains(string(raw), `"region"`) || strings.Contains(string(raw), `"externalId"`) {
		t.Errorf("cleared keys still on disk: %s", raw)
	}
}

func TestMCP_PerOrgIsolation(t *testing.T) {
	rt := newTestRuntime(t)
	ctxA := auth.WithTenantID(context.Background(), "org-a")
	ctxB := auth.WithTenantID(context.Background(), "org-b")

	mustPutMCP(t, rt, ctxA, "aws", awsPatch())
	// Warm B's cache before A writes again, to catch a cache that is keyed
	// wrongly rather than a store that is.
	if n := len(mustMCP(t, rt, ctxB).Connectors); n != 0 {
		t.Fatalf("org B sees %d connectors written by org A", n)
	}
	mustPutMCP(t, rt, ctxB, "aws", MCPConnectorPatch{
		Enabled: boolPtr(true),
		Secrets: map[string]*string{"externalId": strPtr("org-b-external-id")},
	})

	if got := mustMCP(t, rt, ctxA).Connectors["aws"].Secrets["externalId"]; got != testExternalID {
		t.Errorf("org A externalId = %q", got)
	}
	if got := mustMCP(t, rt, ctxB).Connectors["aws"].Secrets["externalId"]; got != "org-b-external-id" {
		t.Errorf("org B externalId = %q", got)
	}
	if _, ok := mustMCP(t, rt, ctxB).Connectors["aws"].Fields["roleArn"]; ok {
		t.Error("org B inherited org A's roleArn")
	}
	// An unscoped ctx is the default tenant: it sees neither org.
	if n := len(mustMCP(t, rt, context.Background()).Connectors); n != 0 {
		t.Errorf("default tenant sees %d connectors of other orgs", n)
	}

	// Deleting in A never touches B.
	if err := rt.DeleteMCPConnector(ctxA, "aws"); err != nil {
		t.Fatalf("delete A: %v", err)
	}
	if _, ok := mustMCP(t, rt, ctxA).Connectors["aws"]; ok {
		t.Error("org A connector survived its delete")
	}
	if got := mustMCP(t, rt, ctxB).Connectors["aws"].Secrets["externalId"]; got != "org-b-external-id" {
		t.Errorf("org B after A's delete: %q (bleed!)", got)
	}
}

func TestMCP_DeleteRemovesConfigAndSecrets(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())
	mustPutMCP(t, rt, ctx, "gcp", MCPConnectorPatch{Fields: map[string]*string{"projectId": strPtr("acme")}})

	if err := rt.DeleteMCPConnector(ctx, "aws"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	m := mustMCP(t, rt, ctx)
	if _, ok := m.Connectors["aws"]; ok {
		t.Error("aws still configured after delete")
	}
	if m.Connectors["gcp"].Fields["projectId"] != "acme" {
		t.Error("deleting aws dropped gcp")
	}
	raw, _ := rt.orgStore.GetOrgSetting(ctx, mcpSettingsKey)
	if strings.Contains(string(raw), `"aws"`) || strings.Contains(string(raw), "roleArn") {
		t.Errorf("deleted connector still on disk: %s", raw)
	}
	if _, ok, _ := rt.MaskedMCPConnector(ctx, "aws"); ok {
		t.Error("masked view still finds the deleted connector")
	}
	// Idempotent.
	if err := rt.DeleteMCPConnector(ctx, "aws"); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if err := rt.DeleteMCPConnector(ctx, "azure"); err != nil {
		t.Errorf("delete of a never-configured connector: %v", err)
	}
}

// A value KubeBolt generated for the org (the AWS external ID) outlives the
// connector's removal: the customer's trust policy names it, and re-adding the
// connector must find it. Everything else goes, and what is left reads as a
// connector nobody set up.
func TestMCP_DeleteKeepsTheNamedFields(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	const generated = "generated-external-id-00001"
	if _, err := rt.EnsureMCPConnectorFields(ctx, "aws", map[string]string{"externalId": generated}); err != nil {
		t.Fatal(err)
	}
	p := awsPatch()
	p.Paused, p.KobiMinRole = boolPtr(true), strPtr("admin")
	mustPutMCP(t, rt, ctx, "aws", p)
	before := mustMCP(t, rt, ctx).Connectors["aws"]
	if err := rt.SetMCPConnectorVerification(ctx, "aws", MCPVerification{ConfigAt: before.UpdatedAt, Verified: &MCPVerified{At: time.Now(), Tools: 3, Identity: "arn:aws:iam::123456789012:root", Warning: "root"}, LastError: "old"}); err != nil {
		t.Fatal(err)
	}
	mustPutMCP(t, rt, ctx, "gcp", MCPConnectorPatch{Fields: map[string]*string{"projectId": strPtr("acme")}})

	if err := rt.DeleteMCPConnector(ctx, "aws", "externalId"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	aws, ok := mustMCP(t, rt, ctx).Connectors["aws"]
	if !ok || len(aws.Fields) != 1 || aws.Fields["externalId"] != generated {
		t.Fatalf("the generated value did not survive the delete alone: ok=%v fields=%v", ok, aws.Fields)
	}
	if aws.Mode != "" || aws.Enabled || aws.Paused || len(aws.Secrets) != 0 || len(aws.SecretsUnreadable) != 0 || aws.KobiMinRole != "" ||
		aws.VerifiedAt != nil || aws.VerifiedTools != 0 || aws.VerifiedIdentity != "" || aws.VerifiedWarning != "" || aws.LastError != "" ||
		aws.UpdatedBy != "" || !aws.UpdatedAt.IsZero() {
		t.Fatalf("the delete kept more than the generated value: %v", aws)
	}
	raw, _ := rt.orgStore.GetOrgSetting(ctx, mcpSettingsKey)
	for _, gone := range []string{"roleArn", "secretsEncoded", testExternalID, "verifiedAt", "admin"} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("%q still on disk after the delete: %s", gone, raw)
		}
	}
	// A verification of the removed configuration cannot land on what is left.
	if err := rt.SetMCPConnectorVerification(ctx, "aws", MCPVerification{ConfigAt: before.UpdatedAt, LastError: "late"}); !errors.Is(err, ErrMCPConnectorChanged) {
		t.Errorf("a late verification after the delete: %v", err)
	}
	// Nothing to keep: the record goes, as before.
	if err := rt.DeleteMCPConnector(ctx, "gcp", "externalId"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustMCP(t, rt, ctx).Connectors["gcp"]; ok {
		t.Error("a connector with nothing to keep survived its delete")
	}
	// Setting it up again finds the same value.
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{Enabled: boolPtr(true), Mode: strPtr("assume-role"), Fields: map[string]*string{"roleArn": strPtr("arn:aws:iam::123456789012:role/KubeBoltRead")}})
	if got := mustMCP(t, rt, ctx).Connectors["aws"].Fields["externalId"]; got != generated {
		t.Fatalf("re-adding the connector lost the generated value: %q", got)
	}
}

func TestMCP_Verification(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())
	before := mustMCP(t, rt, ctx).Connectors["aws"].UpdatedAt

	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	passed := MCPVerification{ConfigAt: before, Verified: &MCPVerified{At: at, Tools: 5, Identity: "arn:aws:sts::123456789012:assumed-role/KubeBoltRead/kubebolt-kobi", Warning: "Root user credentials."}}
	if err := rt.SetMCPConnectorVerification(ctx, "aws", passed); err != nil {
		t.Fatalf("verify: %v", err)
	}
	aws := mustMCP(t, rt, ctx).Connectors["aws"]
	if aws.VerifiedAt == nil || !aws.VerifiedAt.Equal(at) || aws.VerifiedTools != 5 || aws.LastError != "" ||
		aws.VerifiedIdentity != passed.Verified.Identity || aws.VerifiedWarning != passed.Verified.Warning {
		t.Errorf("verification not recorded: %v", aws)
	}
	if !aws.UpdatedAt.Equal(before) {
		t.Error("a test result moved UpdatedAt — it is not a configuration change")
	}
	masked, _, _ := rt.MaskedMCPConnector(ctx, "aws")
	if masked.VerifiedAt == nil || masked.VerifiedTools != 5 || masked.VerifiedIdentity == "" || masked.VerifiedWarning == "" {
		t.Errorf("masked view misses the verification: %+v", masked)
	}

	// A failed re-test records its error and keeps the last verification.
	if err := rt.SetMCPConnectorVerification(ctx, "aws", MCPVerification{ConfigAt: before, LastError: "The role could not be assumed."}); err != nil {
		t.Fatalf("verify failure: %v", err)
	}
	aws = mustMCP(t, rt, ctx).Connectors["aws"]
	if aws.LastError != "The role could not be assumed." || aws.VerifiedAt == nil || aws.VerifiedWarning == "" {
		t.Errorf("after a failure: %v", aws)
	}
	// A later pass without a warning clears it.
	passed.Verified.Warning = ""
	if err := rt.SetMCPConnectorVerification(ctx, "aws", passed); err != nil {
		t.Fatal(err)
	}
	if aws = mustMCP(t, rt, ctx).Connectors["aws"]; aws.VerifiedWarning != "" || aws.LastError != "" {
		t.Errorf("a clean pass kept the warning or the error: %v", aws)
	}

	// Pausing is not a configuration change: the badge stays.
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{Paused: boolPtr(true)})
	if mustMCP(t, rt, ctx).Connectors["aws"].VerifiedAt == nil {
		t.Error("pausing cleared the verification")
	}
	// Re-sending an unchanged field is not one either.
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{Fields: map[string]*string{"region": strPtr("us-east-1")}})
	if mustMCP(t, rt, ctx).Connectors["aws"].VerifiedAt == nil {
		t.Error("an unchanged field cleared the verification")
	}
	// New credentials are: the badge — identity and warning included —
	// belonged to the old ones.
	passed.Verified.Warning = "Root user credentials."
	passed.ConfigAt = mustMCP(t, rt, ctx).Connectors["aws"].UpdatedAt
	if err := rt.SetMCPConnectorVerification(ctx, "aws", passed); err != nil {
		t.Fatal(err)
	}
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{Secrets: map[string]*string{"externalId": strPtr("new-external-id")}})
	aws = mustMCP(t, rt, ctx).Connectors["aws"]
	if aws.VerifiedAt != nil || aws.VerifiedTools != 0 || aws.VerifiedIdentity != "" || aws.VerifiedWarning != "" || aws.LastError != "" {
		t.Errorf("new credentials kept the old verification: %v", aws)
	}

	// A result for a connector that is gone must not resurrect it.
	if err := rt.SetMCPConnectorVerification(ctx, "gcp", MCPVerification{Verified: &MCPVerified{At: at, Tools: 3}}); !errors.Is(err, ErrMCPConnectorNotFound) {
		t.Errorf("verification of an unconfigured connector: err=%v", err)
	}
	if _, ok := mustMCP(t, rt, ctx).Connectors["gcp"]; ok {
		t.Error("verification created a connector")
	}
}

// A verification describes one configuration. When the connector was saved
// again while the test or the bind ran — on this replica or another, whose
// save this process may not have seen yet — the result must not land on the
// new configuration.
func TestMCP_VerificationOnlyLandsOnTheTestedConfiguration(t *testing.T) {
	a := newTestRuntime(t)
	b := secondReplica(t, a)
	ctx := context.Background()
	mustPutMCP(t, a, ctx, "aws", awsPatch())
	tested := mustMCP(t, b, ctx).Connectors["aws"] // replica B's snapshot

	mustPutMCP(t, a, ctx, "aws", MCPConnectorPatch{Fields: map[string]*string{"region": strPtr("eu-west-1")}})
	err := b.SetMCPConnectorVerification(ctx, "aws", MCPVerification{ConfigAt: tested.UpdatedAt, LastError: "The old role could not be assumed."})
	if !errors.Is(err, ErrMCPConnectorChanged) {
		t.Fatalf("stale verification: err = %v, want ErrMCPConnectorChanged", err)
	}
	b.InvalidateMCP(ctx)
	if got := mustMCP(t, b, ctx).Connectors["aws"]; got.LastError != "" || got.Fields["region"] != "eu-west-1" {
		t.Fatalf("the old configuration's error landed on the new one: %v", got)
	}
}

// A connector that binds again clears the error its failed bind wrote, and
// only that one.
func TestMCP_VerificationIfLastError(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())
	at := mustMCP(t, rt, ctx).Connectors["aws"].UpdatedAt
	bindErr := "AWS did not answer in time."
	if err := rt.SetMCPConnectorVerification(ctx, "aws", MCPVerification{ConfigAt: at, LastError: "The canary read was denied."}); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetMCPConnectorVerification(ctx, "aws", MCPVerification{ConfigAt: at, IfLastError: &bindErr}); !errors.Is(err, ErrMCPConnectorChanged) {
		t.Fatalf("cleared another error: %v", err)
	}
	if got := mustMCP(t, rt, ctx).Connectors["aws"].LastError; got != "The canary read was denied." {
		t.Fatalf("lastError = %q", got)
	}
}

// secondReplica is another process over the same store: its own cache, its
// own write lock, the same data.
func secondReplica(t *testing.T, rt *Runtime) *Runtime {
	t.Helper()
	other, err := NewRuntime(rt.store, rt.orgStore, config.CopilotConfig{}, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte("test-jwt-secret-32-bytes-padding-"))
	if err != nil {
		t.Fatalf("second replica: %v", err)
	}
	return other
}

// On a two-replica install, disabling or deleting a connector on one replica
// must stop the other from using it — within the cache's bound, not at its
// next restart — and enabling one must reach the other too.
func TestMCP_SecondReplicaSeesChangesWithinTheTTL(t *testing.T) {
	a := newTestRuntime(t)
	b := secondReplica(t, a)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	b.mcp.now = func() time.Time { return now }
	ctx := auth.WithTenantID(context.Background(), "org-a")

	// B has already served a turn: its cache holds "nothing configured".
	if n := len(mustMCP(t, b, ctx).Connectors); n != 0 {
		t.Fatalf("B starts with %d connectors", n)
	}
	mustPutMCP(t, a, ctx, "aws", awsPatch())
	now = now.Add(mcpCacheTTL)
	if aws, ok := mustMCP(t, b, ctx).Connectors["aws"]; !ok || !aws.Enabled || aws.Secrets["externalId"] != testExternalID {
		t.Fatalf("B does not see the connector A enabled: ok=%v %v", ok, aws)
	}

	mustPutMCP(t, a, ctx, "aws", MCPConnectorPatch{Enabled: boolPtr(false)})
	now = now.Add(mcpCacheTTL / 2)
	if !mustMCP(t, b, ctx).Connectors["aws"].Enabled {
		t.Fatal("B re-read before its entry expired; the TTL is what bounds the store reads")
	}
	now = now.Add(mcpCacheTTL / 2)
	if mustMCP(t, b, ctx).Connectors["aws"].Enabled {
		t.Fatal("B still serves a connector A disabled")
	}

	if err := a.DeleteMCPConnector(ctx, "aws"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(mcpCacheTTL)
	if _, ok := mustMCP(t, b, ctx).Connectors["aws"]; ok {
		t.Fatal("B still holds the credentials of a connector A deleted")
	}
}

// gatedOrgStore holds the first armed read of the MCP key after it has read
// the value, standing in for a slow store read that a write overtakes.
type gatedOrgStore struct {
	auth.OrgSettingStore
	mu    sync.Mutex
	armed bool
	read  chan struct{}
	gate  chan struct{}
}

func (g *gatedOrgStore) GetOrgSetting(ctx context.Context, key string) ([]byte, error) {
	v, err := g.OrgSettingStore.GetOrgSetting(ctx, key)
	g.mu.Lock()
	hold := g.armed && key == mcpSettingsKey
	if hold {
		g.armed = false
	}
	g.mu.Unlock()
	if hold {
		close(g.read)
		<-g.gate
	}
	return v, err
}

// A reload reads the store outside the cache lock. When a write lands while
// it does, what it read is older than the write and must not be cached.
func TestMCP_ReloadOvertakenByAWriteIsNotCached(t *testing.T) {
	base := newTestRuntime(t)
	gated := &gatedOrgStore{OrgSettingStore: base.orgStore, read: make(chan struct{}), gate: make(chan struct{})}
	rt, err := NewRuntime(base.store, gated, config.CopilotConfig{}, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte("test-jwt-secret-32-bytes-padding-"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())

	gated.mu.Lock()
	gated.armed = true
	gated.mu.Unlock()
	done := make(chan ResolvedMCP)
	go func() {
		m, _ := rt.MCP(ctx)
		done <- m
	}()
	<-gated.read
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{Enabled: boolPtr(false)})
	close(gated.gate)
	if stale := <-done; !stale.Connectors["aws"].Enabled {
		t.Fatal("the overtaken read did not see the old value; the test proves nothing")
	}
	if mustMCP(t, rt, ctx).Connectors["aws"].Enabled {
		t.Fatal("a read overtaken by a write was cached over it")
	}
}

// The role floor says who may use the connector, not how it connects: setting
// it keeps UpdatedAt (the bindings' key) and the verification.
func TestMCP_KobiMinRoleIsNotAConfigurationChange(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())
	before := mustMCP(t, rt, ctx).Connectors["aws"]
	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	if err := rt.SetMCPConnectorVerification(ctx, "aws", MCPVerification{ConfigAt: before.UpdatedAt, Verified: &MCPVerified{At: at, Tools: 5}}); err != nil {
		t.Fatal(err)
	}

	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{KobiMinRole: strPtr("viewer"), UpdatedBy: "someone-else"})
	aws := mustMCP(t, rt, ctx).Connectors["aws"]
	if aws.KobiMinRole != "viewer" || !aws.UpdatedAt.Equal(before.UpdatedAt) || aws.UpdatedBy != before.UpdatedBy || aws.VerifiedAt == nil {
		t.Fatalf("after a floor change: %v", aws)
	}
	if m, _, _ := rt.MaskedMCPConnector(ctx, "aws"); m.KobiMinRole != "viewer" {
		t.Fatalf("masked floor = %q", m.KobiMinRole)
	}
	mustPutMCP(t, rt, ctx, "aws", MCPConnectorPatch{KobiMinRole: strPtr("")})
	if got := mustMCP(t, rt, ctx).Connectors["aws"].KobiMinRole; got != "" {
		t.Fatalf("\"\" did not restore the default: %q", got)
	}
	if err := rt.PutMCPConnector(ctx, "aws", MCPConnectorPatch{KobiMinRole: strPtr("owner")}); !IsValidation(err) {
		t.Fatalf("an unknown role was stored: %v", err)
	}
}

// Generated values are written once and never replaced, and writing one is
// not a configuration change.
func TestMCP_EnsureFieldsNeverReplacesAValue(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()

	wrote, err := rt.EnsureMCPConnectorFields(ctx, "aws", map[string]string{"externalId": "first-generated-value-0001"})
	if err != nil || !wrote {
		t.Fatalf("first ensure: wrote=%v err=%v", wrote, err)
	}
	aws, ok := mustMCP(t, rt, ctx).Connectors["aws"]
	if !ok || aws.Fields["externalId"] != "first-generated-value-0001" || aws.Mode != "" || aws.Enabled || !aws.UpdatedAt.IsZero() {
		t.Fatalf("after the first ensure: ok=%v %v", ok, aws)
	}
	wrote, err = rt.EnsureMCPConnectorFields(ctx, "aws", map[string]string{"externalId": "second-generated-value-02"})
	if err != nil || wrote {
		t.Fatalf("second ensure: wrote=%v err=%v", wrote, err)
	}
	if got := mustMCP(t, rt, ctx).Connectors["aws"].Fields["externalId"]; got != "first-generated-value-0001" {
		t.Fatalf("a generated value was replaced: %q", got)
	}

	// On a configured connector it keeps UpdatedAt and the verification.
	mustPutMCP(t, rt, ctx, "gcp", MCPConnectorPatch{Enabled: boolPtr(true), Mode: strPtr("adc"), Fields: map[string]*string{"projectId": strPtr("acme")}})
	gcp := mustMCP(t, rt, ctx).Connectors["gcp"]
	if err := rt.SetMCPConnectorVerification(ctx, "gcp", MCPVerification{ConfigAt: gcp.UpdatedAt, Verified: &MCPVerified{At: time.Now(), Tools: 4}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.EnsureMCPConnectorFields(ctx, "gcp", map[string]string{"token": "generated-token-value-001"}); err != nil {
		t.Fatal(err)
	}
	after := mustMCP(t, rt, ctx).Connectors["gcp"]
	if !after.UpdatedAt.Equal(gcp.UpdatedAt) || after.VerifiedAt == nil || after.Fields["projectId"] != "acme" || after.Fields["token"] == "" {
		t.Fatalf("ensure changed the configuration around it: %v", after)
	}
	if _, err := rt.EnsureMCPConnectorFields(ctx, "a__b", map[string]string{"x": "y"}); !IsValidation(err) {
		t.Fatalf("a malformed id was accepted: %v", err)
	}
}

// Replica B's cache predates the value replica A generated. B asks for one
// too, from that stale snapshot: the stored value wins, and B's very next read
// must show it — not only once the cache expires — or B's admin view shows no
// external ID and a valid save is refused for want of one.
func TestMCP_EnsureFieldsOnAStaleReplicaShowsTheStoredValue(t *testing.T) {
	a := newTestRuntime(t)
	b := secondReplica(t, a)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	b.mcp.now = func() time.Time { return now }
	ctx := auth.WithTenantID(context.Background(), "org-a")

	// B has served a Kobi turn: its cache holds "nothing configured".
	if n := len(mustMCP(t, b, ctx).Connectors); n != 0 {
		t.Fatalf("B starts with %d connectors", n)
	}
	if wrote, err := a.EnsureMCPConnectorFields(ctx, "aws", map[string]string{"externalId": "generated-on-replica-a-01"}); err != nil || !wrote {
		t.Fatalf("A: wrote=%v err=%v", wrote, err)
	}
	wrote, err := b.EnsureMCPConnectorFields(ctx, "aws", map[string]string{"externalId": "generated-on-replica-b-02"})
	if err != nil || wrote {
		t.Fatalf("B replaced or failed: wrote=%v err=%v", wrote, err)
	}
	if got := mustMCP(t, b, ctx).Connectors["aws"].Fields["externalId"]; got != "generated-on-replica-a-01" {
		t.Fatalf("B still serves its stale snapshot: externalId = %q", got)
	}
}

// Rotating the JWT secret makes stored secrets undecryptable. That must be
// reported per secret, not fail the whole read, and re-entering recovers.
func TestMCP_UnreadableSecretAfterKeyChange(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())
	mustPutMCP(t, rt, ctx, "gcp", MCPConnectorPatch{Fields: map[string]*string{"projectId": strPtr("acme")}})

	rotated, err := NewRuntime(rt.store, rt.orgStore, config.CopilotConfig{}, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte("a-different-jwt-secret-after-rotation"))
	if err != nil {
		t.Fatalf("rotated runtime: %v", err)
	}
	m, err := rotated.MCP(ctx)
	if err != nil {
		t.Fatalf("an unreadable secret must not fail the read: %v", err)
	}
	aws := m.Connectors["aws"]
	if len(aws.SecretsUnreadable) != 1 || aws.SecretsUnreadable[0] != "externalId" {
		t.Errorf("SecretsUnreadable = %v", aws.SecretsUnreadable)
	}
	if _, ok := aws.Secrets["externalId"]; ok {
		t.Error("an unreadable secret surfaced in Secrets")
	}
	if aws.Fields["roleArn"] == "" || m.Connectors["gcp"].Fields["projectId"] != "acme" {
		t.Error("non-secret config lost with the key")
	}
	masked, _, _ := rotated.MaskedMCPConnector(ctx, "aws")
	if masked.SecretsConfigured["externalId"] || len(masked.SecretsUnreadable) != 1 {
		t.Errorf("masked view should ask for externalId again: configured=%v unreadable=%v", masked.SecretsConfigured, masked.SecretsUnreadable)
	}

	// Re-entering the secret under the new key restores it.
	if err := rotated.PutMCPConnector(ctx, "aws", MCPConnectorPatch{Secrets: map[string]*string{"externalId": strPtr(testExternalID)}}); err != nil {
		t.Fatalf("re-enter: %v", err)
	}
	aws = mustMCP(t, rotated, ctx).Connectors["aws"]
	if aws.Secrets["externalId"] != testExternalID || len(aws.SecretsUnreadable) != 0 {
		t.Errorf("after re-entry: secret=%q unreadable=%v", aws.Secrets["externalId"], aws.SecretsUnreadable)
	}
}

// Two admins saving different connectors at the same time must both land:
// the blob holds every connector, so an unserialized read-modify-write drops
// one of them. Run with -race.
func TestMCP_ConcurrentPutsKeepBothConnectors(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	const rounds = 25

	var wg sync.WaitGroup
	for _, id := range []string{"aws", "gcp", "azure"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				v := fmt.Sprintf("%s-%d", id, i)
				if err := rt.PutMCPConnector(ctx, id, MCPConnectorPatch{
					Enabled: boolPtr(true),
					Fields:  map[string]*string{"marker": &v},
					Secrets: map[string]*string{"token": &v},
				}); err != nil {
					t.Errorf("put %s: %v", id, err)
					return
				}
			}
		}(id)
	}
	// Readers alongside the writers exercise the cache under -race.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, err := rt.MCP(ctx); err != nil {
				t.Errorf("mcp: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	m := mustMCP(t, rt, ctx)
	for _, id := range []string{"aws", "gcp", "azure"} {
		want := fmt.Sprintf("%s-%d", id, rounds-1)
		c, ok := m.Connectors[id]
		if !ok {
			t.Errorf("%s lost to a concurrent write", id)
			continue
		}
		if c.Fields["marker"] != want || c.Secrets["token"] != want {
			t.Errorf("%s: marker=%q token=%q, want %q", id, c.Fields["marker"], c.Secrets["token"], want)
		}
	}
}

// What MCP returns belongs to the caller: the gateway merges a draft over it
// for a connectivity test, and that must not leak into the cache.
func TestMCP_ReturnsCopies(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())

	m := mustMCP(t, rt, ctx)
	m.Connectors["aws"].Fields["roleArn"] = "arn:aws:iam::999999999999:role/Draft"
	m.Connectors["aws"].Secrets["externalId"] = "draft-secret"
	delete(m.Connectors, "aws")

	again := mustMCP(t, rt, ctx).Connectors["aws"]
	if again.Fields["roleArn"] != "arn:aws:iam::123456789012:role/KubeBoltRead" || again.Secrets["externalId"] != testExternalID {
		t.Errorf("caller mutation reached the cache: roleArn=%q", again.Fields["roleArn"])
	}
}

func TestMCP_Validation(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()
	cases := []struct {
		name string
		id   string
		p    MCPConnectorPatch
	}{
		{"empty id", "", MCPConnectorPatch{}},
		{"double underscore id", "aws__x", MCPConnectorPatch{}},
		{"uppercase id", "AWS", MCPConnectorPatch{}},
		{"bad mode", "aws", MCPConnectorPatch{Mode: strPtr("Assume Role")}},
		{"empty field name", "aws", MCPConnectorPatch{Fields: map[string]*string{"": strPtr("x")}}},
		{"empty secret name", "aws", MCPConnectorPatch{Secrets: map[string]*string{"": strPtr("x")}}},
		{"oversized field", "aws", MCPConnectorPatch{Fields: map[string]*string{"roleArn": strPtr(strings.Repeat("a", maxMCPFieldBytes+1))}}},
		{"oversized secret", "aws", MCPConnectorPatch{Secrets: map[string]*string{"credentialJSON": strPtr(strings.Repeat("a", maxMCPSecretBytes+1))}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rt.PutMCPConnector(ctx, tc.id, tc.p)
			if !IsValidation(err) {
				t.Fatalf("want a ValidationError, got %v", err)
			}
		})
	}
	if n := len(mustMCP(t, rt, ctx).Connectors); n != 0 {
		t.Errorf("rejected patches still wrote %d connectors", n)
	}
	// An empty mode clears it and is valid.
	if err := rt.PutMCPConnector(ctx, "aws", MCPConnectorPatch{Mode: strPtr("")}); err != nil {
		t.Errorf("empty mode: %v", err)
	}
	if !IsValidation(rt.DeleteMCPConnector(ctx, "a__b")) {
		t.Error("delete accepted a malformed id")
	}
}

// flakyOrgStore fails reads of the MCP key on demand, to prove a transient
// store error is never mistaken for "nothing configured".
type flakyOrgStore struct {
	auth.OrgSettingStore
	mu      sync.Mutex
	failGet bool
	sets    int
}

func (f *flakyOrgStore) GetOrgSetting(ctx context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	fail := f.failGet
	f.mu.Unlock()
	if fail && key == mcpSettingsKey {
		return nil, errors.New("connection reset by peer")
	}
	return f.OrgSettingStore.GetOrgSetting(ctx, key)
}

func (f *flakyOrgStore) SetOrgSetting(ctx context.Context, key string, value []byte) error {
	f.mu.Lock()
	f.sets++
	f.mu.Unlock()
	return f.OrgSettingStore.SetOrgSetting(ctx, key, value)
}

func TestMCP_TransientReadErrorNeverOverwrites(t *testing.T) {
	base := newTestRuntime(t)
	flaky := &flakyOrgStore{OrgSettingStore: base.orgStore}
	rt, err := NewRuntime(base.store, flaky, config.CopilotConfig{}, config.NotificationsConfig{}, config.AuthConfig{}, config.GeneralConfig{}, config.IngestChannelConfig{}, []byte("test-jwt-secret-32-bytes-padding-"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	ctx := context.Background()
	mustPutMCP(t, rt, ctx, "aws", awsPatch())

	flaky.mu.Lock()
	flaky.failGet = true
	setsBefore := flaky.sets
	flaky.mu.Unlock()
	rt.InvalidateMCP(ctx)

	if _, err := rt.MCP(ctx); err == nil {
		t.Error("a store error was reported as an empty configuration")
	}
	if err := rt.PutMCPConnector(ctx, "gcp", MCPConnectorPatch{Enabled: boolPtr(true)}); err == nil {
		t.Error("put succeeded without reading the existing connectors")
	}
	if err := rt.DeleteMCPConnector(ctx, "gcp"); err == nil {
		t.Error("delete succeeded without reading the existing connectors")
	}
	flaky.mu.Lock()
	if flaky.sets != setsBefore {
		t.Errorf("%d writes happened on top of an unread blob", flaky.sets-setsBefore)
	}
	flaky.failGet = false
	flaky.mu.Unlock()

	// The failure was not cached: the next read sees the real config.
	if got := mustMCP(t, rt, ctx).Connectors["aws"].Secrets["externalId"]; got != testExternalID {
		t.Errorf("after recovery: externalId = %q", got)
	}
}
