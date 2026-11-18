package copilot

import (
	sigsyaml "sigs.k8s.io/yaml"
)

// esoProviderConnector maps an External Secrets provider (the key under a
// store's spec.provider) to the cloud connector that reads its source.
var esoProviderConnector = map[string]string{
	"aws":     "aws",   // Secrets Manager and Parameter Store
	"gcpsm":   "gcp",   // Google Secret Manager
	"azurekv": "azure", // Azure Key Vault
}

// yamlReader is what externalSecretStore needs of the connector.
type yamlReader interface {
	GetResourceYAML(resourceType, namespace, name string) ([]byte, error)
}

// externalSecretStore summarizes the store behind an External Secrets object
// read with get_resource_yaml: for an ExternalSecret, the store its
// secretStoreRef names (read here, one more call); for a store, itself. nil
// when the type is another, or the store cannot be read.
//
// Its "provider" is the cloud connector that reads the store's source, so the
// remote key the ExternalSecret names is a cloud resource the cluster itself
// placed: the model knows which connector reads it without classifying a
// network destination first (finding #91).
func externalSecretStore(conn yamlReader, resourceType, namespace string, objYAML []byte) map[string]any {
	var obj map[string]any
	if sigsyaml.Unmarshal(objYAML, &obj) != nil {
		return nil
	}
	spec, _ := obj["spec"].(map[string]any)
	storeKind, storeName, storeNS := "", "", namespace
	switch resourceType {
	case "externalsecrets":
		ref, _ := spec["secretStoreRef"].(map[string]any)
		storeName, _ = ref["name"].(string)
		storeKind, _ = ref["kind"].(string)
		if storeKind == "" {
			storeKind = "SecretStore"
		}
		if storeName == "" || conn == nil {
			return nil
		}
		storeType := "secretstores"
		if storeKind == "ClusterSecretStore" {
			storeType, storeNS = "clustersecretstores", ""
		}
		b, err := conn.GetResourceYAML(storeType, storeNS, storeName)
		if err != nil {
			return nil
		}
		var store map[string]any
		if sigsyaml.Unmarshal(b, &store) != nil {
			return nil
		}
		spec, _ = store["spec"].(map[string]any)
	case "secretstores", "clustersecretstores":
		md, _ := obj["metadata"].(map[string]any)
		storeName, _ = md["name"].(string)
		storeKind, _ = obj["kind"].(string)
	default:
		return nil
	}
	providers, _ := spec["provider"].(map[string]any)
	for name, cfg := range providers {
		m, _ := cfg.(map[string]any)
		out := map[string]any{"kind": storeKind, "name": storeName, "type": name}
		if id, ok := esoProviderConnector[name]; ok {
			out["provider"] = id
		}
		if s, _ := m["service"].(string); s != "" {
			out["service"] = s
		}
		if r, _ := m["region"].(string); r != "" {
			out["region"] = r
		}
		return out
	}
	return nil
}
