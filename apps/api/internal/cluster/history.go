package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DetailedRevision is the per-revision shape returned by the
// detailed-history endpoint (?detailed=true). Same shape across
// Deployment / StatefulSet / DaemonSet so the frontend renders one
// timeline component for all three.
//
// Field semantics:
//   - revision: the deployment-revision annotation (Deployments) or
//     ControllerRevision.Revision (STS/DS). Always > 0 for valid
//     revisions.
//   - name: ReplicaSet name (Deployment) or ControllerRevision name
//     (STS/DS). Useful for debugging and as a stable React key.
//   - images: every container in the pod template, NOT just the
//     first one — multi-container workloads need the full picture
//     for the rollback diff to make sense.
//   - changeCause: from `kubernetes.io/change-cause` annotation on
//     the RS/CR. Most teams don't set it, so it's commonly empty.
//   - replicaCount: live replicas attributable to this revision.
//     For Deployments this is RS.Status.Replicas (non-zero only
//     during a rollout or for the active RS). For STS/DS it's
//     parent.spec.replicas when revision==current, else 0 (old
//     ControllerRevisions have no pods).
//   - active: this revision is the one currently serving traffic.
//     Exactly one revision is active at any time per workload.
type DetailedRevision struct {
	Revision     int64       `json:"revision"`
	Name         string      `json:"name"`
	CreatedAt    string      `json:"createdAt"`
	Age          string      `json:"age"`
	Images       []ImagePair `json:"images"`
	ChangeCause  string      `json:"changeCause"`
	ReplicaCount int32       `json:"replicaCount"`
	Active       bool        `json:"active"`
}

// ImagePair is the container/image tuple shared between the
// detailed-history payload and the set-image action. Lives in the
// cluster package so both consumers (api/handlers + the future
// rollback flow) reference the same type without circular imports.
type ImagePair struct {
	Container string `json:"container"`
	Image     string `json:"image"`
}

// DetailedHistoryResponse is the envelope returned to the HTTP
// layer. CurrentRevision lets the UI mark the active row without
// re-deriving from the per-row Active bool — useful when the
// Active flag is computed from a stale informer.
type DetailedHistoryResponse struct {
	CurrentRevision int64              `json:"currentRevision"`
	Revisions       []DetailedRevision `json:"revisions"`
}

// changeCauseAnnotation is the standard kubernetes.io/change-cause
// annotation that `kubectl annotate` writes and `kubectl rollout
// history` displays. Copied from the workload onto the RS/CR by the
// controller at revision-creation time.
const changeCauseAnnotation = "kubernetes.io/change-cause"

// deploymentRevisionAnnotation tags ReplicaSets with the integer
// revision they represent. Set by the Deployment controller; absent
// only on RSs that aren't part of a rollout history (rare).
const deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"

// GetDeploymentHistoryDetailed returns the full per-revision metadata
// the rollout-history UI needs: every container's image, the change
// cause annotation, replica count, and the active flag. This is the
// `?detailed=true` variant of GetDeploymentHistory; the old method
// is preserved unchanged so the current History tab keeps working
// until the UI cuts over.
func (c *Connector) GetDeploymentHistoryDetailed(namespace, name string) (DetailedHistoryResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return DetailedHistoryResponse{}, err
	}

	currentRev, _ := strconv.ParseInt(dep.Annotations[deploymentRevisionAnnotation], 10, 64)

	rsList, err := c.clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return DetailedHistoryResponse{Revisions: []DetailedRevision{}}, err
	}

	var revs []DetailedRevision
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !isOwnedBy(rs.OwnerReferences, "Deployment", name, dep.UID) {
			continue
		}
		revs = append(revs, replicaSetToDetailedRevision(rs, currentRev))
	}

	sort.Slice(revs, func(i, j int) bool { return revs[i].Revision > revs[j].Revision })

	if revs == nil {
		revs = []DetailedRevision{}
	}
	return DetailedHistoryResponse{CurrentRevision: currentRev, Revisions: revs}, nil
}

