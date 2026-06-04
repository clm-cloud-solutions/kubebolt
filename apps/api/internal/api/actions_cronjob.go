package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// CronJob ergonomics — three actions that kubectl exposes but
// KubeBolt didn't, all important enough that the operator was
// previously dropping to a terminal:
//
//   - suspend: pause the schedule so future runs don't fire while
//              you investigate. Pure spec.suspend=true patch.
//   - resume:  inverse, spec.suspend=false. Both flag changes are
//              tiny merge patches; we Get-then-Patch so we can
//              detect "already in target state" no-ops and surface
//              them in the response.
//   - trigger: equivalent to `kubectl create job --from=cronjob/X`.
//              Reproduces what kubectl does internally: clones the
//              CronJob's jobTemplate spec into a fresh Job, tags
//              it with the standard `cronjob.kubernetes.io/
//              instantiate=manual` annotation + an ownerReference
//              back to the CronJob, then Creates it. The OwnerRef
//              is what makes the manual run show up in
//              `kubectl get jobs --selector ...` and in the
//              CronJob's child-jobs view.
//
// Suspend/resume are gated Editor+, same tier as restart/scale/
// set-image. Trigger is also Editor+ — it spawns a Job that runs
// arbitrary code from the CronJob's spec, but that code was
// already approved by whoever wrote the CronJob in the first
// place; the operator isn't injecting anything new.

var cronJobActionableTypes = map[string]bool{
	"cronjobs": true,
}

// handleCronJobSuspend marks the CronJob's spec.suspend=true.
// Future scheduled runs won't fire until resumed. In-flight Jobs
// continue to completion — suspend only stops FUTURE runs.
func (h *handlers) handleCronJobSuspend(w http.ResponseWriter, r *http.Request) {
	h.handleSetCronJobSuspend(w, r, true /*suspend*/, "suspend")
}

// handleCronJobResume — inverse of suspend.
func (h *handlers) handleCronJobResume(w http.ResponseWriter, r *http.Request) {
	h.handleSetCronJobSuspend(w, r, false /*suspend*/, "resume")
}

// handleSetCronJobSuspend is the shared implementation behind
// suspend/resume. Pulled out so the audit-action label and "alreadyX"
// response key are the only things that vary. Same Get-then-Patch
// shape as cordon/uncordon for the same reason: detect no-ops without
// triggering admission webhooks for nothing.
func (h *handlers) handleSetCronJobSuspend(w http.ResponseWriter, r *http.Request, target bool, action string) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if !cronJobActionableTypes[resourceType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot %s %s — only cronjobs support this action", action, resourceType))
		return
	}

	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	clientset := conn.Clientset()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	cj, err := clientset.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		auditMutation(r, action, resourceType, namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}

	// CronJob.Spec.Suspend is *bool — nil means "use default false".
	// Treat nil as false for the comparison so a never-toggled
	// CronJob doesn't get a spurious "already not suspended" no-op
	// response when the operator clicks Resume on it.
	currentSuspended := cj.Spec.Suspend != nil && *cj.Spec.Suspend
	already := currentSuspended == target

	params := map[string]any{
		"target":          target,
		"alreadyAtTarget": already,
	}

	if already {
		auditMutation(r, action, resourceType, namespace, name, params, nil)
		cjDetail, _ := conn.GetResourceDetail("cronjobs", namespace, name)
		respondJSON(w, http.StatusOK, buildCronJobSuspendResponse(action, cjDetail, true))
		return
	}

	patch, err := buildCronJobSuspendPatch(target)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build patch")
		return
	}
	if _, err := clientset.BatchV1().CronJobs(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		auditMutation(r, action, resourceType, namespace, name, params, err)
		log.Printf("%s failed for cronjob/%s/%s: %v", action, namespace, name, err)
		respondMutationError(w, err)
		return
	}

	// Recent-writes overlay — same pattern as deployments.paused and
	// nodes.unschedulable. Covers the read-after-write window (the
	// few hundred ms between Patch landing and the informer cache
	// catching up) so a manual Refresh after Suspend/Resume reads
	// the post-patch value, not the stale informer state. See
	// cluster/recent_writes.go.
	conn.RecentWrites().Record("cronjobs", namespace, name, "suspend", target, 5*time.Second)

	auditMutation(r, action, resourceType, namespace, name, params, nil)
	cjDetail, _ := conn.GetResourceDetail("cronjobs", namespace, name)
	// GetResourceDetail's overlay-application picks up the Record
	// above. The explicit override below is belt-and-suspenders for
	// the response payload itself.
	if cjDetail != nil {
		cjDetail["suspend"] = target
	}
	respondJSON(w, http.StatusOK, buildCronJobSuspendResponse(action, cjDetail, false))
}

// buildCronJobSuspendPatch is the JSON merge patch for flipping
// spec.suspend. Kept pure so tests can assert the exact bytes
// without standing up an apiserver.
func buildCronJobSuspendPatch(suspend bool) ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{
			"suspend": suspend,
		},
	})
}

func buildCronJobSuspendResponse(action string, cj map[string]interface{}, already bool) map[string]interface{} {
	resp := map[string]interface{}{
		"cronJob": cj,
	}
	switch action {
	case "suspend":
		resp["status"] = "suspended"
		resp["alreadySuspended"] = already
	case "resume":
		resp["status"] = "resumed"
		resp["alreadyActive"] = already
	default:
		resp["status"] = action
		resp["alreadyAtTarget"] = already
	}
	return resp
}

// triggerRequest is the (optional) body for /trigger. All fields
// are optional — without a body, we auto-generate a Job name and
// don't auto-suspend.
type triggerRequest struct {
	JobName              string `json:"jobName"`
	SuspendAfterTrigger  bool   `json:"suspendAfterTrigger"`
}

