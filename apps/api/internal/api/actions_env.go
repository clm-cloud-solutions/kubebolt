package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Set env — `kubectl set env` ergonomics. Strategic merge patch on
// container env arrays, supporting both ADD/UPDATE and REMOVE in
// the same request via the per-entry `$patch: delete` directive.
//
// The interesting design choice here is REMOVE. Strategic merge
// would normally only add or update entries when patching a
// keyed-by-name struct list (which is what `env` is). To remove a
// specific entry, we emit `{name: "X", "$patch": "delete"}` for that
// entry — strategic merge interprets the directive and drops the
// entry from the live env list. This matches what kubectl set env
// does internally and avoids the brittler alternatives:
//
//   - JSON 6902 patch with index-based remove (positional, fragile to
//     concurrent edits).
//   - Get-then-Update of the full resource (loses optimistic
//     concurrency on every other field — if HPA bumped replicas
//     between our Get and Update, our Update would overwrite that).
//
// Strategic merge patch keeps the operation surgical and atomic.
//
// Scope of v1:
//   - container-level env edits (per-key); `envFrom:` (whole-CM/Secret
//     import) is out of scope.
//   - literal value, ConfigMap reference, Secret reference, downward-
//     API field reference, and resource-field reference are all
//     supported sources.
//   - reference validation: ConfigMap / Secret name presence is
//     pre-checked against the namespace (skipped when optional=true).
//   - triggerRollout=true also patches the rollout-restart annotation
//     so existing pods pick up literal-value changes immediately
//     instead of on the next pod restart.

var setEnvableTypes = map[string]bool{
	"deployments":  true,
	"statefulsets": true,
	"daemonsets":   true,
}

// envVarNameRE — K8s env var names must be C_IDENTIFIERs. The apiserver
// rejects malformed names, but pre-checking client-side surfaces the
// error before the audit log records a failed mutation.
var envVarNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type setEnvRequest struct {
	Containers     []containerEnvPatch `json:"containers"`
	TriggerRollout bool                `json:"triggerRollout,omitempty"`
}

type containerEnvPatch struct {
	Container     string         `json:"container"`
	InitContainer bool           `json:"initContainer,omitempty"`
	Env           []envVarPatch  `json:"env"`
}

// envVarPatch is one row of the operator's edit. Action discriminates
// add/update from remove — explicit so we don't have to guess intent
// from the presence of `value` (an empty literal is still a valid set,
// not a remove).
type envVarPatch struct {
	Name      string                  `json:"name"`
	Action    string                  `json:"action"` // "set" | "remove"
	Value     *string                 `json:"value,omitempty"`
	ValueFrom *corev1.EnvVarSource    `json:"valueFrom,omitempty"`
}

// envEntryPair is the from/to envelope returned in the response.
// Carries the resolved kind (literal vs CM ref vs Secret ref vs
// fieldRef) so the UI can show a clean before/after table without
// having to inspect ValueFrom variants.
type envEntryPair struct {
	Name      string                  `json:"name"`
	Kind      string                  `json:"kind"` // "literal" | "configMap" | "secret" | "field" | "resourceField"
	Value     string                  `json:"value,omitempty"`
	ValueFrom *corev1.EnvVarSource    `json:"valueFrom,omitempty"`
}

