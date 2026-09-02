package cluster

import (
	"log/slog"

	corev1 "k8s.io/api/core/v1"
)

// WorkloadOwner resolves a resource to the workload an operator actually thinks
// in terms of, collapsing a ReplicaSet to its owning Deployment.
//
// Why this exists: Trivy Operator labels every VulnerabilityReport with the
// ReplicaSet, never the Deployment — `trivy-operator.resource.name` reads
// `coredns-5f668975dd`, and the report's ownerReference points at the
// ReplicaSet itself, so the parent is not in the payload at all.
//
// That name carries a pod-template-hash, which changes on EVERY rollout. Since
// the resource name is part of a finding's fingerprint, the same CVE on the
// same image reads as a brand-new finding after each deploy while the previous
// one resolves — so `firstSeen` resets and "this CVE has been open for 40 days"
// silently becomes "found a minute ago". On a cluster that deploys daily,
// nothing ever ages enough to look alarming.
//
// Resolution is free: the connector already runs a ReplicaSet informer, so this
// is a cache read, not an API call. That matters because the caller is the
// findings sweep, which on agent-proxy clusters pays real seconds per apiserver
// round-trip over the tunnel.
//
// Returns the input unchanged when the kind is not a ReplicaSet, when the
// informer is not running (permission-gated SA), when the ReplicaSet is gone,
// or when it has no Deployment owner (a bare ReplicaSet is legitimate). Never
// guesses by string-munging the name.
//
// Every give-up path logs, because a silent one is indistinguishable from a
// working collapse until findings start churning. Observed 2026-08-05 on an
// agent-proxy cluster: two ReplicaSets stopped resolving while twenty-three
// others in the same sweep pass kept working, and the finding identity flipped
// back to the ReplicaSet — resetting firstSeen, the exact churn this function
// exists to prevent. The reason lives in WHICH branch fires, and nothing said.
//
// DEBUG, not WARN: on a healthy cluster the "not a ReplicaSet" case fires for
// most findings, and the rest are legitimate shapes (bare ReplicaSet, gated
// informer). The cache size rides along so "the cache is empty" and "the cache
// has 40 ReplicaSets but not this one" — very different faults — can be told
// apart from the line alone.
//
// The third return says whether the answer is SETTLED: true when it will not
// change once the caches are warm, false when the lookup could not be performed
// this time and a later pass would answer differently. Only the cache-miss
// branch is unsettled; a bare ReplicaSet and a permanently absent informer are
// both final, degraded answers.
//
// The distinction exists because the caller fingerprints on the name it gets
// back, so an unsettled answer would mint a SECOND identity for a finding that
// already has one, and resolve the first — the churn documented above, observed
// as batches of ReplicaSet-named records resolving hours after they were
// written. Telling the two apart lets the caller wait instead of guessing;
// logging alone could only report the damage after the fact.
func (c *Connector) WorkloadOwner(namespace, kind, name string) (string, string, bool) {
	if name == "" || c == nil {
		return kind, name, true
	}
	if kind == "Pod" {
		return c.podWorkloadOwner(namespace, name)
	}
	if kind != "ReplicaSet" {
		return kind, name, true
	}
	if c.replicaSetLister == nil {
		slog.Debug("workload owner: no ReplicaSet informer",
			slog.String("namespace", namespace), slog.String("name", name))
		return kind, name, true
	}
	rs, err := c.replicaSetLister.ReplicaSets(namespace).Get(name)
	if err != nil || rs == nil {
		cached, _ := c.replicaSetLister.List(everythingSelector())
		slog.Debug("workload owner: ReplicaSet not in cache",
			slog.String("namespace", namespace),
			slog.String("name", name),
			slog.Int("cached_replicasets", len(cached)),
			slog.String("error", errText(err)))
		return kind, name, false
	}
	for _, ref := range rs.OwnerReferences {
		if ref.Kind == "Deployment" && ref.Name != "" {
			return "Deployment", ref.Name, true
		}
	}
	slog.Debug("workload owner: ReplicaSet has no Deployment owner",
		slog.String("namespace", namespace), slog.String("name", name))
	return kind, name, true
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// podControllerKinds are the owners a Pod may be collapsed onto. Deliberately a
// closed list rather than "whatever owns it":
//
// STATIC PODS are owned by a NODE. kube-apiserver, etcd, kube-scheduler and
// kube-controller-manager all name the same node as owner, so collapsing on any
// owner would fuse four distinct components into one row and lose which one
// violates. A Node is not a workload; the pod is the unit there.
//
// A bare Pod (no owner at all) is the unit too, and falls through untouched.
var podControllerKinds = map[string]bool{
	"ReplicaSet":  true,
	"DaemonSet":   true,
	"StatefulSet": true,
	"Job":         true,
}

// podWorkloadOwner collapses a Pod onto the workload that owns it, chaining
// through a ReplicaSet to its Deployment.
//
// Why a Pod needs this at all: Kyverno validates a workload TWICE — the pod
// against the rule and the controller's template against an auto-generated twin
// — so the same defect arrives once per level. Collapsing makes them converge on
// one row, and it stops the pod-level half from dying and being reborn with the
// clock at zero on every restart, which is the churn this file exists to
// prevent.
//
// Stops at the Job on purpose: a Job's pods are one run of one workload, and a
// CronJob's runs are things an operator compares against each other. Collapsing
// a Job into its CronJob would erase which run failed.
//
// Measured on the dev cluster before writing this: of 12 pods carrying a policy
// violation, 5 were fully covered by their owner (redundant), 1 was ownerless
// and 4 were static pods — and 2 carried a violation their controller does NOT
// report. That last pair is why this collapses the IDENTITY rather than dropping
// the pod's finding: `hostPort` is defaulted from `containerPort` at admission
// when `hostNetwork: true`, so the violation exists only on the materialized
// pod. Dropping it would hide a port bound on the node; collapsing it files it
// against the DaemonSet, which is where it gets fixed.
func (c *Connector) podWorkloadOwner(namespace, name string) (string, string, bool) {
	if c.podLister == nil {
		slog.Debug("workload owner: no Pod informer",
			slog.String("namespace", namespace), slog.String("name", name))
		return "Pod", name, true
	}
	pod, err := c.podLister.Pods(namespace).Get(name)
	if err != nil || pod == nil {
		slog.Debug("workload owner: Pod not in cache",
			slog.String("namespace", namespace),
			slog.String("name", name),
			slog.String("error", errText(err)))
		return "Pod", name, false
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Name == "" || !podControllerKinds[ref.Kind] {
			continue
		}
		if ref.Kind == "ReplicaSet" {
			// Chain: the ReplicaSet name carries a pod-template-hash that moves on
			// every rollout, so stopping here would only relocate the churn. The
			// chained call's settled flag rides out with it — a Pod resolved off a
			// ReplicaSet the cache has not seen is just as unsettled.
			return c.WorkloadOwner(namespace, "ReplicaSet", ref.Name)
		}
		return ref.Kind, ref.Name, true
	}
	return "Pod", name, true
}

// ContextNameForClusterID is the inverse of ClusterIDForContext: given the
// kube-system UID a record was stamped with, return the context name the
// Manager routes by.
//
// The two identities are not interchangeable and mixing them fails in the worst
// way — silently, with a 404 from the apiserver rather than a clear "unknown
// cluster". A persisted finding carries the UID (that is the canonical id
// everywhere: agent samples, ClusterInfo, the Falco ingest), while runtime
// resolution keys on the context name, which for an agent-proxy cluster is
// `agent:<uid>`. Any handler that starts from stored data and needs a live
// connector has to cross that seam.
//
// Falls back to the input unchanged for a direct kubeconfig context, whose
// name is arbitrary and whose UID lives in the persisted map — that case
// already resolves correctly because the caller's own context name is used.
func (m *Manager) ContextNameForClusterID(clusterID string) string {
	if clusterID == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for contextName, cid := range m.agentProxyContexts {
		if cid == clusterID {
			return contextName
		}
	}
	return clusterID
}

// CountPodsRunningImage returns how many pods of `namespace` currently run
// `image` in any container — init, sidecar or main.
//
// It answers the question a CVE row provokes and neither Trivy nor the finding
// can: how much is actually exposed right now. A scan report describes an IMAGE;
// the blast radius is the number of live pods carrying it, which changes with
// every scale event while the finding does not.
//
// Reads the running Pod informer, so it costs a cache walk rather than an
// apiserver round-trip — the same reason WorkloadOwner is free. Returns 0 when
// the informer is not running (a permission-gated SA), which the caller must
// render as unknown rather than as "nothing affected".
func (c *Connector) CountPodsRunningImage(namespace, image string) int {
	if c == nil || c.podLister == nil || image == "" {
		return 0
	}
	pods, err := c.podLister.Pods(namespace).List(everythingSelector())
	if err != nil {
		return 0
	}
	n := 0
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		if podUsesImage(pod.Spec.Containers, image) || podUsesImage(pod.Spec.InitContainers, image) {
			n++
		}
	}
	return n
}

// podUsesImage matches on the container's declared image. Comparing the
// declared reference (not status.imageID) is deliberate: that is the same
// string Trivy reconstructs its report from, so the two agree even when a tag
// has been repointed at a new digest since the pod started.
func podUsesImage(containers []corev1.Container, image string) bool {
	for i := range containers {
		if containers[i].Image == image {
			return true
		}
	}
	return false
}
