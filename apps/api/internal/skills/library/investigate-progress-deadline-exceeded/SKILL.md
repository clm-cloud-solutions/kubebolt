---
name: investigate-progress-deadline-exceeded
description: How to investigate a rollout that stalled (ProgressDeadlineExceeded): a broken revision, or pods that cannot be scheduled.
listed: false
---
Before blaming the new revision, find out why its pods never became
available. Pods Pending with FailedScheduling / insufficient cpu or memory
while the cluster should be adding nodes, or new nodes failing to join, point
at a limit outside the cluster: a regional vCPU or instance quota, IP/subnet
exhaustion, a node group or node pool already at its maximum size, a failed
scaling activity. When you have cloud read tools, confirm it (the quota's value
and usage, the node group's scaling activities or status) and name it with its
region. If the new pods start and then fail, follow the crash-loop / readiness
evidence instead.
