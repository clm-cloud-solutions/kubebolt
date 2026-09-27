# Archive

Engineering notes, design explorations and early specifications that are no
longer maintained. They are kept for history and for the reasoning behind
decisions — **not** as documentation of how KubeBolt works today. Several are
written in Spanish, and some describe work that shipped differently, never
shipped, or belongs to KubeBolt Cloud rather than the open-source edition.

For current documentation start at [docs/README.md](../README.md) or
<https://kubebolt.io/docs>.

| Document | What it was |
|---|---|
| [CODE_REVIEW.md](CODE_REVIEW.md) | Phase 1 code review (March 2026, Spanish) |
| [kubebolt-distribution-spec.md](kubebolt-distribution-spec.md) | Distribution plan: single binary, Homebrew, krew, Docker, a planned Operator and install script (the last two never shipped) |
| [kubebolt-v3.html](kubebolt-v3.html) | The original static UI prototype |
| [metrics-only-cluster-support.md](metrics-only-cluster-support.md) | Gap analysis that led to metrics-only clusters (implemented in `cluster/manager.go`) |
| [kobi-mcp-readonly.md](kobi-mcp-readonly.md) | Feasibility study for the read-only MCP server (Spanish). The shipped feature is documented in [guides/kobi-mcp.md](../guides/kobi-mcp.md) |
| [kubebolt-copilot-skill/](kubebolt-copilot-skill/) | Pre-Kobi skill specification for the copilot. The prompts that ship live in `apps/api/internal/copilot/prompts/` |
| [design/](design/) | Kobi chat rebrand: UI mockup and the port inventory from the Cloud edition (Spanish) |
| [model-ai-comparison/](model-ai-comparison/) | Model comparisons for cluster analysis and for Kobi Autopilot incident reasoning. Autopilot is part of KubeBolt Cloud, not the open-source edition |
