package api

import (
	"sort"
	"testing"
)

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A row keeps what the Pods tab, the Logs and Terminal pickers, the rollout
// panel, the node note and Autopilot read, and nothing else.
func TestPodListRows_KeepsOnlyWhatReadersUse(t *testing.T) {
	full := map[string]interface{}{
		"name": "web-7d9-a", "namespace": "shop", "uid": "u-1", "status": "Running", "ready": "1/2",
		"restarts": int32(3), "nodeName": "node-a", "ip": "10.0.0.7", "hostIP": "192.0.2.10",
		"createdAt": "2026-11-09T10:00:00Z", "age": "5m", "startTime": "2026-11-09T10:00:01Z",
		"hostNetwork":     false,
		"labels":          map[string]string{"app": "web"},
		"annotations":     map[string]string{"a": "b"},
		"ownerReferences": []map[string]interface{}{{"kind": "ReplicaSet"}},
		"conditions":      []map[string]interface{}{{"type": "Ready"}},
		"volumes":         []map[string]interface{}{{"name": "data"}},
		"cpuRequest":      int64(100), "cpuLimit": int64(500), "memoryRequest": int64(1 << 20), "memoryLimit": int64(1 << 21),
		"cpuUsage": int64(40), "memoryUsage": int64(1 << 19), "cpuPercent": 8.0, "memoryPercent": 25.0,
		"containers": []map[string]interface{}{
			{"name": "web", "image": "web:1", "ports": []int{8080}, "resources": map[string]interface{}{}, "volumeMounts": []string{"/data"},
				"imagePullPolicy": "IfNotPresent", "state": map[string]interface{}{"state": "running"}, "ready": true},
			{"name": "debugger-x", "ephemeral": true, "image": "busybox", "state": map[string]interface{}{"state": "terminated"}},
		},
	}
	rows := podListRows([]map[string]interface{}{full})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]

	want := []string{"containers", "cpuLimit", "cpuPercent", "cpuRequest", "cpuUsage", "createdAt", "hostNetwork", "ip",
		"memoryLimit", "memoryPercent", "memoryRequest", "memoryUsage", "name", "namespace", "nodeName", "ready", "restarts", "status"}
	if got := keysOf(row); len(got) != len(want) {
		t.Fatalf("row keys = %v, want %v", got, want)
	} else {
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("row keys = %v, want %v", got, want)
			}
		}
	}
	if row["ready"] != "1/2" || row["status"] != "Running" || row["nodeName"] != "node-a" {
		t.Errorf("values changed: %v", row)
	}

	cs := row["containers"].([]map[string]interface{})
	if len(cs) != 2 {
		t.Fatalf("got %d containers, want 2", len(cs))
	}
	if got := keysOf(cs[0]); len(got) != 3 || got[0] != "name" || got[1] != "ready" || got[2] != "state" {
		t.Errorf("container keys = %v, want [name ready state]", got)
	}
	if cs[1]["ephemeral"] != true || cs[1]["state"].(map[string]interface{})["state"] != "terminated" {
		t.Errorf("the ephemeral container lost what the Terminal picker reads: %v", cs[1])
	}
}

// The Pods tab tells "no metrics" from "zero" by whether cpuUsage exists, so
// a row without metrics must not gain empty keys.
func TestPodListRows_AbsentStaysAbsent(t *testing.T) {
	rows := podListRows([]map[string]interface{}{{"name": "p", "status": "Pending"}})
	if _, ok := rows[0]["cpuUsage"]; ok {
		t.Error("cpuUsage appeared on a row that had no metrics")
	}
	if _, ok := rows[0]["containers"]; ok {
		t.Error("containers appeared on a row that had none")
	}
}

func TestPodListRows_NilAndEmpty(t *testing.T) {
	if podListRows(nil) != nil {
		t.Error("nil must stay nil (the response keeps items: null)")
	}
	if got := podListRows([]map[string]interface{}{}); got == nil || len(got) != 0 {
		t.Errorf("empty must stay an empty list, got %v", got)
	}
}