// handleCronJobTrigger creates a fresh Job from the CronJob's
// jobTemplate. Replicates what `kubectl create job
// --from=cronjob/X` does internally:
//
//  1. Get the source CronJob.
//  2. Build a Job with:
//     - jobTemplate.spec verbatim (so env, volumes, resources,
//       backoffLimit, etc. all carry forward).
//     - Annotation `cronjob.kubernetes.io/instantiate=manual` —
//       the standard marker kubectl uses, surfaces the manual
//       origin in `kubectl describe job`.
//     - OwnerReference back to the CronJob, so the manual run
//       shows up in the CronJob's child-jobs list and gets
//       garbage-collected with the parent.
//     - Labels copied from jobTemplate.Labels so any selectors
//       work uniformly across scheduled and manual runs.
//  3. Create.
//
// If the operator-supplied jobName conflicts with an existing Job,
// we return 409 with a useful message. Auto-generated names use
// `<cronjob>-manual-<unix>` which has unix-second collision risk
// only at >1 manual trigger/sec — acceptable for now.
func (h *handlers) handleCronJobTrigger(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if !cronJobActionableTypes[resourceType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot trigger %s — only cronjobs support this action", resourceType))
		return
	}

	var body triggerRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}

	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	clientset := conn.Clientset()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	cj, err := clientset.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		auditMutation(r, "trigger", resourceType, namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}

	jobName := body.JobName
	if jobName == "" {
		jobName = fmt.Sprintf("%s-manual-%d", name, time.Now().Unix())
	}

	// auth.ContextClaims returns nil when auth is disabled — leave
	// username empty so the audit annotation isn't a fake "system"
	// stamp on a manually-fired Job.
	username := ""
	if claims := auth.ContextClaims(r); claims != nil {
		username = claims.Username
	}

	job := buildManualJobFromCronJob(cj, jobName, username)

	created, err := clientset.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		auditMutation(r, "trigger", resourceType, namespace, name, map[string]any{"jobName": jobName}, err)
		// Name conflict deserves a clearer status code than the
		// generic mutation error path: 409 instead of 500.
		if apierrors.IsAlreadyExists(err) {
			respondError(w, http.StatusConflict, fmt.Sprintf("job %q already exists in namespace %q — pick a different name or wait for the existing one to finish", jobName, namespace))
			return
		}
		log.Printf("trigger failed for cronjob/%s/%s: %v", namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "trigger", resourceType, namespace, name, map[string]any{
		"jobName":             created.Name,
		"suspendAfterTrigger": body.SuspendAfterTrigger,
	}, nil)

	// Build the canonical Job map directly from the freshly-created
	// object (NOT via conn.GetResourceDetail, which would read from
	// the informer cache and return "not found" until the watch
	// event propagates). The frontend uses this to pre-populate its
	// detail-cache so the navigated-to /jobs/<ns>/<name> page can
	// render the job from cache while the informer catches up.
	// Same defense against informer lag we used in cordon.
	jobMap := cluster.JobToMap(created)

	// Optional follow-up: suspend the cron after triggering. Useful
	// when the operator wants to inspect a problem in isolation
	// without the schedule firing again. We do this AFTER the Job is
	// created so a failed suspend doesn't undo the manual trigger.
	if body.SuspendAfterTrigger {
		patch, _ := buildCronJobSuspendPatch(true)
		if _, err := clientset.BatchV1().CronJobs(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			log.Printf("post-trigger suspend failed for cronjob/%s/%s: %v", namespace, name, err)
			// Don't fail the whole request — the trigger worked, the
			// suspend is bonus. Surface the partial success in the
			// response.
			respondJSON(w, http.StatusOK, map[string]interface{}{
				"status":       "triggered",
				"job":          jobMap,
				"fromCronJob":  name,
				"suspendError": err.Error(),
			})
			return
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "triggered",
		"job":         jobMap,
		"fromCronJob": name,
		"suspended":   body.SuspendAfterTrigger,
	})
}

// buildManualJobFromCronJob clones the CronJob's jobTemplate into
// a fresh Job ready for Create. Pulled out as a pure function so
// tests can assert the resulting Job structure (annotations,
// owner ref, labels, spec) against fixtures without an apiserver.
//
// `username` is the operator's identity; if empty (auth disabled),
// we skip the triggered-by annotation entirely so the audit chain
// doesn't lie about who initiated the run.
func buildManualJobFromCronJob(cj *batchv1.CronJob, jobName, username string) *batchv1.Job {
	annotations := map[string]string{
		// Standard kubectl annotation. `kubectl describe job` shows
		// "Created by: cronjob/X (manual)" thanks to this.
		"cronjob.kubernetes.io/instantiate": "manual",
		"kubebolt.io/triggered-from":        cj.Name,
	}
	if username != "" {
		annotations["kubebolt.io/triggered-by"] = username
	}

	// Copy the jobTemplate labels so existing selectors work
	// uniformly across scheduled and manual runs. nil-safe — many
	// CronJobs don't set jobTemplate labels at all.
	labels := map[string]string{}
	for k, v := range cj.Spec.JobTemplate.Labels {
		labels[k] = v
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        jobName,
			Namespace:   cj.Namespace,
			Annotations: annotations,
			Labels:      labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       cj.Name,
				UID:        cj.UID,
			}},
		},
		// Verbatim copy of the schedule's spec — including
		// backoffLimit, completions, parallelism, ttlSecondsAfter-
		// Finished, all of it. Same behavior as kubectl.
		Spec: cj.Spec.JobTemplate.Spec,
	}
}

