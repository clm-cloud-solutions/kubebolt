package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/describe"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
)

// What a reader of KubeBolt never sees — a Secret's values, a ConfigMap's
// credentials, a store's inline credentials, an Argo CD Application's values —
// is hidden on every path that shows an object: the YAML tab, the Helm release
// view, describe, Kobi's YAML read.

// A chart's values the way a Postgres install carries them.
func chartValues() map[string]interface{} {
	return map[string]interface{}{
		"replicaCount": float64(2),
		"image":        map[string]interface{}{"repository": "bitnami/postgresql", "digest": "sha256:" + strings.Repeat("ab12", 16)},
		"auth": map[string]interface{}{
			"postgresPassword": "pg-admin-s3cret",
			"password":         "app-s3cret",
			"username":         "orders",
			"existingSecret":   "orders-db-credentials",
			"pin":              float64(482913),
		},
		"serviceAccount": map[string]interface{}{"automountServiceAccountToken": true},
		"externalDatabase": map[string]interface{}{
			"url": "postgres://orders:url-s3cret@db.shop.internal:5432/orders",
		},
		"extraEnvVars": []interface{}{
			map[string]interface{}{"name": "SMTP_PASSWORD", "value": "smtp-s3cret"},
			map[string]interface{}{"name": "LOG_LEVEL", "value": "debug"},
		},
		"primary": map[string]interface{}{
			"extendedConfiguration": "max_connections = 200\nreplication_password = conf-s3cret\n",
		},
	}
}

func TestRedactValuesTree_HidesCredentialsKeepsTheRest(t *testing.T) {
	v := chartValues()
	if !redactValuesTree(v) {
		t.Fatal("nothing was hidden")
	}
	raw, _ := json.Marshal(v)
	out := string(raw)
	for _, leaked := range []string{"pg-admin-s3cret", "app-s3cret", "url-s3cret", "smtp-s3cret", "conf-s3cret", "482913"} {
		if strings.Contains(out, leaked) {
			t.Errorf("a credential survived: %s\n%s", leaked, out)
		}
	}
	for _, kept := range []string{"bitnami/postgresql", strings.Repeat("ab12", 16), `"username":"orders"`,
		`"existingSecret":"orders-db-credentials"`, `"automountServiceAccountToken":true`, "LOG_LEVEL", `"value":"debug"`,
		"max_connections = 200", `"replicaCount":2`} {
		if !strings.Contains(out, kept) {
			t.Errorf("lost what is not a credential: %s\n%s", kept, out)
		}
	}
}

func TestRedactValuesText(t *testing.T) {
	text := "auth:\n  password: app-s3cret\nreplicaCount: 2\n"
	got, changed := RedactValuesText(text)
	if !changed || strings.Contains(got, "app-s3cret") || !strings.Contains(got, "replicaCount: 2") {
		t.Errorf("values text: changed=%v\n%s", changed, got)
	}
	clean := "replicaCount: 2\n"
	if got, changed := RedactValuesText(clean); changed || got != clean {
		t.Errorf("clean values were rewritten: %q", got)
	}
	// Text that is not a mapping is filtered line by line.
	if got, _ := RedactValuesText("password=line-s3cret"); strings.Contains(got, "line-s3cret") {
		t.Errorf("unparsed text kept its credential: %s", got)
	}
}

const chartManifest = `---
# Source: orders-db/templates/secrets.yaml
apiVersion: v1
kind: Secret
metadata:
  name: orders-db
type: Opaque
data:
  postgres-password: cGctYWRtaW4tczNjcmV0
stringData:
  password: app-s3cret
---
# Source: orders-db/templates/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: orders-db-config
data:
  DB_PASSWORD: cm-s3cret
  LOG_LEVEL: info
---
# Source: orders-db/templates/statefulset.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: orders-db
spec:
  replicas: 1
`

func TestRedactManifest(t *testing.T) {
	got := RedactManifest(chartManifest)
	for _, leaked := range []string{"cGctYWRtaW4tczNjcmV0", "app-s3cret", "cm-s3cret"} {
		if strings.Contains(got, leaked) {
			t.Errorf("a credential survived: %s\n%s", leaked, got)
		}
	}
	for _, kept := range []string{
		"# Source: orders-db/templates/secrets.yaml",
		"# Source: orders-db/templates/configmap.yaml",
		"LOG_LEVEL: info",
		"postgres-password: REDACTED",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("lost: %s\n%s", kept, got)
		}
	}
	// A document with nothing to hide comes back byte for byte.
	sts := chartManifest[strings.Index(chartManifest, "# Source: orders-db/templates/statefulset.yaml"):]
	if !strings.HasSuffix(got, sts) {
		t.Errorf("the StatefulSet document changed:\n%s", got)
	}
	if strings.Count(got, "\n---\n")+1 != strings.Count(chartManifest, "\n---\n")+1 {
		t.Errorf("document count changed:\n%s", got)
	}
}

