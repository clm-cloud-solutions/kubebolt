// Curated catalog of LLM models KubeBolt's Copilot can talk to.
//
// "Anthropic" lists the Claude family — they use the Anthropic API
// shape natively. "OpenAI" is the broader OpenAI-compatible umbrella:
// OpenAI's own models plus everyone who speaks the OpenAI Chat
// Completions wire format (xAI Grok, Alibaba Qwen, Meta Llama via
// hosted providers, DeepSeek, Mistral on Together/Fireworks, and any
// custom deployment that exposes /v1/chat/completions). The user
// chooses the model from the list; the chosen value is the EXACT
// model ID the provider's API expects.
//
// Adding to the catalog: append a ModelOption entry. The runtime
// doesn't validate against this list — operators can type any value
// in the "Custom" branch. The catalog is for ergonomics, not safety.

export type ProviderID = 'anthropic' | 'openai'

export interface ModelOption {
  /** Provider-side model identifier. Sent to the API as `model`. */
  id: string
  /** Display name in the dropdown. Pick something an operator would
   *  recognise at a glance — vendor's marketing name, no version
   *  suffix unless the version IS what distinguishes the option. */
  label: string
  /** One-line context line shown below the selector. Tells the
   *  operator what trade-off they're picking (capability/cost/speed). */
  description: string
  /** Optional category for visual grouping in the dropdown. When
   *  multiple options share a group, render them as an <optgroup>. */
  group?: string
}

// ─── Anthropic ───────────────────────────────────────────────────────

export const ANTHROPIC_MODELS: ModelOption[] = [
  // Current top-of-line. Listed first within each tier so the picker
  // surfaces the newest by default.
  {
    id: 'claude-opus-4-7',
    label: 'Claude Opus 4.7',
    description: 'Most capable Anthropic model. Best for deep reasoning, complex investigations. Slowest and most expensive.',
    group: 'Claude 4 (current)',
  },
  {
    id: 'claude-sonnet-4-6',
    label: 'Claude Sonnet 4.6',
    description: 'Balanced default — fast enough for live chat, deep enough for most operator tasks. Recommended for production.',
    group: 'Claude 4 (current)',
  },
  {
    id: 'claude-haiku-4-5',
    label: 'Claude Haiku 4.5',
    description: 'Fast and cheap. Good for high-volume diagnostics and short turns. Less depth than Sonnet on tricky tool chains.',
    group: 'Claude 4 (current)',
  },
  // Previous-generation but still served by Anthropic. Useful for
  // accounts pinned to an older version, or for cost optimisation when
  // newer tiers aren't worth the price delta for the operator's workload.
  {
    id: 'claude-opus-4-6',
    label: 'Claude Opus 4.6',
    description: 'Previous Opus revision. Still served by Anthropic; useful when an account is pinned to it or for A/B comparisons.',
    group: 'Claude 4 (previous)',
  },
  {
    id: 'claude-sonnet-4-5',
    label: 'Claude Sonnet 4.5',
    description: 'Previous Sonnet revision. Still supported; comparable depth to 4.6 at a slightly different price/latency profile.',
    group: 'Claude 4 (previous)',
  },
  // Legacy. Kept for accounts that haven't been enabled for Claude 4
  // yet, and as a fallback when newer models hit availability issues.
  // Earlier Claude 4 builds. Anthropic still serves these on most
  // accounts (verified via /v1/models against a current key); keep as
  // alternates when an operator wants a specific revision pinned.
  {
    id: 'claude-opus-4-5-20251101',
    label: 'Claude Opus 4.5 (2025-11-01)',
    description: 'Earlier Opus 4 revision. Pinned by date — Anthropic prefers explicit date strings for cross-revision stability.',
    group: 'Claude 4 (earlier revisions)',
  },
  {
    id: 'claude-opus-4-1-20250805',
    label: 'Claude Opus 4.1 (2025-08-05)',
    description: 'Original Opus 4.1 build. Kept for accounts pinned to its exact behavior.',
    group: 'Claude 4 (earlier revisions)',
  },
  {
    id: 'claude-opus-4-20250514',
    label: 'Claude Opus 4 (2025-05-14)',
    description: 'Original Claude 4 Opus. First Claude 4 generation release.',
    group: 'Claude 4 (earlier revisions)',
  },
  {
    id: 'claude-sonnet-4-20250514',
    label: 'Claude Sonnet 4 (2025-05-14)',
    description: 'Original Claude 4 Sonnet. First Claude 4 generation Sonnet release.',
    group: 'Claude 4 (earlier revisions)',
  },
]

