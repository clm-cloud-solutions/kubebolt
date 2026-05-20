package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Set HPA bounds — strategic merge patch on spec.minReplicas /
// spec.maxReplicas of a HorizontalPodAutoscaler. The companion of the
// rest of the set_* family but scoped to autoscaling/v1.
//
// Why a dedicated endpoint instead of the generic YAML PUT:
//   - HPA spec is small; a full-YAML round-trip is overkill for what's
//     almost always a maxReplicas bump.
//   - We want a hard upper cap (maxReplicas <= 1000) enforced server-
//     side so Kobi proposals and direct UI clicks share the same
//     safety floor. The YAML editor doesn't know about that ceiling.
//   - max >= min validation is friendlier here than letting the
//     apiserver bounce a 422 back through the audit log.
//
// Scope of v1:
//   - autoscaling/v1 only (the lister + connector use v1). v2's
//     behavior/metrics block is out of scope; if you need to edit
//     those, use the YAML editor.
//   - Both fields optional but at least one must be present (400 if
//     both omitted). Sending only `minReplicas` to lower the floor is
//     a valid case (e.g. cost optimization on quiet workloads).

// maxReplicasSafetyCap is a server-side ceiling on the maxReplicas a
// caller can patch into an HPA. Far above any reasonable production
// workload and below the point where misuse would cause runaway pod
// creation. Tuned to be roughly "if you legitimately need more than
// this, your cluster ops should sign off through a different
// channel."
const maxReplicasSafetyCap = 1000

type setHpaBoundsRequest struct {
	MinReplicas *int32 `json:"minReplicas,omitempty"`
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`
}

// hpaBoundsPair is the from/to envelope returned in the response, so
// the UI can render a clean before/after without re-reading state.
type hpaBoundsPair struct {
	MinReplicas int32 `json:"minReplicas"`
	MaxReplicas int32 `json:"maxReplicas"`
}

func (h *handlers) handleSetHpaBounds(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	// Route accepts both alias (hpas) and full kind (horizontalpodautoscalers)
	// to keep the URL ergonomic and consistent with the rest of the API,
	// which is alias-friendly throughout.
	if resourceType != "hpas" && resourceType != "horizontalpodautoscalers" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"cannot set-bounds on %s — only hpas / horizontalpodautoscalers", resourceType))
		return
	}

	var body setHpaBoundsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.MinReplicas == nil && body.MaxReplicas == nil {
		respondError(w, http.StatusBadRequest,
			"at least one of minReplicas or maxReplicas is required")
		return
	}
	if body.MinReplicas != nil && *body.MinReplicas < 0 {
		respondError(w, http.StatusBadRequest, "minReplicas must be >= 0")
		return
	}
	if body.MaxReplicas != nil && *body.MaxReplicas < 1 {
		respondError(w, http.StatusBadRequest, "maxReplicas must be >= 1")
		return
	}
	if body.MaxReplicas != nil && *body.MaxReplicas > maxReplicasSafetyCap {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"maxReplicas must be <= %d (safety cap)", maxReplicasSafetyCap))
		return
	}
	if body.MinReplicas != nil && body.MaxReplicas != nil && *body.MaxReplicas < *body.MinReplicas {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"maxReplicas (%d) must be >= minReplicas (%d)", *body.MaxReplicas, *body.MinReplicas))
		return
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	clientset := conn.Clientset()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Capture pre-patch state for audit + from/to response. Doubles as
	// existence check — a 404 here means the HPA does not exist and we
	// fail before we ever shape the patch.
	current, err := clientset.AutoscalingV1().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		auditMutation(r, "set_hpa_bounds", resourceType, namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}

	from := hpaBoundsPair{
		MinReplicas: 1, // K8s default when spec.minReplicas is nil
		MaxReplicas: current.Spec.MaxReplicas,
	}
	if current.Spec.MinReplicas != nil {
		from.MinReplicas = *current.Spec.MinReplicas
	}

	// Resolved to-state: keep whatever side the caller didn't change.
	to := from
	if body.MinReplicas != nil {
		to.MinReplicas = *body.MinReplicas
	}
	if body.MaxReplicas != nil {
		to.MaxReplicas = *body.MaxReplicas
	}
	// Cross-validate the resolved pair (handles "send only min that
	// would invert the current max" case).
	if to.MaxReplicas < to.MinReplicas {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"resolved maxReplicas (%d) would be < minReplicas (%d) — adjust the other bound in the same patch",
			to.MaxReplicas, to.MinReplicas))
		return
	}

	// Short-circuit if neither bound actually changed — saves a
	// useless audit entry and frontend rollout-wait.
	if to == from {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":     "unchanged",
			"fromBounds": from,
			"toBounds":   to,
		})
		return
	}

	patchBytes, err := buildHpaBoundsPatch(body.MinReplicas, body.MaxReplicas)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build patch")
		return
	}

	params := map[string]any{
		"fromBounds": from,
		"toBounds":   to,
	}

	_, err = clientset.AutoscalingV1().HorizontalPodAutoscalers(namespace).Patch(
		ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		auditMutation(r, "set_hpa_bounds", resourceType, namespace, name, params, err)
		log.Printf("Set-HPA-bounds failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "set_hpa_bounds", resourceType, namespace, name, params, nil)
	resource, _ := conn.GetResourceDetail(resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "patched",
		"fromBounds": from,
		"toBounds":   to,
		"resource":   resource,
	})
}

// buildHpaBoundsPatch shapes the strategic-merge JSON for the HPA
// spec mutation. Only the dimensions the caller actually set appear
// in the patch — strategic merge then leaves untouched fields alone.
func buildHpaBoundsPatch(min, max *int32) ([]byte, error) {
	spec := map[string]any{}
	if min != nil {
		spec["minReplicas"] = *min
	}
	if max != nil {
		spec["maxReplicas"] = *max
	}
	return json.Marshal(map[string]any{"spec": spec})
}
