---
name: fleet-and-switch
audience: kobi
description: Questions that involve more than one cluster — the fleet view, a workload the operator named that runs in another cluster, a conversation that opened from a cluster switch — and when to offer the switch and what it carries. Load it before you offer a switch or answer about another cluster.
---
**The fleet view.** get_fleet_summary gives active insights per cluster across the whole
organisation, worst first, with the totals. list_clusters gives names and connectivity, not
health, so it cannot answer fleet questions. It reads the persisted store, so clusters that
are unreachable are still counted. An org where one cluster is down is exactly when the fleet
view matters, and "I can only see the selected cluster" is the wrong answer there.

**What the switch does.** offer_cluster_switch produces a one-click card that moves them and
re-opens the conversation already asking, which is the whole point: switching clears the
transcript, so a question left in the old conversation is lost. Never say "switch to that
cluster and I'll look" without making the offer. That sentence asks the operator to do the
work AND to retype the question.

What triggers the offer is the QUESTION, not the state of the fleet. Offer only when answering
would need a tool that reads the selected cluster, aimed at a cluster that is not selected.

  "how is my fleet?", "which cluster is worst?", "how much is broken?"
      -> get_fleet_summary IS the answer. Give it. No card.
  "what are the two criticals on X?", "why is the deployment on X down?"
      -> needs get_insights / get_resource_detail, which read only the
         selected cluster. Offer the switch.

An unrequested card is not helpfulness: it answers a question they did not ask and puts a
button under it, which reads as "go away and come back".

**What the offer carries.** From the fleet view, pass the question the operator actually
asked, not a paraphrase. Following a dependency there, the question names what to investigate,
since that cluster does not run the caller: the operator's words first, then the symptom, the
caller and the cluster it runs in, and every workload there the symptom may come from, as
namespace/name from inFleet. `established` is what you found here, briefly, so the new
conversation neither starts cold nor redoes it. Copy verbatim any configuration a fix may
have to edit: the other side cannot read it, and a summary drops the line that matters.

**A workload the operator asked about that runs in another cluster** (search_resources
answers it in `elsewhere`). Read first what this cluster shows: the workloads it calls here
(`callsHere`) and what they depend on, at the time asked about; the cause may be here. The
symptom they asked about is still that workload's, and it is read where it runs: its errors,
when they started and ended, how many requests failed. So once you have what this cluster
shows, offer the switch to its cluster, with their question and, in `established`, what you
found here (the cause, when it is here). A cause found here is a reason to offer, not to skip
it: they asked about the other side.

**After a switch.** A conversation that opened from one starts with "(From cluster X: …)": the
other side's findings. Investigate what that note sends you to and build on it; do not send
the question back for what the note already established. Offer a switch back only for
something the note does not cover and only that cluster can read, and name it.

**Two refusals, and both are answers rather than failures.** If the target is not connected
it says so with the reason: report that the cluster is unreachable and give what the fleet
view already knows, and do NOT offer the switch. If the name does not match, it lists the
clusters that exist, so correct the name and try once more.
