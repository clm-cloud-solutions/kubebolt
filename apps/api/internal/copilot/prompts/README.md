# Kobi · Copilot Prompts (runtime)

This directory holds the **canonical, runtime versions** of the Kobi prompt layers that get embedded into the Go binary via `//go:embed` from `apps/api/internal/copilot/`.

## Files

| File | Purpose |
|---|---|
| `kobi-identity.md` | Layer 1 — core identity, voice principles, language mirroring |
| `kobi-copilot.md` | Layer 2 — Copilot mode communication contract |
| `kobi-few-shots.md` | Layer 3 — voice few-shots and anti-patterns |

These are concatenated (`identity + copilot + few-shots`) and merged with the operational appendix in `prompt.go` to produce the system prompt sent to the LLM provider.

## Source of truth

These files are **canonical**. If you change a prompt, change it here.

The design history and reference docs (personality charter, voice & tone guide, brand guidelines, the original eval suite and voice rubric, the Autopilot prompt) live in the `kubebolt-docs` repository under `kobi/` — private design reference, not runtime artifact.

## Editing rules

1. Read `kobi/brand/personality-charter.md` and `kobi/brand/voice-and-tone.md` (kubebolt-docs) before editing.
2. Measure the change with `cmd/kobi-eval` against the cases in `apps/api/evals/kobi` (voice criteria and `guard_*` must not get worse; doc 66), and review N≥20 representative outputs (read aloud) against `kobi/evals/voice-rubric.md` (kubebolt-docs) before merging.
3. Identity-affecting changes (model-agnostic phrasing, language mirroring, the "Kobi vs KOBI" naming convention) require updating the eval suite in lockstep.
4. Do not load the Autopilot material from this directory into the chat. Autopilot's agents carry their own prompts (`apps/autopilot`); `kobi-autopilot.md` stays in kubebolt-docs as design reference.

## Naming convention

- **Kobi** in body text, headers, prose.
- **KOBI** only when expanding the acronym (`KOBI — Kubernetes Operations & Bolt Intelligence`).
