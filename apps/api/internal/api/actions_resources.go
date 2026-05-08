package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Set resources — `kubectl set resources` ergonomics for Deployment /
// StatefulSet / DaemonSet. Strategic merge patch on
// spec.template.spec.containers[?(@.name==X)].resources, mirroring the
// shape and semantics of handleSetImage. Same Editor+ auth gate.
//
// Operationally this is the focused alternative to a YAML round-trip
// for the most common right-sizing operation: bump a workload's
// cpu/memory request or limit without finding the right line in the
// pod template. The dialog also surfaces inline next to the
// RightSizingPanel rows on the Capacity dashboard, so a "NEAR-LIMIT"
// or "OVER-PROV" recommendation is one click from the matching modal.
//
// Scope of v1:
//   - cpu / memory only on the requests + limits sub-objects
//   - per-container patching, init containers via the InitContainer flag
//   - field-absent OR empty-string = "leave this dimension alone"
//   - removing a field (e.g. dropping a stale cpu request) is NOT
//     supported in v1 — strategic merge can't express delete cleanly
//     without rewriting the full resources sub-object, and the operator
//     has the YAML editor for that path. Documented in
//     internal/k8s-operations/tier2-set-resources.md as a v2 follow-up.

var setResourceableTypes = map[string]bool{
	"deployments":  true,
	"statefulsets": true,
	"daemonsets":   true,
}

type setResourcesRequest struct {
	Containers []containerResourcesPatch `json:"containers"`
}

// containerResourcesPatch is one row of the request — the operator's
// override for a single container's resources sub-object.
//
// CPU and memory fields are *string so we can distinguish:
//   - field absent (nil)        → operator didn't mention this dimension
//   - field present but empty   → same as absent in v1 (see file-level
//                                 comment about field removal)
//   - field present with value  → patch this dimension
type containerResourcesPatch struct {
	Container     string             `json:"container"`
	InitContainer bool               `json:"initContainer,omitempty"`
	Requests      *resourceQuantity `json:"requests,omitempty"`
	Limits        *resourceQuantity `json:"limits,omitempty"`
}

type resourceQuantity struct {
	CPU    *string `json:"cpu,omitempty"`
	Memory *string `json:"memory,omitempty"`
}

// resourcePair is the from/to envelope returned in the response. Same
// shape as the request's containerResourcesPatch but with concrete
// (non-pointer) values — clients shouldn't have to deal with nullable
// strings on the way back.
type resourcePair struct {
	Container     string                  `json:"container"`
	InitContainer bool                    `json:"initContainer,omitempty"`
	Requests      map[string]string       `json:"requests,omitempty"`
	Limits        map[string]string       `json:"limits,omitempty"`
}

