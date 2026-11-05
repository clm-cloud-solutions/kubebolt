package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	sigsyaml "sigs.k8s.io/yaml"
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

	if err := c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", shown, WriteOptions{}); err != nil {
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
	if err := c.ApplyResourceYAML(context.Background(), "configmaps", "shop", "app", []byte(edited), WriteOptions{}); err != nil {
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
		name: "no name in the YAML", resourceType: "secrets", objName: "db",
		yaml:          "apiVersion: v1\nkind: Secret\nmetadata:\n  namespace: shop\ndata:\n  username: REDACTED\n",
		wantInMessage: `has no metadata.name. Keep the name of the object being edited, "db"`,
	}, {
		name: "moved to another namespace", resourceType: "configmaps", objName: "app",
		yaml:          "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: billing\ndata:\n  DB_PASSWORD: REDACTED\n",
		wantInMessage: "can't be moved",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := roundTripConnector(t, dbSecret(), appConfig())
			err := c.ApplyResourceYAML(context.Background(), tc.resourceType, "shop", tc.objName, []byte(tc.yaml), WriteOptions{})
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
	_ = c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", []byte(cases[0].yaml), WriteOptions{})
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
		restoreHiddenValues("configmaps", submitted, live)
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
			restoreHiddenValues("deployments", submitted, live)
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
		restoreHiddenValues("deployments", submitted, live)
		if got := submitted["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})[lastAppliedAnnotation]; got != "mine" {
			t.Errorf("annotation: %v", got)
		}
	})

	t.Run("no last-applied on the live object adds none", func(t *testing.T) {
		submitted := map[string]interface{}{"metadata": map[string]interface{}{"name": "web"}}
		restoreHiddenValues("deployments", submitted, map[string]interface{}{})
		if _, ok := submitted["metadata"].(map[string]interface{})["annotations"]; ok {
			t.Errorf("metadata: %v", submitted["metadata"])
		}
	})
}

// copiedTo is the viewer's YAML pasted into New resource for another
// namespace, as a person copying a pull secret would.
func copiedTo(t *testing.T, shown []byte, namespace string) *unstructured.Unstructured {
	t.Helper()
	var raw map[string]interface{}
	if err := sigsyaml.Unmarshal(shown, &raw); err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{Object: raw}
	obj.SetNamespace(namespace)
	obj.SetResourceVersion("")
	return obj
}

// A new object has no stored value behind REDACTED: creating it stored the
// word itself, six bytes of garbage under a Secret's data. Nothing is created.
func TestCreateResource_RefusesWhatTheViewerHid(t *testing.T) {
	cases := []struct {
		resourceType, name string
		gvr                schema.GroupVersionResource
		wantInMessage      []string
	}{
		{"secrets", "db", secretsGVR, []string{`"password", "username" read REDACTED`, "new Secret", "stringData"}},
		{"configmaps", "app", configMapsGVR, []string{`"DB_PASSWORD" reads REDACTED`, "new ConfigMap"}},
	}
	for _, tc := range cases {
		t.Run(tc.resourceType, func(t *testing.T) {
			c := roundTripConnector(t, dbSecret(), appConfig())
			shown, err := c.GetResourceYAML(tc.resourceType, "shop", tc.name)
			if err != nil {
				t.Fatal(err)
			}

			_, err = c.CreateResource(context.Background(), tc.resourceType, "billing", copiedTo(t, shown, "billing"), WriteOptions{})
			var refused *EditRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("want an EditRefusedError, got %v", err)
			}
			for _, want := range tc.wantInMessage {
				if !strings.Contains(refused.Error(), want) {
					t.Errorf("message %q lacks %q", refused.Error(), want)
				}
			}
			_, err = c.dynamicClient.Resource(tc.gvr).Namespace("billing").Get(context.Background(), tc.name, metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				t.Errorf("a refused create left an object behind: %v", err)
			}
		})
	}
}