func (h *handlers) handleSetEnv(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}
	if !setEnvableTypes[resourceType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"cannot set-env on %s — only deployments, statefulsets, and daemonsets", resourceType))
		return
	}

	var body setEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Containers) == 0 {
		respondError(w, http.StatusBadRequest, "containers is required and must be non-empty")
		return
	}

	// Per-row validation: container name, env entries' names match
	// C_IDENTIFIER, set rows must specify exactly one of value/valueFrom,
	// remove rows must specify neither, and we reject duplicate names
	// within a single container's edit list (ambiguous intent).
	for ci, c := range body.Containers {
		if c.Container == "" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("containers[%d].container is required", ci))
			return
		}
		if len(c.Env) == 0 {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("containers[%d].env is required and must be non-empty", ci))
			return
		}
		seenNames := make(map[string]bool, len(c.Env))
		for ei, e := range c.Env {
			if e.Name == "" {
				respondError(w, http.StatusBadRequest, fmt.Sprintf("containers[%d].env[%d].name is required", ci, ei))
				return
			}
			if !envVarNameRE.MatchString(e.Name) {
				respondError(w, http.StatusBadRequest, fmt.Sprintf(
					"containers[%d].env[%d].name %q must match C_IDENTIFIER (letter/underscore start, then letters/digits/underscores)", ci, ei, e.Name))
				return
			}
			if seenNames[e.Name] {
				respondError(w, http.StatusBadRequest, fmt.Sprintf(
					"containers[%d].env duplicate entry for %q — at most one set or remove per name per request", ci, e.Name))
				return
			}
			seenNames[e.Name] = true
			switch e.Action {
			case "set":
				hasValue := e.Value != nil
				hasValueFrom := e.ValueFrom != nil
				if hasValue == hasValueFrom {
					respondError(w, http.StatusBadRequest, fmt.Sprintf(
						"containers[%d].env[%d] (set %q): exactly one of value or valueFrom is required", ci, ei, e.Name))
					return
				}
			case "remove":
				if e.Value != nil || e.ValueFrom != nil {
					respondError(w, http.StatusBadRequest, fmt.Sprintf(
						"containers[%d].env[%d] (remove %q): value/valueFrom must NOT be set on a remove", ci, ei, e.Name))
					return
				}
			default:
				respondError(w, http.StatusBadRequest, fmt.Sprintf(
					"containers[%d].env[%d].action must be \"set\" or \"remove\" (got %q)", ci, ei, e.Action))
				return
			}
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

	// 1. Pre-validate ConfigMap / Secret references exist in the
	//    namespace (unless optional=true). The apiserver fails the
	//    Patch on missing refs only when the new pod tries to start —
	//    a confusing failure mode. Catching it here gives the operator
	//    a clear "ConfigMap X not found" error before the audit log
	//    records a mutation that's about to silently break the next
	//    pod restart.
	if err := validateEnvSourceRefs(ctx, clientset, namespace, body.Containers); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 2. Capture the current env state per affected container for
	//    audit + response diff. Splits normal vs init lookups so the
	//    InitContainer flag routes correctly.
	currentNormal, currentInit, err := getCurrentContainerEnv(ctx, clientset, resourceType, namespace, name)
	if err != nil {
		auditMutation(r, "set_env", resourceType, namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}

	fromEnv := make([]envContainerSnapshot, 0, len(body.Containers))
	for _, row := range body.Containers {
		var src []envEntryPair
		var validNames []string
		if row.InitContainer {
			validNames = envContainerNames(currentInit)
			for _, c := range currentInit {
				if c.Container == row.Container {
					src = c.Env
					break
				}
			}
		} else {
			validNames = envContainerNames(currentNormal)
			for _, c := range currentNormal {
				if c.Container == row.Container {
					src = c.Env
					break
				}
			}
		}
		if src == nil && !envContainerExists(validNames, row.Container) {
			kind := "container"
			if row.InitContainer {
				kind = "init container"
			}
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"%s %q not found in %s/%s; valid %ss: %v",
				kind, row.Container, resourceType, name, kind, validNames))
			return
		}
		fromEnv = append(fromEnv, envContainerSnapshot{
			Container:     row.Container,
			InitContainer: row.InitContainer,
			Env:           src,
		})
	}

	// 3. Build the patch. Adds/updates emit a regular env entry;
	//    removes emit `{name, "$patch": "delete"}`. Strategic merge
	//    interprets the directive and drops the matching entry from
	//    the live env list.
	patchBytes, err := buildSetEnvPatch(body.Containers, body.TriggerRollout)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build patch")
		return
	}

	// 4. toEnv is the requested state — strictly derived from the
	//    body so the diff is always honest about what the operator
	//    asked for. Removes show up as Kind="" / no value / no
	//    valueFrom, which the UI renders as "(removed)".
	toEnv := buildToEnvSnapshots(body.Containers)

	params := map[string]any{
		"fromEnv":        fromEnv,
		"toEnv":          toEnv,
		"triggerRollout": body.TriggerRollout,
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
		auditMutation(r, "set_env", resourceType, namespace, name, params, err)
		log.Printf("Set-env failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "set_env", resourceType, namespace, name, params, nil)
	resource, _ := conn.GetResourceDetail(resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "patched",
		"fromEnv":        fromEnv,
		"toEnv":          toEnv,
		"triggerRollout": body.TriggerRollout,
		"resource":       resource,
	})
}