// GetWorkloadHistoryDetailed returns the same DetailedHistoryResponse
// shape for StatefulSets and DaemonSets. ControllerRevision data is
// stored as a JSON-encoded patch in `Data.Raw`; for STS this is a
// partial StatefulSet (`{spec:{template:...}}`) and for DS the same
// shape but for DaemonSet. We unmarshal just the path we need.
func (c *Connector) GetWorkloadHistoryDetailed(resourceType, namespace, name string) (DetailedHistoryResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var ownerKind string
	var currentRevName string // CR name, resolved to int after we walk the list
	var liveReplicas int32

	switch resourceType {
	case "statefulsets":
		ownerKind = "StatefulSet"
		sts, err := c.clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return DetailedHistoryResponse{}, err
		}
		// UpdateRevision is the latest rolled-out revision (a CR name);
		// fall back to CurrentRevision when no rollout has happened.
		currentRevName = sts.Status.UpdateRevision
		if currentRevName == "" {
			currentRevName = sts.Status.CurrentRevision
		}
		if sts.Spec.Replicas != nil {
			liveReplicas = *sts.Spec.Replicas
		}
	case "daemonsets":
		ownerKind = "DaemonSet"
		ds, err := c.clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return DetailedHistoryResponse{}, err
		}
		liveReplicas = ds.Status.DesiredNumberScheduled
		// DaemonSets don't expose the current revision directly; we
		// derive it as the highest revision among owned CRs.
	default:
		return DetailedHistoryResponse{}, fmt.Errorf("unsupported workload type for detailed history: %s", resourceType)
	}

	crList, err := c.clientset.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return DetailedHistoryResponse{}, err
	}

	var revs []DetailedRevision
	currentRev := int64(0)
	maxRev := int64(0)
	for i := range crList.Items {
		cr := &crList.Items[i]
		if !isOwnedByKindName(cr.OwnerReferences, ownerKind, name) {
			continue
		}
		images, _ := extractImagesFromControllerRevision(cr.Data.Raw)
		rev := DetailedRevision{
			Revision:    cr.Revision,
			Name:        cr.Name,
			CreatedAt:   cr.CreationTimestamp.Format(time.RFC3339),
			Age:         formatAge(cr.CreationTimestamp.Time),
			Images:      images,
			ChangeCause: cr.Annotations[changeCauseAnnotation],
		}
		if cr.Revision > maxRev {
			maxRev = cr.Revision
		}
		// STS: match by CR name to find the active revision.
		if currentRevName != "" && cr.Name == currentRevName {
			currentRev = cr.Revision
		}
		revs = append(revs, rev)
	}

	// DaemonSet: highest revision is the live one (matches
	// `kubectl rollout history` behavior). Same fallback for STS
	// when the status fields are empty (e.g. fresh STS, no
	// rollouts yet).
	if currentRev == 0 {
		currentRev = maxRev
	}

	for i := range revs {
		if revs[i].Revision == currentRev {
			revs[i].Active = true
			revs[i].ReplicaCount = liveReplicas
		}
	}

	sort.Slice(revs, func(i, j int) bool { return revs[i].Revision > revs[j].Revision })

	if revs == nil {
		revs = []DetailedRevision{}
	}
	return DetailedHistoryResponse{CurrentRevision: currentRev, Revisions: revs}, nil
}

// replicaSetToDetailedRevision is the pure mapper from a Deployment-
// owned ReplicaSet to a DetailedRevision row. Pulled out so tests
// can drive it without an apiserver — the same reason set-image
// extracted buildSetImagePatch.
func replicaSetToDetailedRevision(rs *appsv1.ReplicaSet, currentRev int64) DetailedRevision {
	revStr := ""
	if rs.Annotations != nil {
		revStr = rs.Annotations[deploymentRevisionAnnotation]
	}
	rev, _ := strconv.ParseInt(revStr, 10, 64)

	cause := ""
	if rs.Annotations != nil {
		cause = rs.Annotations[changeCauseAnnotation]
	}

	return DetailedRevision{
		Revision:     rev,
		Name:         rs.Name,
		CreatedAt:    rs.CreationTimestamp.Format(time.RFC3339),
		Age:          formatAge(rs.CreationTimestamp.Time),
		Images:       containersToImagePairs(rs.Spec.Template.Spec.Containers),
		ChangeCause:  cause,
		ReplicaCount: rs.Status.Replicas,
		Active:       rev == currentRev,
	}
}

