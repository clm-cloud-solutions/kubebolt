package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// Tests for handleEditMetadata cover four high-risk behaviors:
//
//   1. Patch SHAPE — buildMetadataPatch must emit a JSON merge patch
//      where add-keys are values and remove-keys are nulls. The
//      apiserver interprets nulls as deletes (RFC 7396); a wrong
//      shape silently no-ops or — worse — leaves stale keys.
//   2. VALIDATION — label key grammar, value rules (labels only),
//      add/remove conflict, annotation byte-size cap.
//   3. DIFF computation — added / updated / removed classification
//      drives the response and the audit log.
//   4. APPLY-EDIT (the helper used for the byte-size pre-flight) —
//      must produce the correct post-patch view without mutating
//      the input.

func TestBuildMetadataPatchShape(t *testing.T) {
	body := editMetadataRequest{
		Labels: &metadataMapEdit{
			Add:    map[string]string{"team": "payments", "env": "staging"},
			Remove: []string{"deprecated-tag"},
		},
		Annotations: &metadataMapEdit{
			Add:    map[string]string{"argocd.argoproj.io/sync-wave": "5"},
			Remove: []string{"kustomize.toolkit.fluxcd.io/ssa"},
		},
	}
	got, err := buildMetadataPatch(body)
	if err != nil {
		t.Fatalf("buildMetadataPatch: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	meta := decoded["metadata"].(map[string]any)
	labels := meta["labels"].(map[string]any)
	if labels["team"] != "payments" || labels["env"] != "staging" {
		t.Errorf("labels.add wrong: %v", labels)
	}
	// remove must marshal as JSON null — assert the underlying type
	// is nil, not an empty string.
	if _, has := labels["deprecated-tag"]; !has {
		t.Error("labels remove key must appear in patch (as null)")
	}
	if labels["deprecated-tag"] != nil {
		t.Errorf("labels remove key value = %v, want nil (JSON null = delete)", labels["deprecated-tag"])
	}

	annotations := meta["annotations"].(map[string]any)
	if annotations["argocd.argoproj.io/sync-wave"] != "5" {
		t.Errorf("annotations.add wrong: %v", annotations)
	}
	if annotations["kustomize.toolkit.fluxcd.io/ssa"] != nil {
		t.Errorf("annotations remove must be null, got %v", annotations["kustomize.toolkit.fluxcd.io/ssa"])
	}
}

func TestBuildMetadataPatchOnlyLabels(t *testing.T) {
	body := editMetadataRequest{
		Labels: &metadataMapEdit{Add: map[string]string{"team": "payments"}},
	}
	got, _ := buildMetadataPatch(body)
	var decoded map[string]any
	_ = json.Unmarshal(got, &decoded)
	meta := decoded["metadata"].(map[string]any)
	if _, has := meta["annotations"]; has {
		t.Error("annotations must be absent from patch when not edited (would clobber existing annotations)")
	}
}

// TestBuildMetadataPatchRawBytes asserts that the raw JSON contains
// the literal `null` token for remove keys — easy to regress if a
// future refactor switches mergeAddRemove to a typed map.
func TestBuildMetadataPatchRawBytes(t *testing.T) {
	body := editMetadataRequest{
		Labels: &metadataMapEdit{Remove: []string{"old-team"}},
	}
	got, _ := buildMetadataPatch(body)
	if !strings.Contains(string(got), `"old-team":null`) {
		t.Errorf("expected `\"old-team\":null` in patch, got %s", got)
	}
}

func TestValidateMetadataMapLabelKeyGrammar(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"simple", "team", false},
		{"with-prefix", "kubebolt.io/team", false},
		{"with-dashes", "team-name", false},
		{"empty", "", true},
		{"space", "team name", true},
		{"leading-dash", "-team", true},
		{"trailing-dash", "team-", true},
		{"slash-only", "/team", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edit := &metadataMapEdit{Add: map[string]string{c.key: "x"}}
			err := validateMetadataMap("labels", edit, true)
			if c.wantErr && err == nil {
				t.Errorf("expected error for key %q, got nil", c.key)
			}
			if !c.wantErr && err != nil {
				t.Errorf("unexpected error for key %q: %v", c.key, err)
			}
		})
	}
}

func TestValidateMetadataMapLabelValueRules(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"empty", "", false},
		{"normal", "payments", false},
		{"with-dots", "v2.9.1", false},
		{"with-dashes", "us-east-1", false},
		{"too-long", strings.Repeat("a", 64), true},
		{"with-space", "us east", true},
		{"leading-dash", "-bad", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edit := &metadataMapEdit{Add: map[string]string{"team": c.value}}
			err := validateMetadataMap("labels", edit, true)
			if c.wantErr && err == nil {
				t.Errorf("expected error for value %q, got nil", c.value)
			}
			if !c.wantErr && err != nil {
				t.Errorf("unexpected error for value %q: %v", c.value, err)
			}
		})
	}
}