func (h *handlers) handleSetResources(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	if !setResourceableTypes[resourceType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"cannot set-resources on %s — only deployments, statefulsets, and daemonsets", resourceType))
		return
	}

	var body setResourcesRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Containers) == 0 {
		respondError(w, http.StatusBadRequest, "containers is required and must be non-empty")
		return
	}

	// Validate per-row shape + parse quantity strings before touching
	// the apiserver. Surfacing a clear "use Mi (mebibytes) instead of
	// mb" client-side beats a generic 422 from the apiserver.
	for i, row := range body.Containers {
		if row.Container == "" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("containers[%d].container is required", i))
			return
		}
		if row.Requests == nil && row.Limits == nil {
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"containers[%d] must specify at least one of requests/limits", i))
			return
		}
		if err := validateResourceQuantity(i, "requests", row.Requests); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateResourceQuantity(i, "limits", row.Limits); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		// limit >= request per dimension. Apiserver enforces this too,
		// but pre-check produces a cleaner error and avoids a partial
		// audit entry for an admission-rejected patch.
		if err := validateLimitGteRequest(i, row); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	clientset := conn.Clientset()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 1. Capture pre-patch state for the audit + response. Splits the
	//    workload's containers into normal + init lookups so the
	//    InitContainer flag routes correctly.
	currentNormal, currentInit, err := getCurrentContainerResources(ctx, clientset, resourceType, namespace, name)
	if err != nil {
		auditMutation(r, "set_resources", resourceType, namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}

	// 2. Validate every requested container exists in the right list
	//    (normal vs init). Surfacing "container does not exist" with
	//    the list of valid names is friendlier than a silent
	//    strategic-merge no-op (which would happily add a phantom
	//    container entry that never matches a running pod).
	fromResources := make([]resourcePair, 0, len(body.Containers))
	for _, row := range body.Containers {
		var found *resourcePair
		var validNames []string
		if row.InitContainer {
			validNames = containerNames(currentInit)
			for i := range currentInit {
				if currentInit[i].Container == row.Container {
					currentInit[i].InitContainer = true
					found = &currentInit[i]
					break
				}
			}
		} else {
			validNames = containerNames(currentNormal)
			for i := range currentNormal {
				if currentNormal[i].Container == row.Container {
					found = &currentNormal[i]
					break
				}
			}
		}
		if found == nil {
			kind := "container"
			if row.InitContainer {
				kind = "init container"
			}
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"%s %q not found in %s/%s; valid %ss: %v",
				kind, row.Container, resourceType, name, kind, validNames))
			return
		}
		fromResources = append(fromResources, *found)
	}

	// 3. Build the strategic merge patch.
	patchBytes, err := buildSetResourcesPatch(body.Containers)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build patch")
		return
	}

	// 4. toResources is the requested state — strings only, no nil
	//    pointers — so the response is symmetric with fromResources.
	toResources := make([]resourcePair, len(body.Containers))
	for i, row := range body.Containers {
		toResources[i] = resourcePair{
			Container:     row.Container,
			InitContainer: row.InitContainer,
			Requests:      flattenQuantity(row.Requests),
			Limits:        flattenQuantity(row.Limits),
		}
	}

	params := map[string]any{
		"fromResources": fromResources,
		"toResources":   toResources,
	}

	switch resourceType {
	case "deployments":
		_, err = clientset.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	case "statefulsets":
		_, err = clientset.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	case "daemonsets":
		_, err = clientset.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	}

	if err != nil {
		auditMutation(r, "set_resources", resourceType, namespace, name, params, err)
		log.Printf("Set-resources failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "set_resources", resourceType, namespace, name, params, nil)
	resource, _ := conn.GetResourceDetail(resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "patched",
		"fromResources": fromResources,
		"toResources":   toResources,
		"resource":      resource,
	})
}

// validateResourceQuantity parses every non-empty cpu/memory quantity
// via resource.ParseQuantity (the same parser the apiserver uses).
// Empty strings AND nil pointers are accepted as "leave this dimension
// alone" — see file-level comment about empty-string semantics.
func validateResourceQuantity(idx int, kind string, q *resourceQuantity) error {
	if q == nil {
		return nil
	}
	if q.CPU != nil && *q.CPU != "" {
		if _, err := resource.ParseQuantity(*q.CPU); err != nil {
			return fmt.Errorf("containers[%d].%s.cpu %q is not a valid quantity (e.g. 200m, 1, 0.5): %v", idx, kind, *q.CPU, err)
		}
	}
	if q.Memory != nil && *q.Memory != "" {
		if _, err := resource.ParseQuantity(*q.Memory); err != nil {
			return fmt.Errorf("containers[%d].%s.memory %q is not a valid quantity (e.g. 384Mi, 1Gi, 512M): %v", idx, kind, *q.Memory, err)
		}
	}
	return nil
}

// validateLimitGteRequest rejects requests where limit is set BELOW
// request on the same dimension on the same container. The apiserver
// rejects this anyway; doing it here surfaces a cleaner error and
// keeps the audit log free of admission-failed entries.
func validateLimitGteRequest(idx int, row containerResourcesPatch) error {
	if row.Requests == nil || row.Limits == nil {
		return nil
	}
	if v, ok := compareQ(row.Requests.CPU, row.Limits.CPU); ok && v > 0 {
		return fmt.Errorf("containers[%d]: limits.cpu (%s) must be >= requests.cpu (%s)", idx, *row.Limits.CPU, *row.Requests.CPU)
	}
	if v, ok := compareQ(row.Requests.Memory, row.Limits.Memory); ok && v > 0 {
		return fmt.Errorf("containers[%d]: limits.memory (%s) must be >= requests.memory (%s)", idx, *row.Limits.Memory, *row.Requests.Memory)
	}
	return nil
}

// compareQ parses two quantity strings and returns +1 if req > limit
// (the violation case), 0 if equal or either side absent. Returns ok=false
// when either string is nil, empty, or unparseable — the caller treats
// that as "skip this dimension." validateResourceQuantity has already
// rejected unparseable values upstream, so unparseable here means
// "not provided", not "bad input".
func compareQ(reqStr, limStr *string) (int, bool) {
	if reqStr == nil || limStr == nil || *reqStr == "" || *limStr == "" {
		return 0, false
	}
	req, err := resource.ParseQuantity(*reqStr)
	if err != nil {
		return 0, false
	}
	lim, err := resource.ParseQuantity(*limStr)
	if err != nil {
		return 0, false
	}
	return req.Cmp(lim), true
}

