package mcpcatalog

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
)

// The deny list is a barrier of its own, not only a check on the table: a
// denied tool that someone adds to the table by mistake is still not bound.
// The table test keeps such entries out; this proves bind refuses them anyway.
func TestGCPBindRefusesDeniedToolsEvenWhenTabled(t *testing.T) {
	ro, rw := gcpBool(true), gcpBool(false)
	name := map[string]any{"name": gcpStr("Name.")}
	rule := gcpToolRule{kobi: true, autopilot: true, paths: []string{"name"}}
	cases := map[string]struct {
		service string
		tool    wire.Tool
		want    string
	}{
		"redis AUTH string":        {"redis", gcpTool("get_instance_auth_string", ro, name, "name"), "denied"},
		"SQL marked read-only":     {"sqladmin", gcpTool("execute_sql_readonly", ro, name), "denied"},
		"GKE objects and logs":     {"container", gcpTool("get_k8s_logs", ro, name), "denied"},
		"write marked read-only":   {"sqladmin", gcpTool("update_instance", ro, name), "denied"},
		"denied service":           {"bigquery", gcpTool("list_dataset_ids", ro, name), "denied"},
		"write annotated as write": {"container", gcpTool("get_cluster", rw, name, "name"), "read-only"},
		"no annotations":           {"container", gcpTool("get_cluster", nil, name, "name"), "read-only"},
	}
	for label, c := range cases {
		s := gcpService{id: c.service, family: gcpFamilyGKE, tools: map[string]gcpToolRule{c.tool.Name: rule}}
		ep := &gcpEndpoint{id: c.service, host: c.service + ".googleapis.com"}
		bt, reason := bindGCPTool(ep, &s, c.tool, gcpTestProject, "")
		if bt != nil || !strings.Contains(reason, c.want) {
			t.Errorf("%s: bound=%v reason=%q, want a refusal mentioning %q", label, bt != nil, reason, c.want)
		}
	}
}

// A catalog that lists a name twice is ambiguous; neither entry is bound,
// whichever of them looks read-only.
func TestGCPDuplicateCatalogEntriesAreNotBound(t *testing.T) {
	ro, rw := gcpBool(true), gcpBool(false)
	parent := map[string]any{"parent": gcpStr("Parent.")}
	f := newGCPFake(t, map[string][]wire.Tool{
		"container": {
			gcpTool("list_clusters", rw, parent, "parent"),
			gcpTool("list_clusters", ro, parent, "parent"),
			gcpTool("list_operations", ro, parent, "parent"),
			gcpTool("list_operations", ro, parent, "parent"),
			gcpTool("list_node_pools", ro, parent, "parent"),
		},
	})
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	if got := gcpSpecNames(bd.Tools(ConsumerKobi)); !reflect.DeepEqual(got, []string{"container_list_node_pools"}) {
		t.Fatalf("kobi tools = %v", got)
	}
	res, err := bd.Call(t.Context(), ConsumerKobi, "container_list_clusters", json.RawMessage(`{"parent":"projects/`+gcpTestProject+`/locations/-"}`))
	if err != nil || !res.IsError || len(f.calls()) != 0 {
		t.Fatalf("a duplicated tool was called: %+v %v (calls %d)", res, err, len(f.calls()))
	}
}

// gcpNestedFake serves list_timeseries with object arguments shaped like
// Google's (interval, aggregation), and a free-form labels map.
func gcpNestedFake(t *testing.T, extraIntervalProps map[string]any) *gcpFake {
	interval := map[string]any{"startTime": gcpStr("Start."), "endTime": gcpStr("End.")}
	for k, v := range extraIntervalProps {
		interval[k] = v
	}
	props := map[string]any{
		"name":     gcpStr("N."),
		"filter":   gcpStr("F."),
		"interval": map[string]any{"type": "object", "properties": interval},
		"aggregation": map[string]any{"type": "object", "properties": map[string]any{
			"alignmentPeriod": gcpStr("P."),
			"groupByFields":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}},
		"view": map[string]any{"type": "string", "enum": []any{"FULL", "HEADERS"}},
	}
	return newGCPFake(t, map[string][]wire.Tool{
		"monitoring": {gcpTool("list_timeseries", gcpBool(true), props, "name", "filter")},
		"logging": {gcpTool("list_log_entries", gcpBool(true), map[string]any{
			"resourceNames": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"filter":        gcpStr("F."),
			// A list of objects, to reach the items branch.
			"orderBy": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"field": gcpStr("Field."),
			}}},
		}, "resourceNames")},
	})
}

// Only top-level fields are pinned, so a schema whose object argument grew a
// field that could name a resource is not bound.
func TestGCPNestedScopedFieldBlocksTheBind(t *testing.T) {
	for _, nested := range []string{"project", "projectId", "parent", "name", "resourceNames"} {
		f := gcpNestedFake(t, map[string]any{nested: gcpStr("Grew.")})
		bd := f.bind(t, gcpTestResolved(gcpTestProject))
		for _, s := range bd.Tools(ConsumerKobi) {
			if s.Name == "monitoring_list_timeseries" {
				t.Errorf("bound although interval.%s could name a resource", nested)
			}
		}
	}
	// Inside a list of objects too.
	s := gcpService{id: "logging", family: gcpFamilyLogs, tools: map[string]gcpToolRule{
		"list_log_entries": {kobi: true, pathLists: []string{"resourceNames"}},
	}}
	tool := gcpTool("list_log_entries", gcpBool(true), map[string]any{
		"resourceNames": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"sources": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
			"projectId": gcpStr("P."),
		}}},
	})
	if bt, reason := bindGCPTool(&gcpEndpoint{id: "logging"}, &s, tool, gcpTestProject, ""); bt != nil || !strings.Contains(reason, "sources[].projectId") {
		t.Fatalf("bound=%v reason=%q", bt != nil, reason)
	}
}

