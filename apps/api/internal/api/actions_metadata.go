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
)

// Edit metadata — kubectl label / kubectl annotate ergonomics. Patches
// metadata.labels and metadata.annotations on any kind the dynamic
// client can resolve. Two maps in one atomic JSON merge patch — either
// both apply or neither does.
//
// Why JSON merge patch (RFC 7396) and not strategic merge: metadata
// fields are scalar string maps, so the merge-by-name semantics that
// strategic merge offers don't add anything. JSON merge has the killer
// feature for this use case: setting a key to null DELETES it from
// the parent map. That's how "remove" rows work — the handler emits
// `{labels: {team: "payments", deprecated-tag: null}}` and the
// apiserver adds the team label AND drops deprecated-tag in one Patch.
//
// Type scope: every kind the dynamic client supports. No type-specific
// gating — metadata is universal.
//
// Cascade-by-selector (apply same patch to selector-matched siblings)
// was specced for v1 but dropped during implementation. The core
// per-resource patch covers the 90% case; cascade is a v2 follow-up
// once we see real demand. See
// internal/k8s-operations/tier2-edit-labels-annotations.md.

// labelKeyRE matches a single segment of K8s label/annotation key
// grammar — letters, digits, dashes, dots, underscores; optional DNS
// subdomain prefix separated by `/`. Looser than the apiserver's
// strict validator, but tight enough to reject obvious typos
// (spaces, leading dashes, etc.) before the audit log records a
// failed mutation.
//
// The regex on its own can't tell a "good" key from a maximally bad
// one — apiserver still has the final say on edge cases — but it
// catches:
//   - empty key
//   - key with whitespace
//   - key starting/ending with non-alphanumeric
//   - prefix segment longer than 253 chars (caught by length check)
var labelKeyRE = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*\/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)

