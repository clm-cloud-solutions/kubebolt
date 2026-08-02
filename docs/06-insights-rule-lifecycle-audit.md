# Insights rule lifecycle — an insight must clear on evidence, not on a clock (Finding #6)

> **Status: ✅ RESOLVED (2026-08-02).** Supersedes the 2026-07-01 pass, which was marked
> resolved but **missed one rule and mis-fixed three others**. Read "What the first pass got
> wrong" before trusting any classification in here.

## The principle

An insight must disappear the way it appears: when the condition that produced it is
observed to be gone. Two things get conflated under the word "window":

| | what it asserts | how you get it |
|---|---|---|
| **Staleness bound** | "I stop believing an old fact" | a clock |
| **Recovery confirmation** | "I observed the system become healthy" | evidence |

A clock delivers neither honestly. While it runs, the insight asserts something false —
the operator sees a healthy workload flagged as broken. When it fires, the insight asserts
a recovery nobody observed. And because Autopilot's `reconcileClearedIncidents` treats the
live insight list as its source of truth, a stale insight holds that safety valve shut: the
incident cannot auto-close, and it re-triggers on every cooldown.

**The rule of the house:**

> Every rule is a predicate over CURRENT state. Historical artifacts — Events,
> `lastTerminationState`, `RestartCount` — may ARM a rule or decorate its message, but may
> never be the only thing keeping it alive.

Timers are permitted in exactly two places:

