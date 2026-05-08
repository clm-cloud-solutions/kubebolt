package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Rollout pause / resume — kubectl rollout pause / kubectl rollout resume.
// Flips Deployment.spec.paused so the deployment controller stops
// reconciling new diffs without scaling pods to zero or rolling back.
// The two main use cases:
//
//   - Freeze a misbehaving rolling update mid-flight while you check
//     metrics, without committing to a rollback that you might not
//     need.
//   - Pre-stage a sequence of edits (set-image + set-resources +
//     set-env, etc.) and resume so they all land as one ReplicaSet
//     instead of N cascading rollouts.
//
// Scope: Deployment-only. Upstream apps/v1 StatefulSet has no
// .spec.paused field as of K8s 1.32; some downstream forks (OpenShift,
// Rancher) ship one but it's non-portable. DaemonSets have no
// equivalent. Wrong types get a 400 with a clear message — see
// internal/k8s-operations/tier2-rollout-pause-resume.md for the full
// type-scope reasoning.
//
// URL paths use the `rollout-` prefix (rollout-pause / rollout-resume)
// rather than reusing /pause /resume because /resume is already taken
// by the CronJob suspend/resume handler. The prefix calques `kubectl
// rollout pause` directly and disambiguates the two semantically
// distinct kubectl verbs.

var rolloutPausableTypes = map[string]bool{
	"deployments": true,
}

// handleRolloutPause sets Deployment.spec.paused=true.
// The deployment controller stops reconciling on the next loop;
// existing pods continue running and any in-flight rolling update
// stays at whatever progress it reached.
func (h *handlers) handleRolloutPause(w http.ResponseWriter, r *http.Request) {
	h.handleSetRolloutPaused(w, r, true /*paused*/, "rollout-pause")
}

// handleRolloutResume — inverse of pause.
func (h *handlers) handleRolloutResume(w http.ResponseWriter, r *http.Request) {
	h.handleSetRolloutPaused(w, r, false /*paused*/, "rollout-resume")
}

// handleSetRolloutPaused is the shared implementation behind pause /
// resume. Pulled out so the audit-action label and "alreadyX" response
// key are the only things that vary. Same Get-then-Patch shape as
// CronJob suspend/resume so we can detect "already at target state"
// and skip the patch (avoids spurious admission-webhook fires for
// no-ops).
func (h *handlers) handleSetRolloutPaused(w http.ResponseWriter, r *http.Request, target bool, action string) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if !rolloutPausableTypes[resourceType] {
		respondError(w, http.StatusBadRequest,
			fmt.Sprintf("rollout pause/resume not supported for %s — only deployments", resourceType))
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

	dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		auditMutation(r, action, resourceType, namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}

	// Deployment.Spec.Paused is a plain bool (zero value = false / "active"),
	// so no nil check needed — unlike CronJob.Spec.Suspend which is *bool.
	already := dep.Spec.Paused == target
	params := map[string]any{
		"target":          target,
		"alreadyAtTarget": already,
		"fromPaused":      dep.Spec.Paused,
		"toPaused":        target,
	}

	if already {
		auditMutation(r, action, resourceType, namespace, name, params, nil)
		detail, _ := conn.GetResourceDetail("deployments", namespace, name)
		respondJSON(w, http.StatusOK, buildRolloutResponse(action, detail, true))
		return
	}

	patch, err := buildRolloutPausedPatch(target)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build patch")
		return
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Patch(ctx, name,
		types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		auditMutation(r, action, resourceType, namespace, name, params, err)
		respondMutationError(w, err)
		return
	}

	// Record the just-patched value in the read-after-write overlay
	// so subsequent GETs (the manual Refresh button, the auto-poll,
	// or the deployments list page) read `paused: target` until the
	// informer cache catches up — typically <500ms but the overlay's
	// 5s TTL gives plenty of headroom. Without this, hitting Refresh
	// in the first second after Pause/Resume reads the pre-patch
	// value from the informer cache and the UI looks like the
	// action didn't take.
	conn.RecentWrites().Record("deployments", namespace, name, "paused", target, 5*time.Second)

	auditMutation(r, action, resourceType, namespace, name, params, nil)
	detail, _ := conn.GetResourceDetail("deployments", namespace, name)
	// Same defensive override on the response payload itself —
	// GetResourceDetail's own overlay-application picks this up too,
	// but we keep the explicit override for clarity (and as belt-and-
	// suspenders if a future refactor breaks the overlay path).
	if detail != nil {
		detail["paused"] = target
	}
	respondJSON(w, http.StatusOK, buildRolloutResponse(action, detail, false))
}

// buildRolloutPausedPatch is the JSON merge patch for flipping
// spec.paused. Kept pure so tests can assert the exact bytes
// without standing up an apiserver.
func buildRolloutPausedPatch(paused bool) ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{
			"paused": paused,
		},
	})
}

// buildRolloutResponse formats the JSON body returned to the UI so
// the response shape carries the action's result vocabulary directly
// (status: "paused" / "resumed", alreadyPaused / alreadyActive flags).
// The UI uses these to drive panel state without re-reading the
// resource detail, which is why the response also carries the full
// (post-patch) deployment payload.
func buildRolloutResponse(action string, dep map[string]interface{}, already bool) map[string]interface{} {
	resp := map[string]interface{}{
		"deployment": dep,
	}
	switch action {
	case "rollout-pause":
		resp["status"] = "paused"
		resp["alreadyPaused"] = already
	case "rollout-resume":
		resp["status"] = "resumed"
		resp["alreadyActive"] = already
	default:
		resp["status"] = action
	}
	return resp
}
