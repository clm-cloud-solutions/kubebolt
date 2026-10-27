package cluster

import (
	"encoding/json"
	"reflect"
	"testing"
)

func managedFieldsOf(t *testing.T, raw string) interface{} {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The two facts db-role-password-rotated needed (2026-10-12), as the lab
// cluster recorded them: the Secret was last written when it was applied, on
// 25 September, and the Deployment's latest revision came from
// `kubectl rollout restart` at 17:16:04Z.
func TestSummarizeManagedFields(t *testing.T) {
	secret := managedFieldsOf(t, `[{"manager":"kubectl-client-side-apply","operation":"Update","apiVersion":"v1","time":"2026-09-25T03:51:49Z",
		"fieldsType":"FieldsV1","fieldsV1":{"f:data":{".":{},"f:password":{},"f:username":{}},
		"f:metadata":{"f:annotations":{".":{},"f:kubectl.kubernetes.io/last-applied-configuration":{}}},"f:type":{}}}]`)
	got := summarizeManagedFields(secret)
	want := []ManagedWrite{{Manager: "kubectl-client-side-apply", Operation: "Update", Time: "2026-09-25T03:51:49Z",
		Fields: []string{"data.password", "data.username", "metadata.annotations.kubectl.kubernetes.io/last-applied-configuration", "type"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("secret:\n got %+v\nwant %+v", got, want)
	}

	deploy := managedFieldsOf(t, `[
		{"manager":"kubectl-client-side-apply","operation":"Update","time":"2026-09-25T02:45:00Z",
		 "fieldsV1":{"f:metadata":{"f:labels":{".":{},"f:app":{}}},"f:spec":{"f:replicas":{},"f:template":{"f:spec":{"f:containers":{"k:{\"name\":\"app\"}":{".":{},"f:image":{}}}}}}}},
		{"manager":"kube-controller-manager","operation":"Update","subresource":"status","time":"2026-10-12T17:17:10Z",
		 "fieldsV1":{"f:status":{"f:availableReplicas":{},"f:conditions":{}}}},
		{"manager":"kubectl-rollout","operation":"Update","time":"2026-10-12T17:16:04Z",
		 "fieldsV1":{"f:spec":{"f:template":{"f:metadata":{"f:annotations":{".":{},"f:kubectl.kubernetes.io/restartedAt":{}}}}}}}
	]`)
	got = summarizeManagedFields(deploy)
	if len(got) != 3 || got[0].Manager != "kube-controller-manager" || got[0].Subresource != "status" ||
		got[1].Manager != "kubectl-rollout" || got[2].Manager != "kubectl-client-side-apply" {
		t.Fatalf("not newest first: %+v", got)
	}
	if !reflect.DeepEqual(got[1].Fields, []string{"spec.template.metadata"}) {
		t.Errorf("rollout fields: %v", got[1].Fields)
	}
	if !reflect.DeepEqual(got[2].Fields, []string{"metadata.labels.app", "spec.replicas", "spec.template.spec"}) {
		t.Errorf("apply fields: %v", got[2].Fields)
	}
	if summarizeManagedFields(nil) == nil || len(summarizeManagedFields(nil)) != 0 {
		t.Error("no managedFields should summarize to an empty list")
	}
}