// Inside object arguments, only keys the model was shown are forwarded:
// Google's parser would also take a snake_case alias or a field its schema no
// longer shows.
func TestGCPNestedUnknownKeysAreRefused(t *testing.T) {
	f := gcpNestedFake(t, nil)
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	p := gcpTestProject

	for label, args := range map[string]string{
		"extra key in an object":     `{"filter":"x","interval":{"startTime":"a","project":"other-project"}}`,
		"snake_case alias":           `{"filter":"x","aggregation":{"alignment_period":"60s"}}`,
		"case variant":               `{"filter":"x","interval":{"StartTime":"a"}}`,
		"extra key in a list object": `{"orderBy":[{"field":"timestamp","project":"other-project"}]}`,
	} {
		tool := "monitoring_list_timeseries"
		if strings.Contains(args, "orderBy") {
			tool = "logging_list_log_entries"
		}
		res, err := bd.Call(t.Context(), ConsumerKobi, tool, json.RawMessage(args))
		if err != nil || !res.IsError || !strings.HasPrefix(res.Text, "Not sent to Google Cloud") || res.Audit["refused"] != "arguments" {
			t.Errorf("%s: res=%+v err=%v", label, res, err)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("refused calls reached Google %d times", n)
	}

	ok := `{"filter":"x","view":"FULL","interval":{"startTime":"a","endTime":"b"},"aggregation":{"alignmentPeriod":"60s","groupByFields":["resource.label.zone"]}}`
	res, err := bd.Call(t.Context(), ConsumerKobi, "monitoring_list_timeseries", json.RawMessage(ok))
	if err != nil || res.IsError {
		t.Fatalf("valid nested arguments refused: %+v %v", res, err)
	}
	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	got := calls[0].args
	if got["name"] != "projects/"+p || !reflect.DeepEqual(got["interval"], map[string]any{"startTime": "a", "endTime": "b"}) {
		t.Fatalf("forwarded %v", got)
	}
	// A value of the wrong type is Google's to reject, not a smuggled key.
	if res, err := bd.Call(t.Context(), ConsumerKobi, "monitoring_list_timeseries", json.RawMessage(`{"filter":"x","interval":"last hour"}`)); err != nil || res.IsError {
		t.Fatalf("type mismatch refused by KubeBolt: %+v %v", res, err)
	}
}

// A free-form object (a map, no declared properties) takes any key.
func TestGCPNestedFreeFormObjects(t *testing.T) {
	schema := map[string]any{"type": "object"}
	if err := gcpCheckNestedKeys("labels", json.RawMessage(`{"team":"a","env":"b"}`), schema, 0); err != nil {
		t.Fatal(err)
	}
	strict := map[string]any{"type": "object", "properties": map[string]any{}}
	if err := gcpCheckNestedKeys("empty", json.RawMessage(`{"x":1}`), strict, 0); err == nil {
		t.Fatal("an object with declared (empty) properties took an undeclared key")
	}
}

// The connection test's canary goes through the same gate as a model's call:
// when list_clusters is not bound it is not called at all.
func TestGCPCanaryOnlyThroughABoundTool(t *testing.T) {
	parent := map[string]any{"parent": gcpStr("Parent.")}
	f := newGCPFake(t, map[string][]wire.Tool{
		"container": {
			gcpTool("list_clusters", gcpBool(false), parent, "parent"), // Google flipped it
			gcpTool("list_node_pools", gcpBool(true), parent, "parent"),
		},
	})
	bd := f.bind(t, gcpTestResolved(gcpTestProject))
	tr, err := bd.Test(t.Context())
	if err != nil || tr.OK || tr.Canary == nil || tr.Canary.OK || tr.Error == "" || tr.Hint == "" {
		t.Fatalf("test = %+v, %v", tr, err)
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("the canary called an unbound tool %d times", n)
	}
}

// $defs that reference each other several times multiply at every level;
// the expansion is bounded even while the depth stays legal.
func TestGCPSchemaExpansionIsBounded(t *testing.T) {
	const fanOut, levels = 10, 5 // 10^4 nodes at depth 5
	defs := map[string]any{}
	for l := 0; l < levels; l++ {
		props := map[string]any{}
		for i := 0; i < fanOut; i++ {
			if l == levels-1 {
				props[fmt.Sprintf("f%d", i)] = map[string]any{"type": "string"}
			} else {
				props[fmt.Sprintf("f%d", i)] = map[string]any{"$ref": fmt.Sprintf("#/$defs/L%d", l+1)}
			}
		}
		defs[fmt.Sprintf("L%d", l)] = map[string]any{"type": "object", "properties": props}
	}
	bomb := map[string]any{
		"type":       "object",
		"$defs":      defs,
		"properties": map[string]any{"root": map[string]any{"$ref": "#/$defs/L0"}},
	}
	if _, err := gcpNormalizeSchema(bomb); err == nil || !strings.Contains(err.Error(), "expands") {
		t.Fatalf("err = %v", err)
	}
	// Every allowlisted schema in Google's real catalog fits.
	for svc, tools := range gcpSnapshotTools(t) {
		for _, tool := range tools {
			if _, err := gcpNormalizeSchema(tool.InputSchema); err != nil {
				t.Errorf("%s/%s: %v", svc, tool.Name, err)
			}
		}
	}
}
