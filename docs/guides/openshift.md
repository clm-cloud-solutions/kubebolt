# KubeBolt on OpenShift

> **Status — 2026-08-16.** TWO independent problems, both reproduced on
> **1.23.1 / OpenShift 4.20 / Kubernetes 1.33.6**:
>
> 1. The **`web` image** does not start under the default `restricted-v2` SCC.
>    `api`, `victoriametrics` and the agent are fine. Two workarounds below.
> 2. The **in-cluster context times out** on clusters with a large Events
>    collection, and reports it as a stuck agent that does not exist.
>
> Both have verified workarounds; the permanent fixes are tracked in
> [Pending fixes](#pending-fixes).

## The symptom

A standard install leaves `kubebolt-web` in `CrashLoopBackOff` while everything
else runs:

```
NAME                            READY   STATUS             RESTARTS
kubebolt-api-...                1/1     Running            0
kubebolt-victoriametrics-0      1/1     Running            0
kubebolt-web-...                0/1     CrashLoopBackOff   7
```

```
$ oc logs deployment/kubebolt-web
/docker-entrypoint.sh: Launching /docker-entrypoint.d/40-api-backend.sh
sed: can't create temp file '/etc/nginx/conf.d/default.confXXXXXX': Permission denied
```

## Why it happens

`restricted-v2` assigns each pod a **random UID** from the namespace's range
(`openshift.io/sa.scc.uid-range`) and always puts it in **GID 0**. The `web` image
is stock `nginx:1.27-alpine`, where every path nginx writes to is `root:root 0755`
— readable by the group, but not writable.

Four paths are denied under an arbitrary UID:

| Path | Written by |
|------|-----------|
| `/etc/nginx/conf.d` | the entrypoint's `sed -i`, which substitutes `API_BACKEND` |
| `/var/cache/nginx` | nginx master, at startup (`client_temp` and friends) |
| `/var/log/nginx` | nginx master, before it drops privileges |
| `/run` | the PID file |

The `sed` is only the first one to be reached. Fixing it alone is not enough —
nginx then dies one step later:

```
[emerg] mkdir() "/var/cache/nginx/client_temp" failed (13: Permission denied)
```

### What is *not* the cause

Two plausible-sounding theories that the evidence rules out:

- **"VictoriaMetrics can't write to its volume."** It can. PersistentVolumeClaims
  are unaffected: `restricted-v2` supplies an `fsGroup` from the namespace range
  and the kubelet chowns the volume to it. Both the VictoriaMetrics PVC and the
  API's `/data` PVC work untouched — which is why those two pods are `Running`.
  The `web` failure is not about volumes at all; it is about paths baked into the
  image.

- **"The chart doesn't define a `securityContext`."** True, but adding one cannot
  fix this, and adding `runAsUser` actively breaks it: `restricted-v2` uses the
  `MustRunAsRange` strategy, so a pod that requests a UID outside the namespace's
  allocated range is **rejected at admission**. Under `restricted-v2` the field
  must be left empty. The fix belongs in the image, not the chart.

## Workaround A — grant the `anyuid` SCC

Fastest path, needs `cluster-admin`. The pod runs as root, exactly as it does on
vanilla Kubernetes.

```bash
oc adm policy add-scc-to-user anyuid -z default -n kubebolt
oc rollout restart deployment/kubebolt-web -n kubebolt
```

> `-z default` is correct today: the `web` Deployment does not set
> `serviceAccountName`, so it runs under the namespace's `default` SA while the
> API runs under `kubebolt`. That inconsistency is itself on the fix list.

This weakens the namespace's security posture. Prefer workaround B if your
platform team will not grant `anyuid`.

## Workaround B — no SCC change

Keeps `restricted-v2`. An initContainer renders the nginx config into an
`emptyDir`, the main container bypasses the entrypoint scripts, and the three
remaining write paths become `emptyDir` mounts (writable because the SCC's
`fsGroup` applies to them).

```bash
oc patch deployment/kubebolt-web -n kubebolt --type=strategic -p '
spec:
  template:
    spec:
      initContainers:
        - name: render-config
          image: ghcr.io/clm-cloud-solutions/kubebolt/web:1.23.1
          command: ["/bin/sh", "-c"]
          args:
            - sed "s|api:8080|${API_BACKEND}|g" /etc/nginx/conf.d/default.conf > /mnt/conf/default.conf
          env:
            - name: API_BACKEND
              value: kubebolt-api:8080
          volumeMounts:
            - name: nginx-conf
              mountPath: /mnt/conf
      containers:
        - name: web
          command: ["nginx", "-g", "daemon off;"]
          volumeMounts:
            - name: nginx-conf
              mountPath: /etc/nginx/conf.d
            - name: nginx-cache
              mountPath: /var/cache/nginx
            - name: nginx-log
              mountPath: /var/log/nginx
            - name: nginx-run
              mountPath: /run
      volumes:
        - name: nginx-conf
          emptyDir: {}
        - name: nginx-cache
          emptyDir: {}
        - name: nginx-log
          emptyDir: {}
        - name: nginx-run
          emptyDir: {}
'
```

Pin the initContainer image to the same tag you installed, and set `API_BACKEND`
to `<release-name>-api:8080`.

> **A `helm upgrade` reverts this patch.** Re-apply it after every upgrade, or
> keep it in a post-render kustomization.

## The agent

The agent chart has the opposite problem: it *does* set a security context, but
pins a UID that `restricted-v2` will reject.

```yaml
# deploy/helm/kubebolt-agent/values.yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532      # rejected: outside the namespace's UID range
```

Drop the UID at install time and let OpenShift assign one. `runAsNonRoot: true`
still holds, because the assigned UID is never 0:

```bash
helm upgrade --install kubebolt-agent \
  oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
  --namespace kubebolt \
  --set podSecurityContext.runAsUser=null \
  ...
```

> `--set-json 'podSecurityContext={}'` does **not** work — Helm merges maps, so an
> empty map leaves the pinned UID in place. Only `=null` removes the key.

Nothing else in the agent needs elevated privileges: the DaemonSet declares no
`hostPath`, `hostNetwork` or `hostPID`, and `readOnlyRootFilesystem: true` is
compatible with `restricted-v2` as-is.

## A second, unrelated problem: the in-cluster context times out

> **Reported 2026-08-16** by the same operator, on a fresh **1.23.1** install on
> **OpenShift 4.20 / Kubernetes 1.33.6**. Independent of the SCC issue above:
> that one is the `web` image, this one is the API's own connection to the
> cluster it runs in. The report also **reproduced the `web` failure verbatim**
> on 1.23.1 — same `sed: can't create temp file` — which confirms it is the image
> and not one cluster's configuration.

The API detects the in-cluster config correctly, reaches the API server, and
finishes the permission probe at **26/31 resource types accessible**. Roughly 25
seconds later it gives up:

```
connect timed out after 25s for context in-cluster — agent may be stuck
```

The UI shows **Cluster unreachable** even though nothing is unreachable.

### The isolation that found it

The reporter did the experiment that matters: they removed **only** `events` from
the `get/list/watch` rule of the `kubebolt` ClusterRole, leaving Pods and Nodes
authorised, and restarted the backend.

| | with `events` | without `events` |
|---|---|---|
| permission probe | 26/31 | 25/31 |
| informer caches | never sync | synced **~1.8s** after the probe |
| UI | Cluster unreachable | **Active** — 5/5 Nodes, 67/67 Deployments, 77 Namespaces |

Their measurements on the same cluster:

```
oc get events -A                 ~32.4s
GET /api/v1/events               HTTP 200 in ~6.35s, ~144,641,645 bytes (~138 MiB)
Nodes, Pods, Deployments, …      < 1s each
```

Everything else — DNS to `kubernetes.default.svc`, authenticated calls to
`/version`, `/api/v1/nodes`, `/api/v1/pods` with the same ServiceAccount —
worked. It is not connectivity, not authentication, not RBAC in general.

### Why it happens

Three things compound, and only the first was visible from outside:

**1. Events are enormous on a busy cluster.** 138 MiB has to be transferred,
deserialised and inserted into a cache before that informer reports synced.

**2. One slow informer blocks every other one.** `Connector.Start()` calls
`factory.WaitForCacheSync(...)` for the WHOLE shared factory, so it returns only
when the last informer is ready. Nodes, Pods and Deployments were ready in 1.8s
and still could not be served, because Events had not finished.

**3. The outer deadline is SHORTER than the inner one.** The connector budgets
`defaultCacheSyncTimeout = 45s` for the sync, but `connectToContext` races
`Start()` against `DefaultConnectTimeout = 25s` and calls `connector.Stop()` when
that fires first. A sync that is progressing normally and would have finished at,
say, 30s is killed at 25 — the inner budget can never be spent.

### Why the message sends you the wrong way

*"agent may be stuck"* names an agent, and on the in-cluster path **there is no
agent**. That outer deadline was written for agent-proxy clusters — its own
comment says so, citing an ~8-minute hang caused by a wedged agent — and the
value comes from `IngestChannelConfig.ConnectTimeoutSeconds`, an **agent-ingest**
setting. It governs the direct in-cluster connect anyway.

This is the same shape as the in-cluster cluster having no ownership row: logic
written for the agent-proxy topology, applied to the one cluster that does not
use it. The error message is the tell — it describes a component that is not in
the picture, which is exactly why the reporter went hunting for connectivity and
RBAC problems first.

### Workarounds

**A — drop `events` from the ClusterRole.** What the reporter did. The cluster
comes up immediately; the cost is that the Events tab and any insight that reads
Events go empty, and the permission probe honestly reports 25/31.

**B — raise the connect timeout.** `ConnectTimeoutSeconds` in the agent ingest
channel settings. It works, but it is the wrong knob twice over: it is an
agent-proxy resilience setting, and loosening it weakens the protection it exists
for on clusters that *do* use agents.

Neither is a fix. See [Pending fixes](#pending-fixes).

## Verifying

Reproduce the failure — and confirm a fix — without an OpenShift cluster, by
running the image the way `restricted-v2` would:

```bash
docker run --rm --user 1000700000:0 --entrypoint /bin/sh \
  ghcr.io/clm-cloud-solutions/kubebolt/web:1.23.1 -c '
    sed -i "s|api:8080|x:1|g" /etc/nginx/conf.d/default.conf
    ls -ld /etc/nginx/conf.d /var/cache/nginx /var/log/nginx /run
  '
```

Any UID works as long as it is not in `/etc/passwd` and the GID is `0` — that is
the whole of what OpenShift does differently.

## Pending fixes

1. **Make the `web` image tolerate an arbitrary UID.** Red Hat's "support
   arbitrary user IDs" rule: the UID is unpredictable but the GID is always 0, so
   everything writable must belong to group root and be group-writable.

   ```dockerfile
   RUN chgrp -R 0 /var/cache/nginx /var/log/nginx /etc/nginx/conf.d /run \
    && chmod -R g=u /var/cache/nginx /var/log/nginx /etc/nginx/conf.d /run
   ```

   Verified: the patched image runs as UID `1000700000:0`, serves `HTTP 200`,
   substitutes `API_BACKEND`, and still runs as root with no regression — so the
   same image keeps working on vanilla Kubernetes and Docker Compose.

2. **Make the agent's `runAsUser` omittable** rather than needing `=null` at the
   call site, so the chart is installable on OpenShift out of the box.

3. **Give the `web` Deployment a `serviceAccountName`.** It silently uses
   `default` today while the API uses the chart's SA — an inconsistency that makes
   SCC grants land on the wrong subject.

4. **Expose optional `podSecurityContext` / `securityContext` on the api and web
   Deployments**, defaulting to empty. Never ship a hardcoded `runAsUser`: on
   OpenShift it is the one value that guarantees rejection.

5. **Stop one informer from blocking the rest.** `Connector.Start()` waits on the
   whole factory, so a 138 MiB Events collection strands Nodes and Pods that were
   ready in under two seconds. Options, roughly in order of cost: bound the Events
   informer (a `limit`, a field selector, or a shorter window); sync it
   asynchronously and let the cluster come up without it; or wait per-informer so
   a slow one degrades its own tab instead of the whole context.

6. **Make the outer connect deadline at least the inner sync budget.**
   `DefaultConnectTimeout` is 25s while `defaultCacheSyncTimeout` is 45s, so the
   connector is killed before it can spend the budget it was given. Whatever the
   values become, the outer one must not be the smaller.

7. **Do not apply the agent-proxy connect deadline to the in-cluster path**, and
   stop saying "agent may be stuck" where there is no agent. The deadline exists
   for wedged agent-proxy calls; on a direct connection it turns a slow-but-
   healthy start into a false "Cluster unreachable", and the message actively
   misdirects whoever debugs it. Same class as the in-cluster cluster having no
   ownership row: agent-proxy logic applied to the one topology that has no agent.

## References

- [Managing security context constraints](https://docs.redhat.com/en/documentation/openshift_container_platform/4.22/html/authentication_and_authorization/managing-pod-security-policies) — the `MustRunAsRange` strategy and the namespace UID range
- [Pod Security Standards changes in OpenShift 4.11+](https://connect.redhat.com/en/blog/important-openshift-changes-pod-security-standards)
