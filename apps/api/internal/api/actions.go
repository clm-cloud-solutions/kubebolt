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

var restartableTypes = map[string]bool{
	"deployments":  true,
	"statefulsets":  true,
	"daemonsets":    true,
}

var scalableTypes = map[string]bool{
	"deployments": true,
	"statefulsets": true,
}

func (h *handlers) handleRestart(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	if !restartableTypes[resourceType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot restart %s — only deployments, statefulsets, and daemonsets", resourceType))
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

	// Patch the pod template annotation to trigger a rollout restart
	// This is exactly what `kubectl rollout restart` does
	restartPatch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`,
		time.Now().Format(time.RFC3339),
	)
	patchBytes := []byte(restartPatch)

	var err error
	switch resourceType {
	case "deployments":
		_, err = clientset.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	case "statefulsets":
		_, err = clientset.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	case "daemonsets":
		_, err = clientset.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	}

	if err != nil {
		log.Printf("Restart failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Printf("Restart triggered: %s/%s/%s", resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}

func (h *handlers) handleScale(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	if !scalableTypes[resourceType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot scale %s — only deployments and statefulsets", resourceType))
		return
	}

	var body struct {
		Replicas int32 `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Replicas < 0 {
		respondError(w, http.StatusBadRequest, "replicas must be >= 0")
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

	// Use the scale subresource
	var currentReplicas int32
	switch resourceType {
	case "deployments":
		scale, err := clientset.AppsV1().Deployments(namespace).GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		currentReplicas = scale.Spec.Replicas
		scale.Spec.Replicas = body.Replicas
		_, err = clientset.AppsV1().Deployments(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "statefulsets":
		scale, err := clientset.AppsV1().StatefulSets(namespace).GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		currentReplicas = scale.Spec.Replicas
		scale.Spec.Replicas = body.Replicas
		_, err = clientset.AppsV1().StatefulSets(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	log.Printf("Scale: %s/%s/%s %d → %d", resourceType, namespace, name, currentReplicas, body.Replicas)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":       "scaled",
		"fromReplicas": currentReplicas,
		"toReplicas":   body.Replicas,
	})
}

func (h *handlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	propagation := metav1.DeletePropagationBackground
	if r.URL.Query().Get("orphan") == "true" {
		propagation = metav1.DeletePropagationOrphan
	}

	var gracePeriod *int64
	if r.URL.Query().Get("force") == "true" {
		zero := int64(0)
		gracePeriod = &zero
	}

	if err := conn.DeleteResource(resourceType, namespace, name, propagation, gracePeriod); err != nil {
		errMsg := err.Error()
		log.Printf("Delete failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		if containsForbidden(errMsg) {
			respondError(w, http.StatusForbidden, errMsg)
		} else {
			respondError(w, http.StatusInternalServerError, errMsg)
		}
		return
	}

	log.Printf("Deleted: %s/%s/%s", resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func containsForbidden(s string) bool {
	for _, sub := range []string{"forbidden", "Forbidden"} {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
