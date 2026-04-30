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

The design history and reference docs (personality charter, voice & tone guide, brand guidelines, eval suite, the Autopilot prompt for future work) live in `internal/kobi/` at the repo root — that directory is gitignored and treated as private design reference, not as runtime artifact.

## Editing rules

1. Read `internal/kobi/brand/personality-charter.md` and `internal/kobi/brand/voice-and-tone.md` before editing.
2. Run a manual review of N≥20 representative outputs (read aloud) against `internal/kobi/evals/voice-rubric.md` before merging.
3. Identity-affecting changes (model-agnostic phrasing, language mirroring, the "Kobi vs KOBI" naming convention) require updating the eval suite in lockstep.
4. Do not load `kobi-autopilot.md` from this directory — Autopilot is deferred for OSS v0. The Autopilot prompt remains in `internal/kobi/prompts/` as design reference for the future Enterprise build.

## Naming convention

- **Kobi** in body text, headers, prose.
- **KOBI** only when expanding the acronym (`KOBI — Kubernetes Operations & Bolt Intelligence`).
