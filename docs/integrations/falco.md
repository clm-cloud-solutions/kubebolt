# Falco — runtime security

Falco watches every node's syscalls and speaks up when something steps outside
the expected: a shell inside a container, a sensitive file read, a binary dropped
and executed. Unlike Trivy and Kyverno, **KubeBolt does not go looking for this
data: Falco pushes it.** It is the only source that works that way, and that
shapes how it is configured.

Events show up under **Security → Runtime**.

---

## The three things it needs

| Value | Where it comes from | What happens without it |
|---|---|---|
| **Ingest URL** | `https://<your-kubebolt>/api/v1/ingest/falco` | falcosidekick logs a connection error and drops the event |
| **Ingest token** | Administration → API Tokens, **cluster-scoped** | `401` — nothing is accepted without one |
| **`cluster_id`** | the `kube-system` namespace UID | optional; if set and it disagrees with the token, `403` |

The **tenant is never sent**: it comes from the token. A sender cannot write into
another organization's feed even if it tries.

### The token MUST be cluster-scoped

Falco has no handshake. The KubeBolt agent announces its cluster when it
connects, but **a pushed event carries only the token**, so if the token does not
name a cluster the event cannot be attributed to one. The API rejects it rather
than accepting it half-attributed:

```
403  this ingest token is not scoped to a cluster; issue a cluster-scoped token
     for Falco so its events can be attributed
```

Issue the token **for this specific cluster** — do not reuse the agent's.

### Getting the `cluster_id`

```bash
kubectl get ns kube-system -o jsonpath='{.metadata.uid}'
```

That UID is the cluster identity KubeBolt uses everywhere.

---

## Install

```bash
CLUSTER_ID=$(kubectl get ns kube-system -o jsonpath='{.metadata.uid}')
TOKEN=<your cluster-scoped ingest token>

helm repo add falcosecurity https://falcosecurity.github.io/charts
helm install falco falcosecurity/falco -n falco --create-namespace \
  --set driver.kind=modern_ebpf \
  --set falcosidekick.enabled=true \
  --set-string falcosidekick.config.webhook.address=https://<your-kubebolt>/api/v1/ingest/falco \
  --set-string falcosidekick.config.webhook.customHeaders="Authorization:Bearer $TOKEN" \
  --set-string falcosidekick.config.customfields="cluster_id:$CLUSTER_ID"
```

> ⚠️ **`customHeaders`, with a capital H.** The chart's `values.yaml` documents it
> as `customheaders` (all lowercase) but **the template reads `customHeaders`**.
> Passing the documented spelling leaves the header empty **with no warning at
> all**, the POST goes out without a token, and the API answers `401` — and the
> obvious diagnosis, the wrong one, is that the token is bad. Check it with:
>
> ```bash
> kubectl get secret -n falco falco-falcosidekick \
>   -o jsonpath='{.data.WEBHOOK_CUSTOMHEADERS}' | base64 -d
> ```

### The driver

`modern_ebpf` (CO-RE) needs kernel ≥5.8 with BTF:

```bash
uname -r                        # ≥ 5.8
ls /sys/kernel/btf/vmlinux      # must exist
```

If it is not available, the chart also offers `ebpf` and `kmod`.

### One sensor per kernel

Falco is a DaemonSet: one pod per node, each seeing **its own** kernel. Correct on
any real cluster.

**On kind or k3d it is not**: the "nodes" are containers sharing a single Docker
kernel, so **every Falco sees the whole machine's syscalls** and the same event
arrives twice — one copy with the Kubernetes identity resolved, another without
it, from the node that does not host the pod. For a lab, pinning it to one node
makes it behave like production:

```bash
helm upgrade falco falcosecurity/falco -n falco --reuse-values \
  --set-string nodeSelector."kubernetes\.io/hostname"=<your-node>
```

No detection is lost: the remaining sensor already sees those syscalls.

---

## Verify it works

```bash
# 1. trip a rule on purpose
kubectl exec -n <ns> deploy/<something> -- cat /etc/shadow

# 2. delivery must report 202
kubectl logs -n falco -l app.kubernetes.io/name=falcosidekick --tail=5
#   → Webhook - POST OK (202)
```

The event lands in **Security → Runtime** in under a minute.

| Symptom | Cause |
|---|---|
| `401 missing Bearer token` | lowercase `customheaders` (see above) |
| `403 not scoped to a cluster` | the token was issued without a cluster |
| `403 cluster_id does not match` | the token belongs to a different cluster than `customfields` |
| `connection refused` | the URL is not reachable from the cluster |
| Nothing in Falco's log | the driver never loaded — check kernel and BTF |

---

## What is stored, and what is not

An event stores the rule, the priority, the pod or node, the process and its
command line, the user, and the rule's tags — including the **MITRE ATT&CK
technique**, which says what was being attempted and not merely which syscall
fired.

**No content is stored**: no file contents, no request bodies. The event says
*what was touched*, never *what was inside*.

Events are a **point-in-time stream**: they age out of the feed instead of being
resolved. There is no state to close, which is why the detail view offers no
"resolved" button — it would lie about what the system can do.

## Noise

Falco's default profile is conservative and fairly talkative. Before silencing a
rule, look at the tab's **Top 5 rules by hits** panel: a single rule accounting
for most of the feed is usually a noisy rule, not an incident. Tuning happens in
Falco (`customRules`), not in KubeBolt — the source is authoritative.
