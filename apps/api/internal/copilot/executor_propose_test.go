package copilot

import (
	"strings"
	"testing"
)

// Tests for the new propose_* helpers added in 06-insight-rule-coverage.
// The existing executor's propose_* cases are not unit-tested through
// the full handler (no Connector mocking infrastructure exists in this
// package), so we test what we can in isolation:
//
//   - The argument parsers normalize / reject correctly.
//   - The credential guardrail regex matches the right names.
//   - The container-name extraction helpers walk the detail map.
//
// In-vivo verification of the full executor flow happens at smoke-test
// time against a real cluster — see 06-insight-rule-coverage.md §"In-vivo
// verification" for the manual checklist.

// ─── parseSetResourcesContainers ─────────────────────────────────────

func TestParseSetResourcesContainersHappyPath(t *testing.T) {
	in := []interface{}{
		map[string]interface{}{
			"container": "api",
			"requests":  map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
			"limits":    map[string]interface{}{"memory": "256Mi"},
		},
		map[string]interface{}{
			"container":     "init",
			"initContainer": true,
			"requests":      map[string]interface{}{"cpu": "50m"},
		},
	}
	got, err := parseSetResourcesContainers(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	if got[0]["container"] != "api" {
		t.Errorf("row 0 container: %v", got[0]["container"])
	}
	requests, ok := got[0]["requests"].(map[string]interface{})
	if !ok || requests["cpu"] != "100m" || requests["memory"] != "128Mi" {
		t.Errorf("row 0 requests: %v", got[0]["requests"])
	}
	if got[1]["initContainer"] != true {
		t.Errorf("row 1 initContainer flag dropped: %v", got[1])
	}
}

func TestParseSetResourcesContainersRejectsBadShapes(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"not an array", "oops", "must be an array"},
		{
			"row not an object",
			[]interface{}{"oops"},
			"must be an object",
		},
		{
			"missing container",
			[]interface{}{map[string]interface{}{"requests": map[string]interface{}{"cpu": "100m"}}},
			"container is required",
		},
		{
			"no requests AND no limits",
			[]interface{}{map[string]interface{}{"container": "api"}},
			"at least one of requests/limits",
		},
		{
			"empty-string values stripped → also rejected",
			[]interface{}{map[string]interface{}{
				"container": "api",
				"requests":  map[string]interface{}{"cpu": "", "memory": ""},
			}},
			"at least one of requests/limits",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSetResourcesContainers(tc.in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error=%q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// ─── parseSetImageEntries ────────────────────────────────────────────

func TestParseSetImageEntriesHappyPath(t *testing.T) {
	in := []interface{}{
		map[string]interface{}{"container": "api", "image": "ghcr.io/acme/api:v2"},
	}
	got, err := parseSetImageEntries(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0]["container"] != "api" || got[0]["image"] != "ghcr.io/acme/api:v2" {
		t.Errorf("got=%v", got)
	}
}

func TestParseSetImageEntriesRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"not an array", 42, "must be an array"},
		{
			"missing container",
			[]interface{}{map[string]interface{}{"image": "x:y"}},
			"container is required",
		},
		{
			"missing image",
			[]interface{}{map[string]interface{}{"container": "api"}},
			"image is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSetImageEntries(tc.in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error=%q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// ─── parseSetEnvContainers ───────────────────────────────────────────

func TestParseSetEnvContainersHappyPath(t *testing.T) {
	in := []interface{}{
		map[string]interface{}{
			"container": "api",
			"env": []interface{}{
				map[string]interface{}{"name": "LOG_LEVEL", "action": "set", "value": "info"},
				map[string]interface{}{"name": "OLD_FLAG", "action": "remove"},
			},
		},
	}
	got, err := parseSetEnvContainers(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1", len(got))
	}
	envList, _ := got[0]["env"].([]map[string]interface{})
	if len(envList) != 2 {
		t.Fatalf("env len=%d, want 2", len(envList))
	}
	if envList[0]["name"] != "LOG_LEVEL" || envList[0]["action"] != "set" || envList[0]["value"] != "info" {
		t.Errorf("env[0]=%v", envList[0])
	}
	if envList[1]["name"] != "OLD_FLAG" || envList[1]["action"] != "remove" {
		t.Errorf("env[1]=%v", envList[1])
	}
}

func TestParseSetEnvContainersRejectsEmptyEnvList(t *testing.T) {
	in := []interface{}{
		map[string]interface{}{
			"container": "api",
			"env":       []interface{}{},
		},
	}
	_, err := parseSetEnvContainers(in)
	if err == nil || !strings.Contains(err.Error(), "env is required") {
		t.Errorf("expected env-required error, got %v", err)
	}
}

// ─── credentialNameRE ────────────────────────────────────────────────

func TestCredentialNameRE(t *testing.T) {
	shouldMatch := []string{
		"DB_PASSWORD",
		"API_SECRET",
		"AUTH_TOKEN",
		"PRIVATE_KEY",
		"USER_CREDENTIAL",
		"my_password_v2",
		"jwtSecret",   // case-insensitive
		"GITHUB_TOKEN", // common in CI
	}
	for _, s := range shouldMatch {
		if !credentialNameRE.MatchString(s) {
			t.Errorf("expected match for %q", s)
		}
	}
	shouldNotMatch := []string{
		"LOG_LEVEL",
		"DATABASE_URL", // 'database' alone is not flagged
		"PORT",
		"NODE_ENV",
		"REGION",
		"FEATURE_FLAG_X",
	}
	for _, s := range shouldNotMatch {
		if credentialNameRE.MatchString(s) {
			t.Errorf("expected NO match for %q", s)
		}
	}
}

// ─── container extraction helpers ────────────────────────────────────

func TestExtractContainerNamesAndImages(t *testing.T) {
	detail := map[string]interface{}{
		"containers": []map[string]interface{}{
			{"name": "api", "image": "ghcr.io/acme/api:v1"},
			{"name": "sidecar", "image": "ghcr.io/acme/sidecar:v3"},
		},
	}
	names := extractContainerNames(detail)
	if !names["api"] || !names["sidecar"] || len(names) != 2 {
		t.Errorf("names=%v", names)
	}
	images := extractContainerImages(detail)
	if images["api"] != "ghcr.io/acme/api:v1" {
		t.Errorf("images[api]=%q", images["api"])
	}
	if images["sidecar"] != "ghcr.io/acme/sidecar:v3" {
		t.Errorf("images[sidecar]=%q", images["sidecar"])
	}
}

func TestExtractContainerNamesEmpty(t *testing.T) {
	// Detail without a `containers` key (cluster-scoped resource) →
	// helpers return empty maps, not nil-dereference panics.
	if got := extractContainerNames(map[string]interface{}{}); len(got) != 0 {
		t.Errorf("names on empty detail: %v", got)
	}
	if got := extractContainerImages(map[string]interface{}{}); len(got) != 0 {
		t.Errorf("images on empty detail: %v", got)
	}
}

// ─── hpaMaxReplicasCap ───────────────────────────────────────────────

func TestHpaMaxReplicasCapMirrorsApiLayer(t *testing.T) {
	// This value MUST mirror api.maxReplicasSafetyCap. If they drift,
	// Kobi will surface a proposal the api layer then rejects. The api
	// layer's TestMaxReplicasSafetyCapDefined also pins to 1000.
	if hpaMaxReplicasCap != 1000 {
		t.Errorf("hpaMaxReplicasCap=%d, want 1000 (must match api.maxReplicasSafetyCap)", hpaMaxReplicasCap)
	}
}
