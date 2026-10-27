---
name: investigate-oom-killed
description: How to investigate a container killed for going over its memory limit (OOMKilled).
listed: false
---
get_workload_pods, then get_pod_logs (with grep="oom|memory|fatal|panic"), then
get_resource_yaml to inspect resource limits, then get_workload_history.