// ─── OpenAI + OpenAI-compatible ──────────────────────────────────────

export const OPENAI_COMPATIBLE_MODELS: ModelOption[] = [
  // ─── OpenAI proper — GPT-5 family (current flagship line) ─────────
  // OpenAI ships a fast cadence on the 5.x line: 5.0 in Aug 2025,
  // 5.1 in Nov 2025, 5.2 in Dec 2025, 5.3-Codex in Feb 2026, then 5.4
  // and 5.5 in quick succession. The catalog lists what is most useful
  // for API-key workflows (some IDs like raw gpt-5.5 require ChatGPT
  // sign-in and aren't available via /v1/chat/completions yet — when
  // ambiguous we err toward the IDs documented in developers.openai.com).
  {
    id: 'gpt-5.5',
    label: 'GPT-5.5',
    description: "OpenAI's flagship for complex reasoning and coding. Use this for the deepest API workflows when your account has access.",
    group: 'OpenAI · GPT-5 (current)',
  },
  {
    id: 'gpt-5.4',
    label: 'GPT-5.4',
    description: 'Previous frontier model. Strong general-purpose default. Slightly cheaper than 5.5.',
    group: 'OpenAI · GPT-5 (current)',
  },
  {
    id: 'gpt-5.4-mini',
    label: 'GPT-5.4 mini',
    description: 'Smaller 5.4 variant. Good balance for high-volume diagnostics that still need 5.x-class reasoning.',
    group: 'OpenAI · GPT-5 (current)',
  },
  {
    id: 'gpt-5.4-nano',
    label: 'GPT-5.4 nano',
    description: 'Smallest 5.4 variant. Sub-second latency for short turns; lighter on multi-step plans.',
    group: 'OpenAI · GPT-5 (current)',
  },
  // Pro variants are intentionally excluded — OpenAI only serves them
  // via /v1/responses, not /v1/chat/completions which is KubeBolt's
  // adapter. Verified May 2026 by direct API probe.
  // ─── OpenAI · GPT-5 (recent) ──────────────────────────────────────
  // Coding-specialised (Codex), audio, image, realtime, transcribe and
  // search-preview variants are excluded from the catalog — they are
  // not suitable as Kobi's general-purpose chat model. The "chat-
  // latest" aliases stay because they ARE the general-purpose anchors.
  {
    id: 'gpt-5.2',
    label: 'GPT-5.2',
    description: 'Stable choice for accounts pinned to it. Use 5.4+ for new work.',
    group: 'OpenAI · GPT-5 (recent)',
  },
  {
    id: 'gpt-5.2-chat-latest',
    label: 'GPT-5.2 (chat-latest)',
    description: 'Always-latest 5.2 chat alias.',
    group: 'OpenAI · GPT-5 (recent)',
  },
  // ─── OpenAI · GPT-5 (legacy) ──────────────────────────────────────
  {
    id: 'gpt-5.1',
    label: 'GPT-5.1',
    description: 'Older 5.1 build. Mostly kept for accounts pinned here. Prefer 5.4+ for new work.',
    group: 'OpenAI · GPT-5 (legacy)',
  },
  {
    id: 'gpt-5.1-chat-latest',
    label: 'GPT-5.1 (chat-latest)',
    description: 'Always-latest 5.1 chat alias.',
    group: 'OpenAI · GPT-5 (legacy)',
  },
  {
    id: 'gpt-5',
    label: 'GPT-5',
    description: 'Original GPT-5 (5.0) released Aug 2025. Legacy; new work should target 5.4 or later.',
    group: 'OpenAI · GPT-5 (legacy)',
  },
  {
    id: 'gpt-5-chat-latest',
    label: 'GPT-5 (chat-latest)',
    description: 'Always-latest 5.0 chat alias.',
    group: 'OpenAI · GPT-5 (legacy)',
  },

  // ─── OpenAI · GPT-4.1 family ──────────────────────────────────────
  // Released April 2025; intermediate between 4o and 5.x. Lighter and
  // cheaper than 5.x; useful when 5.x is overkill.
  {
    id: 'gpt-4.1',
    label: 'GPT-4.1',
    description: 'Intermediate GPT-4.1 flagship. Cheaper than GPT-5.x with strong general behavior.',
    group: 'OpenAI · GPT-4.1',
  },
  {
    id: 'gpt-4.1-mini',
    label: 'GPT-4.1 mini',
    description: 'Smaller, faster 4.1. Good cost/performance for high-volume diagnostic workflows.',
    group: 'OpenAI · GPT-4.1',
  },
  {
    id: 'gpt-4.1-nano',
    label: 'GPT-4.1 nano',
    description: 'Smallest 4.1 variant. Sub-second latency for short turns.',
    group: 'OpenAI · GPT-4.1',
  },

  // ─── OpenAI · GPT-4o (previous-generation flagship line) ──────────
  {
    id: 'gpt-4o',
    label: 'GPT-4o',
    description: "Previous-generation OpenAI flagship. Strong general performance, good tool-use latency. Still callable.",
    group: 'OpenAI · GPT-4o (previous)',
  },
  {
    id: 'gpt-4o-mini',
    label: 'GPT-4o mini',
    description: 'Cheaper, faster GPT-4o variant. Good for high-volume diagnostics where depth is secondary.',
    group: 'OpenAI · GPT-4o (previous)',
  },

  // ─── OpenAI · o-series reasoning models ───────────────────────────
  // Better at multi-step tool planning than the GPT-x line; slower and
  // pricier. Verified against /v1/models — only the IDs below are
  // still served (o1-mini and o1-preview were retired).
  {
    id: 'o4-mini',
    label: 'o4 mini',
    description: "OpenAI's o4 reasoning model in its mini variant. Cheapest reasoning option for routine multi-step plans.",
    group: 'OpenAI · o-series (reasoning)',
  },
  {
    id: 'o3',
    label: 'o3',
    description: "OpenAI's o3 reasoning model. Strong depth on multi-step investigations.",
    group: 'OpenAI · o-series (reasoning)',
  },
  {
    id: 'o3-mini',
    label: 'o3 mini',
    description: 'Smaller o3 variant. Good balance of depth and latency.',
    group: 'OpenAI · o-series (reasoning)',
  },

  // ─── xAI Grok ─────────────────────────────────────────────────────
  // Set base URL to https://api.x.ai/v1 and pass an xAI API key.
  // Catalog verified against /v1/models on a current xAI account
  // (May 2026). xAI ships the 4.20 line as three distinct date-pinned
  // variants (non-reasoning, reasoning, multi-agent); each is its own
  // model ID rather than a parameter. Image / video / build variants
  // are intentionally excluded — they aren't general-purpose chat
  // models suitable as Kobi's primary or fallback.
  {
    id: 'grok-4.3',
    label: 'Grok 4.3',
    description: "xAI's current flagship — fastest and most capable general-purpose Grok. Recommended primary for xAI installs.",
    group: 'xAI Grok (current)',
  },
  // grok-4.20-multi-agent is intentionally excluded — xAI returns
  // "Multi Agent requests are not allowed on chat completions" when
  // calling it through /v1/chat/completions. It requires xAI's
  // dedicated Multi Agent endpoint which KubeBolt's adapter doesn't
  // speak yet (May 2026 probe).
  {
    id: 'grok-4.20-0309-reasoning',
    label: 'Grok 4.20 reasoning',
    description: 'Reasoning-tuned 4.20. Better than non-reasoning on multi-step tool plans; slower per turn.',
    group: 'xAI Grok (current)',
  },
  {
    id: 'grok-4.20-0309-non-reasoning',
    label: 'Grok 4.20 (non-reasoning)',
    description: 'Standard 4.20 build without reasoning mode. Faster turns when explicit reasoning isn\'t needed.',
    group: 'xAI Grok (current)',
  },

  // ─── DeepSeek ─────────────────────────────────────────────────────
  // Base URL: https://api.deepseek.com/v1
  {
    id: 'deepseek-chat',
    label: 'DeepSeek Chat',
    description: "DeepSeek's general chat model. Set base URL to https://api.deepseek.com/v1.",
    group: 'DeepSeek',
  },
  {
    id: 'deepseek-reasoner',
    label: 'DeepSeek Reasoner',
    description: "DeepSeek's reasoning-tuned model. Slower but better at multi-step tool plans.",
    group: 'DeepSeek',
  },

  // ─── Alibaba Qwen ─────────────────────────────────────────────────
  // Base URL: https://dashscope-intl.aliyuncs.com/compatible-mode/v1
  {
    id: 'qwen-max',
    label: 'Qwen Max',
    description: "Alibaba's flagship. Set base URL to DashScope's OpenAI-compatible mode.",
    group: 'Alibaba Qwen',
  },
  {
    id: 'qwen-plus',
    label: 'Qwen Plus',
    description: 'Mid-tier Qwen. Same DashScope endpoint setup.',
    group: 'Alibaba Qwen',
  },
  {
    id: 'qwen-turbo',
    label: 'Qwen Turbo',
    description: 'Cheapest Qwen. Same DashScope endpoint setup.',
    group: 'Alibaba Qwen',
  },

  // ─── Meta Llama (via Groq for fast inference) ─────────────────────
  // Base URL: https://api.groq.com/openai/v1
  {
    id: 'llama-3.3-70b-versatile',
    label: 'Llama 3.3 70B (Groq)',
    description: "Meta's Llama 3.3 70B served by Groq's fast inference. Set base URL to https://api.groq.com/openai/v1.",
    group: 'Meta Llama (via Groq)',
  },
  {
    id: 'llama-3.1-70b-versatile',
    label: 'Llama 3.1 70B (Groq)',
    description: 'Slightly older 70B Llama on Groq. Comparable depth, marginally different price.',
    group: 'Meta Llama (via Groq)',
  },
  {
    id: 'llama-3.1-8b-instant',
    label: 'Llama 3.1 8B (Groq)',
    description: 'Tiny Llama variant on Groq. Sub-second latency, lower depth — fine for short operator queries.',
    group: 'Meta Llama (via Groq)',
  },

  // ─── Mistral (multiple hosts) ─────────────────────────────────────
  // Operator picks the hosting URL. La Plateforme, Together, Fireworks
  // all expose these via OpenAI-compatible endpoints.
  {
    id: 'mistral-large-latest',
    label: 'Mistral Large',
    description: 'Mistral flagship. Many providers host it; set base URL to the one you have an API key for (La Plateforme, Together, Fireworks).',
    group: 'Mistral',
  },
  {
    id: 'mistral-medium-latest',
    label: 'Mistral Medium',
    description: 'Mid-tier Mistral. Best balance of cost and depth for the Mistral family.',
    group: 'Mistral',
  },
  {
    id: 'mistral-small-latest',
    label: 'Mistral Small',
    description: 'Smallest Mistral. Cheap, fast, suitable for short turns.',
    group: 'Mistral',
  },
  {
    id: 'open-mistral-nemo',
    label: 'Mistral Nemo (open)',
    description: 'Apache-licensed Mistral Nemo. Run via La Plateforme or any compatible host.',
    group: 'Mistral',
  },
]

