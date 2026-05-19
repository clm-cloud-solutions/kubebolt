package api

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigsyaml "sigs.k8s.io/yaml"
)

// Apply new manifest — kubectl create -f equivalent. Tier 2 #10.
//
// Lands a brand-new resource via the dynamic client. The kubectl
// apply (idempotent upsert) variant is intentionally NOT exposed
// here in v1 — this endpoint mirrors `kubectl create` semantics
// (fails on AlreadyExists) so the operator's intent is unambiguous.
// Operators editing an existing resource use the YAML PUT endpoint.
//
// URL shape: POST /resources/:type/:ns. Resource NAME comes from the
// body (metadata.name). Cluster-scoped kinds use `_` as the
// namespace placeholder.
//
// Body: a single-document YAML or JSON manifest. Multi-doc bodies
// are rejected — accepting them would force decisions about partial-
// success (if doc 2 fails after doc 1 created, do we delete doc 1?)
// that don't have great answers. v2 can revisit when there's real
// demand for compose-style workflows from the UI.
//
// Validation pre-flight surfaces three classes of error before the
// apiserver round-trip:
//
//   1. URL/body kind consistency. The URL :type and the body's
//      kind+apiVersion must agree on the GVR. A mismatch (e.g.
//      POST /resources/services/... with a Deployment body) gets a
//      clean 400 with "URL says X, body says Y" instead of a confusing
//      apiserver "no such resource" or "kind not registered."
//
//   2. Namespace consistency. URL :ns must match the body's
//      metadata.namespace if present (auto-injected from URL when
//      absent). Cluster-scoped kinds with a non-`_` URL :ns reject.
//
//   3. Forbidden field guard. metadata.generateName isn't accepted
//      in v1 — the modal needs a deterministic name to navigate to
//      after create. A `status` block is silently stripped (the
//      apiserver also strips it, but the strip happens server-side
//      with an undocumented message; we strip client-side for
//      clarity).
//
// Audit log captures the full manifest. Operators creating Secrets
// via this endpoint will have the values in the audit log — that's
// by design (audit needs the diff), but security-conscious shops
// should gate this endpoint to Admin role for Secret kinds (see
// the spec for the threat model + recommendation).

// createKindByType is the inverse of resourceTypeToGVR's resource
// component, mapping the URL-segment plural ("deployments") to the
// canonical Kind ("Deployment"). Used to validate that the body's
// kind matches what the URL :type expects.
//
// Keep in sync with the resourceTypeToGVR map in cluster/connector.go;
// adding a new GVR there means adding the matching Kind here. Drift
// will surface as a "URL says X, body says Y" rejection when the
// operator actually uses that kind.
var createKindByType = map[string]string{
	"pods":                     "Pod",
	"nodes":                    "Node",
	"namespaces":               "Namespace",
	"services":                 "Service",
	"configmaps":               "ConfigMap",
	"secrets":                  "Secret",
	"persistentvolumeclaims":   "PersistentVolumeClaim",
	"pvcs":                     "PersistentVolumeClaim",
	"persistentvolumes":        "PersistentVolume",
	"pvs":                      "PersistentVolume",
	"events":                   "Event",
	"deployments":              "Deployment",
	"statefulsets":             "StatefulSet",
	"daemonsets":               "DaemonSet",
	"replicasets":              "ReplicaSet",
	"jobs":                     "Job",
	"cronjobs":                 "CronJob",
	"ingresses":                "Ingress",
	"hpas":                     "HorizontalPodAutoscaler",
	"horizontalpodautoscalers": "HorizontalPodAutoscaler",
	"storageclasses":           "StorageClass",
	"roles":                    "Role",
	"clusterroles":             "ClusterRole",
	"rolebindings":             "RoleBinding",
	"clusterrolebindings":      "ClusterRoleBinding",
}

