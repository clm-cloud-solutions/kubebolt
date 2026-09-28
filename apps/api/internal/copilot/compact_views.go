package copilot

import (
	"sort"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Compact views for the two tools whose payload is shaped for a screen, not
// for a model.
//
// list_resources and get_cluster_overview return what the UI renders: every
// row with its annotations, volumes, owner references, full container specs
// (env included) and every condition; the overview with the dashboard's event
// feed and every pod of every workload. For a model that is the wrong trade:
// fourteen kube-system pods came to 41 KB, past the 32 KB cap, and the cut
// made Kobi count 18 pods where there were 14 (2026-09-28). The REST API and
// the UI are untouched — only what Kobi and Autopilot read.
//
// What stays is what an investigation keys on: identity, status, readiness,
// restarts, placement, labels (selectors are matched against them), the owner,
// each container's image, state, last termination and resources, and every
// condition that is NOT in its healthy state, with its message. What goes is
// what get_resource_detail answers for one object: annotations, volumes, env,
// ports, probes, mounts, healthy conditions.

// listHint tells the model where the rest is, so a summary never reads as the
// whole object.
const listHint = "Rows are summaries for scanning: annotations, volumes, env, ports, probes and healthy conditions are left out. Use get_resource_detail (or get_resource_describe / get_resource_yaml) for the full object."

// rowDropKeys are left out of every list row.
var rowDropKeys = map[string]struct{}{
	"annotations": {},
	"volumes":     {},
	"uid":         {},
}

// containerKeepKeys are the container fields a list row keeps.
var containerKeepKeys = map[string]struct{}{
	"name": {}, "image": {}, "ready": {}, "state": {}, "resources": {}, "ephemeral": {},
}

// compactResourceList is list_resources' answer: the same list, each row
// reduced to what a scan needs.
func compactResourceList(list models.ResourceList) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(list.Items))
	for _, it := range list.Items {
		items = append(items, compactRow(it))
	}
	return map[string]interface{}{
		"kind":  list.Kind,
		"items": items,
		"total": list.Total,
		"note":  listHint,
	}
}

func compactRow(row map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(row))
	for k, v := range row {
		if _, drop := rowDropKeys[k]; drop {
			continue
		}
		switch k {
		case "ownerReferences":
			if owner := ownerOf(v); owner != "" {
				out["owner"] = owner
			}
		case "conditions":
			// A pod that ran to completion is not Ready by design; listing
			// that as a problem would send the model after a finished Job.
			if st, _ := row["status"].(string); st == "Succeeded" || st == "Completed" {
				continue
			}
			if bad := abnormalConditions(v); len(bad) > 0 {
				out["conditions"] = bad
			}
		case "containers":
			out["containers"] = compactContainers(v)
		default:
			out[k] = v
		}
	}
	return out
}

// ownerOf renders the owner as "Kind/name", preferring the controller.
func ownerOf(v interface{}) string {
	refs, ok := v.([]map[string]interface{})
	if !ok || len(refs) == 0 {
		return ""
	}
	pick := refs[0]
	for _, r := range refs {
		if c, _ := r["controller"].(bool); c {
			pick = r
			break
		}
	}
	kind, _ := pick["kind"].(string)
	name, _ := pick["name"].(string)
	if kind == "" || name == "" {
		return ""
	}
	return kind + "/" + name
}

// abnormalConditions keeps the conditions that are NOT in their healthy
// state. For most types healthy is status "True" (Ready, Available,
// PodScheduled, Progressing…); for the pressure family and the failure
// conditions it is "False". The message stays: it carries the reason a pod
// is unschedulable or a rollout is stuck.
func abnormalConditions(v interface{}) []map[string]interface{} {
	conds, ok := v.([]map[string]interface{})
	if !ok {
		return nil
	}
	var out []map[string]interface{}
	for _, c := range conds {
		typ, _ := c["type"].(string)
		status, _ := c["status"].(string)
		healthy := status == "True"
		if conditionHealthyWhenFalse(typ) {
			healthy = status == "False"
		}
		if healthy {
			continue
		}
		bad := map[string]interface{}{"type": typ, "status": status}
		if r, _ := c["reason"].(string); r != "" {
			bad["reason"] = r
		}
		if m, _ := c["message"].(string); m != "" {
			if len(m) > 240 {
				m = m[:240] + "…"
			}
			bad["message"] = m
		}
		out = append(out, bad)
	}
	return out
}

func conditionHealthyWhenFalse(typ string) bool {
	return strings.HasSuffix(typ, "Pressure") || typ == "NetworkUnavailable" ||
		typ == "ReplicaFailure" || typ == "Failed" || typ == "DisruptionTarget"
}

func compactContainers(v interface{}) []map[string]interface{} {
	cs, ok := v.([]map[string]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(cs))
	for _, c := range cs {
		kept := make(map[string]interface{}, len(containerKeepKeys))
		for k, val := range c {
			if _, keep := containerKeepKeys[k]; keep {
				kept[k] = val
			}
		}
		out = append(out, kept)
	}
	return out
}