export const MODELS_BY_PROVIDER: Record<ProviderID, ModelOption[]> = {
  anthropic: ANTHROPIC_MODELS,
  openai: OPENAI_COMPATIBLE_MODELS,
}

// Custom sentinel — when picked, the form reveals a free-text input so
// operators can type any model name (their hosted custom deployment,
// a pre-release model not yet in the catalog, etc.). The form persists
// whatever they type as the model ID.
export const CUSTOM_MODEL_VALUE = '__custom__'

/**
 * findModelOption returns the catalog entry for `modelId` under the
 * given provider, or null when the ID isn't in the curated list
 * (which means the operator picked Custom previously and typed it).
 */
export function findModelOption(provider: ProviderID, modelId: string): ModelOption | null {
  const list = MODELS_BY_PROVIDER[provider] ?? []
  return list.find((m) => m.id === modelId) ?? null
}

/**
 * groupModels returns the catalog options grouped by their `group`
 * field, preserving insertion order so the visual hierarchy reads
 * top-to-bottom the way an operator scans: newest first within each
 * provider, then "legacy" branches at the bottom.
 */
export function groupModels(options: ModelOption[]): Array<{ group: string; items: ModelOption[] }> {
  const result: Array<{ group: string; items: ModelOption[] }> = []
  for (const opt of options) {
    const group = opt.group ?? 'Other'
    const existing = result.find((g) => g.group === group)
    if (existing) {
      existing.items.push(opt)
    } else {
      result.push({ group, items: [opt] })
    }
  }
  return result
}