func TestRedactHelmRelease(t *testing.T) {
	r := &helm.Release{
		Values:   chartValues(),
		Manifest: chartManifest,
		Notes:    "Connect with:\n  PGPASSWORD=notes-s3cret psql -h orders-db\n",
	}
	RedactHelmRelease(r)
	raw, _ := json.Marshal(r)
	for _, leaked := range []string{"pg-admin-s3cret", "app-s3cret", "cm-s3cret", "notes-s3cret", "cGctYWRtaW4tczNjcmV0"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("the release detail still carries %s", leaked)
		}
	}
	RedactHelmRelease(nil) // no panic
}

// ---- Argo CD, through the viewer and back through the editor ----

var argoAppsGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

func argoApp() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]interface{}{"name": "orders", "namespace": "shop", "resourceVersion": "4"},
		"spec": map[string]interface{}{
			"project": "default",
			"source": map[string]interface{}{
				"repoURL": "https://charts.example.com", "chart": "orders", "targetRevision": "1.2.3",
				"helm": map[string]interface{}{
					"values":       "auth:\n  password: argo-values-s3cret\nreplicaCount: 2\n",
					"valuesObject": map[string]interface{}{"apiKey": "argo-object-s3cret", "region": "us-east-2"},
					"parameters": []interface{}{
						map[string]interface{}{"name": "db.password", "value": "argo-param-s3cret"},
						map[string]interface{}{"name": "image.tag", "value": "1.2.3"},
					},
				},
			},
			"sources": []interface{}{map[string]interface{}{
				"repoURL": "https://git.example.com/ops.git",
				"plugin": map[string]interface{}{"env": []interface{}{
					map[string]interface{}{"name": "VAULT_TOKEN", "value": "argo-plugin-s3cret"},
				}},
			}},
		},
	}}
}

var argoSecrets = []string{"argo-values-s3cret", "argo-object-s3cret", "argo-param-s3cret", "argo-plugin-s3cret"}

func argoConnector(objs ...runtime.Object) *Connector {
	return &Connector{dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{argoAppsGVR: "ApplicationList"}, objs...)}
}

