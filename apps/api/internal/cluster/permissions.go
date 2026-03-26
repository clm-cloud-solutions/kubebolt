package cluster

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ResourcePermission represents access rights for a single resource type.
type ResourcePermission struct {
	Resource        string   `json:"resource"`
	Group           string   `json:"group"`
	CanList         bool     `json:"canList"`
	CanWatch        bool     `json:"canWatch"`
	CanGet          bool     `json:"canGet"`
	NamespaceScoped bool     `json:"namespaceScoped,omitempty"` // true if access is via RoleBindings, not cluster-wide
	Namespaces      []string `json:"namespaces,omitempty"`      // namespaces where access is granted
}

// ResourcePermissions maps resource type keys to their permission results.
type ResourcePermissions map[string]*ResourcePermission

// CanListWatch returns true if both list and watch are permitted for the given key.
func (p ResourcePermissions) CanListWatch(key string) bool {
	perm, ok := p[key]
	if !ok {
		return true // unknown resources assumed permitted
	}
	return perm.CanList && perm.CanWatch
}

// ScopedNamespaces returns the list of accessible namespaces if any permission
// is namespace-scoped. Returns nil for cluster-wide access.
func (p ResourcePermissions) ScopedNamespaces() []string {
	for _, perm := range p {
		if perm.NamespaceScoped && len(perm.Namespaces) > 0 {
			return perm.Namespaces
		}
	}
	return nil
}

// AllPermitted returns a permission map with all resources allowed.
func AllPermitted() ResourcePermissions {
	perms := make(ResourcePermissions, len(resourceDefs))
	for key, def := range resourceDefs {
		perms[key] = &ResourcePermission{
			Resource: def.resource,
			Group:    def.group,
			CanList:  true,
			CanWatch: true,
			CanGet:   true,
		}
	}
	return perms
}

// PermissionDeniedError is returned when a resource is not accessible.
type PermissionDeniedError struct {
	Resource string
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("insufficient permissions to access %s", e.Resource)
}

// resourceDef describes a Kubernetes resource for permission probing.
type resourceDef struct {
	group        string
	resource     string
	clusterScope bool // true for cluster-scoped resources (no namespace fallback)
}

// resourceDefs maps internal keys to their K8s API group and resource name.
// This must stay in sync with setupInformers().
var resourceDefs = map[string]resourceDef{
	// Core v1
	"pods":           {group: "", resource: "pods"},
	"nodes":          {group: "", resource: "nodes", clusterScope: true},
	"namespaces":     {group: "", resource: "namespaces", clusterScope: true},
	"services":       {group: "", resource: "services"},
	"endpointslices": {group: "discovery.k8s.io", resource: "endpointslices"},
	"configmaps":     {group: "", resource: "configmaps"},
	"secrets":        {group: "", resource: "secrets"},
	"pvcs":           {group: "", resource: "persistentvolumeclaims"},
	"pvs":            {group: "", resource: "persistentvolumes", clusterScope: true},
	"events":         {group: "", resource: "events"},
	// Apps v1
	"deployments":  {group: "apps", resource: "deployments"},
	"statefulsets":  {group: "apps", resource: "statefulsets"},
	"daemonsets":    {group: "apps", resource: "daemonsets"},
	"replicasets":   {group: "apps", resource: "replicasets"},
	// Batch v1
	"jobs":     {group: "batch", resource: "jobs"},
	"cronjobs": {group: "batch", resource: "cronjobs"},
	// Networking v1
	"ingresses": {group: "networking.k8s.io", resource: "ingresses"},
	// Autoscaling v1
	"hpas": {group: "autoscaling", resource: "horizontalpodautoscalers"},
	// Storage v1
	"storageclasses": {group: "storage.k8s.io", resource: "storageclasses", clusterScope: true},
	// RBAC v1
	"roles":               {group: "rbac.authorization.k8s.io", resource: "roles"},
	"clusterroles":        {group: "rbac.authorization.k8s.io", resource: "clusterroles", clusterScope: true},
	"rolebindings":        {group: "rbac.authorization.k8s.io", resource: "rolebindings"},
	"clusterrolebindings": {group: "rbac.authorization.k8s.io", resource: "clusterrolebindings", clusterScope: true},
}

// discoverAccessibleNamespaces returns a list of namespace names to use for
// namespace-scoped permission probing. It tries listing namespaces from the API;
// if that fails, it falls back to a small set of common namespaces.
func discoverAccessibleNamespaces(clientset kubernetes.Interface) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nsList, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err == nil && len(nsList.Items) > 0 {
		names := make([]string, 0, len(nsList.Items))
		for _, ns := range nsList.Items {
			names = append(names, ns.Name)
		}
		return names
	}
	// Fallback: common namespaces
	return []string{"default", "kube-system", "kube-public"}
}

