---
name: incident-history
audience: kobi
description: Answering from stored history (get_insight_episodes, get_insight_episode): is this new, what happened last night or on a cluster that is down, how it ended last time, what resolutionKind can and cannot say. Load it before you answer about the past of an insight or a workload.
---
get_insights is the PRESENT — what is wrong right now, from the live engine. It
cannot say whether this already happened, how it ended last time, or anything
at all about a cluster that is currently down.

**get_insight_episodes is the past**, and it reads stored history, so it answers
when the cluster does not. Reach for it whenever the answer depends on time
rather than on state:

- "Is this new?" A workload that has crash-looped four times this week is a
  different conversation from one failing for the first time. Say which it is —
  you can, and the difference usually decides whether to dig or to escalate.
- "What happened last night / on the cluster that is down?" Pass
  cluster="all" to reach clusters that are no longer in the selector at all.
  An unreachable cluster is a reason to use this tool, never a reason to say
  you cannot know.
- "How did it end last time?" Check resolutionKind and flapCount before
  proposing anything: something that resolved itself in three minutes twice
  today does not need a remediation, it needs someone to look at why it flaps.

**get_insight_episode** gets ONE episode whole — its timeline (with WHO did
each transition: the rule, the watchdog, or a person who silenced it), the
typed evidence the rule recorded when it fired, and its recurrence. Use it to
explain a specific episode; use the list to survey.

**One honest limit on resolutionKind.** Today it is only ever
"cleared_no_action_recorded" (the UI's "auto_recovered"), "rule_changed" or
empty — the value meaning a human remediated it is never written by the
current code. "cleared_no_action_recorded" says the signal went away and
KubeBolt did nothing; it tells you nothing about whether a person acted, in
the cluster or outside it. Never report that nobody intervened, or that it
recovered by itself, on the strength of this field.
