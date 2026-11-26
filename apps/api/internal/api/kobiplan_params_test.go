package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// jsonShape describes a type as its JSON sees it: every field's JSON name and
// shape, at every level. Two types with the same shape encode the same.
func jsonShape(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return jsonShape(t.Elem())
	case reflect.Slice:
		return "[" + jsonShape(t.Elem()) + "]"
	case reflect.Map:
		return "map[" + jsonShape(t.Key()) + "]" + jsonShape(t.Elem())
	case reflect.Struct:
		var fields []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" || !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields = append(fields, name+":"+jsonShape(f.Type))
		}
		sort.Strings(fields)
		return "{" + strings.Join(fields, ",") + "}"
	}
	return t.Kind().String()
}

// A field added to either type, at any level, changes its shape.
func TestKobiPlanSetResourcesParamsHaveTheEndpointsShape(t *testing.T) {
	endpoint := jsonShape(reflect.TypeOf(setResourcesRequest{}))
	plan := jsonShape(reflect.TypeOf(kobiplan.SetResourcesParams{}))
	if endpoint != plan {
		t.Fatalf("the plan's set_resources params drifted from the endpoint's body:\nendpoint %s\n    plan %s", endpoint, plan)
	}
}

// A plan step's set_resources params are sent to this endpoint byte for byte
// (Kobi Plans spec). kobiplan.SetResourcesParams is the plan's copy of
// setResourcesRequest; a field added to one and not the other would be
// dropped silently on the way, so both directions must round-trip whole.
func TestKobiPlanSetResourcesParamsMatchTheEndpoint(t *testing.T) {
	s := func(v string) *string { return &v }

	// The endpoint's body, every field set, into the plan's type.
	endpoint := setResourcesRequest{Containers: []containerResourcesPatch{{
		Container: "api", InitContainer: true,
		Requests: &resourceQuantity{CPU: s("120m"), Memory: s("300Mi")},
		Limits:   &resourceQuantity{CPU: s("1"), Memory: s("1Gi")},
	}}}
	want, _ := json.Marshal(endpoint)
	plan, err := kobiplan.DecodeSetResources(want)
	if err != nil {
		t.Fatalf("the plan's type refuses the endpoint's body: %v", err)
	}
	if got, _ := json.Marshal(plan); !bytes.Equal(got, want) {
		t.Fatalf("endpoint → plan lost something:\n got %s\nwant %s", got, want)
	}

	// And the plan's params, every field set, into the endpoint's type.
	fromPlan := kobiplan.SetResourcesParams{Containers: []kobiplan.ContainerResources{{
		Container: "api", InitContainer: true,
		Requests: &kobiplan.Resources{CPU: s("120m"), Memory: s("300Mi")},
		Limits:   &kobiplan.Resources{CPU: s("1"), Memory: s("1Gi")},
	}}}
	want, _ = json.Marshal(fromPlan)
	var body setResourcesRequest
	dec := json.NewDecoder(bytes.NewReader(want))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("the endpoint refuses the plan's params: %v", err)
	}
	if got, _ := json.Marshal(body); !bytes.Equal(got, want) {
		t.Fatalf("plan → endpoint lost something:\n got %s\nwant %s", got, want)
	}
}
