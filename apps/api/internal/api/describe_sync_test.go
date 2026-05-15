package api

import (
	"reflect"
	"sort"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// TestDescribeMapsInSync pins the contract that the REST endpoint's describe
// support and the Copilot tool executor's describe support carry IDENTICAL
// type → GroupKind mappings. The two maps live in different packages
// (apps/api/internal/api/describe.go vs apps/api/internal/copilot/describe.go)
// because the executor doesn't go through the HTTP handler — it calls
// describeResource directly with its own map.
//
// Drift between them produces a confusing failure mode: the operator's UI
// Describe button works for a type, but Kobi reports it as unsupported (or
// vice versa) because the path the request takes determines which map is
// consulted. This already bit us once on 2026-05-15 with resourcequotas:
// the api map was updated but the copilot mirror was missed, and Kobi
// kept refusing the type while curl-against-the-endpoint succeeded.
//
// If you intentionally need to drop a type from one map, refactor BOTH
// packages to share a single source rather than letting them silently
// diverge here. SPEC §3.2.1 has the full coverage roadmap.
func TestDescribeMapsInSync(t *testing.T) {
	apiKeys := sortedKeys(resourceTypeToGroupKind)
	copilotKeys := sortedKeysCopilot(copilot.ResourceTypeToGroupKind)

	if !reflect.DeepEqual(apiKeys, copilotKeys) {
		// Compute the symmetric difference so the failure message tells the
		// developer exactly which entries to add and where.
		missingInCopilot := setSubtract(apiKeys, copilotKeys)
		missingInAPI := setSubtract(copilotKeys, apiKeys)
		t.Errorf(
			"describe maps drifted between packages.\n"+
				"  Missing in apps/api/internal/copilot/describe.go: %v\n"+
				"  Missing in apps/api/internal/api/describe.go:     %v\n"+
				"Add the missing entries to keep the UI Describe button and Kobi's get_resource_describe in lockstep.",
			missingInCopilot, missingInAPI,
		)
	}

	// Same keys is necessary but not sufficient — verify the GroupKind
	// values match too. A typo in one map (e.g. wrong API group) would
	// route the request to a non-existent describer at runtime.
	for k, vAPI := range resourceTypeToGroupKind {
		if vCopilot, ok := copilot.ResourceTypeToGroupKind[k]; ok && vCopilot != vAPI {
			t.Errorf("describe maps disagree on %q: api=%+v copilot=%+v", k, vAPI, vCopilot)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedKeysCopilot is the same as sortedKeys but for the copilot package's
// concrete value type. Inlined as a separate function to avoid importing
// schema.GroupKind into the generic constraint here.
func sortedKeysCopilot[V any](m map[string]V) []string { return sortedKeys(m) }

func setSubtract(a, b []string) []string {
	bset := make(map[string]struct{}, len(b))
	for _, s := range b {
		bset[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := bset[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}
