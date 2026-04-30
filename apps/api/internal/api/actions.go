package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
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

// auditMutation emits a structured audit log entry for any cluster mutation.
// `source` distinguishes user-initiated UI actions ("ui") from Copilot
// proposals approved by the user ("copilot_proposal"); it comes from the
// X-KubeBolt-Action-Source header. The PoC writes to stderr via slog; the
// production version will persist to BoltDB.
func auditMutation(r *http.Request, action, resourceType, namespace, name string, params map[string]any, err error) {
	source := r.Header.Get("X-KubeBolt-Action-Source")
	if source == "" {
		source = "ui"
	}
	var userID, username string
	if claims := auth.ContextClaims(r); claims != nil {
		userID = claims.UserID
		username = claims.Username
	}
	role := string(auth.ContextRole(r))
	result := "success"
	attrs := []any{
		slog.String("audit", "mutation"),
		slog.String("action", action),
		slog.String("source", source),
		slog.String("user_id", userID),
		slog.String("username", username),
		slog.String("role", role),
		slog.String("target_type", resourceType),
		slog.String("target_namespace", namespace),
		slog.String("target_name", name),
		slog.Any("params", params),
	}
	if err != nil {
		result = "error"
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	attrs = append(attrs, slog.String("result", result))
	if err != nil {
		slog.Warn("cluster mutation", attrs...)
	} else {
		slog.Info("cluster mutation", attrs...)
	}
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
		auditMutation(r, "restart_workload", resourceType, namespace, name, nil, err)
		log.Printf("Restart failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "restart_workload", resourceType, namespace, name, nil, nil)
	// Return the post-mutation object so the client can call setQueryData
	// on its `['resource-detail', type, ns, name]` query and reflect the
	// change immediately, without waiting for the next WS event or poll.
	// The informer cache may lag by a few ms behind the K8s API write;
	// the WS event that follows will reconcile any small staleness.
	resource, _ := conn.GetResourceDetail(resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "restarting",
		"resource": resource,
	})
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

	params := map[string]any{"replicas": body.Replicas}

	// Use the scale subresource
	var currentReplicas int32
	switch resourceType {
	case "deployments":
		scale, err := clientset.AppsV1().Deployments(namespace).GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			auditMutation(r, "scale_workload", resourceType, namespace, name, params, err)
			respondMutationError(w, err)
			return
		}
		currentReplicas = scale.Spec.Replicas
		scale.Spec.Replicas = body.Replicas
		_, err = clientset.AppsV1().Deployments(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
		if err != nil {
			auditMutation(r, "scale_workload", resourceType, namespace, name, params, err)
			respondMutationError(w, err)
			return
		}
	case "statefulsets":
		scale, err := clientset.AppsV1().StatefulSets(namespace).GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			auditMutation(r, "scale_workload", resourceType, namespace, name, params, err)
			respondMutationError(w, err)
			return
		}
		currentReplicas = scale.Spec.Replicas
		scale.Spec.Replicas = body.Replicas
		_, err = clientset.AppsV1().StatefulSets(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
		if err != nil {
			auditMutation(r, "scale_workload", resourceType, namespace, name, params, err)
			respondMutationError(w, err)
			return
		}
	}

	params["fromReplicas"] = currentReplicas
	auditMutation(r, "scale_workload", resourceType, namespace, name, params, nil)
	// See handleRestart for rationale on returning the post-mutation
	// object — lets the client setQueryData and reflect the change
	// before the next WS event arrives.
	resource, _ := conn.GetResourceDetail(resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":       "scaled",
		"fromReplicas": currentReplicas,
		"toReplicas":   body.Replicas,
		"resource":     resource,
	})
}

func (h *handlers) handleRollback(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	// PoC: rollback only supported for Deployments. STS/DS use ControllerRevisions
	// with different rollback semantics; left for a future iteration.
	if resourceType != "deployments" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot rollback %s — only deployments are supported", resourceType))
		return
	}

	// toRevision is optional; 0 (or absent) means "previous revision".
	var body struct {
		ToRevision int `json:"toRevision"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	params := map[string]any{"toRevision": body.ToRevision}

	fromRev, toRev, err := conn.RollbackDeployment(namespace, name, body.ToRevision)
	if fromRev > 0 {
		params["fromRevision"] = fromRev
	}
	if toRev > 0 {
		params["resolvedToRevision"] = toRev
	}
	if err != nil {
		auditMutation(r, "rollback_deployment", resourceType, namespace, name, params, err)
		log.Printf("Rollback failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		// "no history / no-op" (precondition failure → 400) is the
		// only path that doesn't fit the generic mutation-error
		// helper. Forbidden + everything else delegate.
		errMsg := err.Error()
		if strings.Contains(errMsg, "no rollback history") || strings.Contains(errMsg, "no-op") || strings.Contains(errMsg, "not found") {
			respondError(w, http.StatusBadRequest, errMsg)
			return
		}
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "rollback_deployment", resourceType, namespace, name, params, nil)
	resource, _ := conn.GetResourceDetail(resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "rolling-back",
		"fromRevision":  fromRev,
		"toRevision":    toRev,
		"resource":      resource,
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
	orphan := r.URL.Query().Get("orphan") == "true"
	if orphan {
		propagation = metav1.DeletePropagationOrphan
	}

	var gracePeriod *int64
	force := r.URL.Query().Get("force") == "true"
	if force {
		zero := int64(0)
		gracePeriod = &zero
	}

	params := map[string]any{"force": force, "orphan": orphan}

	if err := conn.DeleteResource(resourceType, namespace, name, propagation, gracePeriod); err != nil {
		auditMutation(r, "delete", resourceType, namespace, name, params, err)
		log.Printf("Delete failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "delete", resourceType, namespace, name, params, nil)
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

// agentSAForbiddenRE picks out the verb + resource from a K8s
// apiserver "User ... cannot <verb> resource <resource>" message
// when the rejected SA is the kubebolt-agent's. We use this to tag
// 403 responses with structured fields so the UI can render a
// tier-aware hint ("agent is in reader mode — switch to operator").
//
// Example match:
//   User "system:serviceaccount:kubebolt-system:kubebolt-agent"
//   cannot patch resource "deployments" in API group "apps" in
//   the namespace "demo"
//
// → verb="patch", resource="deployments"
var agentSAForbiddenRE = regexp.MustCompile(
	`User "system:serviceaccount:[^"]*:kubebolt-agent" cannot ([a-z]+) resource "([^"]+)"`,
)

// respondMutationError maps a cluster-mutation error (from any of
// the action handlers) to the right HTTP status + payload. The
// frontend's resource action UIs catch this shape to render
// guidance instead of dumping the raw message in an alert().
//
// Status mapping:
//   - K8s 403 / "forbidden" in message → 403, with optional
//     `agentRbacForbidden:true` + verb + resource when the rejected
//     SA matches our agent.
//   - Anything else → 500.
//
// Audit logging is the caller's responsibility — same as before
// (this function is purely about shaping the HTTP response).
func respondMutationError(w http.ResponseWriter, err error) {
	msg := err.Error()
	if apierrors.IsForbidden(err) || containsForbidden(msg) {
		payload := map[string]any{"error": msg}
		if m := agentSAForbiddenRE.FindStringSubmatch(msg); len(m) == 3 {
			payload["agentRbacForbidden"] = true
			payload["verb"] = m[1]
			payload["resource"] = m[2]
		}
		respondJSON(w, http.StatusForbidden, payload)
		return
	}
	respondError(w, http.StatusInternalServerError, msg)
}
