# Changelog

All notable changes to KubeBolt are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.5.0] — 2026-04-22

Major release centered on production-ready AI Copilot, multi-channel
notifications, admin analytics and a test suite that covers both
backend and frontend.

### Added

- **AI Copilot — conversation memory**
  - Auto-compact folds older turns into a summary when the context
    approaches the configured budget × threshold (80% default), using
    the provider's cheap-tier model (Haiku 4.5 / gpt-4o-mini) so the
    compaction itself barely spends tokens.
  - Manual "new session with summary" (scissors icon in the panel)
    folds the entire transcript into a single summary so the user can
    pivot topics without losing context.
  - Tool history now persists across turns — the backend returns the
    full messages array on `done` and the frontend replaces its state
    with it. Matches the accumulative context model documented by
    Anthropic and OpenAI.
  - Session counter in the panel shows the real input size reported by
    the provider (input + cache reads + cache creations), not a
    client-side approximation.
  - Transcript auto-clears on a successful cluster switch — prior
    conversation wouldn't apply to the new cluster's resources.
- **AI Copilot — product knowledge base**
  - New `get_kubebolt_docs` tool with ~25 hand-curated topics covering
    UI surfaces, admin pages, configuration and keyboard shortcuts.
  - Tool description inlines the topic list so the LLM discovers keys
    without a round-trip; fuzzy-match fallback for off-by-one keys.
- **AI Copilot — scope guardrail**
  - System prompt now defines in-scope (Kubernetes, DevOps/SRE
    supporting cluster ops, KubeBolt) vs out-of-scope topics.
  - Out-of-scope questions get a one-sentence polite refusal with a
    redirect, never a partial answer.
- **AI Copilot — contextual triggers**
  - "Ask Copilot" buttons added to Insight cards, Resource Detail
    headers (Pods, Deployments, StatefulSets, Services, Nodes), and
    Warning events in the Events page.
  - Each button pre-loads a template prompt with the relevant context
    (cluster, namespace, name, symptom) and launches the Copilot panel.
- **AI Copilot — multi-provider support & caching**
  - GPT-5 / o-series compatibility via `max_completion_tokens`.
  - Anthropic prompt caching (`cache_control: ephemeral`) on system
    prompt + tool definitions.
  - OpenAI automatic caching parsed from `prompt_tokens_details.cached_tokens`
    and normalized to the Anthropic convention.
  - Fallback provider with auto-retry on 429 / 5xx.
- **Admin — Copilot Usage analytics** (`/admin/copilot-usage`)
  - Tiles: sessions, tokens billed + cache hit %, estimated USD cost
    (list-price per provider/model), avg duration + compact counts.
  - Stacked-bar timeseries chart (cache read / fresh input / output).
  - Top tools chart with call counts and error rates.
  - Recent sessions table with drill-down modal showing tool
    breakdown and compact events.
  - BoltDB-backed retention: 30 days / 5000 entries cap.
  - Configurable range (24h / 7d / 30d) with refresh button.
- **Admin — Notifications global settings**
  - Master enable/disable toggle.
  - Base URL for deep links in messages.
  - Resolved-insight alerts.
  - Email digest modes: instant / hourly / daily.
- **Structured logging**
  - `log/slog` JSON logger with daily rotation.
  - Per-session copilot logs include token breakdown, tool calls, tool
    bytes, duration, fallback flag and compaction events.
- **Testing**
  - 86 Go backend tests covering copilot, auth, config, insights,
    notifications and admin API handlers (race detector enabled in CI).
  - 14 frontend tests under vitest + jsdom for trigger prompt builders,
    suggestion generator and compact endpoint serialization.
  - CI now runs `go vet`, `go test -race` and `npm test` before build.

### Changed

- Context window sizes corrected: Claude Sonnet 4.6 and Opus 4.6/4.7 now
  report their true 1M-token budgets.
- AskCopilotButton refreshed with gradient + ring + hover glow to
  distinguish the AI entry points from the standard UI chrome.
- Admin pages (Clusters, Users, Notifications) use the full content
  width with a 3-column notification channel grid.
- Pre-release tags (`-rc`, `-beta`, `-alpha`) are auto-detected in the
  release workflow so the GitHub Release is correctly marked.

### Fixed

- Copilot chat panel: assistant messages no longer overflow the panel
  on long responses.
- Input textarea returns focus automatically after the Copilot finishes
  responding, so the user can keep typing without clicking.
- Conversation memory: removed tool-result elision (which was actively
  invalidating provider prompt caches) in favor of compaction. Measured
  sessions saw ~2× reduction in billed tokens once the caches stay warm
  across questions.
- Usage store: prune by cap now iterates the cursor instead of relying
  on `b.Stats()`, which didn't reflect pending writes inside a
  transaction and left the bucket one entry over the cap.

### Known issues

- `/cluster/overview` currently returns zero for `health.insights.*`
  counters because `Connector.GetOverview` doesn't pass the active
  insights through `GetHealth`. Pending for a follow-up release.
- Tool-result JSON truncation is byte-aligned; a structure-aware
  truncator is on the roadmap.

---

## [1.4.0] — 2026-03-28

Previous stable release. See git history for commits in range
[`v1.3.0..v1.4.0`](https://github.com/clm-cloud-solutions/kubebolt/compare/v1.3.0...v1.4.0).

## Earlier

Release history for 1.0.x through 1.3.0 is available in the GitHub
releases page: <https://github.com/clm-cloud-solutions/kubebolt/releases>.
