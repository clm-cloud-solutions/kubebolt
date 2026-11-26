package kobiplan

import (
	"fmt"
	"strings"
)

// Export of a consolidation step (plans-16). The pool lives in the
// customer's infrastructure, so the text removes the very nodes the engine
// chose, one at a time, each emptied first:
//   - cordon stops new pods and drain moves the ones it runs, respecting
//     PodDisruptionBudgets;
//   - the provider's own command then deletes that instance and lowers the
//     pool's size by one (lowering the size alone lets the provider pick
//     which node goes, and the emptied one could stay, cordoned);
//   - whatever sets the pool's size (Terraform, eksctl, a pipeline) must say
//     the new size too, or its next run brings the node back.

// pool renders a remove_nodes step.
func (r *renderer) pool(s Step) {
	params, err := DecodeRemoveNodes(s.Params)
	r.line("# %d · %s — %s", s.Order, poolWords(s.Target.Name), "fewer nodes")
	if err != nil {
		r.line("# %v", err)
		return
	}
	r.poolSteps++
	r.line("#   %d → %d nodes%s", params.NodesBefore, params.NodesAfter, instanceWords(params.Nodes))
	if m := s.Impact.Estimated.Money; m != nil {
		r.line("#   %s a month less on the bill at list prices, once the node%s gone", moneyWords(*m), pick(len(params.Nodes), " is", "s are"))
	}
	switch params.Autoscaler {
	case AutoscalerClusterAutoscaler:
		r.line("# The Cluster Autoscaler runs here: it removes a node on its own once its requests stay under its threshold (50%% by default)")
		r.line("# and its pods fit elsewhere. If it has not, these commands do it by hand; it does not bring the node back unless pods wait.")
	case AutoscalerKarpenter:
		r.line("# Karpenter sizes this pool: with consolidation on (WhenEmptyOrUnderutilized) it removes such a node on its own.")
	}
	if len(params.Nodes) > 1 {
		r.line("# One node at a time: before the next, check for a day that no pod is left Pending and nothing restarts.")
	}

	if r.in.Format == ExportDiff {
		r.line("# The pool's size, where it is declared (Terraform, eksctl, a pipeline):")
		r.line("---")
		r.yaml(map[string]any{
			"nodePool": params.Pool, "provider": providerWords(params.PoolSource),
			"nodes":  map[string]int{"before": params.NodesBefore, "after": params.NodesAfter},
			"remove": nodeList(params.Nodes),
		})
		return
	}

	for i, n := range params.Nodes {
		r.line("")
		r.line("# Node %s%s", n.Name, zoneWords(n.Zone))
		r.line("kubectl cordon %s", n.Name)
		r.line("kubectl drain %s --ignore-daemonsets --delete-emptydir-data --timeout=15m", n.Name)
		for _, l := range removeCommand(params, n, params.NodesBefore-i-1) {
			r.line("%s", l)
		}
	}
	r.line("# Then set the pool's size to %d wherever it is declared (Terraform, eksctl, a pipeline), or its next run brings the node back.", params.NodesAfter)
	r.line("# Roll back:")
	for _, n := range params.Nodes {
		r.line("# kubectl uncordon %s   (while the node still exists)", n.Name)
	}
	for _, l := range growCommand(params) {
		r.line("# %s", l)
	}
}

