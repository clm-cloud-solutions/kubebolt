package cluster

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// FieldManagers reads an object's field managers as "manager/operation"
// (e.g. "eks/Apply") with one metadata-only GET. The informer caches drop
// managedFields (stripManagedFieldsOption), and an EKS add-on is recognised
// by its eks field manager when it carries no label (Kobi Plans,
// provider_managed).
func (c *Connector) FieldManagers(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) ([]string, error) {
	if c.metadataClient == nil {
		return nil, errors.New("no metadata client")
	}
	obj, err := c.metadataClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(obj.ManagedFields))
	for _, m := range obj.ManagedFields {
		out = append(out, m.Manager+"/"+string(m.Operation))
	}
	return out, nil
}
