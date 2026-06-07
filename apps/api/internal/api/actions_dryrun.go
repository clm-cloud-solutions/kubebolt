package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
)

// Server-side dry-run support for Kobi action proposals. The proposal card
// fires the SAME action endpoint with ?dryRun=true BEFORE the user clicks
// Execute, so it can show "would this apply, or would the cluster reject it?"
// — turning the proposal's "this will happen" description into a verified
// preview. dryRun=All runs full admission (quota, LimitRange, webhooks,
// validation) but never persists, so it's read-only and safe to auto-fire.

// dryRunRequested reports whether the caller asked for a dry-run preview.
func dryRunRequested(r *http.Request) bool {
	return r.URL.Query().Get("dryRun") == "true"
}

// dryRunAll returns the value for a client-go *Options.DryRun field: ["All"]
// when on, nil otherwise (so the same call site serves both real and dry runs).
func dryRunAll(on bool) []string {
	if on {
		return []string{"All"}
	}
	return nil
}

// dryRunResponse is the uniform envelope the proposal card consumes for a
// dry-run. ok=true → the apiserver would accept the mutation; ok=false carries
// a human-readable rejection reason (+ a structured quota breakdown when the
// blocker is a ResourceQuota, the headline case).
type dryRunResponse struct {
	OK      bool         `json:"ok"`
	Message string       `json:"message"`
	Quota   *quotaDetail `json:"quota,omitempty"`
}

// quotaDetail is the parsed shape of a "exceeded quota" admission rejection so
// the card can render req/used/limit instead of a raw Go error string.
type quotaDetail struct {
	Name      string `json:"name"`
	Requested string `json:"requested"`
	Used      string `json:"used"`
	Limited   string `json:"limited"`
}

// respondDryRun writes the dry-run envelope. A nil err means the dry-run
// mutation was accepted → ok + the caller's "Would …" message. A non-nil err
// is the apiserver's rejection, humanized for the card.
func respondDryRun(w http.ResponseWriter, err error, wouldMsg string) {
	if err == nil {
		respondJSON(w, http.StatusOK, dryRunResponse{OK: true, Message: wouldMsg})
		return
	}
	msg, quota := humanizeMutationError(err)
	respondJSON(w, http.StatusOK, dryRunResponse{OK: false, Message: msg, Quota: quota})
}

// humanizeMutationError turns a client-go error into a non-jargony reason for
// the rejected preview. ResourceQuota rejections also yield a structured
// quotaDetail (best-effort parse; falls back to the cleaned message).
func humanizeMutationError(err error) (string, *quotaDetail) {
	if err == nil {
		return "", nil
	}
	msg := err.Error()
	switch {
	case apierrors.IsForbidden(err) || containsForbidden(msg):
		if strings.Contains(msg, "exceeded quota") {
			if q := parseQuotaError(msg); q != nil {
				return fmt.Sprintf("ResourceQuota %q exceeded", q.Name), q
			}
		}
		// RBAC / policy forbidden — strip the leading "... is forbidden: " noise.
		if i := strings.Index(msg, "forbidden: "); i >= 0 {
			return strings.TrimSpace(msg[i+len("forbidden: "):]), nil
		}
		return msg, nil
	case apierrors.IsInvalid(err):
		// Validation (e.g. limit < request). The detail after the last colon is
		// usually the actionable part; keep it readable.
		return "Invalid: " + lastSegment(msg), nil
	case apierrors.IsConflict(err):
		return "Conflict — the resource changed underneath; refresh and retry", nil
	case apierrors.IsNotFound(err):
		return "Not found — the resource no longer exists", nil
	case apierrors.IsAlreadyExists(err):
		return "Already exists", nil
	default:
		return msg, nil
	}
}

// parseQuotaError extracts the quota name + requested/used/limited segments
// from a resourcequota admission message of the shape:
//
//	... is forbidden: exceeded quota: demo-tight, requested: limits.cpu=200m,
//	used: limits.cpu=1050m, limited: limits.cpu=1200m
//
// Returns nil when the shape doesn't match so the caller falls back cleanly.
func parseQuotaError(msg string) *quotaDetail {
	idx := strings.Index(msg, "exceeded quota: ")
	if idx < 0 {
		return nil
	}
	rest := msg[idx+len("exceeded quota: "):]
	name := strings.TrimSpace(segmentBefore(rest, ", requested:"))
	if name == "" {
		return nil
	}
	q := &quotaDetail{
		Name:      name,
		Requested: grabSegment(rest, "requested: ", ", used:"),
		Used:      grabSegment(rest, "used: ", ", limited:"),
		Limited:   grabSegment(rest, "limited: ", ""),
	}
	if q.Requested == "" && q.Used == "" && q.Limited == "" {
		return nil
	}
	return q
}

// segmentBefore returns the part of s up to sep (or all of s if sep absent).
func segmentBefore(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i]
	}
	return s
}

// grabSegment returns the text after `start` up to `end` (to end-of-string when
// end is ""). Empty when start isn't present.
func grabSegment(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	if end != "" {
		if j := strings.Index(s, end); j >= 0 {
			s = s[:j]
		}
	}
	return strings.TrimSpace(s)
}