// checkSSAR runs a single SelfSubjectAccessReview and returns whether access is allowed.
func checkSSAR(clientset kubernetes.Interface, group, resource, verb, namespace string) bool {
	attrs := &authorizationv1.ResourceAttributes{
		Verb:     verb,
		Resource: resource,
		Group:    group,
	}
	if namespace != "" {
		attrs.Namespace = namespace
	}
	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attrs},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	return err == nil && resp.Status.Allowed
}

// probePermissions uses SelfSubjectAccessReview to check permissions for all resource types.
// For namespace-scoped resources denied at cluster level, it retries in a known namespace
// to detect RoleBinding-based access (e.g., SA with view in specific namespaces).
// If SSAR itself is forbidden, falls back to all-permitted (preserves current behavior).
func probePermissions(clientset kubernetes.Interface) ResourcePermissions {
	perms := make(ResourcePermissions, len(resourceDefs))

	// Test one SSAR first to check if the API is available
	testReview := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     "list",
				Resource: "pods",
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, testReview, metav1.CreateOptions{})
	if err != nil {
		log.Printf("Warning: SelfSubjectAccessReview not available (%v) — assuming full permissions", err)
		return AllPermitted()
	}

	type probeResult struct {
		key string
		ok  bool
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 10)
		results []probeResult
	)

	// Phase 1: Probe all resources at cluster scope
	for key, def := range resourceDefs {
		wg.Add(1)
		go func(key, group, resource string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			allowed := checkSSAR(clientset, group, resource, "list", "")

			mu.Lock()
			results = append(results, probeResult{key: key, ok: allowed})
			mu.Unlock()
		}(key, def.group, def.resource)
	}
	wg.Wait()

	// Apply cluster-scope results
	for key, def := range resourceDefs {
		perms[key] = &ResourcePermission{
			Resource: def.resource,
			Group:    def.group,
		}
	}
	for _, r := range results {
		perm := perms[r.key]
		perm.CanList = r.ok
		perm.CanWatch = r.ok
		perm.CanGet = r.ok
	}

	// Phase 2: For namespace-scoped resources denied at cluster level,
	// check if the SA has access in any specific namespace (RoleBinding-based).
	// If so, create namespace-scoped informers instead of cluster-scoped ones.
	var nsRetries []string
	for key, def := range resourceDefs {
		if !def.clusterScope && !perms[key].CanList {
			nsRetries = append(nsRetries, key)
		}
	}

	if len(nsRetries) > 0 {
		probeNamespaces := discoverAccessibleNamespaces(clientset)
		if len(probeNamespaces) > 0 {
			log.Printf("Probing %d namespace-scoped resources for namespace-level access...", len(nsRetries))
			// Pick one resource (pods) to find which namespaces this SA can access
			var accessibleNS []string
			for _, ns := range probeNamespaces {
				if checkSSAR(clientset, "", "pods", "list", ns) {
					accessibleNS = append(accessibleNS, ns)
				}
			}
			if len(accessibleNS) > 0 {
				log.Printf("Found namespace-level access in %d namespaces: %v", len(accessibleNS), accessibleNS)
				// For each denied resource, probe in the first accessible namespace
				var wg2 sync.WaitGroup
				for _, key := range nsRetries {
					wg2.Add(1)
					go func(key string) {
						defer wg2.Done()
						sem <- struct{}{}
						defer func() { <-sem }()

						def := resourceDefs[key]
						if checkSSAR(clientset, def.group, def.resource, "list", accessibleNS[0]) {
							mu.Lock()
							perm := perms[key]
							perm.CanList = true
							perm.CanWatch = true
							perm.CanGet = true
							perm.NamespaceScoped = true
							mu.Unlock()
						}
					}(key)
				}
				wg2.Wait()

				// Store the accessible namespaces for informer creation
				mu.Lock()
				for _, p := range perms {
					if p.NamespaceScoped {
						p.Namespaces = accessibleNS
					}
				}
				mu.Unlock()
			}
		}
	}

	// Log summary
	permitted := 0
	for _, p := range perms {
		if p.CanList && p.CanWatch {
			permitted++
		}
	}
	log.Printf("Permission probe complete: %d/%d resource types accessible", permitted, len(perms))

	return perms
}