// removeCommand deletes one instance and lowers its pool's size by one, to
// size (the pool after this node goes).
func removeCommand(p RemoveNodesParams, n RemovedNode, size int) []string {
	switch p.PoolSource {
	case PoolEKS:
		if id := awsInstanceID(n.ProviderID); id != "" {
			return []string{
				"# If the node group's minimum is above " + fmt.Sprint(size) + ", lower it first (aws eks update-nodegroup-config … --scaling-config minSize=" + fmt.Sprint(size) + ").",
				"aws autoscaling terminate-instance-in-auto-scaling-group --instance-id " + id + " --should-decrement-desired-capacity",
			}
		}
	case PoolAKS:
		return []string{
			`az aks nodepool delete-machines --resource-group "${RESOURCE_GROUP:?the cluster's resource group}" --cluster-name "${CLUSTER:?the AKS cluster's name}" --nodepool-name ` + p.Pool + " --machine-names " + n.Name,
		}
	case PoolGKE:
		if mig := gkeInstanceGroup(n.Name); mig != "" && n.Zone != "" {
			return []string{
				"# The pool's instance group in " + n.Zone + " (check it with: gcloud compute instance-groups managed list):",
				"gcloud compute instance-groups managed delete-instances " + mig + " --instances=" + n.Name + " --zone=" + n.Zone,
			}
		}
	case PoolKarpenter:
		return []string{"kubectl delete node " + n.Name + "   # Karpenter terminates its instance"}
	}
	return []string{"# Remove this machine where it is created: its pool could not be told from the node's labels."}
}

// growCommand puts the pool back at its size.
func growCommand(p RemoveNodesParams) []string {
	switch p.PoolSource {
	case PoolEKS:
		return []string{`aws eks update-nodegroup-config --cluster-name "${CLUSTER:?the EKS cluster's name}" --nodegroup-name ` + p.Pool + " --scaling-config desiredSize=" + fmt.Sprint(p.NodesBefore)}
	case PoolAKS:
		return []string{`az aks nodepool scale --resource-group "$RESOURCE_GROUP" --cluster-name "$CLUSTER" --name ` + p.Pool + " --node-count " + fmt.Sprint(p.NodesBefore)}
	case PoolGKE:
		var out []string
		for _, n := range p.Nodes {
			if mig := gkeInstanceGroup(n.Name); mig != "" && n.Zone != "" {
				size := "$(gcloud compute instance-groups managed describe " + mig + " --zone=" + n.Zone + " --format='value(targetSize)')"
				out = append(out, "gcloud compute instance-groups managed resize "+mig+" --size=$(( "+size+" + 1 )) --zone="+n.Zone)
			}
		}
		if len(out) > 0 {
			return out
		}
	case PoolKarpenter:
		return []string{"Karpenter adds a node again as soon as a pod cannot be scheduled."}
	}
	return []string{"Add the machine back where it is created."}
}

// awsInstanceID reads i-… from aws:///us-east-1c/i-0abc.
func awsInstanceID(providerID string) string {
	if !strings.HasPrefix(providerID, "aws://") {
		return ""
	}
	id := providerID[strings.LastIndex(providerID, "/")+1:]
	if !strings.HasPrefix(id, "i-") {
		return ""
	}
	return id
}

// gkeInstanceGroup is the managed instance group GKE names after the node:
// gke-<cluster>-<pool>-<hash>-<suffix> belongs to gke-<cluster>-<pool>-<hash>-grp.
func gkeInstanceGroup(node string) string {
	i := strings.LastIndex(node, "-")
	if !strings.HasPrefix(node, "gke-") || i < 0 {
		return ""
	}
	return node[:i] + "-grp"
}

func instanceWords(ns []RemovedNode) string {
	if len(ns) == 0 || ns[0].InstanceType == "" {
		return ""
	}
	return " (" + ns[0].InstanceType + ")"
}

func zoneWords(z string) string {
	if z == "" {
		return ""
	}
	return " (" + z + ")"
}

func nodeList(ns []RemovedNode) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return out
}

func providerWords(source string) string {
	switch source {
	case PoolEKS:
		return "EKS node group"
	case PoolAKS:
		return "AKS node pool"
	case PoolGKE:
		return "GKE node pool"
	case PoolKarpenter:
		return "Karpenter NodePool"
	}
	return "nodes without a pool"
}

func moneyWords(m Money) string {
	return fmt.Sprintf("%.2f %s", m.Monthly, strings.ToUpper(m.Currency))
}