func TestCreateResource_TakesRealValues(t *testing.T) {
	c := roundTripConnector(t)
	objs := map[string]*unstructured.Unstructured{
		"secrets": {Object: map[string]interface{}{
			"apiVersion": "v1", "kind": "Secret",
			"metadata":   map[string]interface{}{"name": "db", "namespace": "billing"},
			"data":       map[string]interface{}{"username": "ZGVtby11c2Vy"},
			"stringData": map[string]interface{}{"password": "s3cr3t"},
		}},
		// REDACTED inside a longer value is a value, not the placeholder.
		"configmaps": {Object: map[string]interface{}{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]interface{}{"name": "app", "namespace": "billing"},
			"data":     map[string]interface{}{"LOG_LEVEL": "info", "BANNER": "REDACTED fields are masked"},
		}},
	}
	for resourceType, obj := range objs {
		if _, err := c.CreateResource(context.Background(), resourceType, "billing", obj, WriteOptions{}); err != nil {
			t.Errorf("%s: %v", resourceType, err)
		}
	}
}

// Plain text under a Secret's data: the API server answers "illegal base64
// data at input byte N" and names no key (a 500 on create). Refused before it
// is sent, naming the key, in an edit and in a create alike.
func TestApplyResourceYAML_PlainTextUnderDataIsRefused(t *testing.T) {
	c := roundTripConnector(t, dbSecret())
	shown, err := c.GetResourceYAML("secrets", "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(shown), "password: REDACTED", "password: s3cr3t-new", 1)

	err = c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", []byte(edited), WriteOptions{})
	var refused *EditRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want an EditRefusedError, got %v", err)
	}
	for _, want := range []string{`"password" under data is not base64`, "stringData"} {
		if !strings.Contains(refused.Error(), want) {
			t.Errorf("message %q lacks %q", refused.Error(), want)
		}
	}
	if data := liveObject(t, c, secretsGVR, "db")["data"].(map[string]interface{}); data["password"] != "czNjcjN0LW9yaWdpbmFs" {
		t.Errorf("a refused edit changed the secret: %v", data)
	}
}

// The way the editor's hint gives: data as the viewer showed it, the new value
// in plain text under stringData.
func TestApplyResourceYAML_StringDataBesideHiddenValues(t *testing.T) {
	c := roundTripConnector(t, dbSecret())
	shown, err := c.GetResourceYAML("secrets", "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	edited := string(shown) + "stringData:\n  password: s3cr3t-new\n"
	if err := c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", []byte(edited), WriteOptions{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := liveObject(t, c, secretsGVR, "db")
	if data := got["data"].(map[string]interface{}); data["username"] != "ZGVtby11c2Vy" {
		t.Errorf("data: %v", data)
	}
	if sd := got["stringData"].(map[string]interface{}); sd["password"] != "s3cr3t-new" {
		t.Errorf("stringData: %v", sd)
	}
}

func TestCreateResource_RefusesValuesThatAreNotBase64(t *testing.T) {
	cases := []struct {
		resourceType  string
		gvr           schema.GroupVersionResource
		obj           map[string]interface{}
		wantInMessage []string
	}{{
		resourceType: "secrets", gvr: secretsGVR,
		obj: map[string]interface{}{
			"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]interface{}{"name": "db", "namespace": "billing"},
			// A YAML number is not a string at all.
			"data": map[string]interface{}{"username": "ZGVtby11c2Vy", "password": "s3cr3t!", "pin": float64(1234)},
		},
		wantInMessage: []string{`"password", "pin" under data are not base64`, "stringData"},
	}, {
		resourceType: "configmaps", gvr: configMapsGVR,
		obj: map[string]interface{}{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata":   map[string]interface{}{"name": "app", "namespace": "billing"},
			"binaryData": map[string]interface{}{"logo.png": "not base64"},
		},
		wantInMessage: []string{`"logo.png" under binaryData is not base64`, "Plain text goes under data"},
	}}
	for _, tc := range cases {
		t.Run(tc.resourceType, func(t *testing.T) {
			c := roundTripConnector(t)
			_, err := c.CreateResource(context.Background(), tc.resourceType, "billing", &unstructured.Unstructured{Object: tc.obj}, WriteOptions{})
			var refused *EditRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("want an EditRefusedError, got %v", err)
			}
			for _, want := range tc.wantInMessage {
				if !strings.Contains(refused.Error(), want) {
					t.Errorf("message %q lacks %q", refused.Error(), want)
				}
			}
			name := tc.obj["metadata"].(map[string]interface{})["name"].(string)
			if _, err := c.dynamicClient.Resource(tc.gvr).Namespace("billing").Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Errorf("a refused create left an object behind: %v", err)
			}
		})
	}
}

