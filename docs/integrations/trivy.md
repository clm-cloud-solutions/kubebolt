# Trivy Operator — vulnerabilities, misconfiguration and CIS

Trivy Operator scans what is actually running: the CVEs in your images, workload
misconfiguration, exposed secrets, RBAC posture and the CIS benchmark. Its
findings land across **Security → CVEs**, **Misconfig**, **RBAC** and
**Compliance**.

Like Kyverno and unlike Falco, nothing is pushed: Trivy writes CRDs and KubeBolt
lists them on its sweep. No token, no webhook, no exposed endpoint.

---

## Install

```bash
helm repo add aqua https://aquasecurity.github.io/helm-charts/
helm install trivy-operator aqua/trivy-operator \
  -n trivy-system --create-namespace \
  --set="trivy.ignoreUnfixed=true"
```

`ignoreUnfixed=true` is a recommendation, not a requirement: a CVE with no
published fix is real, but it is not a task — there is nothing to upgrade to. Turn
it off when you need the full inventory for an audit, and expect the list to grow
a lot.

The CIS benchmark ships as a separate report and is generated on its own
schedule; nothing extra to install.

KubeBolt detects the operator by the `app.kubernetes.io/name=trivy-operator`
label, falling back to pods named `trivy-operator*` in `trivy-system`,
`trivy-operator` or `security`.

---

## Verify it works

```bash
# reports appear a few minutes after install, one per workload
kubectl get vulnerabilityreports -A
kubectl get configauditreports -A
kubectl get clustercompliancereports
```

| Symptom | Cause |
|---|---|
| Card says "not installed" | no pods matching the label or the fallback namespaces |
| Card "degraded" | operator pods present but not Ready |
| Reports exist, no CVEs in KubeBolt | they are all MEDIUM/LOW — see below |
| No reports after 10 minutes | the operator cannot pull images (private registry without credentials) |
| CIS empty, everything else fine | the compliance report runs on its own schedule; give it a cycle |

---

## What becomes a finding, and what does not

**CVEs: only CRITICAL and HIGH.** MEDIUM and LOW are dropped on purpose. On a
busy cluster they are thousands of rows, and a list nobody can finish is a list
nobody reads — the dashboard's job is the actionable set, not the inventory. The
full detail always remains in the reports themselves.

This is the opposite of the Kyverno rule, where an unlabelled severity still
becomes a finding. The difference is who decided: a policy was written by someone
on purpose, while a CVE's severity is assigned upstream and grades a risk that
may not apply here.

**Compliance: only failing controls.** A passing control produces nothing.

Each CVE carries its remediation ready to act on — `upgrade <pkg> <installed> →
<fixed>` — and the **full image reference**, rebuilt from the three fields Trivy
splits it across. That last part matters more than it looks: two workloads
sharing an image are **one fix**, and without the reference nothing in the row
says so.

---

## The CIS trap: a control that is a rollup

Some CIS controls are not independent checks — they are **sums of checks KubeBolt
already stores one by one**. Any control whose checks are `AVD-KSV-*` is
evaluated from the same config-audit reports that already produced their own
findings.

Counting both would double the same problem: once as N workload misconfigurations
and again as the control that groups them. Those controls are flagged as
**rollups**, so the dashboard can show them as posture without adding them to the
actionable total.

This was verified against a live cluster: for all 35 controls where both sides had
data, the control's own "N failing" equalled the summed failing results of exactly
those checks. The duplication is arithmetic, not approximate.

---

## Cost of the sweep

KubeBolt lists six report types per cluster, each a cluster-wide LIST that
crosses the agent tunnel. It is affordable because the cost tracks **payload, not
object count**: measured on a dev cluster, 57 vulnerability reports took 110 ms
while 120 config-audit reports took 29 ms — a vulnerability report carries
hundreds of CVEs, an assessment report a handful of checks.

Worth knowing if you run Trivy on a very large cluster and the sweep starts
showing up in latency: the lever is Trivy's scan scope, not KubeBolt's read.
