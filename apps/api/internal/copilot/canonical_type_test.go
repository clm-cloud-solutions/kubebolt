package copilot

import "testing"

// The spellings below are not invented: they are the `type` values models
// actually passed to get_resource_describe in Autopilot's recorded runs
// (pod ×430, Pod ×21, deployment ×15, Deployment, node) — every one of which
// failed before CanonicalResourceType existed.
func TestCanonicalResourceType_ModelSpellings(t *testing.T) {
	cases := map[string]string{
		// what models send
		"pod":        "pods",
		"Pod":        "pods",
		"deployment": "deployments",
		"Deployment": "deployments",
		"node":       "nodes",
		" pod ":      "pods",
		// already canonical — must be untouched
		"pods":        "pods",
		"deployments": "deployments",
		"Pods":        "pods",
		// Kinds shared by several keys resolve to KubeBolt's own alias
		// (the shortest), deterministically.
		"persistentvolumeclaim":   "pvcs",
		"PersistentVolumeClaim":   "pvcs",
		"horizontalpodautoscaler": "hpas",
		"poddisruptionbudget":     "pdbs",
		"endpointslice":           "endpoints",
		// a real key is never rewritten to its shorter sibling
		"persistentvolumeclaims":   "persistentvolumeclaims",
		"horizontalpodautoscalers": "horizontalpodautoscalers",
		"endpointslices":           "endpointslices",
		// unknown passes through, so the downstream error still names it
		"gizmo": "gizmo",
		"":      "",
	}
	for in, want := range cases {
		if got := CanonicalResourceType(in); got != want {
			t.Errorf("CanonicalResourceType(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every Kind in the map must be reachable — so a type added to
// ResourceTypeToGroupKind is callable by its Kind with no second edit.
func TestCanonicalResourceType_EveryKindResolves(t *testing.T) {
	for key, gk := range ResourceTypeToGroupKind {
		got := CanonicalResourceType(gk.Kind)
		if ResourceTypeToGroupKind[got].Kind != gk.Kind {
			t.Errorf("Kind %q (from key %q) resolved to %q, which is not a %s", gk.Kind, key, got, gk.Kind)
		}
	}
}

// The failing path end to end: nsResourceArgs is what describe, detail and
// yaml read their type through.
func TestNsResourceArgs_NormalisesType(t *testing.T) {
	typ, ns, name := nsResourceArgs(map[string]interface{}{
		"type": "Pod", "namespace": "autopilot-demo", "name": "image-app-7c6c5bbdb-lh5fj",
	})
	if typ != "pods" || ns != "autopilot-demo" || name != "image-app-7c6c5bbdb-lh5fj" {
		t.Fatalf("nsResourceArgs = (%q, %q, %q)", typ, ns, name)
	}
}