// extractImagesFromControllerRevision unmarshals just enough of a
// ControllerRevision's embedded JSON to recover container images.
// The embedded shape for STS/DS is `{spec:{template:{spec:{
// containers:[...]}}}}` (verified against client-go test fixtures
// + kubectl source). We only walk the path we need so the function
// is robust against schema additions in future API versions.
func extractImagesFromControllerRevision(raw []byte) ([]ImagePair, error) {
	if len(raw) == 0 {
		return []ImagePair{}, nil
	}
	var partial struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []corev1.Container `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &partial); err != nil {
		return []ImagePair{}, fmt.Errorf("decode ControllerRevision: %w", err)
	}
	return containersToImagePairs(partial.Spec.Template.Spec.Containers), nil
}

// RollbackStatefulSet reverts a StatefulSet's pod template to the
// one captured in the target ControllerRevision. Mechanics:
//
//  1. Find the CR whose Revision == toRevision (or the latest non-
//     current one when toRevision == 0, matching `kubectl rollout
//     undo` default).
//  2. Decode its embedded pod template from Data.Raw.
//  3. Copy ONLY the template into sts.spec.template — replicas,
//     updateStrategy, serviceName, etc. stay as-is. This matches
//     `kubectl rollout undo`'s behavior; rollback is a template-
//     only revert.
//  4. Update via the typed clientset so the controller picks up
//     the change and rolls pods in reverse-ordinal order.
//
// Returns (fromRevision, toRevision) for the audit log + UI before/
// after summary. Errors map cleanly to the same HTTP layer that
// already serves Deployment rollbacks: "no rollback history",
// "target revision N not found", "no-op".
func (c *Connector) RollbackStatefulSet(namespace, name string, toRevision int64) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sts, err := c.clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, 0, err
	}

	crList, err := c.clientset.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, err
	}

	var owned []appsv1.ControllerRevision
	currentRev := int64(0)
	currentRevName := sts.Status.UpdateRevision
	if currentRevName == "" {
		currentRevName = sts.Status.CurrentRevision
	}
	for i := range crList.Items {
		cr := crList.Items[i]
		if !isOwnedByKindName(cr.OwnerReferences, "StatefulSet", name) {
			continue
		}
		owned = append(owned, cr)
		if cr.Name == currentRevName {
			currentRev = cr.Revision
		}
	}

	target, err := pickRollbackTarget(owned, toRevision, currentRev)
	if err != nil {
		return currentRev, 0, err
	}

	template, err := decodeControllerRevisionTemplate(target.Data.Raw)
	if err != nil {
		return currentRev, target.Revision, fmt.Errorf("decode target revision %d: %w", target.Revision, err)
	}

	sts.Spec.Template = *template
	if _, err := c.clientset.AppsV1().StatefulSets(namespace).Update(ctx, sts, metav1.UpdateOptions{}); err != nil {
		return currentRev, target.Revision, err
	}
	return currentRev, target.Revision, nil
}