// envContainerSnapshot is one container's full env list at the moment
// of capture. Used for both fromEnv and toEnv envelopes.
type envContainerSnapshot struct {
	Container     string         `json:"container"`
	InitContainer bool           `json:"initContainer,omitempty"`
	Env           []envEntryPair `json:"env"`
}

// validateEnvSourceRefs walks every set-row's valueFrom and checks
// that the referenced ConfigMap / Secret exists in the workload's
// namespace. Skips refs marked optional=true (kubelet would tolerate
// the missing source; we shouldn't be stricter than the apiserver).
//
// Field refs / resource-field refs are not validated — the apiserver
// resolves them at pod-start time and there's nothing to pre-check.
func validateEnvSourceRefs(ctx context.Context, clientset kubernetes.Interface, namespace string, containers []containerEnvPatch) error {
	checkedCM := map[string]bool{}
	checkedSecret := map[string]bool{}
	for ci, c := range containers {
		for ei, e := range c.Env {
			if e.Action != "set" || e.ValueFrom == nil {
				continue
			}
			if r := e.ValueFrom.ConfigMapKeyRef; r != nil {
				optional := r.Optional != nil && *r.Optional
				if optional || checkedCM[r.Name] {
					continue
				}
				if _, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, r.Name, metav1.GetOptions{}); err != nil {
					if apierrors.IsNotFound(err) {
						return fmt.Errorf("containers[%d].env[%d] (%q): ConfigMap %q not found in namespace %s", ci, ei, e.Name, r.Name, namespace)
					}
					return fmt.Errorf("validating ConfigMap %q: %v", r.Name, err)
				}
				checkedCM[r.Name] = true
			}
			if r := e.ValueFrom.SecretKeyRef; r != nil {
				optional := r.Optional != nil && *r.Optional
				if optional || checkedSecret[r.Name] {
					continue
				}
				if _, err := clientset.CoreV1().Secrets(namespace).Get(ctx, r.Name, metav1.GetOptions{}); err != nil {
					if apierrors.IsNotFound(err) {
						return fmt.Errorf("containers[%d].env[%d] (%q): Secret %q not found in namespace %s", ci, ei, e.Name, r.Name, namespace)
					}
					return fmt.Errorf("validating Secret %q: %v", r.Name, err)
				}
				checkedSecret[r.Name] = true
			}
		}
	}
	return nil
}

// getCurrentContainerEnv reads the workload's pod template and returns
// each container's current env list. Splits normal vs init so the
// handler's InitContainer-flag dispatch doesn't need a second Get.
func getCurrentContainerEnv(ctx context.Context, clientset kubernetes.Interface, resourceType, namespace, name string) ([]envContainerSnapshot, []envContainerSnapshot, error) {
	switch resourceType {
	case "deployments":
		d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containersToEnvSnapshots(d.Spec.Template.Spec.Containers, false),
			containersToEnvSnapshots(d.Spec.Template.Spec.InitContainers, true), nil
	case "statefulsets":
		sts, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containersToEnvSnapshots(sts.Spec.Template.Spec.Containers, false),
			containersToEnvSnapshots(sts.Spec.Template.Spec.InitContainers, true), nil
	case "daemonsets":
		ds, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containersToEnvSnapshots(ds.Spec.Template.Spec.Containers, false),
			containersToEnvSnapshots(ds.Spec.Template.Spec.InitContainers, true), nil
	}
	return nil, nil, fmt.Errorf("unsupported resource type: %s", resourceType)
}

func containersToEnvSnapshots(cs []corev1.Container, init bool) []envContainerSnapshot {
	out := make([]envContainerSnapshot, len(cs))
	for i, c := range cs {
		entries := make([]envEntryPair, len(c.Env))
		for j, e := range c.Env {
			entries[j] = envVarToEnvEntryPair(e)
		}
		out[i] = envContainerSnapshot{Container: c.Name, InitContainer: init, Env: entries}
	}
	return out
}