// multiDocSeparatorRE matches the YAML document separator. A bare
// `---` at the start of a document is allowed (kubectl writes it);
// what we reject is a SECOND document marker after content. Conservative:
// any `---` line that has non-whitespace content before AND after it.
var multiDocSeparatorRE = regexp.MustCompile(`(?m)^---\s*$`)

func (h *handlers) handleCreateResource(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	if namespace == "_" {
		namespace = ""
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	if !conn.SupportedResourceType(resourceType) {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unsupported resource type %q", resourceType))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap — manifests don't legitimately exceed this
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(body) == 0 {
		respondError(w, http.StatusBadRequest, "request body is empty")
		return
	}

	// Reject multi-document manifests. We split the body on `---`
	// markers; anything more than one non-empty section means the
	// operator pasted multiple documents.
	if hasMultiDoc(body) {
		respondError(w, http.StatusBadRequest,
			"multi-document manifests are not supported in v1 — split into separate Apply requests, or use kubectl apply -f for batched operations")
		return
	}

	// sigs.k8s.io/yaml handles both YAML and JSON inputs — JSON is
	// just a YAML subset. Single decoder for both Content-Types.
	var raw map[string]interface{}
	if err := sigsyaml.Unmarshal(body, &raw); err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid YAML/JSON: %v", err))
		return
	}
	if raw == nil {
		respondError(w, http.StatusBadRequest, "manifest is empty after parsing")
		return
	}

	// Strip status — the apiserver does this anyway, but stripping
	// here means the operator gets a clean response shape and the
	// audit log doesn't carry a meaningless empty status block.
	delete(raw, "status")

	// Strip managedFields if present — internal apiserver tracking
	// that shouldn't survive a paste-from-clipboard workflow.
	if metadata, ok := raw["metadata"].(map[string]interface{}); ok {
		delete(metadata, "managedFields")
		// last-applied-configuration is a kubectl-apply-internal
		// marker; if the operator pasted YAML from `kubectl get -o
		// yaml`, this will be present. Strip it so the new resource
		// doesn't carry stale apply state.
		if anns, ok := metadata["annotations"].(map[string]interface{}); ok {
			delete(anns, "kubectl.kubernetes.io/last-applied-configuration")
			if len(anns) == 0 {
				delete(metadata, "annotations")
			}
		}
	}

	obj := &unstructured.Unstructured{Object: raw}

	// 1. apiVersion + kind required.
	apiVersion := obj.GetAPIVersion()
	kind := obj.GetKind()
	if apiVersion == "" || kind == "" {
		respondError(w, http.StatusBadRequest, "manifest must specify both apiVersion and kind")
		return
	}

	// 2. URL :type must match the body's kind. Look up the expected
	//    kind from the URL :type via the createKindByType map.
	expectedKind, hasMapping := createKindByType[resourceType]
	if !hasMapping {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unsupported resource type %q for create", resourceType))
		return
	}
	if kind != expectedKind {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"manifest kind/url mismatch: URL targets %s but body declares kind=%s (expected %s)",
			resourceType, kind, expectedKind))
		return
	}

	// 3. apiVersion check — the URL's GVR has a Group/Version; the
	//    body's apiVersion must parse to the same. For core types,
	//    apiVersion is bare ("v1"); for grouped types, it's
	//    "<group>/<version>".
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid apiVersion %q: %v", apiVersion, err))
		return
	}
	expectedGV := expectedGroupVersionFor(resourceType)
	if gv != expectedGV {
		respondError(w, http.StatusBadRequest, fmt.Sprintf(
			"manifest apiVersion mismatch: URL targets %s but body declares apiVersion=%s (expected %s)",
			resourceType, apiVersion, expectedGV.String()))
		return
	}

	// 4. Forbid generateName in v1 — the modal navigates to the new
	//    resource after Apply, which needs a deterministic name.
	if obj.GetGenerateName() != "" {
		respondError(w, http.StatusBadRequest,
			"metadata.generateName is not supported in v1 — specify metadata.name explicitly")
		return
	}
	if obj.GetName() == "" {
		respondError(w, http.StatusBadRequest, "metadata.name is required")
		return
	}

	// 5. Cluster-scope vs namespace consistency.
	clusterScoped := conn.IsClusterScopedType(resourceType)
	if clusterScoped {
		if namespace != "" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"%s is cluster-scoped — use _ as the URL namespace placeholder", resourceType))
			return
		}
		if obj.GetNamespace() != "" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"%s is cluster-scoped — metadata.namespace must be empty", resourceType))
			return
		}
	} else {
		if namespace == "" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"%s is namespaced — URL must specify a namespace (use _ only for cluster-scoped kinds)", resourceType))
			return
		}
		bodyNS := obj.GetNamespace()
		if bodyNS != "" && bodyNS != namespace {
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"namespace mismatch: URL says %q but body says metadata.namespace=%q", namespace, bodyNS))
			return
		}
		if bodyNS == "" {
			// Auto-inject — operator pasted a manifest without a
			// namespace, the URL is the source of truth.
			obj.SetNamespace(namespace)
		}
	}

	// 6. Audit BEFORE the create attempt. The audit captures intent
	//    (who tried to create what), so a Forbidden / AlreadyExists
	//    response still leaves a usable trail.
	auditParams := map[string]any{
		"apiVersion":  apiVersion,
		"kind":        kind,
		"name":        obj.GetName(),
		"manifestLen": len(body),
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	created, err := conn.CreateResource(ctx, resourceType, namespace, obj)
	if err != nil {
		auditMutation(r, "create", resourceType, namespace, obj.GetName(), auditParams, err)
		switch {
		case apierrors.IsAlreadyExists(err):
			respondError(w, http.StatusConflict, fmt.Sprintf(
				"%s %q already exists in %s — use the YAML editor to update an existing resource", kind, obj.GetName(), describeNamespace(namespace, clusterScoped)))
			return
		case apierrors.IsInvalid(err):
			// IsInvalid messages from the apiserver are usually
			// well-formed and field-specific — pass through verbatim.
			respondError(w, http.StatusUnprocessableEntity, err.Error())
			return
		case apierrors.IsForbidden(err):
			respondError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("Create failed for %s/%s/%s: %v", resourceType, namespace, obj.GetName(), err)
		respondMutationError(w, err)
		return
	}

	auditMutation(r, "create", resourceType, namespace, obj.GetName(), auditParams, nil)

	// Read the newly-created resource through the connector so the
	// response carries the same detail shape every other mutation
	// handler returns (restart / scale / set-* all include `resource`).
	// Without this the frontend would have to issue a second
	// /resources/.../{ns}/{name} request whose first attempt typically
	// 404s because the informer cache hasn't observed the create yet.
	//
	// The cache lag is small (single-digit-to-tens of ms in practice)
	// but real — the apiserver's Create() returns when its store has
	// the write, while our connector's GetResourceDetail reads through
	// the local SharedInformer lister, which only updates after the
	// watch event lands. We poll briefly to bridge that window.
	resourceDetail := readPostCreateDetail(conn, resourceType, created.GetNamespace(), created.GetName())

	respondJSON(w, http.StatusCreated, map[string]interface{}{
		"status":     "created",
		"name":       created.GetName(),
		"namespace":  created.GetNamespace(),
		"kind":       created.GetKind(),
		"apiVersion": created.GetAPIVersion(),
		"uid":        string(created.GetUID()),
		// May be nil if the informer cache never caught up within the
		// retry window — the frontend treats nil as "skip the seed,
		// fall through to the regular detail fetch with retry."
		"resource": resourceDetail,
	})
}

// postCreateDetailAttempts and postCreateDetailDelay define the short
// retry loop used to bridge the apiserver-write → informer-cache-update
// gap right after a Create(). Tuned conservatively:
//   - 5 attempts × 100ms = 500ms total upper bound — well under any
//     reasonable UX latency budget for a create-and-navigate flow.
//   - Linear (not exponential) backoff because the typical gap is a
//     handful of ms; exponential would over-wait on the common case.
const (
	postCreateDetailAttempts = 5
	postCreateDetailDelay    = 100 * time.Millisecond
)

// readPostCreateDetail polls the connector for the just-created resource
// until the informer cache has observed it, or returns nil after the
// retry budget. Nil is a valid response — the caller still returns 201
// Created with the apiserver-confirmed metadata, and the frontend
// gracefully degrades (no cache seed, regular detail fetch with retry).
//
// Exposed as a package var so tests can stub the connector-detail
// reader without spinning up a full cluster. The default implementation
// is the real GetResourceDetail call.
var readPostCreateDetail = func(conn detailReader, resourceType, namespace, name string) map[string]interface{} {
	for i := 0; i < postCreateDetailAttempts; i++ {
		if detail, err := conn.GetResourceDetail(resourceType, namespace, name); err == nil && detail != nil {
			return detail
		}
		if i < postCreateDetailAttempts-1 {
			time.Sleep(postCreateDetailDelay)
		}
	}
	return nil
}

// detailReader is the narrow interface the retry helper needs from the
// connector. Lets the test stub a fake reader instead of the full
// *cluster.Connector type (which carries a lot more surface).
type detailReader interface {
	GetResourceDetail(resourceType, namespace, name string) (map[string]interface{}, error)
}

// hasMultiDoc returns true when the body contains a YAML document
// separator that splits two non-empty documents. A leading `---` on
// its own (kubectl convention) doesn't count — that's still one doc.
func hasMultiDoc(body []byte) bool {
	matches := multiDocSeparatorRE.FindAllIndex(body, -1)
	if len(matches) == 0 {
		return false
	}
	// Walk each separator. The body has multiple documents iff
	// there's content (non-whitespace) BOTH before AND after some
	// `---` line. A leading separator (only whitespace before the
	// first match) doesn't make the body multi-doc.
	for _, m := range matches {
		before := strings.TrimSpace(string(body[:m[0]]))
		after := strings.TrimSpace(string(body[m[1]:]))
		if before != "" && after != "" {
			return true
		}
	}
	return false
}

// expectedGroupVersionFor returns the GroupVersion the URL :type
// implies. Mirrors the GVR map in cluster/connector.go; the API
// handler can't reach into that package's private resourceTypeToGVR
// directly, but it doesn't need the full GVR — just the GroupVersion
// for the apiVersion consistency check.
func expectedGroupVersionFor(resourceType string) schema.GroupVersion {
	switch resourceType {
	case "pods", "nodes", "namespaces", "services", "configmaps", "secrets",
		"persistentvolumeclaims", "pvcs", "persistentvolumes", "pvs", "events":
		return schema.GroupVersion{Group: "", Version: "v1"}
	case "deployments", "statefulsets", "daemonsets", "replicasets":
		return schema.GroupVersion{Group: "apps", Version: "v1"}
	case "jobs", "cronjobs":
		return schema.GroupVersion{Group: "batch", Version: "v1"}
	case "ingresses":
		return schema.GroupVersion{Group: "networking.k8s.io", Version: "v1"}
	case "hpas", "horizontalpodautoscalers":
		return schema.GroupVersion{Group: "autoscaling", Version: "v1"}
	case "storageclasses":
		return schema.GroupVersion{Group: "storage.k8s.io", Version: "v1"}
	case "roles", "clusterroles", "rolebindings", "clusterrolebindings":
		return schema.GroupVersion{Group: "rbac.authorization.k8s.io", Version: "v1"}
	}
	// Unknown — caller already validated the type, so this branch
	// shouldn't fire in practice. Return an empty GV so the
	// mismatch check surfaces a clear error.
	return schema.GroupVersion{}
}

func describeNamespace(ns string, clusterScoped bool) string {
	if clusterScoped {
		return "the cluster"
	}
	return fmt.Sprintf("namespace %q", ns)
}