// RollbackDaemonSet does the equivalent for DaemonSets. The only
// shape difference vs STS is that DaemonSets don't expose
// status.{update,current}Revision — we identify the active
// revision as max(Revision) among owned CRs.
func (c *Connector) RollbackDaemonSet(namespace, name string, toRevision int64) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ds, err := c.clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, 0, err
	}

	crList, err := c.clientset.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, err
	}

	var owned []appsv1.ControllerRevision
	currentRev := int64(0)
	for i := range crList.Items {
		cr := crList.Items[i]
		if !isOwnedByKindName(cr.OwnerReferences, "DaemonSet", name) {
			continue
		}
		owned = append(owned, cr)
		if cr.Revision > currentRev {
			currentRev = cr.Revision
		}
	}

	target, err := pickRollbackTarget(owned, toRevision, currentRev)
	if err != nil {
		return currentRev, 0, err
	}

	template, err := decodeControllerRevisionTemplate(target.Data.Raw)
	if err != nil {
		return currentRev, target.Revision, fmt.Errorf("decode target revision %d: %w", target.Revision, err)
	}

	ds.Spec.Template = *template
	if _, err := c.clientset.AppsV1().DaemonSets(namespace).Update(ctx, ds, metav1.UpdateOptions{}); err != nil {
		return currentRev, target.Revision, err
	}
	return currentRev, target.Revision, nil
}

// pickRollbackTarget implements the shared revision-selection logic
// used by both STS and DS rollback. toRevision==0 picks the most
// recent revision that isn't the current one (default-undo
// semantics); a positive value picks the exact match. Errors map
// 1:1 to the strings the Deployment rollback returns so the HTTP
// layer can keep its existing 400-vs-500 routing untouched.
func pickRollbackTarget(owned []appsv1.ControllerRevision, toRevision, currentRev int64) (*appsv1.ControllerRevision, error) {
	if len(owned) < 2 {
		return nil, fmt.Errorf("workload has no rollback history (need at least 2 revisions, found %d)", len(owned))
	}
	// Sort newest-first so the default (toRevision==0) walks in the
	// right order.
	sort.Slice(owned, func(i, j int) bool { return owned[i].Revision > owned[j].Revision })

	if toRevision == 0 {
		for i := range owned {
			if owned[i].Revision != currentRev {
				return &owned[i], nil
			}
		}
		return nil, fmt.Errorf("no eligible previous revision found")
	}
	if toRevision == currentRev {
		return nil, fmt.Errorf("target revision %d is the current one (no-op)", toRevision)
	}
	for i := range owned {
		if owned[i].Revision == toRevision {
			return &owned[i], nil
		}
	}
	return nil, fmt.Errorf("target revision %d not found", toRevision)
}

// decodeControllerRevisionTemplate unmarshals the embedded pod
// template from a CR's Data.Raw. Same partial-decode trick as
// extractImagesFromControllerRevision but returns the full template
// (we need every field for the rollback Update, not just images).
func decodeControllerRevisionTemplate(raw []byte) (*corev1.PodTemplateSpec, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("ControllerRevision has empty data")
	}
	var partial struct {
		Spec struct {
			Template corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &partial); err != nil {
		return nil, err
	}
	if len(partial.Spec.Template.Spec.Containers) == 0 {
		return nil, fmt.Errorf("ControllerRevision has empty pod template")
	}
	return &partial.Spec.Template, nil
}

// containersToImagePairs is the shared mapper used by both the RS
// path and the ControllerRevision path. Returns an empty (non-nil)
// slice when the input is empty so the JSON output is `[]` not
// `null`.
func containersToImagePairs(cs []corev1.Container) []ImagePair {
	if len(cs) == 0 {
		return []ImagePair{}
	}
	out := make([]ImagePair, len(cs))
	for i, c := range cs {
		out[i] = ImagePair{Container: c.Name, Image: c.Image}
	}
	return out
}

// isOwnedBy / isOwnedByKindName — owner-reference matching. The
// UID-checked variant is used when we have the parent object (and
// can reject UID-recycled name collisions); the name-only variant
// is used when we don't have UID handy (DaemonSet path, where we
// only have the URL params).
func isOwnedBy(refs []metav1.OwnerReference, kind, name string, uid types.UID) bool {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name && r.UID == uid {
			return true
		}
	}
	return false
}

func isOwnedByKindName(refs []metav1.OwnerReference, kind, name string) bool {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