// getCurrentContainerResources reads the workload's pod template and
// returns two parallel slices: one for normal containers, one for init
// containers. Both are sourced from the same Get so the InitContainer
// flag dispatch in the handler doesn't need to re-read.
func getCurrentContainerResources(ctx context.Context, clientset kubernetes.Interface, resourceType, namespace, name string) ([]resourcePair, []resourcePair, error) {
	switch resourceType {
	case "deployments":
		d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containersToResourcePairs(d.Spec.Template.Spec.Containers, false),
			containersToResourcePairs(d.Spec.Template.Spec.InitContainers, true), nil
	case "statefulsets":
		sts, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containersToResourcePairs(sts.Spec.Template.Spec.Containers, false),
			containersToResourcePairs(sts.Spec.Template.Spec.InitContainers, true), nil
	case "daemonsets":
		ds, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containersToResourcePairs(ds.Spec.Template.Spec.Containers, false),
			containersToResourcePairs(ds.Spec.Template.Spec.InitContainers, true), nil
	}
	return nil, nil, fmt.Errorf("unsupported resource type: %s", resourceType)
}

func containersToResourcePairs(cs []corev1.Container, init bool) []resourcePair {
	out := make([]resourcePair, len(cs))
	for i, c := range cs {
		out[i] = resourcePair{
			Container:     c.Name,
			InitContainer: init,
			Requests:      resourceListToMap(c.Resources.Requests),
			Limits:        resourceListToMap(c.Resources.Limits),
		}
	}
	return out
}

func resourceListToMap(rl corev1.ResourceList) map[string]string {
	if len(rl) == 0 {
		return nil
	}
	out := make(map[string]string, len(rl))
	for name, q := range rl {
		out[string(name)] = q.String()
	}
	return out
}

func containerNames(pairs []resourcePair) []string {
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.Container
	}
	return out
}

// buildSetResourcesPatch builds a strategic-merge patch body that
// only includes the dimensions the operator actually asked to change.
// Empty / nil dimensions are omitted from the patch — strategic merge
// then leaves them untouched. This is the v1 design; "explicitly
// remove a dimension" would need a JSON 6902 patch or a Get-then-PUT
// of the full resources sub-object and is deferred to v2.
//
// Both `containers` and `initContainers` are present in the patch only
// if at least one row targets them, so the patch JSON stays minimal.
func buildSetResourcesPatch(rows []containerResourcesPatch) ([]byte, error) {
	var normal, initC []map[string]interface{}
	for _, row := range rows {
		entry := map[string]interface{}{"name": row.Container}
		resourcesObj := map[string]interface{}{}
		if reqs := buildQuantityMap(row.Requests); len(reqs) > 0 {
			resourcesObj["requests"] = reqs
		}
		if lims := buildQuantityMap(row.Limits); len(lims) > 0 {
			resourcesObj["limits"] = lims
		}
		// Skip rows where the operator didn't actually specify any
		// non-empty dimension — they'd no-op anyway, no point sending
		// a phantom container entry to the apiserver.
		if len(resourcesObj) == 0 {
			continue
		}
		entry["resources"] = resourcesObj
		if row.InitContainer {
			initC = append(initC, entry)
		} else {
			normal = append(normal, entry)
		}
	}

	podSpec := map[string]interface{}{}
	if len(normal) > 0 {
		podSpec["containers"] = normal
	}
	if len(initC) > 0 {
		podSpec["initContainers"] = initC
	}
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": podSpec,
			},
		},
	}
	return json.Marshal(patch)
}

// buildQuantityMap drops nil/empty fields so the patch only carries
// the dimensions the operator named. Empty string is treated the same
// as absent in v1 — see file-level comment.
func buildQuantityMap(q *resourceQuantity) map[string]string {
	if q == nil {
		return nil
	}
	out := map[string]string{}
	if q.CPU != nil && *q.CPU != "" {
		out["cpu"] = *q.CPU
	}
	if q.Memory != nil && *q.Memory != "" {
		out["memory"] = *q.Memory
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// flattenQuantity is the response-side mirror of buildQuantityMap —
// turns a *resourceQuantity into a map[string]string for the
// fromResources / toResources envelope.
func flattenQuantity(q *resourceQuantity) map[string]string {
	if q == nil {
		return nil
	}
	out := map[string]string{}
	if q.CPU != nil && *q.CPU != "" {
		out["cpu"] = *q.CPU
	}
	if q.Memory != nil && *q.Memory != "" {
		out["memory"] = *q.Memory
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