func envVarToEnvEntryPair(e corev1.EnvVar) envEntryPair {
	pair := envEntryPair{Name: e.Name, Value: e.Value}
	if e.ValueFrom == nil {
		pair.Kind = "literal"
		return pair
	}
	pair.ValueFrom = e.ValueFrom
	switch {
	case e.ValueFrom.ConfigMapKeyRef != nil:
		pair.Kind = "configMap"
	case e.ValueFrom.SecretKeyRef != nil:
		pair.Kind = "secret"
	case e.ValueFrom.FieldRef != nil:
		pair.Kind = "field"
	case e.ValueFrom.ResourceFieldRef != nil:
		pair.Kind = "resourceField"
	default:
		pair.Kind = "literal"
	}
	return pair
}

func envContainerNames(snaps []envContainerSnapshot) []string {
	out := make([]string, len(snaps))
	for i, s := range snaps {
		out[i] = s.Container
	}
	return out
}

func envContainerExists(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// buildSetEnvPatch emits a strategic-merge patch that ADDS / UPDATES
// env entries normally and REMOVES via the per-entry `$patch: delete`
// directive. K8s honors this directive on struct lists keyed by name
// (env qualifies); strategic merge uses it as an explicit per-element
// remove without us having to rewrite the entire env array.
//
// `triggerRollout` adds the kubectl-rollout-restart annotation so
// existing pods are rotated immediately. Without it, env changes
// take effect only on the next pod restart (which may be far in the
// future for stable workloads).
func buildSetEnvPatch(rows []containerEnvPatch, triggerRollout bool) ([]byte, error) {
	var normal, initC []map[string]interface{}
	for _, row := range rows {
		entries := make([]map[string]interface{}, 0, len(row.Env))
		for _, e := range row.Env {
			entry := map[string]interface{}{"name": e.Name}
			switch e.Action {
			case "set":
				if e.Value != nil {
					entry["value"] = *e.Value
				}
				if e.ValueFrom != nil {
					entry["valueFrom"] = e.ValueFrom
				}
			case "remove":
				entry["$patch"] = "delete"
			}
			entries = append(entries, entry)
		}
		containerEntry := map[string]interface{}{
			"name": row.Container,
			"env":  entries,
		}
		if row.InitContainer {
			initC = append(initC, containerEntry)
		} else {
			normal = append(normal, containerEntry)
		}
	}

	podSpec := map[string]interface{}{}
	if len(normal) > 0 {
		podSpec["containers"] = normal
	}
	if len(initC) > 0 {
		podSpec["initContainers"] = initC
	}
	template := map[string]interface{}{
		"spec": podSpec,
	}
	if triggerRollout {
		// Same restart annotation kubectl rollout restart writes —
		// changing the pod template metadata triggers a rolling
		// update with the workload's existing strategy.
		template["metadata"] = map[string]interface{}{
			"annotations": map[string]interface{}{
				"kubectl.kubernetes.io/restartedAt": time.Now().Format(time.RFC3339),
			},
		}
	}
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": template,
		},
	}
	return json.Marshal(patch)
}

// buildToEnvSnapshots reflects what the operator asked for, NOT the
// post-patch live state. Removes show up as a kind="removed" entry
// so the response diff is unambiguous about the operator's intent.
func buildToEnvSnapshots(rows []containerEnvPatch) []envContainerSnapshot {
	out := make([]envContainerSnapshot, len(rows))
	for i, row := range rows {
		entries := make([]envEntryPair, 0, len(row.Env))
		for _, e := range row.Env {
			pair := envEntryPair{Name: e.Name}
			switch e.Action {
			case "set":
				if e.Value != nil {
					pair.Kind = "literal"
					pair.Value = *e.Value
				} else if e.ValueFrom != nil {
					pair.ValueFrom = e.ValueFrom
					switch {
					case e.ValueFrom.ConfigMapKeyRef != nil:
						pair.Kind = "configMap"
					case e.ValueFrom.SecretKeyRef != nil:
						pair.Kind = "secret"
					case e.ValueFrom.FieldRef != nil:
						pair.Kind = "field"
					case e.ValueFrom.ResourceFieldRef != nil:
						pair.Kind = "resourceField"
					}
				}
			case "remove":
				pair.Kind = "removed"
			}
			entries = append(entries, pair)
		}
		out[i] = envContainerSnapshot{
			Container:     row.Container,
			InitContainer: row.InitContainer,
			Env:           entries,
		}
	}
	return out
}
