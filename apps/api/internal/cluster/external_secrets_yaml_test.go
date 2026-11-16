package cluster

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// Finding #91: KubeBolt could not read what an External Secret syncs, so a
// rotated credential could not be traced to its source. ExternalSecrets and
// their stores are read through the YAML path, v1 first and v1beta1 after it,
// and a store's inline credentials are hidden.

func esGVR(version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "external-secrets.io", Version: version, Resource: resource}
}

func esConnector(objs ...runtime.Object) *Connector {
	lists := map[schema.GroupVersionResource]string{}
	for _, v := range []string{"v1", "v1beta1"} {
		lists[esGVR(v, "externalsecrets")] = "ExternalSecretList"
		lists[esGVR(v, "secretstores")] = "SecretStoreList"
		lists[esGVR(v, "clustersecretstores")] = "ClusterSecretStoreList"
	}
	return &Connector{dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objs...)}
}

func externalSecret(version string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/" + version, "kind": "ExternalSecret",
		"metadata": map[string]interface{}{"name": "orders-db-credentials", "namespace": "shop-backend"},
		"spec": map[string]interface{}{
			"secretStoreRef": map[string]interface{}{"kind": "ClusterSecretStore", "name": "aws-sm"},
			"data": []interface{}{map[string]interface{}{
				"secretKey": "password",
				"remoteRef": map[string]interface{}{"key": "shop/orders-db", "property": "password"},
			}},
		},
		"status": map[string]interface{}{"refreshTime": "2026-11-16T14:49:31Z"},
	}}
}

func TestExternalSecretYAML_ReadsEitherVersion(t *testing.T) {
	for _, version := range []string{"v1", "v1beta1"} {
		c := esConnector(externalSecret(version))
		y, err := c.GetResourceYAML("externalsecrets", "shop-backend", "orders-db-credentials")
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if !strings.Contains(string(y), "key: shop/orders-db") || !strings.Contains(string(y), "name: aws-sm") {
			t.Errorf("%s: the remote key or the store is missing:\n%s", version, y)
		}
	}
	if _, err := esConnector().GetResourceYAML("externalsecrets", "shop-backend", "missing"); err == nil {
		t.Error("a missing ExternalSecret read without an error")
	}
}

func TestSecretStoreYAML_HidesInlineCredentialsKeepsReferences(t *testing.T) {
	store := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/v1", "kind": "ClusterSecretStore",
		"metadata": map[string]interface{}{"name": "aws-sm"},
		"spec": map[string]interface{}{"provider": map[string]interface{}{
			"aws": map[string]interface{}{
				"service": "SecretsManager", "region": "us-east-2",
				"role": "arn:aws:iam::123456789012:role/eso-reader",
				"auth": map[string]interface{}{"secretRef": map[string]interface{}{
					"accessKeyIDSecretRef":     map[string]interface{}{"name": "aws-creds", "key": "access-key"},
					"secretAccessKeySecretRef": map[string]interface{}{"name": "aws-creds", "key": "secret-key"},
				}},
			},
			"fake": map[string]interface{}{"data": []interface{}{
				map[string]interface{}{"key": "/db", "value": "plain-text-password"},
			}},
			"webhook": map[string]interface{}{"headers": map[string]interface{}{"Authorization": "Bearer inline-token"}},
		}},
	}}
	y, err := esConnector(store).GetResourceYAML("clustersecretstores", "", "aws-sm")
	if err != nil {
		t.Fatal(err)
	}
	out := string(y)
	for _, leaked := range []string{"plain-text-password", "inline-token"} {
		if strings.Contains(out, leaked) {
			t.Errorf("an inline credential reached the YAML: %s", leaked)
		}
	}
	for _, kept := range []string{"region: us-east-2", "service: SecretsManager", "role/eso-reader", "name: aws-creds", "key: secret-key"} {
		if !strings.Contains(out, kept) {
			t.Errorf("a reference was lost: %s\n%s", kept, out)
		}
	}
}

func TestExternalSecretsTypes(t *testing.T) {
	if !isClusterScoped("clustersecretstores") || isClusterScoped("secretstores") || isClusterScoped("externalsecrets") {
		t.Error("scope of the External Secrets types")
	}
	if g, ok := olderVersionGVR("externalsecrets"); !ok || g.Version != "v1beta1" || g.Group != "external-secrets.io" {
		t.Errorf("older version: %+v %v", g, ok)
	}
	if _, ok := olderVersionGVR("deployments"); ok {
		t.Error("a built-in type has an older version to try")
	}
}
