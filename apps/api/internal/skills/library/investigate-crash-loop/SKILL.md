---
name: investigate-crash-loop
description: How to investigate a container in CrashLoopBackOff: OOM, a startup failure, or a dependency it cannot reach.
listed: false
---
get_workload_pods, then get_pod_logs (with grep="oom|memory|fatal|panic"), then
get_resource_yaml to inspect resource limits, then get_workload_history.

If the logs show the container exits on a failed call to a dependency
(connection refused/timeout, DNS failure, authentication or 'too many
connections' from a database, cache, queue or API), the dependency is the
suspect, not the container. Call get_external_connections for this workload;
for a destination that classifies as a managed cloud service, and when you have
cloud read tools, read that resource's state (instance status; a recent stop,
failover, reboot or maintenance event and who made it, from its activity or
audit log; connections against its limit). Check get_workload_history too: a
crash that began with a revision that changed the connection target is a config
regression, not an outage.
