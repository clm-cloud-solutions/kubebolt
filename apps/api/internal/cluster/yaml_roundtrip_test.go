package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var (
	secretsGVR    = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	configMapsGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
)

const lastAppliedFixture = `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"db"}}`

func roundTripConnector(t *testing.T, objs ...runtime.Object) *Connector {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		secretsGVR:    "SecretList",
		configMapsGVR: "ConfigMapList",
	}, objs...)
	return &Connector{dynamicClient: dyn}
}

func liveObject(t *testing.T, c *Connector, gvr schema.GroupVersionResource, name string) map[string]interface{} {
	t.Helper()
	obj, err := c.dynamicClient.Resource(gvr).Namespace("shop").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return obj.Object
}

func dbSecret() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{
			"name": "db", "namespace": "shop", "resourceVersion": "7",
			"annotations": map[string]interface{}{lastAppliedAnnotation: lastAppliedFixture, "team": "payments"},
		},
		"type": "Opaque",
		// demo-user / s3cr3t-original
		"data": map[string]interface{}{"username": "ZGVtby11c2Vy", "password": "czNjcjN0LW9yaWdpbmFs"},
	}}
}

func appConfig() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": "app", "namespace": "shop", "resourceVersion": "3"},
		"data":     map[string]interface{}{"DB_PASSWORD": "pg-original", "LOG_LEVEL": "info"},
	}}
}

// Saving the YAML tab without touching it wrote "REDACTED" over every Secret
// value (valid base64: six bytes of garbage) and dropped last-applied. The
// viewer's own output, sent back, must leave the object as it was.
func TestApplyResourceYAML_ViewerOutputSavedAsIsKeepsTheSecret(t *testing.T) {
	c := roundTripConnector(t, dbSecret())
	shown, err := c.GetResourceYAML("secrets", "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(shown), "ZGVtby11c2Vy") || strings.Contains(string(shown), lastAppliedAnnotation) {
		t.Fatalf("the viewer showed what it must hide:\n%s", shown)
	}

	if err := c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", shown); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := liveObject(t, c, secretsGVR, "db")
	data := got["data"].(map[string]interface{})
	if data["username"] != "ZGVtby11c2Vy" || data["password"] != "czNjcjN0LW9yaWdpbmFs" {
		t.Errorf("secret values were overwritten: %v", data)
	}
	ann := got["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
	if ann[lastAppliedAnnotation] != lastAppliedFixture || ann["team"] != "payments" {
		t.Errorf("annotations after the save: %v", ann)
	}
}

// An edit next to a hidden value: the edited key changes, the hidden one keeps
// what it stored.
func TestApplyResourceYAML_EditBesideAHiddenConfigMapValue(t *testing.T) {
	c := roundTripConnector(t, appConfig())
	shown, err := c.GetResourceYAML("configmaps", "shop", "app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shown), "DB_PASSWORD: REDACTED") {
		t.Fatalf("DB_PASSWORD should be hidden:\n%s", shown)
	}

	edited := strings.Replace(string(shown), "LOG_LEVEL: info", "LOG_LEVEL: debug", 1)
	if err := c.ApplyResourceYAML(context.Background(), "configmaps", "shop", "app", []byte(edited)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	data := liveObject(t, c, configMapsGVR, "app")["data"].(map[string]interface{})
	if data["DB_PASSWORD"] != "pg-original" || data["LOG_LEVEL"] != "debug" {
		t.Errorf("data after the edit: %v", data)
	}
}

func TestApplyResourceYAML_RefusesWhatItCannotKeep(t *testing.T) {
	cases := []struct {
		name, resourceType, objName, yaml, wantInMessage string
	}{{
		name: "placeholder under a new secret key", resourceType: "secrets", objName: "db",
		yaml:          "apiVersion: v1\nkind: Secret\nmetadata:\n  name: db\n  namespace: shop\ndata:\n  username: REDACTED\n  token: REDACTED\n",
		wantInMessage: `"token"`,
	}, {
		name: "renamed in the YAML", resourceType: "secrets", objName: "db",
		yaml:          "apiVersion: v1\nkind: Secret\nmetadata:\n  name: db-copy\n  namespace: shop\ndata:\n  username: REDACTED\n",
		wantInMessage: "can't be renamed",
	}, {
		name: "moved to another namespace", resourceType: "configmaps", objName: "app",
		yaml:          "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: billing\ndata:\n  DB_PASSWORD: REDACTED\n",
		wantInMessage: "can't be moved",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := roundTripConnector(t, dbSecret(), appConfig())
			err := c.ApplyResourceYAML(context.Background(), tc.resourceType, "shop", tc.objName, []byte(tc.yaml))
			var refused *EditRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("want an EditRefusedError, got %v", err)
			}
			if !strings.Contains(refused.Error(), tc.wantInMessage) {
				t.Errorf("message %q lacks %q", refused.Error(), tc.wantInMessage)
			}
		})
	}
	// Nothing was written.
	c := roundTripConnector(t, dbSecret())
	_ = c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", []byte(cases[0].yaml))
	if data := liveObject(t, c, secretsGVR, "db")["data"].(map[string]interface{}); data["password"] != "czNjcjN0LW9yaWdpbmFs" {
		t.Errorf("a refused edit changed the secret: %v", data)
	}
}

func TestRestoreHiddenValues(t *testing.T) {
	live := map[string]interface{}{
		"metadata": map[string]interface{}{"annotations": map[string]interface{}{lastAppliedAnnotation: "{}"}},
		"data":     map[string]interface{}{"DB_PASSWORD": "pg-original", "LOG_LEVEL": "info"},
	}

	t.Run("a placeholder over a value the viewer showed is what the person wrote", func(t *testing.T) {
		submitted := map[string]interface{}{"data": map[string]interface{}{"DB_PASSWORD": "REDACTED", "LOG_LEVEL": "REDACTED"}}
		if err := restoreHiddenValues("configmaps", submitted, live); err != nil {
			t.Fatal(err)
		}
		data := submitted["data"].(map[string]interface{})
		if data["DB_PASSWORD"] != "pg-original" || data["LOG_LEVEL"] != "REDACTED" {
			t.Errorf("data: %v", data)
		}
	})

	t.Run("last-applied comes back even without annotations or metadata", func(t *testing.T) {
		for _, submitted := range []map[string]interface{}{
			{},
			{"metadata": map[string]interface{}{"annotations": nil}},
		} {
			if err := restoreHiddenValues("deployments", submitted, live); err != nil {
				t.Fatal(err)
			}
			md := submitted["metadata"].(map[string]interface{})
			if md["annotations"].(map[string]interface{})[lastAppliedAnnotation] != "{}" {
				t.Errorf("metadata: %v", md)
			}
		}
	})

	t.Run("a last-applied the YAML carries is kept", func(t *testing.T) {
		submitted := map[string]interface{}{"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{lastAppliedAnnotation: "mine"},
		}}
		if err := restoreHiddenValues("deployments", submitted, live); err != nil {
			t.Fatal(err)
		}
		if got := submitted["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})[lastAppliedAnnotation]; got != "mine" {
			t.Errorf("annotation: %v", got)
		}
	})

	t.Run("no last-applied on the live object adds none", func(t *testing.T) {
		submitted := map[string]interface{}{"metadata": map[string]interface{}{"name": "web"}}
		if err := restoreHiddenValues("deployments", submitted, map[string]interface{}{}); err != nil {
			t.Fatal(err)
		}
		if _, ok := submitted["metadata"].(map[string]interface{})["annotations"]; ok {
			t.Errorf("metadata: %v", submitted["metadata"])
		}
	})
}
