package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
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
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	// Default propagation + grace period — matches `kubectl delete
	// pod <name>` semantics. The owner reconcile loop picks up the
	// deletion and re-creates the pod with the same spec.
	if err := conn.DeleteResource("pods", namespace, name, metav1.DeletePropagationBackground, nil, false); err != nil {
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

	conn := h.manager.Connector(r.Context())
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

// debugPodRequest is the wire shape for POST /resources/pods/:ns/:name/debug.
// Mirrors `kubectl debug -it <pod> --image=X --target=Y` semantics: spawn
// an ephemeral container inside the running pod that shares its network +
// process namespace with `targetContainer` so the operator can shell into
// distroless / scratch-image / read-only-fs containers where `kubectl exec`
// can't reach (no shell binary, no writable filesystem).
//
// Required: image. Recommended: targetContainer (without it the debug
// container only shares the pod's network namespace, which limits the
// distroless use case). Command defaults to `["sh"]` if omitted; the
// terminal-tab handler later may auto-fall-back to `sh` when the chosen
// shell isn't on the image.
type debugPodRequest struct {
	Image                  string   `json:"image"`
	TargetContainer        string   `json:"targetContainer,omitempty"`
	Command                []string `json:"command,omitempty"`
	ShareProcessNamespace  bool     `json:"shareProcessNamespace,omitempty"`
}

// ephemeralNameRegex constrains the auto-generated container name to
// the DNS-1123 subset that Kubernetes accepts for container names.
// Trim anything ugly from the image string before stitching it into the
// suffix so the generated name doesn't fail apiserver validation.
var ephemeralNameRegex = regexp.MustCompile(`[^a-z0-9-]+`)

// generateEphemeralContainerName builds a stable-ish unique name based
// on the image short-name + a unix-time tail. Multiple debug-container
// spawns on the same pod each get distinct names so the apiserver
// doesn't reject the second one with "ephemeralContainers[N].name:
// Duplicate value".
func generateEphemeralContainerName(image string) string {
	short := image
	if i := strings.LastIndex(short, "/"); i >= 0 {
		short = short[i+1:]
	}
	if i := strings.Index(short, ":"); i >= 0 {
		short = short[:i]
	}
	short = ephemeralNameRegex.ReplaceAllString(strings.ToLower(short), "-")
	short = strings.Trim(short, "-")
	if short == "" {
		short = "debug"
	}
	// Truncate so the final name stays under DNS-1123's 63-byte cap with
	// room for the suffix (10 chars: hyphen + unix seconds).
	if len(short) > 40 {
		short = short[:40]
	}
	return fmt.Sprintf("debugger-%s-%d", short, time.Now().Unix())
}

// handleDebugPod injects an ephemeral container into a running Pod via
// the apiserver's `ephemeralcontainers` subresource. Spec #09 V2 / Item
// 4 / C1 — the audit's only post-1.11 deferred item. Editor+ role.
//
// Use case: distroless / scratch / read-only-fs containers where the
// Terminal tab's exec path can't open a shell (no `sh` / `bash` binary
// on disk). Ephemeral container runs alongside the target with a shared
// pid+net namespace, so the operator can `ps` the target's processes
// and `curl` from inside its network identity even though the target
// itself has no debugging tools.
//
// Returns the auto-generated container name so the UI can navigate to
// the Terminal tab pre-selected on the new container.
func (h *handlers) handleDebugPod(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if resourceType != "pods" {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("cannot debug %s — endpoint only accepts pods", resourceType))
		return
	}
	if namespace == "_" {
		namespace = ""
	}

	var req debugPodRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Image) == "" {
		respondError(w, http.StatusBadRequest, "image is required")
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

	// Fetch the pod's current ephemeral-container subresource. The
	// apiserver returns the FULL pod object (legacy of how the
	// subresource was designed), but we'll only mutate
	// spec.ephemeralContainers before sending it back.
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		auditMutation(r, "debug_pod", "pods", namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}

	// Validate targetContainer (if specified) actually exists on the
	// pod. Without this check, an apiserver-side error comes back as a
	// generic 422 which is hard to translate into useful UI copy.
	if req.TargetContainer != "" {
		found := false
		for _, c := range pod.Spec.Containers {
			if c.Name == req.TargetContainer {
				found = true
				break
			}
		}
		if !found {
			// Also check initContainers — some debug workflows target
			// a stuck initContainer. Kubernetes 1.28+ allows this.
			for _, c := range pod.Spec.InitContainers {
				if c.Name == req.TargetContainer {
					found = true
					break
				}
			}
		}
		if !found {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("targetContainer %q not found in pod (containers: %s)", req.TargetContainer, containerNamesCSV(pod)))
			return
		}
	}

	cmd := req.Command
	if len(cmd) == 0 {
		// Default to `sh` — works on busybox/alpine/most debug images.
		// Operator can override via `command` if their image needs
		// `/bin/bash` or similar.
		cmd = []string{"sh"}
	}

	ephemeralName := generateEphemeralContainerName(req.Image)
	newEphemeral := corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:                     ephemeralName,
			Image:                    req.Image,
			Command:                  cmd,
			ImagePullPolicy:          corev1.PullIfNotPresent,
			Stdin:                    true,
			TTY:                      true,
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		},
		TargetContainerName: req.TargetContainer,
	}

	pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, newEphemeral)

	// UpdateEphemeralContainers is the dedicated subresource that
	// apiserver accepts on a running pod. Patching `pods/<name>`
	// directly with a new ephemeralContainers entry fails — the
	// subresource exists precisely for this mutation path.
	dryRun := dryRunRequested(r)
	_, err = clientset.CoreV1().Pods(namespace).UpdateEphemeralContainers(ctx, name, pod, metav1.UpdateOptions{DryRun: dryRunAll(dryRun)})
	if dryRun {
		respondDryRun(w, err, fmt.Sprintf("Would attach · debug container %q (image %s)", ephemeralName, req.Image))
		return
	}
	if err != nil {
		auditMutation(r, "debug_pod", "pods", namespace, name, nil, err)
		log.Printf("Debug pod (spawn ephemeral) failed for %s/%s: %v", namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "debug_pod", "pods", namespace, name, map[string]any{
		"image":           req.Image,
		"targetContainer": req.TargetContainer,
		"ephemeralName":   ephemeralName,
	}, nil)

	respondJSON(w, http.StatusOK, map[string]any{
		"status":                "spawned",
		"ephemeralContainerName": ephemeralName,
	})
}

// containerNamesCSV is the comma-separated list of regular + init
// containers, used as the error message context when the operator
// specified a targetContainer name that doesn't exist on the pod.
func containerNamesCSV(pod *corev1.Pod) string {
	names := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name+" (init)")
	}
	return strings.Join(names, ", ")
}
