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
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
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

// setImageableTypes are workload kinds whose pod template containers
// can be patched in-place via `kubectl set image`. Same set as restart;
// they all expose `spec.template.spec.containers[]`.
var setImageableTypes = map[string]bool{
	"deployments":  true,
	"statefulsets": true,
	"daemonsets":   true,
}

// auditMutation emits a structured audit log entry for any cluster mutation.
// `source` distinguishes user-initiated UI actions ("ui") from Copilot
// proposals approved by the user ("copilot_proposal"); it comes from the
// X-KubeBolt-Action-Source header. It logs via slog (operational log line)
// AND, when an audit store is wired (Sprint 1), persists a durable record to
// the kobi_actions bucket for the admin action-history view.
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

	// Durable audit trail (Sprint 1). No-op until SetAuditStore wires a
	// store. OriginatingInsightID closes the insight→Kobi→action provenance
	// loop opened in Sprint 0 (frontend sends X-KubeBolt-Origin-Insight when
	// the Kobi chat was seeded from an insight).
	if auditStore != nil {
		clusterID := ""
		if auditClusterID != nil {
			clusterID = auditClusterID()
		}
		rec := &audit.Record{
			ID:                   uuid.New().String(),
			Timestamp:            time.Now().UTC(),
			Source:               source,
			UserID:               userID,
			Username:             username,
			Role:                 role,
			ClusterID:            clusterID,
			Action:               action,
			TargetType:           resourceType,
			TargetNamespace:      namespace,
			TargetName:           name,
			Params:               params,
			Result:               result,
			OriginatingInsightID: r.Header.Get("X-KubeBolt-Origin-Insight"),
		}
		if err != nil {
			rec.Error = err.Error()
		}
		if e := auditStore.Append(rec); e != nil {
			slog.Warn("audit persist failed", slog.String("error", e.Error()))
		}
	}
}

// copilotDestructiveBlocked reports whether this request is a Kobi-proposed
// destructive action that the admin has disabled via the destructive-ops
// sub-switch (Sprint 1). UI-initiated actions are governed by RBAC, not this
// gate — only copilot_proposal-sourced mutations are blocked. Defense in
// depth behind the tool-list filter (which already hides delete from the LLM
// when destructive ops are off; this also catches scale-to-0, which shares
// the non-destructive scale tool and so can't be tool-filtered).
func (h *handlers) copilotDestructiveBlocked(r *http.Request) bool {
	return !h.resolvedCopilotConfig().DestructiveActionsEnabled &&
		r.Header.Get("X-KubeBolt-Action-Source") == "copilot_proposal"
}

// resolvedCopilotConfig returns the live Copilot config (env baseline +
// BoltDB admin override) when the settings runtime is wired, else the env
// baseline — so the Sprint 1 governance toggles take effect from the admin
// UI without a restart.
func (h *handlers) resolvedCopilotConfig() config.CopilotConfig {
	if h.settingsRuntime != nil {
		return h.settingsRuntime.Copilot()
	}
	return h.copilotConfig
}