// overviewWarningEvents is how many recent Warning events the overview keeps.
const overviewWarningEvents = 10

// compactOverview is get_cluster_overview's answer. Counts, capacity and
// health stay whole. The event feed keeps the most recent Warnings (the full
// stream is get_events). Workloads are summarised per namespace, naming only
// the ones that need attention — fewer ready replicas than desired, or a pod
// not ready — and the ones scaled to zero; the healthy rest is a count
// (list_resources names them). Permissions keep only what is denied.
func compactOverview(ov models.ClusterOverview) map[string]interface{} {
	out := map[string]interface{}{
		"clusterName":       ov.ClusterName,
		"kubernetesVersion": ov.KubernetesVersion,
		"platform":          ov.Platform,
		"cloudProvider":     ov.CloudProvider,
		"region":            ov.Region,
		"counts": map[string]models.ResourceCount{
			"nodes": ov.Nodes, "pods": ov.Pods, "namespaces": ov.Namespaces,
			"services": ov.Services, "deployments": ov.Deployments,
			"statefulSets": ov.StatefulSets, "daemonSets": ov.DaemonSets,
			"jobs": ov.Jobs, "cronJobs": ov.CronJobs, "ingresses": ov.Ingresses,
			"networkPolicies": ov.NetworkPolicies, "podDisruptionBudgets": ov.PodDisruptionBudgets,
			"gateways": ov.Gateways, "httpRoutes": ov.HTTPRoutes,
			"serviceAccounts": ov.ServiceAccounts, "certificates": ov.Certificates,
			"argocdApps": ov.ArgoCDApps, "vpas": ov.VPAs,
			"ciliumNetworkPolicies":            ov.CiliumNetworkPolicies,
			"ciliumClusterwideNetworkPolicies": ov.CiliumClusterwideNetworkPolicies,
			"helmReleases":                     ov.HelmReleases, "endpoints": ov.Endpoints,
			"configMaps": ov.ConfigMaps, "secrets": ov.Secrets, "pvcs": ov.PVCs,
			"pvs": ov.PVs, "hpas": ov.HPAs,
		},
		"cpu":    ov.CPU,
		"memory": ov.Memory,
		"health": ov.Health,
	}

	var warnings []models.KubeEvent
	for _, e := range ov.Events {
		if e.Type == "Warning" {
			warnings = append(warnings, e)
		}
	}
	sort.SliceStable(warnings, func(i, j int) bool { return warnings[i].Timestamp > warnings[j].Timestamp })
	totalWarnings := len(warnings)
	if len(warnings) > overviewWarningEvents {
		warnings = warnings[:overviewWarningEvents]
	}
	out["recentWarnings"] = warnings
	out["eventsNote"] = "Only the most recent Warning events are shown; get_events has the full stream, filterable by object."
	if totalWarnings > len(warnings) {
		out["warningsOmitted"] = totalWarnings - len(warnings)
	}

	type nsSummary struct {
		Namespace      string                   `json:"namespace"`
		Workloads      int                      `json:"workloads"`
		Healthy        int                      `json:"healthy"`
		NeedsAttention []map[string]interface{} `json:"needsAttention,omitempty"`
		ScaledToZero   []string                 `json:"scaledToZero,omitempty"`
	}
	var namespaces []nsSummary
	for _, nw := range ov.NamespaceWorkloads {
		s := nsSummary{Namespace: nw.Namespace, Workloads: len(nw.Workloads)}
		for _, w := range nw.Workloads {
			if w.Replicas == 0 {
				s.ScaledToZero = append(s.ScaledToZero, w.Kind+"/"+w.Name)
				continue
			}
			var notReady []map[string]interface{}
			for _, p := range w.Pods {
				if !p.Ready {
					notReady = append(notReady, map[string]interface{}{"name": p.Name, "status": p.Status})
				}
			}
			if w.ReadyReplicas >= w.Replicas && len(notReady) == 0 {
				s.Healthy++
				continue
			}
			entry := map[string]interface{}{
				"kind": w.Kind, "name": w.Name, "status": w.Status,
				"replicas": w.Replicas, "readyReplicas": w.ReadyReplicas,
			}
			if len(notReady) > 0 {
				entry["podsNotReady"] = notReady
			}
			s.NeedsAttention = append(s.NeedsAttention, entry)
		}
		namespaces = append(namespaces, s)
	}
	out["namespaces"] = namespaces
	out["workloadsNote"] = "Healthy workloads are counted, not named; list_resources names them and get_workload_metrics has their usage."

	absent := make(map[string]struct{}, len(ov.AbsentResources))
	for _, k := range ov.AbsentResources {
		absent[k] = struct{}{}
	}
	var denied []string
	for k, ok := range ov.Permissions {
		if _, isAbsent := absent[k]; !ok && !isAbsent {
			denied = append(denied, k)
		}
	}
	sort.Strings(denied)
	if len(denied) > 0 {
		out["deniedResources"] = denied
	}
	if len(ov.AbsentResources) > 0 {
		out["notInstalled"] = ov.AbsentResources
	}
	return out
}
