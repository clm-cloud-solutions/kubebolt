package cluster

import (
	"log"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"
)

// Optional CRDs installed or removed after the connector was built.
//
// WHY THIS EXISTS. probePermissions decides once, when the connector is built,
// whether each optional CRD (cert-manager, Argo CD, VPA, the Cilium policies) is
// served, and nothing asked again. In the AWS lab the agent registered at 20:52Z
// and Cilium was installed at 21:00Z: from then on the Overview said "Optional
// integrations not detected: Cilium", Kobi's compact views marked the policies
// notInstalled, and Kobi answered "the Cilium policy CRDs are not installed" in a
// namespace holding two CiliumNetworkPolicies. An agent rollout did not help (the
// connector outlives it); only an API restart did (finding #76 §5).
//
// The optional CRDs are listed on demand through the dynamic client (crds.go) —
// there is no informer to start or stop — so refreshing their permission flags is
// the whole fix.
//
// COST. One discovery request for the groups, plus one per version of each
// optional group the cluster serves (usually none to three), every interval, on
// the permission probe's own rate-limited client.
const optionalCRDRefreshInterval = 2 * time.Minute

// StartOptionalCRDRefresh re-checks the optional CRDs every interval until the
// connector stops.
func (c *Connector) StartOptionalCRDRefresh() {
	go func() {
		tick := time.NewTicker(optionalCRDRefreshInterval)
		defer tick.Stop()
		for {
			select {
			case <-c.stopCh:
				return
			case <-tick.C:
				c.RefreshOptionalCRDs()
			}
		}
	}()
}

// RefreshOptionalCRDs asks discovery which optional CRDs the cluster serves now
// and updates their permissions. Only a CRD the ServiceAccount may list moves:
// installed → listable, removed → absent. An RBAC denial stays a denial, and a
// group discovery could not answer for keeps its current state.
func (c *Connector) RefreshOptionalCRDs() {
	if c == nil {
		return
	}
	client := c.probeClient
	if client == nil {
		client = c.clientset
	}
	if client == nil {
		return
	}
	served, checked := discoverOptionalCRDs(client.Discovery())
	if len(checked) == 0 {
		return
	}
	c.permsMu.Lock()
	next, changes := applyOptionalCRDDiscovery(c.permissions, served, checked)
	if len(changes) > 0 {
		c.permissions = next
	}
	c.permsMu.Unlock()
	for _, ch := range changes {
		if ch.installed {
			log.Printf("Optional CRD %s installed in cluster %q — now available", ch.groupResource, c.clusterName)
		} else {
			log.Printf("Optional CRD %s no longer served in cluster %q — marking unavailable", ch.groupResource, c.clusterName)
		}
	}
}

// discoverOptionalCRDs reports, for each optional CRD ("group/resource"), whether
// the cluster serves it in any version (served) and whether discovery could tell
// (checked) — the same any-version rule probePermissions applies. A group absent
// from the group list is checked and not served; a version that fails with
// anything but NotFound leaves its group unchecked.
func discoverOptionalCRDs(disc discovery.DiscoveryInterface) (served, checked map[string]bool) {
	groups, err := disc.ServerGroups()
	if err != nil || groups == nil {
		return nil, nil
	}
	versions := map[string][]string{}
	for _, g := range groups.Groups {
		for _, v := range g.Versions {
			versions[g.Name] = append(versions[g.Name], v.GroupVersion)
		}
	}
	// The optional groups, each with the resources KubeBolt reads from it.
	wanted := map[string][]string{}
	for _, def := range resourceDefs {
		if def.optional {
			wanted[def.group] = append(wanted[def.group], def.resource)
		}
	}
	served, checked = map[string]bool{}, map[string]bool{}
	for group, resources := range wanted {
		names := map[string]bool{}
		ok := true
		for _, gv := range versions[group] {
			list, err := disc.ServerResourcesForGroupVersion(gv)
			if err != nil {
				if apierrors.IsNotFound(err) {
					continue // the version went away between the two calls
				}
				ok = false
				break
			}
			for _, r := range list.APIResources {
				names[r.Name] = true
			}
		}
		if !ok {
			continue
		}
		for _, r := range resources {
			key := group + "/" + r
			checked[key] = true
			served[key] = names[r]
		}
	}
	return served, checked
}

type optionalCRDChange struct {
	groupResource string
	installed     bool
}

// applyOptionalCRDDiscovery returns perms with each checked optional CRD's
// availability set from served, and what changed. It never edits perms or its
// entries: a changed entry is a copy in a new map, so a reader holding the old
// map keeps a consistent view. With no change it returns perms itself.
//
// Absent already means "RBAC grants it, the cluster does not serve it"
// (probePermissions), so an RBAC grant is CanList || Absent; anything else was
// denied and is left alone.
func applyOptionalCRDDiscovery(perms ResourcePermissions, served, checked map[string]bool) (ResourcePermissions, []optionalCRDChange) {
	var next ResourcePermissions
	var changes []optionalCRDChange
	set := func(key string, p ResourcePermission) {
		if next == nil {
			next = make(ResourcePermissions, len(perms))
			for k, v := range perms {
				next[k] = v
			}
		}
		next[key] = &p
	}
	for key, def := range resourceDefs {
		if !def.optional {
			continue
		}
		gr := def.group + "/" + def.resource
		p := perms[key]
		if p == nil || !checked[gr] || !(p.CanList || p.Absent) {
			continue
		}
		switch {
		case served[gr] && p.Absent:
			np := *p
			np.CanList, np.CanWatch, np.CanGet, np.Absent = true, true, true, false
			set(key, np)
			changes = append(changes, optionalCRDChange{groupResource: gr, installed: true})
		case !served[gr] && !p.Absent:
			np := *p
			np.CanList, np.CanWatch, np.CanGet, np.Absent = false, false, false, true
			set(key, np)
			changes = append(changes, optionalCRDChange{groupResource: gr, installed: false})
		}
	}
	if next == nil {
		return perms, nil
	}
	return next, changes
}
