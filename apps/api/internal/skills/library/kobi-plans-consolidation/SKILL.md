---
name: kobi-plans-consolidation
description: How to present a consolidation plan — which nodes each pool can do without, why, what keeps the others, and how the person takes a node out. Attached, with kobi-plans, to a conversation's first consolidation plan.
listed: false
audience: kobi
---
**What it is.** A consolidation plan says, pool by pool, which nodes the pool could do without: their pods fit on the others. It is what turns freed requests into a lower bill, which falls only when a node goes. kobi-plans' rules hold (words, never our names; copy the figures; suggestions, never orders); this adds what is particular to it.

**The figure.** `monthly` is the OpenCost list price of the nodes the plan suggests removing. Say it as what the cluster's bill would fall by at list prices once those nodes are gone and the pool stays smaller, and that with Savings Plans, reservations or committed-use discounts it falls by less. Never call it a saving and never promise it. Without it, the cluster has no OpenCost prices.

**Why a node is left over.** Quote each step's `why` in words. The engine counts each pod's larger of request and P95 use over the week, keeps every node under 80 % of what it can allocate, keeps at least 3 nodes and one per zone, and leaves room to lose one more node. It moves a pod only where the scheduler would put it: its node selector and node affinity, the node's taints, one replica per node when the workload asks for it, its host ports and its volume's zone. Do not re-measure it with another tool or offer other margins: they are the plan's.

**When it depends on a right-sizing plan** (`dependsOn`): the node is left over only with the requests that plan leaves. Say it first: that plan's steps come first, and the pool step waits until they are applied; removing the node before would leave pods without room.

**Who changes the pool.** KubeBolt never changes a pool: the step is exported. The export empties each node (cordon, then drain, which respects PodDisruptionBudgets) and removes that very instance with the provider's command, one node at a time. Whatever declares the pool's size (Terraform, eksctl, a pipeline) must say the new size too, or its next run brings the node back: say so, and ask the person where their pool's size is set; never guess. Say what the step's `autoscaler` does on its own.

**Risk reasons in words:** `less_headroom` — one node less is less room for a failure or a spike; `short_history` — under a week of usage, so a weekly or monthly peak may be missing; `depends_on_plan` — see above.

**Pools kept, in words:** `pool_min_nodes` — the pool is at the fewest nodes the plan takes it to (3, and one per zone); `pool_headroom` — no node's pods fit on the others with room to spare; `pool_blocked` — a pod cannot move, and the detail says which and why: a PodDisruptionBudget that allows no disruption, a pod with no controller, data on the node's own disk, a pod marked not to be evicted, a pod whose scheduling rules leave it no node among those that stay, or a pod with rules the plan does not simulate (pod affinity, a hard topology spread, anti-affinity across zones). A step's `nodesKept` names nodes kept in the same way.

**Nothing to remove** (`nothing_to_plan`): say why from each pool's exclusion, in words, and stop there. Add no figures of your own from the node list, and never suggest removing a node the plan keeps: that would be the verdict it refused, with less care.

**Present it in this order, short:**
1. What the bill would fall by at list prices, and on what condition.
2. Per pool: from how many nodes to how many, which node, and why.
3. The pools kept, and why.
4. The next step: Export, one node at a time, watching each for a week.

**After a node goes,** the plan watches the pool for 7 days: it stays at its new size, no pod is left waiting for a node it can run on, the kubelet evicts nothing for pressure and nothing is OOMKilled. It compares the cluster's bill before and after: only the bill shows what was saved.
