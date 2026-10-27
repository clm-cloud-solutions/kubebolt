---
name: workload-revisions
description: What a workload's revisions say about an incident — when it started, whether an earlier revision ran healthy, when a rollback fixes it and when there is nothing to roll back to. Load it when the cause is a value in the workload's spec, or before you name a rollback.
---
get_workload_history is the record of what each revision changed (its
`changes`) and when it was created. Read it whenever the cause is a value in
the workload's spec (an env var, image, command, probe, resources). For a
Deployment, its `rollout` already says which revision is current, which are
serving now and where rollback_deployment returns: take those from there.

- The revision whose `changes` introduced the cause dates its start: its
  createdAt (rollout events expire within about an hour).
- What a previous revision had comes from its `changes`, never from
  assumption. Its readyReplicas are current: a replaced revision always shows
  replicas/readyReplicas 0, which says nothing about how it ran. Whether it ran
  healthy comes from the events and logs of its time, or from a stalled rollout,
  where its ReplicaSet is still serving.
- When an earlier revision did not have the cause and ran healthy, this is a bad
  release, and `rollback_deployment` restores that whole prior spec, whatever
  field broke, including one no other verb edits: the rollback is the fix, not a
  manual edit of the spec. That covers the stalled rollout (new ReplicaSet 0
  ready, the previous one still serving) and the finished rollout whose new pods
  are Ready but fail requests. Name the revision the rollback returns to.
- With a single revision, or none that ever ran healthy (the Deployment never
  reached Available per its events), there is nothing to roll back to: say so
  plainly. That rules out rollback and nothing else: a memory limit below what
  the container allocates is still fixed by `patch_deployment_resources`, and a
  probe too tight by `patch_probe`.
