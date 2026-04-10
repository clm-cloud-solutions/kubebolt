# KubeBolt Insights Rules Reference

KubeBolt's Insights Engine evaluates 12 rules against the current cluster state. The copilot
should understand each rule to explain insights to users and suggest remediation.

## Rule Definitions

### 1. crash-loop (Critical)

**Condition:** Pod in `CrashLoopBackOff` with restarts > 3 in the last hour.

**What it means:** The container starts, crashes, and Kubernetes keeps restarting it with exponential backoff. The pod never becomes healthy.

**Common causes:**
- Application error on startup (missing config, bad environment variable, dependency not reachable)
- OOM kill at startup (memory limit too low for initialization)
- Missing or wrong entrypoint/command in the container image
- Liveness probe failing immediately after startup

**Remediation:**
1. Check logs: `kubectl logs <pod> -n <ns> --previous` (the `--previous` flag shows the crashed container's logs)
2. Check events for the pod for scheduling/image issues
3. Verify environment variables and ConfigMap/Secret mounts
4. If OOM: increase memory limit
5. If liveness probe: add or increase `initialDelaySeconds`

---

### 2. oom-killed (Critical)

**Condition:** Pod terminated with OOMKilled reason (exit code 137).

**What it means:** The container exceeded its memory limit and the Linux OOM killer terminated it. Kubernetes will restart the pod, but if it consistently needs more memory, it will keep getting killed.

**Common causes:**
- Memory limit set too low for the workload
- Memory leak in the application
- Unexpected traffic spike causing higher memory usage
- JVM heap not aligned with container memory limit

**Remediation:**
1. Check current memory usage vs limit — if usage consistently approaches the limit, increase it
2. Suggested new limit: current peak usage × 1.3 (30% headroom)
3. For JVMs: ensure `-Xmx` is set to ~75% of container memory limit
4. If usage grows over time (leak): investigate application-level memory management

---

### 3. cpu-throttle-risk (Warning)

**Condition:** CPU usage > 80% of limit sustained.

**What it means:** The workload is approaching its CPU limit. When it hits the limit, the Linux CFS scheduler throttles the container, causing increased latency and slower processing.

**Remediation:**
1. Increase CPU limit if throttling is causing performance issues
2. Consider scaling horizontally (more replicas) if single-pod CPU is maxed
3. Check if HPA is configured — if not, consider adding one
4. Profile the application for CPU-intensive operations that could be optimized

---

### 4. memory-pressure (Warning)

**Condition:** Memory usage > 85% of limit.

**What it means:** The workload is close to its memory limit. An OOMKill is likely if usage spikes. This is a proactive warning before the critical `oom-killed` event.

**Remediation:**
1. Increase memory limit with headroom: current usage × 1.25
2. Monitor if usage is stable or trending upward (leak)
3. Check for in-memory caches or buffers that could be bounded

---

### 5. resource-underrequest (Info)

**Condition:** CPU/Memory requests < 40% of actual usage.

**What it means:** The resource requests are much lower than what the workload actually uses. The Kubernetes scheduler uses requests to decide node placement. Under-requesting causes the scheduler to pack too many pods on a node, leading to resource contention.

**Remediation:**
1. Set requests closer to actual average usage (e.g., p50 of actual usage)
2. This improves scheduling accuracy and prevents noisy-neighbor issues
3. Use KubeBolt metrics to determine appropriate request values

---

### 6. zero-replicas (Critical)

**Condition:** Deployment with 0 available replicas (but desired > 0).

**What it means:** The deployment has no running pods. The application is completely unavailable.

**Common causes:**
- All pods failing to schedule (insufficient resources, node affinity, taints)
- Image pull failures (wrong image name, missing credentials)
- All pods crash-looping
- PVC not binding (for stateful workloads)

**Remediation:**
1. Check pod events for scheduling failures
2. Check if nodes have sufficient resources
3. Verify image exists and pull secrets are configured
4. Check for PVC binding issues if volumes are used

---

### 7. pvc-pending (Warning)

**Condition:** PersistentVolumeClaim in Pending state for > 5 minutes.

**What it means:** The PVC cannot find or provision a matching PersistentVolume. Pods waiting for this PVC will be stuck in Pending.

**Common causes:**
- No PV matches the PVC's requirements (size, access mode, storage class)
- StorageClass provisioner is not working or not installed
- Cloud provider quota exceeded (can't create new volumes)
- StorageClass has `volumeBindingMode: WaitForFirstConsumer` and no pod is scheduled yet

**Remediation:**
1. Check if the StorageClass exists and is configured correctly
2. Verify the provisioner is running (check kube-system namespace)
3. Check cloud provider quotas if using dynamic provisioning
4. Manually create a PV if using static provisioning

---

### 8. node-not-ready (Critical)

**Condition:** Node condition Ready != True.

**What it means:** A node in the cluster is not healthy. Pods on this node may be evicted or unable to run. Kubernetes will eventually reschedule pods to healthy nodes (after the `pod-eviction-timeout`, default 5 minutes).

**Common causes:**
- kubelet not running or crashing
- Node out of disk, memory, or PIDs
- Network issues (node can't reach API server)
- Underlying VM/instance health issue

**Remediation:**
1. Check node conditions for specific reasons (DiskPressure, MemoryPressure, PIDPressure)
2. SSH into the node and check kubelet status: `systemctl status kubelet`
3. For managed K8s (EKS/GKE/AKS): check the cloud console for instance health
4. If node is permanently unhealthy, cordon and drain it

---

### 9. hpa-maxed-out (Warning)

**Condition:** HPA current replicas == maximum replicas.

**What it means:** The HorizontalPodAutoscaler has scaled the workload to its configured maximum, but the scaling metric (usually CPU) still exceeds the target. The workload may need more capacity than the HPA allows.

**Remediation:**
1. Increase `maxReplicas` on the HPA if the cluster can support more pods
2. Check if the pods themselves are undersized — vertical scaling (bigger pods) might be better
3. Review the HPA target utilization — maybe 80% target is too aggressive
4. Check if there's a steady-state load increase vs a transient spike

---

### 10. frequent-restarts (Warning)

**Condition:** Pod with > 5 restarts in the last 24 hours (but not in CrashLoopBackOff).

**What it means:** The pod is restarting intermittently — not crash-looping, but not stable either. This could indicate a flaky dependency, intermittent OOM, or health check issues.

**Remediation:**
1. Check logs across restarts — look for patterns (time of day, specific errors)
2. Review liveness/readiness probes — may be too aggressive
3. Check if restarts correlate with traffic patterns or deployments
4. Look for intermittent OOM events in pod events

---

### 11. image-pull-backoff (Critical)

**Condition:** Pod in `ImagePullBackOff` or `ErrImagePull` state.

**What it means:** Kubernetes cannot pull the container image. The pod will never start until this is resolved.

**Common causes:**
- Image name or tag is wrong
- Private registry and pull secret is not configured or expired
- Image was deleted from the registry
- Registry is unreachable (network/DNS issues)

**Remediation:**
1. Verify the image exists: `docker pull <image>` locally
2. Check pull secrets: `kubectl get pod <pod> -o jsonpath='{.spec.imagePullSecrets}'`
3. Verify the secret exists and has valid credentials
4. Check DNS resolution from the node to the registry

---

### 12. evicted-pods (Info)

**Condition:** Pods evicted from a node.

**What it means:** Kubernetes evicted pods because the node ran out of resources (disk, memory, or PIDs). Evicted pods are dead — they won't restart on the same node. The controller (Deployment, etc.) creates new pods on other nodes.

**Common causes:**
- Node disk pressure (logs, images, or emptyDir filling up)
- Node memory pressure (too many pods, or system processes consuming memory)
- PID exhaustion (fork bombs or too many processes)

**Remediation:**
1. Check which node triggered evictions and its conditions
2. Clean up disk if DiskPressure: prune old images, rotate logs
3. Review resource limits on pods scheduled to this node
4. Consider adding more nodes to distribute load