// labelValueRE — labels values are at most 63 chars, must be empty or
// match `[A-Za-z0-9.-_]*` (with constraints on first/last char).
// Annotations have NO value-content rules; they can be anything.
var labelValueRE = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`)

const (
	maxLabelKeyLen   = 253 + 1 + 63 // prefix (DNS subdomain max) + "/" + name (DNS label max)
	maxLabelValueLen = 63
	// K8s caps total annotation byte size at 256 KiB across all
	// keys + values on a single resource. Pre-checking this saves
	// the operator from a confusing apiserver rejection on a
	// patch that LOOKED fine.
	maxAnnotationsBytes = 256 * 1024
)

type editMetadataRequest struct {
	Labels      *metadataMapEdit `json:"labels,omitempty"`
	Annotations *metadataMapEdit `json:"annotations,omitempty"`
}

type metadataMapEdit struct {
	Add    map[string]string `json:"add,omitempty"`
	Remove []string          `json:"remove,omitempty"`
}

// metadataDiff is the response-side from/to envelope per metadata
// map (labels OR annotations). The UI renders it as a diff table —
// keys appearing in `removed` are struck through, `added` are
// highlighted, `updated` show the from→to.
type metadataDiff struct {
	From    map[string]string `json:"from"`
	To      map[string]string `json:"to"`
	Added   []string          `json:"added,omitempty"`
	Updated []string          `json:"updated,omitempty"`
	Removed []string          `json:"removed,omitempty"`
}

func (h *handlers) handleEditMetadata(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	var body editMetadataRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if (body.Labels == nil || (len(body.Labels.Add) == 0 && len(body.Labels.Remove) == 0)) &&
		(body.Annotations == nil || (len(body.Annotations.Add) == 0 && len(body.Annotations.Remove) == 0)) {
		respondError(w, http.StatusBadRequest, "at least one of labels.add / labels.remove / annotations.add / annotations.remove is required")
		return
	}

	// Validation pass — keys, values, and the add/remove conflict
	// guard. We check both maps even if the operator only edited one,
	// because a malformed empty `Add: {"": "x"}` payload should be
	// rejected even when the matching `Remove` is empty.
	if body.Labels != nil {
		if err := validateMetadataMap("labels", body.Labels, true /*enforceValueRules*/); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if body.Annotations != nil {
		if err := validateMetadataMap("annotations", body.Annotations, false /*enforceValueRules*/); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 1. Read pre-patch state for the response diff. Same dynamic-
	//    client read path the YAML view uses, so we get the live
	//    metadata even on Custom Resources.
	preDetail, err := conn.GetResourceDetail(resourceType, namespace, name)
	if err != nil {
		auditMutation(r, "edit_metadata", resourceType, namespace, name, nil, err)
		respondMutationError(w, err)
		return
	}
	preLabels, preAnns := metadataMapsFromDetail(preDetail)

	// 2. Pre-flight annotation size check — the apiserver caps the
	//    sum of all annotation key+value bytes at 256 KiB. Computing
	//    the post-patch total and rejecting before the round-trip
	//    surfaces a clearer error to the operator.
	if body.Annotations != nil {
		newAnns := applyMapEdit(preAnns, body.Annotations)
		if size := totalAnnotationsBytes(newAnns); size > maxAnnotationsBytes {
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"annotations total %d bytes exceeds the 256 KiB limit. Drop a large annotation before adding more.", size))
			return
		}
	}

	// 3. Build the JSON merge patch. Removes appear as null values
	//    in the merge map, which RFC 7396 specifies as "delete this
	//    key from the parent."
	patchBytes, err := buildMetadataPatch(body)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to build patch")
		return
	}

	postObj, err := conn.PatchResourceMetadata(ctx, resourceType, namespace, name, patchBytes)
	if err != nil {
		auditMutation(r, "edit_metadata", resourceType, namespace, name, nil, err)
		log.Printf("Edit-metadata failed for %s/%s/%s: %v", resourceType, namespace, name, err)
		respondMutationError(w, err)
		return
	}

	// 4. Build the diff envelope from the live post-patch object.
	//    Reading from the apiserver's response (not informer cache)
	//    gives us a fresh ground-truth view without any read-after-
	//    write lag, so the UI can render the diff confidently.
	postLabels, postAnns := metadataMapsFromUnstructured(postObj)

	labelDiff := computeMapDiff(preLabels, postLabels)
	annotationDiff := computeMapDiff(preAnns, postAnns)

	params := map[string]any{
		"labels":      labelDiff,
		"annotations": annotationDiff,
	}
	auditMutation(r, "edit_metadata", resourceType, namespace, name, params, nil)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "patched",
		"labels":      labelDiff,
		"annotations": annotationDiff,
	})
}

// validateMetadataMap walks an Add/Remove pair and returns a clean
// 400-error message on the first problem. Skip value-rule checks for
// annotations (the values can be arbitrary text — JSON, certs, etc.).
func validateMetadataMap(field string, edit *metadataMapEdit, enforceValueRules bool) error {
	if edit == nil {
		return nil
	}
	// Add+remove conflict — if the operator says both add "team=X"
	// and remove "team", the intent is ambiguous. Reject up front.
	addKeys := make(map[string]struct{}, len(edit.Add))
	for k := range edit.Add {
		addKeys[k] = struct{}{}
	}
	for _, k := range edit.Remove {
		if _, conflict := addKeys[k]; conflict {
			return fmt.Errorf("%s: key %q appears in both add and remove — pick one", field, k)
		}
	}
	for k, v := range edit.Add {
		if k == "" {
			return fmt.Errorf("%s.add: empty key", field)
		}
		if len(k) > maxLabelKeyLen {
			return fmt.Errorf("%s.add: key %q exceeds %d chars", field, k, maxLabelKeyLen)
		}
		if !labelKeyRE.MatchString(k) {
			return fmt.Errorf("%s.add: key %q is not a valid metadata key (must be a DNS subdomain prefix + name; letters/digits/dashes/dots/underscores)", field, k)
		}
		if enforceValueRules {
			if len(v) > maxLabelValueLen {
				return fmt.Errorf("%s.add[%q]: value exceeds %d chars (label values are limited; consider an annotation)", field, k, maxLabelValueLen)
			}
			if !labelValueRE.MatchString(v) {
				return fmt.Errorf("%s.add[%q]: value %q must be empty or letters/digits/dashes/dots/underscores", field, k, v)
			}
		}
	}
	for _, k := range edit.Remove {
		if k == "" {
			return fmt.Errorf("%s.remove: empty key", field)
		}
	}
	return nil
}

// buildMetadataPatch emits the JSON merge patch body. Adds become
// `{key: value}`, removes become `{key: null}` — RFC 7396 specifies
// that null in a merge patch deletes the key from the parent.
func buildMetadataPatch(body editMetadataRequest) ([]byte, error) {
	metadata := map[string]interface{}{}
	if body.Labels != nil {
		metadata["labels"] = mergeAddRemove(body.Labels)
	}
	if body.Annotations != nil {
		metadata["annotations"] = mergeAddRemove(body.Annotations)
	}
	patch := map[string]interface{}{"metadata": metadata}
	return json.Marshal(patch)
}

// mergeAddRemove flattens Add+Remove into a single merge-patch map
// where removes are explicit nils (which json.Marshal emits as
// `null` and the apiserver interprets as "delete this key").
func mergeAddRemove(edit *metadataMapEdit) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range edit.Add {
		out[k] = v
	}
	for _, k := range edit.Remove {
		out[k] = nil
	}
	return out
}

// applyMapEdit returns a NEW map representing the post-patch state
// of `current` after applying the operator's add/remove. Used by
// the annotations-byte-size pre-flight check; doesn't mutate the
// input.
func applyMapEdit(current map[string]string, edit *metadataMapEdit) map[string]string {
	out := make(map[string]string, len(current))
	for k, v := range current {
		out[k] = v
	}
	if edit == nil {
		return out
	}
	for k, v := range edit.Add {
		out[k] = v
	}
	for _, k := range edit.Remove {
		delete(out, k)
	}
	return out
}

// totalAnnotationsBytes counts the byte size that the apiserver's
// 256-KiB cap measures: the sum of len(key)+len(value) across every
// entry. This is an approximation (the apiserver computes the
// full-object byte size including JSON overhead), but it's tight
// enough to catch the common "I tried to add a 200 KiB embedded
// JSON blob" case before the request hits the wire.
func totalAnnotationsBytes(m map[string]string) int {
	total := 0
	for k, v := range m {
		total += len(k) + len(v)
	}
	return total
}

// metadataMapsFromDetail extracts labels + annotations from a detail
// map produced by Connector.GetResourceDetail. The detail shape uses
// `safeLabels` / `safeAnnotations` which return map[string]string,
// so we type-assert and copy.
func metadataMapsFromDetail(detail map[string]interface{}) (map[string]string, map[string]string) {
	return extractStringMap(detail, "labels"), extractStringMap(detail, "annotations")
}

// metadataMapsFromUnstructured digs into `metadata.labels` /
// `metadata.annotations` of a freshly-Patched unstructured object.
// Returns empty (non-nil) maps if either field is missing — saves
// every caller from a nil check.
func metadataMapsFromUnstructured(obj map[string]interface{}) (map[string]string, map[string]string) {
	meta, _ := obj["metadata"].(map[string]interface{})
	if meta == nil {
		return map[string]string{}, map[string]string{}
	}
	return stringMapFromInterface(meta["labels"]), stringMapFromInterface(meta["annotations"])
}

func stringMapFromInterface(v interface{}) map[string]string {
	out := map[string]string{}
	m, ok := v.(map[string]interface{})
	if !ok {
		return out
	}
	for k, raw := range m {
		if s, ok := raw.(string); ok {
			out[k] = s
		}
	}
	return out
}

func extractStringMap(m map[string]interface{}, key string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	v, ok := m[key]
	if !ok || v == nil {
		return map[string]string{}
	}
	if mp, ok := v.(map[string]string); ok {
		return mp
	}
	return stringMapFromInterface(v)
}

// computeMapDiff classifies every key into added / updated / removed.
// Added — present in `to`, absent in `from`.
// Removed — present in `from`, absent in `to`.
// Updated — present in both, value changed.
// Unchanged keys appear in `to` but in none of the three lists.
func computeMapDiff(from, to map[string]string) metadataDiff {
	diff := metadataDiff{
		From: from,
		To:   to,
	}
	for k, vTo := range to {
		if vFrom, hadIt := from[k]; hadIt {
			if vFrom != vTo {
				diff.Updated = append(diff.Updated, k)
			}
		} else {
			diff.Added = append(diff.Added, k)
		}
	}
	for k := range from {
		if _, has := to[k]; !has {
			diff.Removed = append(diff.Removed, k)
		}
	}
	return diff
}