func argoLive(t *testing.T, c *Connector) map[string]interface{} {
	t.Helper()
	obj, err := c.dynamicClient.Resource(argoAppsGVR).Namespace("shop").Get(context.Background(), "orders", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return obj.Object
}

func TestArgoApplicationYAML_HidesValues(t *testing.T) {
	c := argoConnector(argoApp())
	y, err := c.GetResourceYAML("argocdapps", "shop", "orders")
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range argoSecrets {
		if strings.Contains(string(y), leaked) {
			t.Errorf("the YAML viewer showed %s:\n%s", leaked, y)
		}
	}
	for _, kept := range []string{"replicaCount: 2", "region: us-east-2", "value: 1.2.3", "VAULT_TOKEN", "chart: orders"} {
		if !strings.Contains(string(y), kept) {
			t.Errorf("lost: %s\n%s", kept, y)
		}
	}
}

// The viewer's YAML saved with one unrelated edit: the hidden values come back
// from the live object, the edit lands.
func TestApplyResourceYAML_ArgoValuesSurviveAnEdit(t *testing.T) {
	c := argoConnector(argoApp())
	shown, err := c.GetResourceYAML("argocdapps", "shop", "orders")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(shown), "project: default", "project: payments", 1)
	if err := c.ApplyResourceYAML(context.Background(), "argocdapps", "shop", "orders", []byte(edited), WriteOptions{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	live := argoLive(t, c)
	raw, _ := json.Marshal(live)
	for _, secret := range argoSecrets {
		if !strings.Contains(string(raw), secret) {
			t.Errorf("the save lost %s:\n%s", secret, raw)
		}
	}
	if strings.Contains(string(raw), redactedPlaceholder) {
		t.Errorf("the placeholder was stored:\n%s", raw)
	}
	if live["spec"].(map[string]interface{})["project"] != "payments" {
		t.Errorf("the edit did not land: %v", live["spec"])
	}
}

// A hidden value edited while it still carries the placeholder cannot be told
// apart from what the person wrote: refused, nothing stored.
func TestApplyResourceYAML_ArgoEditedHiddenValueIsRefused(t *testing.T) {
	c := argoConnector(argoApp())
	shown, err := c.GetResourceYAML("argocdapps", "shop", "orders")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(shown), "replicaCount: 2", "replicaCount: 3", 1)
	err = c.ApplyResourceYAML(context.Background(), "argocdapps", "shop", "orders", []byte(edited), WriteOptions{})
	var refused *EditRefusedError
	if err == nil || !errors.As(err, &refused) || !strings.Contains(refused.Reason, "spec.source.helm.values") {
		t.Fatalf("want a refusal naming spec.source.helm.values, got %v", err)
	}
	raw, _ := json.Marshal(argoLive(t, c))
	if !strings.Contains(string(raw), "argo-values-s3cret") {
		t.Error("the refused edit changed the object")
	}

	// Writing the value out in full, without the placeholder, is an edit.
	var obj map[string]interface{}
	if err := sigsyaml.Unmarshal(shown, &obj); err != nil {
		t.Fatal(err)
	}
	obj["spec"].(map[string]interface{})["source"].(map[string]interface{})["helm"].(map[string]interface{})["values"] = "replicaCount: 3\n"
	full, _ := sigsyaml.Marshal(obj)
	if err := c.ApplyResourceYAML(context.Background(), "argocdapps", "shop", "orders", full, WriteOptions{}); err != nil {
		t.Fatalf("a full rewrite was refused: %v", err)
	}
	helmSpec := argoLive(t, c)["spec"].(map[string]interface{})["source"].(map[string]interface{})["helm"].(map[string]interface{})
	if helmSpec["values"] != "replicaCount: 3\n" {
		t.Errorf("values after a full rewrite: %v", helmSpec["values"])
	}
	if helmSpec["valuesObject"].(map[string]interface{})["apiKey"] != "argo-object-s3cret" {
		t.Error("an untouched hidden value was not restored beside the rewrite")
	}
}

// #91 hid a store's inline credentials in the viewer and nothing put them
// back: saving the store from the editor wrote REDACTED over its credential.
func TestApplyResourceYAML_SecretStoreKeepsItsInlineCredential(t *testing.T) {
	store := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/v1", "kind": "SecretStore",
		"metadata": map[string]interface{}{"name": "fake", "namespace": "shop"},
		"spec": map[string]interface{}{"provider": map[string]interface{}{"fake": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{"key": "/db", "value": "store-inline-s3cret"}},
		}}},
	}}
	c := esConnector(store)
	shown, err := c.GetResourceYAML("secretstores", "shop", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(shown), "store-inline-s3cret") {
		t.Fatalf("the viewer showed the credential:\n%s", shown)
	}
	if err := c.ApplyResourceYAML(context.Background(), "secretstores", "shop", "fake", shown, WriteOptions{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	obj, err := c.dynamicClient.Resource(esGVR("v1", "secretstores")).Namespace("shop").Get(context.Background(), "fake", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(obj.Object)
	if !strings.Contains(string(raw), "store-inline-s3cret") || strings.Contains(string(raw), redactedPlaceholder) {
		t.Errorf("the store after a save:\n%s", raw)
	}
}

// ---- describe ----

// describe hides what the viewer hides: a ConfigMap's credentials, an
// Application's values.
func TestDescribe_HidesWhatTheViewerHides(t *testing.T) {
	objects := map[string]interface{}{
		"/api/v1/namespaces/shop/configmaps/app": map[string]interface{}{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]interface{}{"name": "app", "namespace": "shop"},
			"data":     map[string]interface{}{"DB_PASSWORD": "describe-cm-s3cret", "LOG_LEVEL": "info"},
		},
		"/apis/argoproj.io/v1alpha1/namespaces/shop/applications/orders": argoApp().Object,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if obj, ok := objects[r.URL.Path]; ok {
			_ = json.NewEncoder(w).Encode(obj)
			return
		}
		// Events and anything else: an empty list.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"apiVersion": "v1", "kind": "List", "items": []interface{}{}})
	}))
	defer srv.Close()
	cfg := &rest.Config{Host: srv.URL}

	cm, ok := DescriberFor(schema.GroupKind{Kind: "ConfigMap"}, cfg)
	if !ok {
		t.Fatal("no ConfigMap describer")
	}
	out, err := cm.Describe("shop", "app", describe.DescriberSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "describe-cm-s3cret") || !strings.Contains(out, "info") {
		t.Errorf("ConfigMap describe:\n%s", out)
	}

	app, ok := GenericDescriberFor(&meta.RESTMapping{
		Resource:         argoAppsGVR,
		GroupVersionKind: argoAppsGVR.GroupVersion().WithKind("Application"),
		Scope:            meta.RESTScopeNamespace,
	}, cfg)
	if !ok {
		t.Fatal("no generic describer")
	}
	out, err = app.Describe("shop", "orders", describe.DescriberSettings{})
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range argoSecrets {
		if strings.Contains(out, leaked) {
			t.Errorf("Application describe printed %s:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "orders") {
		t.Errorf("Application describe lost the object:\n%s", out)
	}
}

func TestHideInResponse_LeavesSecretsToTheirDescriber(t *testing.T) {
	secret := []byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"db"},"data":{"password":"czNjcjN0"}}`)
	if got := hideInResponse(secret); string(got) != string(secret) {
		t.Errorf("a Secret was rewritten (its describer prints only sizes): %s", got)
	}
	if got := hideInResponse([]byte("not json")); string(got) != "not json" {
		t.Errorf("non-JSON was rewritten: %s", got)
	}
}