// The API server's own answers on kind (create --dry-run=server), value by
// value: the check refuses exactly what it refuses.
func TestCheckBase64Values_TakesWhatTheAPIServerDecodes(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
		ok    bool
	}{
		{"padded", "czNjcjN0", true},
		{"folded with a newline", "czNjcjN0LW9y\naWdpbmFs", true},
		{"empty", "", true},
		{"null", nil, true},
		{"plain text", "s3cr3t!", false},
		{"a space inside", "czNj cjN0", false},
		{"missing padding", "czNjcjN", false},
		{"a YAML number", float64(1234), false},
	}
	for _, tc := range cases {
		obj := map[string]interface{}{"data": map[string]interface{}{"k": tc.value}}
		if err := checkBase64Values("secrets", obj); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	// Only Secrets' data and ConfigMaps' binaryData are base64.
	plain := map[string]interface{}{"data": map[string]interface{}{"LOG_LEVEL": "info"}}
	if err := checkBase64Values("configmaps", plain); err != nil {
		t.Errorf("a ConfigMap's data refused: %v", err)
	}
}

// "hola" is valid base64, and the API server stored it as three bytes of
// garbage without a word (found creating a Secret from New resource). It
// decodes to binary: refused naming the keys, in an edit and in a create,
// until the person confirms it is binary on purpose.
func TestCreateResource_TextThatParsesAsBase64(t *testing.T) {
	typed := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]interface{}{"name": "db", "namespace": "billing"},
			// "hola" and "aqui", as typed; "demo-user", properly encoded.
			"data": map[string]interface{}{"password": "hola", "username": "aqui", "user": "ZGVtby11c2Vy"},
		}}
	}
	c := roundTripConnector(t)

	_, err := c.CreateResource(context.Background(), "secrets", "billing", typed(), WriteOptions{})
	var refused *EditRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want an EditRefusedError, got %v", err)
	}
	if got := strings.Join(refused.BinaryValues, ","); got != "password,username" {
		t.Errorf("binary values %q, want password,username", got)
	}
	for _, want := range []string{`"password", "username" under data decode to binary data`, "stringData", "keep it as binary"} {
		if !strings.Contains(refused.Error(), want) {
			t.Errorf("message %q lacks %q", refused.Error(), want)
		}
	}
	if _, err := c.dynamicClient.Resource(secretsGVR).Namespace("billing").Get(context.Background(), "db", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("a refused create left an object behind: %v", err)
	}

	if _, err := c.CreateResource(context.Background(), "secrets", "billing", typed(), WriteOptions{KeepBinary: true}); err != nil {
		t.Fatalf("confirmed as binary: %v", err)
	}
}

func TestApplyResourceYAML_TextThatParsesAsBase64(t *testing.T) {
	c := roundTripConnector(t, dbSecret())
	shown, err := c.GetResourceYAML("secrets", "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	edited := []byte(strings.Replace(string(shown), "password: REDACTED", "password: hola", 1))

	err = c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", edited, WriteOptions{})
	var refused *EditRefusedError
	if !errors.As(err, &refused) || strings.Join(refused.BinaryValues, ",") != "password" {
		t.Fatalf("want password refused as binary, got %v", err)
	}
	if data := liveObject(t, c, secretsGVR, "db")["data"].(map[string]interface{}); data["password"] != "czNjcjN0LW9yaWdpbmFs" {
		t.Errorf("a refused edit changed the secret: %v", data)
	}

	if err := c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", edited, WriteOptions{KeepBinary: true}); err != nil {
		t.Fatalf("confirmed as binary: %v", err)
	}
	data := liveObject(t, c, secretsGVR, "db")["data"].(map[string]interface{})
	if data["password"] != "hola" || data["username"] != "ZGVtby11c2Vy" {
		t.Errorf("data after the confirmed edit: %v", data)
	}
}

