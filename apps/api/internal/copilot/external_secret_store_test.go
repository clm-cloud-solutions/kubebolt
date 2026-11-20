package copilot

import (
	"errors"
	"reflect"
	"testing"
)

type yamlStub map[string]string

func (y yamlStub) GetResourceYAML(resourceType, namespace, name string) ([]byte, error) {
	if b, ok := y[resourceType+"/"+namespace+"/"+name]; ok {
		return []byte(b), nil
	}
	return nil, errors.New("not found")
}

const externalSecretYAML = `kind: ExternalSecret
metadata: {name: orders-db-credentials, namespace: shop-backend}
spec:
  secretStoreRef: {kind: ClusterSecretStore, name: aws-sm}
  data:
  - secretKey: password
    remoteRef: {key: shop/orders-db, property: password}
`

// Finding #91: reading the ExternalSecret gives the exact key; the store behind
// it says which cloud holds that key, and where.
func TestExternalSecretStore(t *testing.T) {
	stores := yamlStub{
		"clustersecretstores//aws-sm":               "kind: ClusterSecretStore\nspec:\n  provider:\n    aws: {service: SecretsManager, region: us-east-2}\n",
		"secretstores/shop-backend/vault":           "kind: SecretStore\nspec:\n  provider:\n    vault: {server: 'https://vault.example.com'}\n",
		"secretstores/shop-backend/key-vault-store": "kind: SecretStore\nmetadata: {name: key-vault-store}\nspec:\n  provider:\n    azurekv: {vaultUrl: 'https://kv.example.net'}\n",
	}
	got := externalSecretStore(stores, "externalsecrets", "shop-backend", []byte(externalSecretYAML))
	want := map[string]any{"kind": "ClusterSecretStore", "name": "aws-sm", "type": "aws", "provider": "aws", "service": "SecretsManager", "region": "us-east-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExternalSecret → %v\nwant %v", got, want)
	}

	// A namespaced store, with no cloud connector behind it: no provider.
	vaultES := []byte("kind: ExternalSecret\nspec:\n  secretStoreRef: {name: vault}\n")
	if v := externalSecretStore(stores, "externalsecrets", "shop-backend", vaultES); v["type"] != "vault" || v["provider"] != nil || v["kind"] != "SecretStore" {
		t.Errorf("a Vault store: %v", v)
	}
	// A store read directly.
	kv := externalSecretStore(stores, "secretstores", "shop-backend", []byte(stores["secretstores/shop-backend/key-vault-store"]))
	if kv["provider"] != "azure" || kv["name"] != "key-vault-store" {
		t.Errorf("a Key Vault store: %v", kv)
	}
	// A store that cannot be read, and another type: nothing.
	missing := []byte("kind: ExternalSecret\nspec:\n  secretStoreRef: {kind: ClusterSecretStore, name: gone}\n")
	if v := externalSecretStore(stores, "externalsecrets", "shop-backend", missing); v != nil {
		t.Errorf("an unreadable store: %v", v)
	}
	if v := externalSecretStore(stores, "deployments", "shop-backend", []byte("kind: Deployment\n")); v != nil {
		t.Errorf("a Deployment: %v", v)
	}
}