func (h *handlers) handleRestart(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	// Pods "restart" via delete (owning controller recreates them);
	// workloads via rollout-restart annotation patch. Dispatch by type
	// before the workload-only validation below — pods don't have a
	// `spec.template` to patch so they need the synth path.
	if resourceType == "pods" {
		h.restartPod(w, r, namespace, name)
		return
	}

	if !restartableTypes[resourceType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot restart %s — only pods, deployments, statefulsets, and daemonsets", resourceType))
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
	// Scale-to-0 is a destructive Kobi action — gated by the destructive-ops
	// sub-switch. Can't be tool-filtered (shares propose_scale_workload), so
	// it's enforced here.
	if body.Replicas == 0 && h.copilotDestructiveBlocked(r) {
		respondError(w, http.StatusForbidden, "scale-to-0 via Kobi is disabled by the destructive-ops admin setting")
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

	// Rollback supports the three workload kinds that have a
	// revision concept. Deployments use ReplicaSet-derived revisions;
	// StatefulSets and DaemonSets use ControllerRevisions.
	if resourceType != "deployments" && resourceType != "statefulsets" && resourceType != "daemonsets" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot rollback %s — only deployments, statefulsets, and daemonsets", resourceType))
		return
	}

	// toRevision is optional; 0 (or absent) means "previous revision".
	var body struct {
		ToRevision int64 `json:"toRevision"`
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

	var fromRev, toRev int64
	var err error
	switch resourceType {
	case "deployments":
		var f, t int
		f, t, err = conn.RollbackDeployment(namespace, name, int(body.ToRevision))
		fromRev, toRev = int64(f), int64(t)
	case "statefulsets":
		fromRev, toRev, err = conn.RollbackStatefulSet(namespace, name, body.ToRevision)
	case "daemonsets":
		fromRev, toRev, err = conn.RollbackDaemonSet(namespace, name, body.ToRevision)
	}
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

// imagePair is the container/image tuple reported in audit + UI.
type imagePair struct {
	Container string `json:"container"`
	Image     string `json:"image"`
}

// handleSetImage patches the container image(s) of a Deployment,
// StatefulSet, or DaemonSet via strategic merge patch — the same
// behavior as `kubectl set image`. Strategic merge knows that the
// `containers` array is keyed by `name`, so we only mutate the
// targeted entries' `image` field; env, volumes, probes, resources,
// and any other container fields are preserved.
//
// The request includes the from-image state in the response so the
// caller (and the audit log) records both sides of the change.
func (h *handlers) handleSetImage(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	if !setImageableTypes[resourceType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot set-image on %s — only deployments, statefulsets, and daemonsets", resourceType))
		return
	}

	var body struct {
		Images []imagePair `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Images) == 0 {
		respondError(w, http.StatusBadRequest, "images is required and must be non-empty")
		return
	}
	for i, img := range body.Images {
		if img.Container == "" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("images[%d].container is required", i))
			return
		}
		if img.Image == "" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("images[%d].image is required", i))
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

	// 1. Capture pre-patch state. We need both the from-image map for
	//    the audit / response AND the set of valid container names so
	//    we can reject "container does not exist" with a useful error
	//    instead of silently no-op'ing (which is what strategic merge
	//    would do — it'd add a phantom container to the array).
	currentImages, err := getCurrentContainerImages(ctx, clientset, resourceType, namespace, name)
	if err != nil {
		auditMutation(r, "set_image", resourceType, namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}
	validContainers := make([]string, 0, len(currentImages))
	for _, p := range currentImages {
		validContainers = append(validContainers, p.Container)
	}

	fromImages := make([]imagePair, 0, len(body.Images))
	for _, req := range body.Images {
		var found *imagePair
		for i := range currentImages {
			if currentImages[i].Container == req.Container {
				found = &currentImages[i]
				break
			}
		}
		if found == nil {
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"container %q not found in %s/%s; valid containers: %v",
				req.Container, resourceType, name, validContainers))
			return
		}
		fromImages = append(fromImages, *found)
	}

	// 2. Short-circuit if every requested image equals the current
	//    image. K8s would no-op the patch (no new revision), but
	//    surfacing "no changes" up front is friendlier than letting
	//    the client poll for a rollout that never happens.
	allUnchanged := true
	for i, req := range body.Images {
		if req.Image != fromImages[i].Image {
			allUnchanged = false
			break
		}
	}
	if allUnchanged {
		respondJSON(w, http.StatusOK, map[string]any{
			"status":     "unchanged",
			"fromImages": fromImages,
			"toImages":   body.Images,
		})
		return
	}

	// 3. Build the strategic merge patch.
	patchBytes, err := buildSetImagePatch(body.Images)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build patch")
		return
	}

	params := map[string]any{
		"fromImages": fromImages,
		"toImages":   body.Images,
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
		auditMutation(r, "set_image", resourceType, namespace, name, params, err)
		log.Printf("Set-image failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "set_image", resourceType, namespace, name, params, nil)
	// Return the post-mutation resource so the client can setQueryData
	// on its detail query and reflect the change immediately, same
	// pattern as handleRestart / handleScale.
	resource, _ := conn.GetResourceDetail(resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "patched",
		"fromImages": fromImages,
		"toImages":   body.Images,
		"resource":   resource,
	})
}

// getCurrentContainerImages returns the current container/image pairs
// of a workload's pod template spec. Used by handleSetImage to capture
// pre-patch state and to validate that every requested container
// actually exists.
func getCurrentContainerImages(ctx context.Context, clientset kubernetes.Interface, resourceType, namespace, name string) ([]imagePair, error) {
	switch resourceType {
	case "deployments":
		d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return containersToImagePairs(d.Spec.Template.Spec.Containers), nil
	case "statefulsets":
		sts, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return containersToImagePairs(sts.Spec.Template.Spec.Containers), nil
	case "daemonsets":
		ds, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return containersToImagePairs(ds.Spec.Template.Spec.Containers), nil
	}
	return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
}

// buildSetImagePatch returns the strategic-merge patch body for a set
// of container/image overrides. The shape
// `{spec:{template:{spec:{containers:[{name,image}]}}}}` is merged by
// name (strategic merge knows the patchMergeKey for PodSpec.containers
// is "name"), so only the targeted entries' image fields are touched —
// every other container field (env, ports, volumeMounts, probes,
// resources) is preserved on both targeted and untargeted containers.
func buildSetImagePatch(images []imagePair) ([]byte, error) {
	containers := make([]map[string]interface{}, len(images))
	for i, img := range images {
		containers[i] = map[string]interface{}{
			"name":  img.Container,
			"image": img.Image,
		}
	}
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": containers,
				},
			},
		},
	}
	return json.Marshal(patch)
}

func containersToImagePairs(cs []corev1.Container) []imagePair {
	out := make([]imagePair, len(cs))
	for i, c := range cs {
		out[i] = imagePair{Container: c.Name, Image: c.Image}
	}
	return out
}

func (h *handlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	// Delete is destructive — gated by the destructive-ops sub-switch for
	// Kobi-proposed actions (defense in depth; the LLM already can't see the
	// delete tool when the sub-switch is off).
	if h.copilotDestructiveBlocked(r) {
		respondError(w, http.StatusForbidden, "delete via Kobi is disabled by the destructive-ops admin setting")
		return
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

// handleCordon marks a Node unschedulable. It's the equivalent of
// `kubectl cordon <node>` — the controller stops placing new pods on
// the node, but existing pods keep running. Operators usually pair
// this with `drain` (Cut 2 of node-maintenance) for a full
// maintenance window; cordon alone is the right call when you want
// to prevent NEW workloads from landing without disrupting current
// ones.
//
// We Get-then-Patch instead of blind-Patch so we can detect the
// already-cordoned no-op and surface it explicitly. Some admission
// webhooks have side effects on every Patch even when the field
// doesn't change, so the round-trip is worth the extra read.
func (h *handlers) handleCordon(w http.ResponseWriter, r *http.Request) {
	h.handleSetNodeSchedulability(w, r, true /*unschedulable*/, "cordon")
}

// handleUncordon is cordon's inverse — clears Node.spec.unschedulable
// so the controller can place pods on the node again. Same Get-then-
// Patch shape, same audit + response semantics.
func (h *handlers) handleUncordon(w http.ResponseWriter, r *http.Request) {
	h.handleSetNodeSchedulability(w, r, false /*unschedulable*/, "uncordon")
}

// handleSetNodeSchedulability is the shared implementation behind
// cordon/uncordon. Pulled out so the audit-action label, target-state
// flag, and "alreadyX" response key are the only things that vary.
func (h *handlers) handleSetNodeSchedulability(w http.ResponseWriter, r *http.Request, target bool, action string) {
	resourceType := chi.URLParam(r, "type")
	name := chi.URLParam(r, "name")

	// Node is cluster-scoped. The router uses the same {type}/{ns}/{name}
	// template as namespaced resources for consistency, with `_` as the
	// placeholder. We don't read namespace because nodes don't have one,
	// but we do enforce that the type is right — otherwise a
	// `POST /resources/pods/.../cordon` would silently no-op.
	if resourceType != "nodes" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot %s %s — only nodes are cordon/uncordon-able", action, resourceType))
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

	node, err := clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		auditMutation(r, action, resourceType, "", name, nil, err)
		respondMutationError(w, err)
		return
	}

	already := node.Spec.Unschedulable == target
	params := map[string]any{
		"target":  target,
		"alreadyAtTarget": already,
	}

	if already {
		// No-op: skip the apiserver round-trip but still return the
		// node detail so the client's setQueryData stays consistent.
		auditMutation(r, action, resourceType, "", name, params, nil)
		nodeDetail, _ := conn.GetResourceDetail("nodes", "", name)
		if nodeDetail != nil {
			// We Get'd live above; informer can still lag, so
			// override defensively for parity with the patch path.
			nodeDetail["unschedulable"] = target
		}
		respondJSON(w, http.StatusOK, buildSchedulabilityResponse(action, nodeDetail, true))
		return
	}

	patch, err := buildSchedulabilityPatch(target)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build patch")
		return
	}

	if _, err := clientset.CoreV1().Nodes().Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		auditMutation(r, action, resourceType, "", name, params, err)
		log.Printf("%s failed for node/%s: %v", action, name, err)
		respondMutationError(w, err)
		return
	}

	// Recent-writes overlay covers the gap between this Patch landing
	// on the apiserver and the informer cache catching up (~hundreds
	// of ms). Subsequent GETs — including the manual Refresh button,
	// the Nodes list page, and the auto-poll — read the overlay and
	// see `unschedulable: target` until either the informer converges
	// or the 5s TTL expires. Without this, hitting Refresh in the
	// first second after Cordon/Uncordon flickered back to the
	// pre-patch value before settling on the next refetch tick.
	// Same root cause + same fix as deployments.paused — see
	// cluster/recent_writes.go.
	conn.RecentWrites().Record("nodes", "", name, "unschedulable", target, 5*time.Second)

	auditMutation(r, action, resourceType, "", name, params, nil)
	nodeDetail, _ := conn.GetResourceDetail("nodes", "", name)
	// GetResourceDetail's overlay-application picks up the Record
	// above, so nodeDetail["unschedulable"] is already correct. The
	// explicit override below is belt-and-suspenders in case a
	// future refactor breaks the overlay path on the read side.
	if nodeDetail != nil {
		nodeDetail["unschedulable"] = target
	}
	respondJSON(w, http.StatusOK, buildSchedulabilityResponse(action, nodeDetail, false))
}

// buildSchedulabilityPatch returns the JSON merge-patch body for
// flipping Node.spec.unschedulable. Extracted so tests can verify
// the exact bytes without standing up an apiserver.
func buildSchedulabilityPatch(unschedulable bool) ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{
			"unschedulable": unschedulable,
		},
	})
}

// buildSchedulabilityResponse mirrors the `{status, alreadyX, node}`
// shape the spec defines so cordon and uncordon return the same
// schema (just with the action-specific status string + flag name).
func buildSchedulabilityResponse(action string, node map[string]interface{}, already bool) map[string]interface{} {
	resp := map[string]interface{}{
		"node": node,
	}
	switch action {
	case "cordon":
		resp["status"] = "cordoned"
		resp["alreadyCordoned"] = already
	case "uncordon":
		resp["status"] = "uncordoned"
		resp["alreadyUncordoned"] = already
	default:
		resp["status"] = action
		resp["alreadyAtTarget"] = already
	}
	return resp
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
