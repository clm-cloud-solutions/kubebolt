---
name: investigate-unreachable-from-outside
description: Users or clients report errors, timeouts or a page that does not load while the cluster looks healthy (pods Ready, callers inside the cluster getting answers). The failure is on the way in.
---
**A complaint about now is not explained by something that already ended.** If every error you find stopped before the report (a burst that ended, events whose lastSeen is older than the complaint), say that it is over and keep looking: the users are describing something still happening. Never rewrite their report to fit an earlier incident.

**Answers inside the cluster say nothing about the way in.** Pods Ready, Services with endpoints and 200s to callers inside the cluster (a load generator, another workload, a probe) show that the workload answers, not that users reach it. When users fail and all of that is green, follow the request from outside:

1. When it started: the first failure seen from outside, in a probe or client that goes through the entry, or in the load balancer's metrics.
2. What fronts the workload: an Ingress, a Gateway and its routes, or a Service of type LoadBalancer. get_external_connections for the workload or its namespace gives it as entry: the address users reach, classified, and on EKS the target groups its pods are in.
3. The cloud load balancer behind that address, through the cloud connector when there is one: its listeners and the health of its targets. Unhealthy targets while the pods are Ready mean the load balancer cannot reach them: a rule between them drops the traffic (security groups or network security groups, network ACLs), or the health check's path or port no longer answers.
4. Healthy targets while users still fail point at the load balancer's own inbound rules or listener, or at the DNS in front of it.
5. The change, in the cloud's audit log: every change in the minutes before the start; without a start, under the id of the network resource that drops the traffic.

**What has been in place since a resource was created does not explain a failure that started minutes ago.** Mention it if it matters; do not offer it as the cause.
