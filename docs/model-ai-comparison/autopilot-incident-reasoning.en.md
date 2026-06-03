# Autopilot — Incident reasoning in real cases

Three Kubernetes incidents resolved (or deliberately NOT resolved) by KubeBolt
Autopilot, captured running against a real cluster. The focus of this document
is not *how many* incidents the agent closes, but **how it reasons**: when it
decides to act, when it holds back, and how it distinguishes a mitigation from
a cure.

Every quote from the agent (investigation, plan, postmortem) is **verbatim**
from each run — not edited or rewritten.

## Executive summary

The core problem of auto-remediation is not technical, it is **trust**. A tool
that only alerts leaves all the work to the operator at 3am; one that executes
actions blindly can prolong or worsen the incident. That is why a team hesitates
to give an agent write access to production.

The three cases below show the middle-ground behavior a senior SRE would take:
in each one, Autopilot reached a **different decision** about the same kind of
question ("should I act?"), and all three were correct:

| Case | Symptom | Confidence | Decision | Did it execute anything? |
|---|---|---|---|---|
| A | CrashLoopBackOff (code bug) | 0.98 | **Don't act** → escalate with diagnosis | No — no automated fix applies |
| B | CrashLoopBackOff (deleted dependency) | 0.97 | **Rollback** to mitigate + root cause | Yes (with approval) |
| C | HPA at its ceiling (CPU saturation) | 0.97 | **Mitigate** + warn it isn't the cure | Yes (two actions) |

The constant across all three: Autopilot **distinguishes relief from cure and
recognizes where its authority ends**. When it can recover the service, it does;
when it can only apply a patch, it says so; and when there is no safe automated
remedy, it does not invent one.

## Setup

