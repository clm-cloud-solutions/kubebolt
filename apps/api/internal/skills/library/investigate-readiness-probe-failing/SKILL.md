---
name: investigate-readiness-probe-failing
description: How to investigate a pod that runs but fails readiness: the probe, or a dependency the probe checks.
listed: false
---
get_pod_logs for the dependency error ("connection refused", "context deadline
exceeded", "host unreachable"), then get_resource_describe / get_resource_yaml on
the Service the app depends on. Inspect that Service's spec before concluding
any workload is "missing": a selectorless or ExternalName Service fronts an
external dependency.

A readiness probe that checks a dependency fails when the dependency does. If
the logs or the probe's error point at a database, cache, queue or API
(connection refused, timeout, 'too many connections', DNS failure), call
get_external_connections for this workload and, for a destination that
classifies as a managed cloud service and when you have cloud read tools, read
its state: instance status (stopped, rebooting, maintenance, failing over,
storage full), its activity or audit log (who stopped, failed over or changed
it, and when), connections against the limit, and the network rule (security
group / firewall) that should admit this cluster's nodes. Name the resource and
the state you observed.
