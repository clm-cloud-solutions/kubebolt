package cluster

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An EndpointSlice that a Kubernetes controller writes is rewritten within
// seconds of any edit: the EndpointSlice controller rebuilds it from its
// Service's selector, and the mirroring controller copies it from the core/v1
// Endpoints of a Service that has none. KubeBolt's «Endpoints» page shows and
// edits slices, so editing one of those looked applied and silently reverted
// (debt-20, seen with shop-data/shop-db). The edit is refused with where to
// make it instead; a mirrored slice's source is editable as `coreendpoints`.

const (
	sliceManagedByLabel = "endpointslice.kubernetes.io/managed-by"
	sliceServiceLabel   = "kubernetes.io/service-name"
	sliceControllerName = "endpointslice-controller.k8s.io"
	sliceMirroringName  = "endpointslicemirroring-controller.k8s.io"
)

// ManagedSliceError refuses an edit to an EndpointSlice a controller rewrites.
type ManagedSliceError struct {
	// Source is what to edit instead: "service" when the slice is built from
	// the Service's selector, "endpoints" when it mirrors the Service's
	// core/v1 Endpoints.
	Source  string
	Service string
}

func (e *ManagedSliceError) Error() string {
	svc := e.Service
	if svc == "" {
		svc = "its Service"
	} else {
		svc = fmt.Sprintf("%q", svc)
	}
	if e.Source == "endpoints" {
		return fmt.Sprintf("This EndpointSlice is a copy of the Endpoints %s, which Kubernetes copies back over it within seconds: an edit here would be undone. Edit the Endpoints %s instead.", svc, svc)
	}
	return fmt.Sprintf("Kubernetes rebuilds this EndpointSlice from the selector of Service %s within seconds: an edit here would be undone. Change the Service's selector or its Pods instead.", svc)
}

// managedSlice reports whether a Kubernetes controller manages the
// EndpointSlice, and what to edit instead; nil for a slice nobody rewrites
// (created by hand or by another tool).
func managedSlice(obj map[string]interface{}) *ManagedSliceError {
	labels, _, _ := unstructured.NestedStringMap(obj, "metadata", "labels")
	switch labels[sliceManagedByLabel] {
	case sliceControllerName:
		return &ManagedSliceError{Source: "service", Service: labels[sliceServiceLabel]}
	case sliceMirroringName:
		return &ManagedSliceError{Source: "endpoints", Service: labels[sliceServiceLabel]}
	}
	return nil
}