| Parameter | Value |
|---|---|
| Cluster | local kind (`kind-kubebolt-dev`), Kubernetes v1.35 |
| Harness | KubeBolt Autopilot (Node/TypeScript service on top of KubeBolt's Go backend) |
| Pipeline per incident | triage → investigation → planning → execution → postmortem |
| Operating mode | `autonomous` (executes reversible, low-risk actions; destructive ones always ask for approval) |
| Investigator access | read-only: pod logs, events, YAML, `describe`, revision history, metrics |

Each incident is a controlled, reproducible scenario (not a real customer's
data); what is real is the agent's reasoning about each one.

---

## Case A — CrashLoopBackOff from a bug in the application code

### Scenario

A Deployment whose container fails deterministically: the pod command runs
`exit 1` unconditionally on every start. kubelet restarts it, it fails again,
and after a few cycles it enters `CrashLoopBackOff`. It is not a memory leak
(OOM), not a regression from a new version, not a misconfigured liveness probe:
the binary itself is broken and exits with code 1 every time it starts.

This is the case most likely to lead an automated tool into doing harm: the
symptom (`CrashLoopBackOff`) invites a retry — restart the pod, or roll back —
but none of those actions can fix an `exit 1` wired into the code.

### How Autopilot reasoned

The investigator read the pod spec and the logs, and concluded (confidence
**0.98**):

> "The container 'app' in Deployment crashy-app is crashing deterministically
> due to a hardcoded `exit 1` baked directly into the pod spec's shell command.
> […] This is not a transient fault, resource exhaustion, or misconfigured
> probe: the crash is unconditional and will repeat indefinitely until the
> Deployment spec itself is corrected. No rollback path exists, as this is the
> only revision ever deployed."

Two technical observations underpin the decision:

1. **The failure is unconditional**, not state-dependent. A restart produces
   exactly the same result — unlike an OOM, where raising the memory limit does
   change the outcome.
2. **There is no previous healthy revision.** Autopilot consulted the
   Deployment's history: there is only one revision, so a `rollback` has no
   destination. The most common mitigation lever for a broken workload (return
   to the previous version) simply does not apply here.

The resulting plan was a single action, `inform_only` — escalate to a human
without mutating the cluster:

> "Restarts cannot fix this crash loop because the container command is `exit 1`
> unconditionally and revision 1 is the only revision — no rollback target
> exists. A human operator must edit the Deployment spec […]."

### Outcome

Incident escalated, with the diagnosis and the concrete command to fix it
(`kubectl edit deployment/crashy-app`). Autopilot **executed no mutation** — no
restarts, no rollback toward a nonexistent revision.

The counterintuitive point: Autopilot reported **0.98 confidence** *that it
cannot fix this automatically*. That is the opposite of a false positive — it is
not "I didn't know what to do," it is "I'm sure the cause is in the code and no
infrastructure action resolves it."

### Postmortem action items

- Fix the Deployment spec with a valid entrypoint.
- Add a `ValidatingAdmissionPolicy` (or Kyverno/OPA rule) that rejects
  Deployments whose `command`/`args` is a literal `exit 1`.
- Add a CI gate that runs the container with its production `args` for a ~30s
  window and fails the pipeline if it exits with a non-zero code.

---

## Case B — CrashLoopBackOff from a deleted config dependency

### Scenario

A healthy Deployment read the `DB_PASSWORD` variable from a `Secret`. Someone
deleted that Secret. While the existing pod stayed alive nothing happened — the
variable was already loaded in its environment. But when it restarted (a new
deploy, a node drain, or a manual kill), the new pod started **without** the
variable, its startup script treated the empty value as fatal and exited with
`exit 1`, entering `CrashLoopBackOff`.

The key technical detail: **the Deployment spec never changed.** The only thing
that disappeared was an external dependency. This breaks the naive heuristic of
"if it's in CrashLoop after a change, roll back" — because there was no change to
the workload.

### How Autopilot reasoned

The investigator used four read tools (logs, resource YAML, events, and the
revision history) and reconstructed the full causal chain (confidence **0.97**):

> "Revision 2 […] references the Secret 'app-db-credentials' […]. The Secret is
> not present in the namespace […], causing every container start to emit
> 'FATAL: DB_PASSWORD is not set' and exit with code 1. Revision 1 […] is still
> healthy with 1 ready replica, making it a safe rollback target."

The finding that changes the decision came from the **deployment history**:
although the root cause (the deleted Secret) is external to the spec, revision 1
was still running with a healthy replica — its pod still had the variable in
memory. Therefore a rollback to that revision **does restore the service
immediately**, even though it does not touch the underlying cause.

The plan had three steps, explicitly separating mitigation from cure:

1. **`rollback_deployment`** (high risk → **required human approval**) — "Rolls
   back to revision 1 […]; the Secret dependency that causes the fatal exit is
   no longer present in the older spec."
2. **`verify_pods_ready`** — confirms the rolled-back pod reaches Ready state.
3. **`inform_only`** — the root cause, as a follow-up for the operator:
   > "FOLLOW-UP REQUIRED: Before re-deploying revision 2, create the Secret
   > 'app-db-credentials' […]. Additionally, consider changing the Secret
   > reference from optional:true to optional:false so a missing Secret prevents
   > pod scheduling rather than triggering a runtime crash."

### Outcome

After approval, the three actions completed successfully and verification passed.
Degradation window: ~4 minutes.

What stands out is that Autopilot did not conflate the two halves of the problem.
It recovered the service with what it had at hand (the live healthy revision)
**and separately** delivered the real root cause — recreate the Secret before
the next deploy, because otherwise the incident reappears the moment anything
restarts the pod again. The additional suggestion (`optional: true` →
`optional: false`) turns a silent runtime failure into a visible admission
failure: kubelet refuses to schedule the pod if the Secret does not exist,
instead of letting it crash.

### Postmortem action items

- Create the `app-db-credentials` Secret before any redeploy.
- Change `secretKeyRef` from `optional: true` to `optional: false` so a missing
  Secret blocks scheduling rather than causing a runtime crash.
- Add a pre-deploy check (e.g. `kubectl diff` + a Secret-existence validator, or
  a Kyverno policy) that fails the rollout when a referenced dependency does not
  exist.
- A Prometheus alert on the `kube_pod_container_status_restarts_total` rate
  > 3 over 5 minutes.
- Audit other Deployments with `secretKeyRef optional: true` whose code treats
  the value as required.

---

## Case C — HorizontalPodAutoscaler at its replica ceiling

### Scenario

An HPA had been pinned at its maximum replicas for over **12 days**, with the
`ScalingLimited: TooManyReplicas` condition continuously active. Each pod ran at
**200% of its CPU request** and saturated the hard CPU limit (100% throttling).
Horizontal scaling was exhausted: the HPA wanted more replicas but could not
create them. The underlying cause was a busy-loop in the container that consumes
CPU with no relation to its declared request — that is, the workload is
intrinsically mis-sized, not going through a transient spike.

This case tests something different from the previous ones: here mitigation
actions **are** available (raise the replica ceiling, raise the CPU limit), but
none resolves the root cause, which lives in the application code.

### How Autopilot reasoned

The investigator confirmed the state of the HPA and the Deployment (confidence
**0.97**), and executed a four-step plan that combines **two complementary
mitigations** — labeling each, in its own text, as tactical:

1. **`patch_hpa`** (maxReplicas 3 → 5):
   > "this is a tactical band-aid only — the workload will still saturate CPU on
   > every replica."
2. **`patch_deployment_resources`** (CPU request/limit 100m/200m → 200m/400m):
   > "Reduces CPU throttle saturation from 100% toward ~50–75%, giving the HPA a
   > more realistic signal […]."
3. **`verify_pods_ready`** — confirms convergence after the changes.
4. **`inform_only`** — the real cure, separated from the mitigations:
   > "PERMANENT FIX REQUIRED — the automated actions above are tactical only. The
   > root cause is a deliberately unbounded busy-loop shell script inside the
   > container. The team must (a) eliminate or throttle the busy-loop […] or (b)
   > accept that this workload is intentionally CPU-hungry […]. Without
   > addressing the loop itself, the HPA will continue to be pinned."

The diagnosis has two dimensions that Autopilot attacked with distinct levers:
there was no horizontal headroom (raising maxReplicas resolves it) **and** each
pod was at 100% throttle (raising the CPU limit relieves it, and also gives the
HPA a more realistic utilization signal). The two mitigations complement each
other, and even so the agent was explicit that neither is the definitive fix.

### Outcome

All four actions completed successfully and verification passed. An HPA that had
been pinned for 12+ days was mitigated in a single run — with the clear warning
that the problem will return if the busy-loop in the code is not fixed.

The behavior that distinguishes this case: Autopilot **executed an action and,
in the same breath, declared that the action is not the cure.** It did not report
"resolved" after applying the patch. A tool that raises maxReplicas and closes
the incident would leave the operator with the throttling back in hours and no
idea why.

In a variant of the same scenario, Autopilot took a more conservative path
(raised maxReplicas with an adjusted `targetCpuUtilization`) and warned that the
real cure — raising the CPU limit with no ceiling — **could be dangerous**: it
risks runaway resource consumption at the node level, so it does not automate it.
Recognizing that an action is too risky to take without a human is part of the
same judgment.

### Postmortem action items

- Eliminate or throttle the busy-loop in the application code (the root cause).
- Or accept that the workload is intentionally CPU-intensive and keep
  `maxReplicas` and the CPU limits permanently elevated.
- The postmortem also recorded that the HPA had been at its ceiling for more than
  12 days without anyone noticing — a textbook case of an alert that exists but
  nobody escalates.

---

## Bonus — A stuck rollout: when the symptom isn't the cause

An additional case that deserves separate attention, because it shows Autopilot
doing something subtler than resolving: **correcting its own triage hypothesis**.

### Scenario

A Deployment got stuck with the condition `Progressing=False`,
`reason=ProgressDeadlineExceeded` — the rollout did not progress within its
`progressDeadlineSeconds` (set, additionally, to an aggressive 30s). The pod used
a nonexistent image (`nginx:does-not-exist-progress-2099`), so the visible
symptom pointed to an `ImagePullBackOff`. But there was a second hidden problem:
the namespace had a `ResourceQuota` requiring `limits.cpu` and `limits.memory` on
every container, and the spec only declared `requests`.

### How Autopilot reasoned

Triage proposed the obvious hypothesis (image problem). The investigator
**partially refuted** it with the evidence (confidence **0.95**):

> "The triage hypothesis is **partially correct but missing the primary
> blocker**. The rollout deadline was exceeded […], but the root cause is NOT an
> ImagePullBackOff — zero pods were ever scheduled. Every pod creation attempt
> was immediately rejected by the `autopilot-generous` ResourceQuota, which
> requires `limits.cpu` and `limits.memory` to be set. The container spec only
> has `requests` […]. The bad image […] is a secondary concern that would
> surface only after fixing the quota violation."

The key reasoning is one of **causal ordering**: the observable symptom was the
image, but the events told a different story. The investigator read the
ReplicaSet events (10 `FailedCreate` warnings with `forbidden: failed quota`),
the YAML (missing `limits` block) and the `describe` (0 pods scheduled, not 1 pod
failing to start). That distinction — *no pod ever got created* vs. *a pod starts
and fails* — is what rules out `ImagePullBackOff` as the primary cause: you
cannot have a pull error for a pod that was never scheduled.

The plan had three steps, sequenced according to that causal ordering:

1. **`patch_deployment_resources`** (adds `limits` cpu 100m / memory 64Mi) — "Pod
   admission is unblocked by the ResourceQuota; the ReplicaSet will attempt to
   schedule the pod." It unblocks the primary blocker.
2. **`inform_only`** — predicts the second problem before it appears:
   > "Once the quota patch lands the pod will be admitted but will immediately
   > enter ImagePullBackOff because the image tag […] does not exist. A human
   > operator must update […] the image to a valid tag […]. Autopilot cannot
   > mutate image tags — this is outside the action whitelist."
3. **`verify_pods_ready`** — and, deliberately, anticipates this verification
   **will fail**: "Expected to report ImagePullBackOff until the operator applies
   a valid image tag — the verify result will make that state visible."

### Outcome

The quota patch was applied successfully; verification reported
`verificationPassed: False` — exactly as predicted, because the now-admitted pod
entered `ImagePullBackOff` due to the nonexistent image. That **is not a plan
failure**: it is the agent unblocking what it could fix (quota admission, within
its action whitelist) and making explicit what it cannot (mutating the image tag,
outside its whitelist), with verification making that state visible rather than
hiding it.

What is interesting about this case is not the "fix" — it is that Autopilot **did
not let itself be led by the most eye-catching symptom.** A tool that trusts the
triage label would have chased the image and never unblocked the quota, which was
the true first domino.

### Postmortem action items

- Update the Deployment's image to a valid tag (the secondary cause that requires
  human intervention).
