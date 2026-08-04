import { describe, it, expect } from 'vitest'
// NOTE — OSS scope. The enterprise edition also has a platform-managed Copilot
// tab with its own buildPatch, and the same rule is tested there. This edition
// ships only the per-org settings tab, so there is one builder to guard.

import { buildPatch as buildAdminPatch } from '@/pages/admin/settings/CopilotSettingsTab'

// Regression: the fallback provider must ride along whenever a fallback is
// enabled, even when the select's value never changed.
//
// stateFromResponse DEFAULTS the fallback provider select to a concrete name
// ('openai' / 'anthropic') when nothing is stored server-side. A pure
// dirty-diff therefore omitted it from the patch — the operator pasted a key
// and a model, saw a provider in the dropdown, and saved a record with no
// provider in it. The backend then resolved a nameless fallback that blew up at
// request time as `unknown provider: `.

const ADMIN_BASE = {
  provider: 'anthropic',
  model: 'claude-haiku-4-5',
  apiKey: '',
  baseURL: '',
  hasFallback: false,
  fallbackProvider: 'openai',
  fallbackModel: '',
  fallbackApiKey: '',
  fallbackBaseURL: '',
  showToolCalls: true,
  actionsEnabled: true,
  destructiveActionsEnabled: true,
  actionProgressTimeoutSeconds: '',
  maxRounds: '',
  autoCompact: true,
  maxTokens: '4096',
  sessionBudgetTokens: '',
  autoCompactThreshold: '',
  compactModel: '',
  compactPreserveTurns: '',
} as const

describe('buildPatch — fallback provider is never dropped', () => {
  it('sends the provider when only the key and model were touched', () => {
    const initial = { ...ADMIN_BASE, hasFallback: false }
    const current = {
      ...ADMIN_BASE,
      hasFallback: true,
      fallbackApiKey: 'sk-fallback',
      fallbackModel: 'gpt-4o-mini',
    }
    const req = buildAdminPatch(initial, current)

    expect(req.patch?.fallback?.provider).toBe('openai')
  })

  it('still omits the whole fallback section when no fallback is enabled', () => {
    const req = buildAdminPatch({ ...ADMIN_BASE }, { ...ADMIN_BASE })
    expect(req.patch?.fallback).toBeUndefined()
  })

  it('clears the stored fallback when the operator turns it off', () => {
    const initial = { ...ADMIN_BASE, hasFallback: true, fallbackModel: 'gpt-4o-mini' }
    const current = { ...ADMIN_BASE, hasFallback: false }
    const req = buildAdminPatch(initial, current)

    expect(req.plaintextFallbackAPIKey).toBe('')
    expect(req.patch?.fallback).toBeUndefined()
  })
})
