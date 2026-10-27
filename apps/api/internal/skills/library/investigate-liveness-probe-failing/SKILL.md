---
name: investigate-liveness-probe-failing
description: How to investigate a liveness probe that keeps failing: probe timing, a hung app, or a dependency the health check calls.
listed: false
---
A liveness endpoint that calls a dependency (a /health that queries the
database) restarts a healthy container whenever that dependency is slow or down.
If the logs show dependency timeouts or connection errors around the probe
failures, treat it like readiness: get_external_connections for this workload,
then — with cloud read tools — the state of the managed service it points at.
Relaxing the probe would hide that; name the dependency.
