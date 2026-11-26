package kobiplan

import (
	"strings"
	"testing"
	"time"
)

func consolidationPlanFor(t *testing.T, source string, providerID func(i int) string) Plan {
	t.Helper()
	nodes := pool(5)
	for i := range nodes {
		nodes[i].PoolSource = source
		nodes[i].ProviderID = providerID(i)
		if source == PoolGKE {
			nodes[i].Name = "gke-prod-general-1a2b3c4d-" + []string{"abcd", "efgh", "ijkl", "mnop", "qrst"}[i]
		}
	}
	pods := withDaemons(nodes, []PodFacts{
		pod(nodes[0].Name, "a", 2000, 8), pod(nodes[1].Name, "b", 1900, 7), pod(nodes[2].Name, "c", 2100, 8), pod(nodes[3].Name, "d", 1800, 8), pod(nodes[4].Name, "e", 600, 2),
	})
	p, err := BuildConsolidation(consolidationInput(nodes, pods, nil))
	if err != nil || len(p.Steps) != 1 {
		t.Fatalf("plan: %v %+v", err, p.Steps)
	}
	return p
}

func renderPool(t *testing.T, p Plan, f ExportFormat) Rendered {
	t.Helper()
	out, err := RenderExport(ExportInput{Plan: p, Format: f, ClusterName: "prod-eks", At: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Each node is emptied, then that very instance goes with the pool's size;
// the rollback grows the pool back; the size where it is declared is the
// person's to change.
func TestExport_RemoveNodes(t *testing.T) {
	p := consolidationPlanFor(t, PoolEKS, func(i int) string { return "aws:///us-east-1b/i-0abc" + string(rune('0'+i)) })
	out := renderPool(t, p, ExportCommands)
	for _, want := range []string{
		"# 1 · pool general — fewer nodes",
		"#   5 → 4 nodes (m6i.xlarge)",
		"#   129.40 EUR a month less on the bill at list prices, once the node is gone",
		"kubectl cordon node-5\nkubectl drain node-5 --ignore-daemonsets --delete-emptydir-data --timeout=15m",
		"aws autoscaling terminate-instance-in-auto-scaling-group --instance-id i-0abc4 --should-decrement-desired-capacity",
		"If the node group's minimum is above 4, lower it first",
		"set the pool's size to 4 wherever it is declared",
		"# kubectl uncordon node-5",
		`--nodegroup-name general --scaling-config desiredSize=5`,
		"watch the pool for 7 days",
		"committed-use discounts the bill falls by less",
	} {
		if !strings.Contains(out.Text, want) {
			t.Errorf("missing %q in:\n%s", want, out.Text)
		}
	}
	if strings.Contains(out.Text, "no command for this change yet") || len(out.Notes) != 1 || !strings.Contains(out.Notes[0], "brings the node back") {
		t.Errorf("notes = %v\n%s", out.Notes, out.Text)
	}

	diff := renderPool(t, p, ExportDiff)
	for _, want := range []string{"nodePool: general", "provider: EKS node group", "after: 4", "- node-5"} {
		if !strings.Contains(diff.Text, want) {
			t.Errorf("diff missing %q in:\n%s", want, diff.Text)
		}
	}
}

// Every provider removes the node by name; a pool it cannot tell says so.
func TestExport_RemoveNodesByProvider(t *testing.T) {
	none := func(int) string { return "" }
	for source, want := range map[string]string{
		PoolAKS:       `az aks nodepool delete-machines --resource-group "${RESOURCE_GROUP:?the cluster's resource group}" --cluster-name "${CLUSTER:?the AKS cluster's name}" --nodepool-name general --machine-names node-5`,
		PoolGKE:       "gcloud compute instance-groups managed delete-instances gke-prod-general-1a2b3c4d-grp --instances=gke-prod-general-1a2b3c4d-qrst --zone=us-east-1c",
		PoolKarpenter: "kubectl delete node node-5   # Karpenter terminates its instance",
		PoolNone:      "# Remove this machine where it is created",
		PoolEKS:       "# Remove this machine where it is created", // no instance id to name
	} {
		if out := renderPool(t, consolidationPlanFor(t, source, none), ExportCommands); !strings.Contains(out.Text, want) {
			t.Errorf("%s: missing %q in:\n%s", source, want, out.Text)
		}
	}
}