// TestValidateMetadataMapAnnotationsValueLoose — annotations must
// accept anything, including newlines and JSON blobs. validation
// for annotations skips the value-content check.
func TestValidateMetadataMapAnnotationsValueLoose(t *testing.T) {
	edit := &metadataMapEdit{
		Add: map[string]string{
			"argocd.argoproj.io/sync-options": "Prune=false,Replace=true",
			"json-blob":                       `{"deeply": {"nested": "yes"}}`,
			"with-newlines":                   "line1\nline2",
		},
	}
	if err := validateMetadataMap("annotations", edit, false); err != nil {
		t.Errorf("annotations should accept arbitrary values, got: %v", err)
	}
}

func TestValidateMetadataMapAddRemoveConflict(t *testing.T) {
	edit := &metadataMapEdit{
		Add:    map[string]string{"team": "payments"},
		Remove: []string{"team"},
	}
	err := validateMetadataMap("labels", edit, true)
	if err == nil {
		t.Fatal("expected error for add+remove conflict on same key")
	}
	if !strings.Contains(err.Error(), "team") {
		t.Errorf("error should name the conflicting key, got: %v", err)
	}
}

func TestApplyMapEditAddRemove(t *testing.T) {
	current := map[string]string{
		"team":           "payments",
		"deprecated-tag": "yes",
	}
	edit := &metadataMapEdit{
		Add:    map[string]string{"team": "platform" /* update */, "env": "prod" /* add */},
		Remove: []string{"deprecated-tag"},
	}
	got := applyMapEdit(current, edit)

	if got["team"] != "platform" {
		t.Errorf("team should be updated to platform, got %q", got["team"])
	}
	if got["env"] != "prod" {
		t.Errorf("env should be added, got %q", got["env"])
	}
	if _, has := got["deprecated-tag"]; has {
		t.Error("deprecated-tag should be removed")
	}
	// Input must not be mutated.
	if _, has := current["env"]; has {
		t.Error("applyMapEdit mutated the input map")
	}
	if current["team"] != "payments" {
		t.Error("applyMapEdit mutated the input map")
	}
}

func TestComputeMapDiff(t *testing.T) {
	from := map[string]string{
		"team":           "payments",
		"env":            "staging",
		"deprecated-tag": "yes",
	}
	to := map[string]string{
		"team":   "platform", // updated
		"env":    "staging",  // unchanged
		"region": "us-east",  // added
	}
	diff := computeMapDiff(from, to)

	expectIn := func(slice []string, key string) {
		t.Helper()
		for _, s := range slice {
			if s == key {
				return
			}
		}
		t.Errorf("expected %q in %v", key, slice)
	}
	expectIn(diff.Added, "region")
	expectIn(diff.Updated, "team")
	expectIn(diff.Removed, "deprecated-tag")

	if len(diff.Added) != 1 || len(diff.Updated) != 1 || len(diff.Removed) != 1 {
		t.Errorf("diff classification wrong: added=%v updated=%v removed=%v", diff.Added, diff.Updated, diff.Removed)
	}
}

func TestTotalAnnotationsBytes(t *testing.T) {
	m := map[string]string{
		"a":   "b",          // 2 bytes
		"key": "value-here", // 13 bytes
	}
	if got := totalAnnotationsBytes(m); got != 15 {
		t.Errorf("totalAnnotationsBytes = %d, want 15", got)
	}
}

// TestStringMapFromInterface — exercises the type-assertion path
// used to extract maps from unstructured.Unstructured.Object.
func TestStringMapFromInterface(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want map[string]string
	}{
		{
			name: "happy path",
			in:   map[string]interface{}{"team": "payments", "env": "prod"},
			want: map[string]string{"team": "payments", "env": "prod"},
		},
		{
			name: "non-string values dropped",
			in:   map[string]interface{}{"team": "payments", "broken": 42},
			want: map[string]string{"team": "payments"},
		},
		{
			name: "nil",
			in:   nil,
			want: map[string]string{},
		},
		{
			name: "wrong type",
			in:   "not a map",
			want: map[string]string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stringMapFromInterface(c.in)
			if len(got) != len(c.want) {
				t.Errorf("len = %d, want %d (got %v)", len(got), len(c.want), got)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestMergeAddRemoveProducesNullForRemoves(t *testing.T) {
	edit := &metadataMapEdit{
		Add:    map[string]string{"keep": "x"},
		Remove: []string{"drop"},
	}
	got := mergeAddRemove(edit)
	if got["keep"] != "x" {
		t.Errorf("add not preserved: %v", got)
	}
	v, has := got["drop"]
	if !has {
		t.Error("remove key must appear in merged map (as nil so json.Marshal emits null)")
	}
	if v != nil {
		t.Errorf("remove value = %v, want nil", v)
	}
}
