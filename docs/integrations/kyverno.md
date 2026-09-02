# Kyverno — policy enforcement

Kyverno enforces the rules *you* wrote: no `hostNetwork`, images from an approved
registry, every workload declares limits. Failed checks land in
**Security → Policy** as policy-violation findings.

KubeBolt reads **PolicyReports**, not Kyverno's own API. That is a deliberate
choice with a useful consequence: `wgpolicyk8s.io` is a shared standard, so
**Gatekeeper works through this same integration** with no extra configuration —
if it writes PolicyReports, KubeBolt reads them.

Nothing is pushed. KubeBolt lists the reports on its sweep, so there is no token,
no webhook and no endpoint to expose.

---

## Install

```bash
helm repo add kyverno https://kyverno.github.io/kyverno
helm install kyverno kyverno/kyverno -n kyverno --create-namespace
```

Kyverno on its own reports nothing: it needs policies. The community set is the
usual starting point:

```bash
helm install kyverno-policies kyverno/kyverno-policies -n kyverno \
  --set podSecurityStandard=baseline \
  --set validationFailureAction=Audit
```

> **Start with `Audit`, not `Enforce`.** In `Audit` mode Kyverno writes the
> report and lets the resource through; in `Enforce` it rejects the admission.
> Turning on a policy set you have never seen the output of, in `Enforce`, is how
> a deploy starts failing on a Friday. KubeBolt reads both modes identically —
> the findings are the same, so there is nothing to gain by rushing.

KubeBolt detects the install by the `app.kubernetes.io/part-of=kyverno` label,
falling back to pods named `kyverno*` in the `kyverno` or `kyverno-system`
namespaces — a hand-rolled manifest without the standard labels is still found.

---

## Verify it works

```bash
# reports exist (one per resource on Kyverno ≥1.9)
kubectl get policyreports -A
kubectl get clusterpolicyreports

# a failing result in one of them
kubectl get policyreports -A -o json |
  jq '.items[].results[] | select(.result=="fail") | {policy, rule, message}' | head
```

Those failures show up in **Security → Policy** after the next sweep.

| Symptom | Cause |
|---|---|
| Card says "not installed" | no pods matching the label or the fallback namespaces |
| Card "degraded" | Kyverno pods present but not Ready |
| Reports exist, KubeBolt shows nothing | every result is `pass`/`warn`/`skip` — only `fail` becomes a finding |
| No reports at all | Kyverno is installed but has no policies |

---

## What becomes a finding, and what does not

**Only `result: fail`.** `pass` and `skip` are not problems; `warn` is audit noise
by design; and `error` means the policy engine itself hiccupped — reporting that
as a resource violation would blame the workload for the engine's bad day.

**Severity defaults to medium**, not dropped. This is the opposite of the CVE
rule, where an unknown severity is discarded. The reason is that a policy is not
a fact of nature: **someone wrote it on purpose**, so a failure matters even when
Kyverno does not label how much. Set `policies.kyverno.io/severity` on the policy
to control it.

---

## Two traps worth knowing

Both come from Kyverno's shape, and both look like KubeBolt bugs when they bite.

### One workload, two rule names

Kyverno validates a workload **twice**: the Pod against your rule, and the
controller's template against an auto-generated twin named `autogen-<rule>`. Same
policy, same defect, two names.

KubeBolt strips the `autogen-` prefix so both collapse into a single row. Without
that, one problem would read as two findings on the same workload, forever
un-closable — fixing the Deployment clears one and leaves the other.

### The finding's title is the policy, not the message

Kyverno writes the JSON path into the message, so the same violation reads
differently depending on where it was caught:

```
Pod:        …rule host-namespaces failed at path /spec/hostNetwork/
DaemonSet:  …rule autogen-host-namespaces failed at path /spec/template/spec/hostNetwork/
```

The title is part of a finding's fingerprint. Carrying the message in it made one
problem into two findings, and any reword by Kyverno resolved the old finding and
minted a new one **with the age clock back at zero** — which quietly destroys the
"oldest unresolved" signal. So the title is `policy/rule` and nothing more; the
message keeps all its value as the remediation text, where it is read rather than
compared.

---

## Where the resource comes from

Worth knowing if a finding ever shows up without one.

The `wgpolicyk8s.io` standard puts the subject in `results[].resources`, and
that is where KubeBolt looks first — Gatekeeper and older Kyverno fill it in.

**Kyverno ≥1.9 leaves it empty.** It writes one report per resource and names the
subject in the report's top-level `scope`. KubeBolt falls back to `scope`, and
then to `ownerReferences`. Without that fallback every violation would arrive
with no resource attached: counted on the dashboard, invisible in the list, and
impossible to act on.
