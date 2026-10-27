---
name: investigate-node-not-ready
description: How to investigate a NotReady node.
listed: false
---
A NotReady node is often a cloud event: the VM stopped, terminated or
impaired, a failed status check, scheduled maintenance or retirement, a
spot/preemptible reclaim, or the node pool being upgraded or scaled down. The
node's spec.providerID names the instance. When you have cloud read tools, read
that instance's state, status checks and scheduled events and its node group's
recent activity; name the instance ID, zone and state.