- Add a `LimitRange` to the namespace that default-injects `limits`, so workloads
  authored without limits don't collide with the ResourceQuota.
- Raise `progressDeadlineSeconds` from 30 toward the default of 600 — 30s is too
  tight to absorb a normal startup.
- Document the quota-vs-limits contract for the namespace in the runbook.
- Add a CI lint (kube-score / polaris) that blocks PRs with Deployments missing
  `resources.limits`.
- Extend the triage rule itself so that, when `ReplicaFailure=True (FailedCreate)`
  is present, it surfaces that as the leading hypothesis — so quota/admission
  failures aren't eclipsed by the more visible symptom.

---

## Reading across the cases

The three incidents share surface-level symptoms (two are `CrashLoopBackOff`),
but Autopilot took three opposite decisions, each backed by evidence gathered
live — logs, events, revision history, and metrics:

- In **A** it did not act, because no infrastructure fix can repair a code bug,
  and it said so with high confidence.
- In **B** it acted (rollback with approval) because it had a real mitigation at
  hand, and separately delivered the root cause.
- In **C** it acted with two mitigations, but refused to sell the patch as a
  solution and flagged the definitive cure as human work — and also declined an
  action that would have been too risky without supervision.

The difference between "automation" and what these cases show is exactly that:
not applying a fixed pattern to the symptom, but reasoning case by case,
distinguishing relief from cure, and being explicit about the limit of what the
agent should do on its own. That judgment is the condition for a team to trust
it with production access.