1. **Arm confirmation** — how long a level must hold before firing (`helmReleaseHookPendingRule`'s
   5 min, `readinessProbeFailingRule`'s 2 min grace). Dampens blips.
2. **Lost-signal fallback** — only where no level can exist, and only derived from the
   signal's own cadence (`probeSilenceWindow`). Never a picked constant.

## The invariant (enforced by a test, not by prose)

> For every rule there must exist a reachable HEALTHY state — **with the involved object
> still present** — in which the rule emits nothing.

A rule whose only exit is deletion or garbage collection is latched to an edge.
`internal/insights/invariant_test.go` holds a sick/healthy fixture pair per rule and asserts
both directions; `TestInvariant_EveryRuleIsCovered` fails if a new rule ships without one.

## What the first pass got wrong

The 2026-07-01 audit classified all 24 rules **in prose** and concluded *"No further
offenders."* Two errors:

1. It filed `livenessProbeFailingRule` under **"Likely OK — current-state based"**. The rule
   reads `state.Events`. Events are historical.
2. It reasoned that *"a time-bounded event (liveness, ~1h TTL)"* resolves itself. A TTL is an
   upper bound on retention, not a clear condition — for that hour the insight is simply wrong.

**Proven in vivo, 2026-08-02, `kind-kubebolt-lab`:**

| moment | insight | pod |
|---|---|---|
| `23:25:50Z` | `liveness-probe-failing` **active** | `1/1 Running Ready` since `22:54:56Z` |
| `00:19:23Z` | **gone** | *unchanged* |

Nothing about the workload changed. The Event crossed `--event-ttl` and was garbage-
collected. **The insight's lifetime was governed by Kubernetes' retention policy, not by the
workload's health.** Autopilot produced several incidents from that one stale insight, each
concluding `already_resolved` and each burning credits.

The other three rules were "fixed" with a 30-minute clock, which is the same category error
with a shorter fuse: an OOM insight kept asserting a broken workload for half an hour after
it recovered, and — the inverse — declared healthy any container whose last termination was
merely old, however unhealthy it was right now.

## Audit result — 24 rules, read individually

**Sound (20).** Level-based, clear the moment the condition does:
`crash-loop`, `cpu-throttle-risk`, `memory-pressure`, `resource-underrequest`,
`zero-replicas`, `progress-deadline-exceeded`, `pvc-pending`, `node-not-ready`,
`hpa-maxed-out`, `image-pull-backoff`, `missing-config-dependency`,
`readiness-probe-failing`, `service-no-endpoints`, `policy-no-match`, `policy-orphan`,
`pdb-no-match`, `helm-release-failed`, `helm-release-hook-pending`, `cert-expiring`,
`argocd-out-of-sync`.

**Defective (4), now fixed:**

| rule | defect | fix |
|---|---|---|
| `liveness-probe-failing` | edge-latched; only the Event's GC cleared it (~1 h) | supersession + probe-cadence silence fallback |
| `oom-killed` | 30-min clock | supersession |
| `frequent-restarts` | 30-min clock; `RestartCount` alone can never fall | supersession |
| `evicted-pods` | 30-min clock hiding a condition that is still true | kept (it IS current state) but **declared in the title** |

The instructive contrast — same monotonic input, opposite safety:

```go
// crash-loop — SOUND
cs.State.Waiting.Reason == "CrashLoopBackOff" && cs.RestartCount > 3
//  ↑ level (can go false)                       ↑ monotonic counter

// frequent-restarts (before) — DEFECTIVE
cs.RestartCount > 5 && terminatedRecently(...)
//  ↑ monotonic            ↑ clock — nothing here can ever go false
```

The defect was never the input. It was the absence of a level.

`missingConfigDependencyRule` already stated the principle in its own comment — *"we read
the container waiting state (current truth) rather than Events (historical) so the insight
clears as soon as the dependency exists"* — it just was never promoted to a house rule, so
each author applied it or not.

## The fix, in code

- **`containerRecovered(cs, since)`** — the replacement run started at/after the failure, is
  Ready, and has held that for `readyGrace` (2 min). Anchored on `State.Running.StartedAt`,
  which every kubelet kill resets, so a live loop can never accumulate the grace. No false
  negatives.
- **`eventLastOccurrence(ev)`** — `Series.LastObservedTime` → `LastTimestamp` → `EventTime`
  → `FirstTimestamp`. Distributions differ on which they populate; reading the wrong one
  yields a zero time and would make every event look ancient.
- **`probeSilenceWindow(probe)`** — `periodSeconds × failureThreshold × 3`, floored at 1 min.
  The lost-signal fallback for the one case with no level: the probe recovered *in place*
  (never reached `failureThreshold`, so no restart, so nothing to supersede).
- **`livenessFailureCleared`** — evidence first, silence only where evidence cannot exist.

## Related fix in the same pass — the record that wouldn't die

Auditing the chain surfaced a second, silent failure independent of the rules.

`Evaluate`'s resolve loop only walks **in-memory** `e.insights`, and that slice started
**empty on every boot**. Nothing hydrated it from the store. `MarkResolved` has exactly one
caller (`persistResolved`, off that loop) and `Prune` refuses to touch active records
("Active records never get pruned"). So a record left `active` when the API restarted — i.e.
on any deploy — whose condition cleared while it was down, was **immortal**.

That is not just stale history. `admitNew` treats a still-`active` record as a
**continuation** (`freshEpisode=false`), so when the same condition genuinely recurred weeks
later, **its notification was silently suppressed** and the card inherited the ancient
`FirstSeen`. The mirror image of the sticky-insight bug: there an insight that won't die,
here a record that won't die — and this one fails quietly.

Fixed by `hydrateLocked` (one-shot on the first evaluation, so it survives the async-connect
race that `SetStore` documents). Hydrated insights get **one grace evaluation** before the
resolve loop may close them, so a half-warm informer cache can't close the whole set at once.

## Also fixed — `insight:resolved` had no listener

`engine.go` has always broadcast `insight:new` / `insight:resolved`. Grep across `apps/web`
found **zero handlers**: `useWebSocket.ts` invalidated clusters, overview, topology and
resources, never `['insights']`. The Insights view relied purely on `refetchInterval`, so a
cleared insight stayed on screen for up to a full refresh cycle. Wired end-to-end on the
backend, dropped on the floor on the client. Now invalidates `['insights']` +
`['cluster-overview']` (which carries `InsightCount`, so the badge moves in step).

## Latency budget, with everything correct

| link | latency |
|---|---|
| engine evaluation (`--insight-interval`) | ≤ 60 s |
| UI (WS, since this pass) | immediate |
| Autopilot poll (`INSIGHT_POLL_INTERVAL_MS`) | 10 s |
| `STALE_GRACE_MS` before an incident auto-closes | 5 min |

So an incident can outlive its insight by ~5 min. That is the *inverse* incoherence and far
less alarming than the previous one; the grace guards against an insight flapping absent for
a single tick, and is left as is.

## Known, deliberately out of scope

**Engine-level disarm damping.** The engine resolves on the first evaluation a fingerprint is
absent. Level rules with an arm grace are fine, but the metric rules (`sustainedOver`) are
damped only on the arm side — one tick under threshold resolves instantly and re-fires. If we
want flap protection, the right shape is ONE engine-level knob (absent for N consecutive
evaluations), not N per-rule timers. Cross-cutting across all 24 rules, so it needs its own
validation pass.

## Code locations

- `apps/api/internal/insights/rules.go` — the 24-rule registry; recovery-evidence helpers.
- `apps/api/internal/insights/invariant_test.go` — the invariant, per rule.
- `apps/api/internal/insights/engine.go` — `hydrateLocked`; resolve loop; `admitNew`.
- `apps/api/internal/insights/engine_hydrate_test.go` — the missed-notification regression.
- `apps/web/src/hooks/useWebSocket.ts` — insight lifecycle → query invalidation.
- `apps/autopilot/src/poller.ts` — `reconcileClearedIncidents`, dedup + cooldown windows.
- `apps/web/src/components/insights/InsightCard.tsx` — renders `firstSeen` (Bug B, 2026-07).
