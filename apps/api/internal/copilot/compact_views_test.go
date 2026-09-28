package copilot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

func podRow(name string, ready bool) map[string]interface{} {
	status := "True"
	if !ready {
		status = "False"
	}
	return map[string]interface{}{
		"name": name, "namespace": "kube-system", "uid": "u-" + name, "status": "Running",
		"ready": "1/1", "restarts": 3, "nodeName": "node-a",
		"labels":      map[string]string{"k8s-app": "kube-dns", "pod-template-hash": "5f66"},
		"annotations": map[string]string{"kubectl.kubernetes.io/last-applied-configuration": strings.Repeat("x", 2000)},
		"ownerReferences": []map[string]interface{}{
			{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "coredns-5f66", "uid": "rs", "controller": true},
		},
		"conditions": []map[string]interface{}{
			{"type": "PodScheduled", "status": "True"},
			{"type": "Ready", "status": status, "reason": "ContainersNotReady", "message": "containers with unready status: [coredns]"},
		},
		"volumes": []map[string]interface{}{{"name": "config", "configMap": "coredns"}},
		"containers": []map[string]interface{}{{
			"name": "coredns", "image": "registry.k8s.io/coredns:v1.11", "imagePullPolicy": "IfNotPresent",
			"ports":        []map[string]interface{}{{"containerPort": 53}},
			"volumeMounts": []map[string]interface{}{{"name": "config", "mountPath": "/etc/coredns"}},
			"env":          []map[string]interface{}{{"name": "TOKEN", "value": "s3cr3t"}},
			"resources":    map[string]interface{}{"requests": map[string]string{"memory": "70Mi"}},
			"state":        map[string]interface{}{"state": "running", "lastTermination": map[string]interface{}{"reason": "OOMKilled", "exitCode": 137}},
			"ready":        ready,
		}},
	}
}

// What an investigation keys on survives; what get_resource_detail answers for
// one object does not; and the model is told where the rest is.
func TestCompactResourceList_KeepsWhatAScanNeeds(t *testing.T) {
	list := models.ResourceList{Kind: "pods", Total: 2, Items: []map[string]interface{}{podRow("coredns-a", true), podRow("coredns-b", false)}}
	out := compactResourceList(list)
	raw, _ := json.Marshal(out)
	s := string(raw)

	for _, keep := range []string{`"k8s-app":"kube-dns"`, `"owner":"ReplicaSet/coredns-5f66"`, `"restarts":3`, `"nodeName":"node-a"`,
		`"image":"registry.k8s.io/coredns:v1.11"`, `"OOMKilled"`, `"memory":"70Mi"`, `"total":2`, "get_resource_detail"} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %s", keep)
		}
	}
	for _, gone := range []string{"last-applied-configuration", `"volumes"`, `"uid"`, `"env"`, "s3cr3t", `"volumeMounts"`, `"ports"`, "PodScheduled"} {
		if strings.Contains(s, gone) {
			t.Errorf("kept %s", gone)
		}
	}
	items := out["items"].([]map[string]interface{})
	if _, ok := items[0]["conditions"]; ok {
		t.Error("a pod with every condition healthy should carry none")
	}
	bad, ok := items[1]["conditions"].([]map[string]interface{})
	if !ok || len(bad) != 1 || bad[0]["type"] != "Ready" || bad[0]["message"] == nil {
		t.Errorf("the not-ready condition and its message must stay: %v", items[1]["conditions"])
	}
}

// The pressure family is healthy when False: a node reporting it True is the
// news, and one reporting False is not.
func TestAbnormalConditions_PressureIsHealthyWhenFalse(t *testing.T) {
	got := abnormalConditions([]map[string]interface{}{
		{"type": "Ready", "status": "True"},
		{"type": "MemoryPressure", "status": "False"},
		{"type": "DiskPressure", "status": "True", "reason": "KubeletHasDiskPressure"},
	})
	if len(got) != 1 || got[0]["type"] != "DiskPressure" {
		t.Fatalf("got %v", got)
	}
}

// Fourteen kube-system pods came to 41 KB and were cut at 32 KB — Kobi then
// counted 18. Rows of that size must now fit many times over.
func TestCompactResourceList_FitsWhereTheFullRowsDidNot(t *testing.T) {
	var items []map[string]interface{}
	for i := 0; i < 50; i++ {
		items = append(items, podRow("pod-"+strings.Repeat("x", i%5), i%3 != 0))
	}
	full, _ := json.Marshal(models.ResourceList{Kind: "pods", Total: 50, Items: items})
	compact, _ := json.Marshal(compactResourceList(models.ResourceList{Kind: "pods", Total: 50, Items: items}))
	if len(compact) >= maxToolResultBytes {
		t.Fatalf("50 compact rows are %d bytes, over the %d cap", len(compact), maxToolResultBytes)
	}
	if len(compact)*3 > len(full) {
		t.Errorf("compact %d bytes vs full %d: expected at least a 3x cut", len(compact), len(full))
	}
}

func TestCompactOverview_NamesOnlyWhatNeedsAttention(t *testing.T) {
	ov := models.ClusterOverview{
		ClusterName:     "dev",
		Pods:            models.ResourceCount{Total: 3, Ready: 2},
		Permissions:     map[string]bool{"pods": true, "secrets": false, "certificates": false},
		AbsentResources: []string{"certificates"},
		NamespaceWorkloads: []models.NamespaceWorkload{{
			Namespace: "shop",
			Workloads: []models.WorkloadSummary{
				{Kind: "Deployment", Name: "web", Replicas: 2, ReadyReplicas: 2, Pods: []models.PodSummary{{Name: "web-1", Ready: true}, {Name: "web-2", Ready: true}}},
				{Kind: "Deployment", Name: "api", Replicas: 2, ReadyReplicas: 1, Status: "Degraded", Pods: []models.PodSummary{{Name: "api-1", Ready: true}, {Name: "api-2", Status: "CrashLoopBackOff"}}},
				{Kind: "Deployment", Name: "batch", Replicas: 0},
			},
		}},
	}
	for i := 0; i < 15; i++ {
		ov.Events = append(ov.Events, models.KubeEvent{Type: "Warning", Reason: "BackOff", Timestamp: "2026-09-28T00:" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + ":00Z"})
		ov.Events = append(ov.Events, models.KubeEvent{Type: "Normal", Reason: "Pulled"})
	}
	raw, _ := json.Marshal(compactOverview(ov))
	s := string(raw)
	for _, keep := range []string{`"name":"api"`, "api-2", "CrashLoopBackOff", `"healthy":1`, "Deployment/batch", `"deniedResources":["secrets"]`, `"notInstalled":["certificates"]`, `"warningsOmitted":5`, "get_events"} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %s in %s", keep, s)
		}
	}
	for _, gone := range []string{`"name":"web"`, "web-1", "Pulled"} {
		if strings.Contains(s, gone) {
			t.Errorf("kept %s", gone)
		}
	}
	if strings.Count(s, `"reason":"BackOff"`) != overviewWarningEvents {
		t.Errorf("want the %d most recent warnings", overviewWarningEvents)
	}
}

// A completed Job pod is not Ready by design: no condition is reported.
func TestCompactRow_CompletedPodCarriesNoConditions(t *testing.T) {
	row := podRow("job-x", false)
	row["status"] = "Succeeded"
	if _, ok := compactRow(row)["conditions"]; ok {
		t.Error("a Succeeded pod listed its not-ready conditions as a problem")
	}
}
