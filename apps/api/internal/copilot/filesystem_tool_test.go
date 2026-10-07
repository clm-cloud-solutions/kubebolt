package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// The filesystem lane shipped in 2.2.0 with every piece tested on its own —
// the query, the tenant scope, the refusal for pods, the description — and
// the tool refused every call for it: the argument validator had its own
// list of metrics and filesystem was not on it. These tests run the tool the
// way the model calls it.

// fakeTarget is the cluster side of the tool: every target exists, workloads
// own one pod.
type fakeTarget struct{}

func (fakeTarget) GetResourceDetail(_, _, _ string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}
func (fakeTarget) ClusterUID() string { return "uid-9" }
func (fakeTarget) GetDeploymentPods(_, _ string) []map[string]interface{} {
	return []map[string]interface{}{{"name": "web-7d4b9c-abcde"}}
}
func (fakeTarget) GetStatefulSetPods(_, _ string) []map[string]interface{} { return nil }
func (fakeTarget) GetDaemonSetPods(_, _ string) []map[string]interface{}   { return nil }
func (fakeTarget) GetJobPods(_, _ string) []map[string]interface{}         { return nil }
func (fakeTarget) GetCronJobJobs(_, _ string) []map[string]interface{}     { return nil }

// fsVM answers every range query with `result` and records the queries.
func fsVM(t *testing.T, result string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		queries = append(queries, r.Form.Get("query"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[` + result + `]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), queries...)
	}
}

func runMetricsTool(t *testing.T, srv *httptest.Server, args map[string]interface{}) workloadMetricsResponse {
	t.Helper()
	var resp workloadMetricsResponse
	withFakeVM(t, srv, func() {
		ctx := auth.WithTenantID(context.Background(), "org-1")
		out, err := (&Executor{}).execGetWorkloadMetrics(ctx, ToolCall{}, args, fakeTarget{})
		if err != nil {
			t.Fatalf("the tool refused the call: %v", err)
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("response is not JSON: %v\n%s", err, out)
		}
	})
	return resp
}

// The question from 2026-09-16, asked the way the prompt tells Kobi to ask
// it, on a node with node-exporter: two real disks, the data disk filling.
func TestFilesystemTool_NodeEndToEnd(t *testing.T) {
	srv, queries := fsVM(t, `
		{"metric":{"mountpoint":"/"},"values":[[1700000000,"41"],[1700007200,"42"],[1700014400,"42"]]},
		{"metric":{"mountpoint":"/var/lib/containerd"},"values":[[1700000000,"53"],[1700007200,"88.7"],[1700014400,"39.9"]]}`)

	resp := runMetricsTool(t, srv, map[string]interface{}{
		"kind": "Node", "namespace": "_", "name": "aks-default-vmss000004",
		"metrics": []interface{}{"filesystem"}, "range": "24h",
	})

	fs, ok := resp.Metrics["filesystem"]
	if !ok {
		t.Fatalf("no filesystem in the response: %+v", resp.Metrics)
	}
	if fs.Error != "" {
		t.Fatalf("filesystem came back as an error: %s", fs.Error)
	}
	if fs.Unit != "percent" {
		t.Errorf("unit = %q, want percent", fs.Unit)
	}
	// The fullest disk, not the first series and not a sum of percentages.
	if fs.Summary.Max != 88.7 {
		t.Errorf("summary.max = %v, want 88.7 (the disk that filled)", fs.Summary.Max)
	}
	if len(fs.PerMountpoint) != 2 {
		t.Fatalf("perMountpoint = %v, want both disks", fs.PerMountpoint)
	}
	if got := fs.PerMountpoint["/var/lib/containerd"].Summary.Max; got != 88.7 {
		t.Errorf("perMountpoint[/var/lib/containerd].max = %v, want 88.7", got)
	}
	if got := fs.PerMountpoint["/"].Summary.Max; got != 42 {
		t.Errorf("perMountpoint[/].max = %v, want 42", got)
	}

	// What reached VM: this node, this org, the peak of each 2-hour step.
	q := strings.Join(queries(), "\n")
	for _, want := range []string{`node="aks-default-vmss000004"`, `tenant_id="org-1"`, "max_over_time(", "[2h:15m]"} {
		if !strings.Contains(q, want) {
			t.Errorf("query lost %s:\n%s", want, q)
		}
	}
}

// Without node-exporter (every production cluster measured for the lane) the
// agent's fallback is one series with no mountpoint: no split, same reading.
func TestFilesystemTool_SingleSeriesHasNoSplit(t *testing.T) {
	srv, _ := fsVM(t, `{"metric":{"node":"n1"},"values":[[1700000000,"53"],[1700000060,"88.7"]]}`)
	resp := runMetricsTool(t, srv, map[string]interface{}{
		"kind": "Node", "name": "n1", "metrics": []interface{}{"filesystem"},
	})
	fs := resp.Metrics["filesystem"]
	if fs.Summary.Max != 88.7 || fs.PerMountpoint != nil {
		t.Errorf("single series: max=%v perMountpoint=%v", fs.Summary.Max, fs.PerMountpoint)
	}
}

// Refusing filesystem for a workload is right; handing the model an empty
// trend instead of the reason is not — an empty trend reads as idle.
func TestFilesystemTool_RefusalReachesTheModel(t *testing.T) {
	srv, _ := fsVM(t, ``)
	resp := runMetricsTool(t, srv, map[string]interface{}{
		"kind": "Deployment", "namespace": "shop", "name": "web",
		"metrics": []interface{}{"cpu", "filesystem"},
	})
	fs, ok := resp.Metrics["filesystem"]
	if !ok {
		t.Fatal("filesystem dropped from the response")
	}
	if !strings.Contains(fs.Error, "kind=Node") {
		t.Errorf("the refusal did not reach the model: error=%q", fs.Error)
	}
	if resp.Metrics["cpu"].Error != "" {
		t.Errorf("cpu was not refused, but carries an error: %q", resp.Metrics["cpu"].Error)
	}
}

// The schema offers what the validator accepts, and nothing else. They were
// two hand-kept lists, and the gap between them is the 2.2.0 bug.
func TestWorkloadMetrics_SchemaAndValidatorAgree(t *testing.T) {
	var enum []string
	for _, d := range ToolDefinitions() {
		if d.Name != "get_workload_metrics" {
			continue
		}
		props, _ := d.InputSchema["properties"].(map[string]interface{})
		raw, _ := props["metrics"].(map[string]interface{})
		items, _ := raw["items"].(map[string]interface{})
		enum, _ = items["enum"].([]string)
	}
	if len(enum) == 0 {
		t.Fatal("get_workload_metrics has no metric enum")
	}
	for _, name := range enum {
		if _, err := parseMetricsArg([]interface{}{name}); err != nil {
			t.Errorf("the schema offers %q and the validator refuses it: %v", name, err)
		}
	}
	_, err := parseMetricsArg([]interface{}{"disk_io"})
	if err == nil || !strings.Contains(err.Error(), "filesystem") {
		t.Errorf("the error for an unknown metric must list the valid set: %v", err)
	}
}