// A Secret that is binary on purpose keeps being editable: its values come
// back as REDACTED and are put back, never checked.
func TestApplyResourceYAML_StoredBinaryValuesAreNotChecked(t *testing.T) {
	keystore := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{"name": "tls-store", "namespace": "shop", "resourceVersion": "4"},
		"data":     map[string]interface{}{"keystore.p12": "3q2+7w=="}, // de ad be ef
	}}
	c := roundTripConnector(t, keystore)
	shown, err := c.GetResourceYAML("secrets", "shop", "tls-store")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(shown), "metadata:\n", "metadata:\n  labels:\n    team: payments\n", 1)
	if err := c.ApplyResourceYAML(context.Background(), "secrets", "shop", "tls-store", []byte(edited), WriteOptions{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if data := liveObject(t, c, secretsGVR, "tls-store")["data"].(map[string]interface{}); data["keystore.p12"] != "3q2+7w==" {
		t.Errorf("data: %v", data)
	}
}

func TestIsPrintableText(t *testing.T) {
	for in, want := range map[string]bool{
		"demo-user":                    true,
		"-----BEGIN CERT-----\nMIIB\n": true,
		`{"auths":{}}`:                 true,
		"\xb5\xeb\x2d":                 false, // "test" read as base64
		"abc\x00def":                   false,
	} {
		if got := IsPrintableText([]byte(in)); got != want {
			t.Errorf("IsPrintableText(%q) = %v, want %v", in, got, want)
		}
	}
}

// Every problem in one refusal: fixing one to be told the next made a person
// read the REDACTED refusal as the answer to a value that was not base64.
// With anything else wrong, the binary values are not offered for "Keep as
// binary": resending them would be refused again.
func TestCreateResource_SaysEveryProblemAtOnce(t *testing.T) {
	c := roundTripConnector(t)
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{"name": "db", "namespace": "billing"},
		"data":     map[string]interface{}{"username": "REDACTED", "password": "abc!", "token": "hola"},
	}}
	_, err := c.CreateResource(context.Background(), "secrets", "billing", obj, WriteOptions{})
	var refused *EditRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want an EditRefusedError, got %v", err)
	}
	lines := strings.Split(refused.Error(), "\n")
	want := []string{`"username" reads REDACTED`, `"password" under data is not base64`, `"token" under data decodes to binary data`}
	if len(lines) != len(want) {
		t.Fatalf("want %d reasons, got %q", len(want), refused.Error())
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("reason %d = %q, want it to start with %q", i, lines[i], w)
		}
	}
	if !strings.Contains(lines[2], "once the rest is fixed") || len(refused.BinaryValues) != 0 {
		t.Errorf("binary offered beside other problems: %q %v", lines[2], refused.BinaryValues)
	}
}

func TestApplyResourceYAML_SaysEveryProblemAtOnce(t *testing.T) {
	c := roundTripConnector(t, dbSecret())
	edited := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: db\n  namespace: shop\n  resourceVersion: \"7\"\n" +
		"data:\n  username: REDACTED\n  password: abc!\n  token: REDACTED\n  pin: hola\n"
	err := c.ApplyResourceYAML(context.Background(), "secrets", "shop", "db", []byte(edited), WriteOptions{})
	var refused *EditRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want an EditRefusedError, got %v", err)
	}
	// username reads REDACTED over a stored value: kept, not a problem.
	for _, w := range []string{`"token" reads REDACTED`, `"password" under data is not base64`, `"pin" under data decodes to binary data`} {
		if !strings.Contains(refused.Error(), w) {
			t.Errorf("refusal %q lacks %q", refused.Error(), w)
		}
	}
	if strings.Contains(refused.Error(), `"username"`) || len(refused.BinaryValues) != 0 {
		t.Errorf("refusal %q, binary %v", refused.Error(), refused.BinaryValues)
	}
	if data := liveObject(t, c, secretsGVR, "db")["data"].(map[string]interface{}); data["password"] != "czNjcjN0LW9yaWdpbmFs" {
		t.Errorf("a refused edit changed the secret: %v", data)
	}
}
