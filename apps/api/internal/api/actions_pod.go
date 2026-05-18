package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// restartPod synthesizes a "restart" on a Pod by deleting it; the
// owning controller (Deployment / StatefulSet / DaemonSet / Job /
// ReplicaSet) recreates it on its next reconcile. Standalone pods
// without a controller stay deleted — the UI confirm modal warns
// operators of that case.
//
// Audit label is "restart_pod" (distinct from "restart_workload"
// which patches the rollout-restart annotation) so audit-log readers
// can tell the two apart even though the user triggered the same
// "Restart" button.
//
// Called from handleRestart when the resource type is "pods" — see
// the dispatch branch there.
func (h *handlers) restartPod(w http.ResponseWriter, r *http.Request, namespace, name string) {
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	// Default propagation + grace period — matches `kubectl delete
	// pod <name>` semantics. The owner reconcile loop picks up the
	// deletion and re-creates the pod with the same spec.
	if err := conn.DeleteResource("pods", namespace, name, metav1.DeletePropagationBackground, nil); err != nil {
		auditMutation(r, "restart_pod", "pods", namespace, name, nil, err)
		log.Printf("Restart pod failed for %s/%s: %v", namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "restart_pod", "pods", namespace, name, nil, nil)
	// `resource: null` matches the workload-restart response shape so
	// the shared frontend handler can treat both uniformly (the
	// setQueryData call is a no-op when resource is null — correct
	// for the pod case where the resource just got deleted).
	respondJSON(w, http.StatusOK, map[string]any{
		"status":   "restarting",
		"resource": nil,
	})
}

// handleEvictPod removes a Pod via the Eviction API
// (`policy/v1.Eviction`), which respects PodDisruptionBudgets.
// Different from Delete: when a PDB would be violated by removing
// this pod, the API returns 429 TooManyRequests instead of evicting,
// so the operator can pick a different pod or wait for the
// disruption window to open.
//
// The 429 path returns a structured JSON payload with
// `pdbBlocked: true` so the frontend can render an explicit
// "blocked by PodDisruptionBudget" message rather than a generic
// rate-limit error.
func (h *handlers) handleEvictPod(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if resourceType != "pods" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot evict %s — endpoint only accepts pods", resourceType))
		return
	}
	if namespace == "_" {
		namespace = ""
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	clientset := conn.Clientset()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	eviction := &policyv1.Eviction{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "policy/v1",
			Kind:       "Eviction",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}

	if err := clientset.PolicyV1().Evictions(namespace).Evict(ctx, eviction); err != nil {
		auditMutation(r, "evict_pod", "pods", namespace, name, nil, err)
		log.Printf("Evict pod failed for %s/%s: %v", namespace, name, err)
		if apierrors.IsTooManyRequests(err) {
			// PDB-protected — pass the apiserver message through with a
			// flag the frontend uses to switch the toast / modal copy
			// from a generic 429 to "blocked by PodDisruptionBudget".
			respondJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":      err.Error(),
				"pdbBlocked": true,
			})
			return
		}
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "evict_pod", "pods", namespace, name, nil, nil)
	respondJSON(w, http.StatusOK, map[string]string{"status": "evicted"})
}