// dryRunMarginalPod dry-run-CREATEs one pod from a workload's pod template so
// the apiserver runs the admission a scaled-up replica would hit — ResourceQuota,
// LimitRange, PodSecurity, scheduling-related webhooks. This is the check that
// matters for "would scaling up actually succeed": the scale subresource itself
// never triggers quota (it only bumps spec.replicas); the ReplicaSet controller's
// pod creation does, asynchronously, which is what silently stalls a scale. The
// dry-run create persists nothing and never schedules. Accurate for a +1 scale
// (the marginal pod); a lower bound for +N (if even one pod won't fit, the scale
// is definitely blocked). Only deployments/statefulsets create pods this way.
func dryRunMarginalPod(ctx context.Context, clientset kubernetes.Interface, resourceType, namespace, name string) error {
	var tmpl corev1.PodTemplateSpec
	switch resourceType {
	case "deployments":
		d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		tmpl = d.Spec.Template
	case "statefulsets":
		s, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		tmpl = s.Spec.Template
	default:
		return nil // not a pod-creating scale target
	}
	return dryRunCreatePod(ctx, clientset, namespace, name, tmpl)
}

// dryRunPatchedPod is the set_resources analogue of dryRunMarginalPod: it
// applies the SAME strategic-merge patch the real mutation would send to the
// workload IN MEMORY, then dry-run-creates a pod from the PATCHED template. That
// surfaces quota/LimitRange rejections of the new requests/limits — which the
// Deployment-object patch dry-run can't, since quota is enforced at pod creation.
func dryRunPatchedPod(ctx context.Context, clientset kubernetes.Interface, resourceType, namespace, name string, patch []byte) error {
	var tmpl corev1.PodTemplateSpec
	switch resourceType {
	case "deployments":
		d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		merged, err := applyStrategicMerge(d, patch, &appsv1.Deployment{})
		if err != nil {
			return err
		}
		tmpl = merged.(*appsv1.Deployment).Spec.Template
	case "statefulsets":
		s, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		merged, err := applyStrategicMerge(s, patch, &appsv1.StatefulSet{})
		if err != nil {
			return err
		}
		tmpl = merged.(*appsv1.StatefulSet).Spec.Template
	case "daemonsets":
		ds, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		merged, err := applyStrategicMerge(ds, patch, &appsv1.DaemonSet{})
		if err != nil {
			return err
		}
		tmpl = merged.(*appsv1.DaemonSet).Spec.Template
	default:
		return nil
	}
	return dryRunCreatePod(ctx, clientset, namespace, name, tmpl)
}

// applyStrategicMerge applies a strategic-merge patch to a typed object and
// returns the merged object (same concrete type as `into`).
func applyStrategicMerge(orig interface{}, patch []byte, into interface{}) (interface{}, error) {
	origJSON, err := json.Marshal(orig)
	if err != nil {
		return nil, err
	}
	mergedJSON, err := strategicpatch.StrategicMergePatch(origJSON, patch, into)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(mergedJSON, into); err != nil {
		return nil, err
	}
	return into, nil
}

// dryRunCreatePod dry-run-creates a representative pod from a workload's
// (possibly patched) template to surface the admission a real replica would hit
// — ResourceQuota and LimitRange above all. Persists nothing, never schedules.
//
// Limitations (the representative pod is not byte-identical to a controller-made
// one): it lacks the controller-injected pod-template-hash label and the
// ReplicaSet/StatefulSet ownerReference. ResourceQuota and LimitRange ignore
// both (they key on namespace + resource quantities), so the headline quota
// prediction is accurate; an admission WEBHOOK scoped by an objectSelector on
// those labels could in theory evaluate this pod differently. Accepted for v1.
func dryRunCreatePod(ctx context.Context, clientset kubernetes.Interface, namespace, name string, tmpl corev1.PodTemplateSpec) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name + "-dryrun-",
			Namespace:    namespace,
			Labels:       tmpl.Labels,
			Annotations:  tmpl.Annotations,
		},
		Spec: tmpl.Spec,
	}
	_, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{DryRun: []string{"All"}})
	// A pods:create RBAC denial is a limitation of THIS preview mechanism, not a
	// rejection of the action — the SA may well be able to scale/patch the
	// workload without being able to create pods directly. Treat it as
	// inconclusive (nil) so we don't show a false "would be rejected"; real
	// quota / LimitRange / webhook rejections still surface.
	if podCreateDeniedByRBAC(err) {
		return nil
	}
	return err
}

// podCreateDeniedByRBAC reports whether err is the apiserver refusing the dry-run
// pod CREATE for RBAC reasons (vs an admission rejection like quota). RBAC
// denials read "... cannot create resource \"pods\" ..."; quota/LimitRange
// denials carry "exceeded quota" / LimitRange text instead.
func podCreateDeniedByRBAC(err error) bool {
	if err == nil || !apierrors.IsForbidden(err) {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "cannot create resource") && !strings.Contains(m, "exceeded quota")
}

// setImageWouldMsg summarizes an image change for the would-apply line.
func setImageWouldMsg(images []imagePair) string {
	if len(images) == 1 {
		return fmt.Sprintf("Would apply · %s → %s", images[0].Container, images[0].Image)
	}
	return fmt.Sprintf("Would apply · %d container images updated", len(images))
}

// lastSegment returns the text after the final ": " in msg, or msg itself.
func lastSegment(msg string) string {
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return strings.TrimSpace(msg[i+2:])
	}
	return msg
}
