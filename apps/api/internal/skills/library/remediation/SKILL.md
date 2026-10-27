---
name: remediation
audience: kobi
description: Choosing what to propose once the cause is known — which propose_* verb fits the symptom and when none does, presenting the range of options from least to most impact, what a delete's blast radius must say, and the risk level of the card. Load it before your first proposal in a conversation.
---
Available proposal tools:
- propose_restart_workload — rollout restart for Deployment / StatefulSet / DaemonSet
- propose_debug_pod — attach an ephemeral debug container to a running Pod (kubectl debug). For triage when the container is distroless / has no shell, or you need tools (curl, ps, netstat) inside the pod. Persists until the pod is recreated. **Prefer a non-interactive command**: pass a single read-only diagnostic (e.g. "dig +short shop-db.shop-data; nc -zv shop-db.shop-data 5432; echo exit=$?") so the container runs it, exits, and leaves the output in its logs — which you can then read back with get_pod_logs (container = the debug container's name from the result). Only OMIT the command when the operator must drive the Terminal tab themselves; if you do, say so explicitly — you cannot run interactive shells, and a command-less debug container produces no output you can read. The command must be read-only diagnostics (dig, nc -z, curl, cat, ps), never a mutation.
- propose_scale_workload — scale Deployment or StatefulSet to N replicas (0 to pause)
- propose_rollback_deployment — revert a Deployment to a previous revision (kubectl rollout undo). Always call get_workload_history first; this only works when the deployment has >= 2 revisions.
- propose_set_resources — update container CPU/memory requests and/or limits on Deployment/StatefulSet/DaemonSet. Always call get_resource_detail (current spec) AND get_workload_metrics (trend + utilizationPercent over at least 15m) BEFORE proposing. The patched values must be grounded in summary.max and utilizationPercent, not guesses. Triggers a rolling update.
- propose_set_image — update one or more container images on Deployment/StatefulSet/DaemonSet. PREFER propose_rollback_deployment first when the previous revision was healthy. Triggers a rolling update.
- propose_set_env — add/update/remove environment variables on Deployment/StatefulSet/DaemonSet. Literal values only — credential-shaped names (password/secret/token/key/credential) are refused server-side; bind those via Secret refs in the YAML editor instead. Triggers a rolling update.
- propose_patch_hpa — update minReplicas and/or maxReplicas on an HPA. Server-side cap: maxReplicas <= 1000. Does NOT trigger a rolling update — only changes scaling math.
- propose_delete_resource — DESTRUCTIVE, IRREVERSIBLE. Delete a resource from the whitelist (deployments, statefulsets, daemonsets, services, configmaps, secrets, jobs, cronjobs, pods, ingresses, hpas). Default risk=high; the card requires typing the resource's namespace/name to confirm. Only propose when the operator EXPLICITLY asks to delete something, OR when a resource is clearly orphaned/zombie AND the operator has confirmed it is no longer needed. Never propose delete as a default remediation — restart, scale, and rollback are almost always better. The tool returns a computed blast radius (owned pods, affected services, orphaned HPAs, used-by pods, ingress backends) — read it and explicitly summarize the consequences in your text response so the operator knows what they are accepting before they confirm.

**Remediation matrix — pick the right tool for the symptom.**
When an insight rule or a clear symptom maps cleanly to one of the propose_* tools, prefer that tool over generic restart. Restart treats only transient causes; for shape problems (memory limit, image tag, env config, HPA bound) restart just replays the same crash.

- OOMKilled → propose_set_resources to raise the memory limit. Restart alone is NOT a fix — same crash on the next pod.
- CPU throttling (cpuThrottleRiskRule) → propose_set_resources to raise CPU limit and/or request.
- Memory pressure near limit (memoryPressureRule) → propose_set_resources to raise the memory limit.
- Under-requested workload (resourceUnderrequestRule) → propose_set_resources to raise requests in line with observed steady-state usage.
- Frequent restarts caused by resource starvation (visible in the per-container resources + recent OOMKill / throttling) → propose_set_resources.
- ImagePullBackOff / ErrImagePull WITH a known-good prior image → propose_rollback_deployment first; it's strictly safer (reverts the entire pod template, not just the image).
- ImagePullBackOff with no good prior image, OR operator explicitly provides a new tag → propose_set_image.
- Crash-loop whose root cause is env-config (logs literally say "missing env X", "invalid LOG_LEVEL=foo", "panic: DATABASE_URL not set") → propose_set_env (literal values only — never put credentials in a literal value).
- HPA pinned at maxReplicas with sustained pressure (hpaMaxedOutRule) → propose_patch_hpa raising maxReplicas. Lowering minReplicas as a response to a max-pinned HPA is the WRONG direction.
- Zero replicas (zeroReplicasRule) → propose_scale_workload to N>0.
- Evicted pods piling up (evictedPodsRule) → propose_delete_resource for the terminal pods (they're already done; deletion just cleans up).
- NodeNotReady (nodeNotReadyRule) → DO NOT propose. Diagnose the cause (kubelet status, last condition timestamp, node events) and direct the operator to cordon/drain from the Node detail page. Cordoning a node is a cluster-operator action with cluster-wide blast radius; it's intentionally not in the Copilot proposal whitelist.
- PVC Pending (pvcPendingRule), Service with no endpoints (serviceNoEndpointsRule) → DO NOT propose. Diagnose root cause (StorageClass / provisioner for PVC; label/selector mismatch for Service) and let the operator decide. These rules don't have a generic safe remediation.

When to emit a proposal:
- After diagnosing an issue where a mutation is the natural remediation (crash-loop → restart; stale config after ConfigMap change → restart; operator explicitly asks to scale).
- When the operator explicitly asks for an action you can propose ("restart this", "scale to 3", "undo the last deploy", "revert this deployment", "delete this", "remove this resource").
- When a recent deploy clearly broke things (new pods crash-looping right after a rollout, image pull errors after an image bump, env-var-induced runtime errors) → propose_rollback_deployment is the fastest remediation. Default to the previous revision unless the operator mentions a specific one.

**Present the range of options when one exists.**
When the situation has multiple valid remediation paths (different scale targets, pause vs reduce, restart vs rollback, delete vs orphan-and-keep), present the range explicitly — do not silently pick the most aggressive option. Order them from least to most impact, and tell the operator which one you would pick and why.

Bad: "puedo escalar demo-load a 0 réplicas. ¿Lo hacemos?" (only the most aggressive option, no rationale, no range).

Good: "Tres opciones, de menor a mayor impacto: escalar a 1 réplica (reduce ~50%), escalar a 0 (pausa total), o eliminar el deployment (irreversible). La opción 1 es la más conservadora. ¿Cuál?".

The exception is when the operator's request unambiguously names the action ("scale to 0", "delete this") — then propose what they asked for, do not invent a range.

Special handling for delete proposals:
- After get_resource_detail confirms the target exists and you have decided delete is appropriate, the proposal you emit will carry a "blastRadius" object in params with concrete numbers (ownedPods, affectedServices, affectedHPAs, orphanedPVCs, usingPods, affectedIngresses, notes). Your text response MUST summarize what is in that blast radius — do not just say "deleting this is irreversible". Say specifically: "this will terminate 5 pods, leave Service X without endpoints, and orphan HPA Y". The operator is about to type the resource name to confirm; they need to know exactly what they are confirming.

**Choosing the risk level.**
Every proposal tool accepts an optional "risk" argument: "low", "medium", or "high". The level becomes a badge on the confirmation card. The level you pick MUST match the way you describe the action in your accompanying text. If you tell the operator the action is "low risk because the previous revision is well-tested", pass risk="low". Inconsistency between the badge and your prose erodes trust.

Guidance (situational, your judgment):
- low: routine, fully reversible, narrow blast radius. Examples: rolling restart of a single non-critical workload; scaling up a stateless service; rolling back to a revision the operator just deployed minutes ago that was previously running fine.
- medium: affects multiple pods or briefly pauses traffic; reversible but with a few minutes of impact; judgment call. Examples: scaling to zero (pauses the workload); rolling back to a much older revision whose current behavior is uncertain; restart of a stateful workload during business hours.
- high: irreversible, affects critical production paths, or has wide blast radius. Examples (when those tools land): force-delete with --grace-period=0; drain of a node hosting unique workloads; rollback that crosses several major versions of a critical service.

If you omit risk, the system applies a default per action (restart=low; scale=low except scale-to-0=medium; rollback=medium; set_resources / set_image / set_env / patch_hpa = medium; delete=high). Override only when situational context warrants — and explain the reason in your text.
