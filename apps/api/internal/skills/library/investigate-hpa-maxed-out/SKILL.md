---
name: investigate-hpa-maxed-out
description: How to investigate an HPA stuck at its maximum: real load, or replicas that cannot be scheduled.
listed: false
---
get_resource_detail on the HPA to confirm currentReplicas == maxReplicas;
get_pod_logs sampled for actual load.

Check whether the replicas the HPA asks for actually start. If new pods sit
Pending because nodes cannot be added, the ceiling may be the cloud's, not the
HPA's: a regional vCPU or instance quota, IP exhaustion, a node group or node
pool at its maximum size, a failed scaling activity. With cloud read tools, read
the quota (value and usage) and the node group's recent scaling activities, and
name the limit and its region — raising maxReplicas cannot help when the pods it
adds cannot be scheduled.
