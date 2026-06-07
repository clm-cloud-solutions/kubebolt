import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ActionProposal } from '@/services/copilot/types'
import { ActionProposalCard, _formatProposalParam_FOR_TEST as formatParam } from './ActionProposalCard'

// Smoke tests for the 4 new propose_* dispatch arms added in
// 06-insight-rule-coverage. The existing 4 actions (restart, scale,
// rollback, delete) were validated in-vivo during the original PoC and
// don't have unit-level dispatch tests here either — we add coverage
// for the new arms only so a future refactor of the runProposal switch
// can't silently drop one and miss it until smoke-test time.
//
// Each test asserts:
//   - The right api.* method is called with the right body shape.
//   - The X-KubeBolt-Action-Source = "copilot_proposal" header value
//     is passed so the audit log distinguishes Copilot from direct UI.

// ─── Mocks ───────────────────────────────────────────────────────────

const apiCalls = {
  setResourcesResource: vi.fn(),
  setImageResource: vi.fn(),
  setEnvResource: vi.fn(),
  patchHpaBounds: vi.fn(),
}

vi.mock('@/services/api', () => ({
  api: {
    setResourcesResource: (...args: unknown[]) => apiCalls.setResourcesResource(...args),
    setImageResource: (...args: unknown[]) => apiCalls.setImageResource(...args),
    setEnvResource: (...args: unknown[]) => apiCalls.setEnvResource(...args),
    patchHpaBounds: (...args: unknown[]) => apiCalls.patchHpaBounds(...args),
    getResourceDetail: vi.fn().mockResolvedValue({}),
    // Auto dry-run fires on mount of a pending card; default to "would apply"
    // so it doesn't interfere with the execute-dispatch assertions.
    getDryRunPreview: vi.fn().mockResolvedValue({ ok: true, message: 'Would apply' }),
  },
  ApiError: class ApiError extends Error {
    status = 0
    payload: Record<string, unknown> | undefined
  },
}))

// CopilotContext exposes hooks the card reads. We stub them all to
// no-op observables; the test only cares about the api dispatch.
vi.mock('@/contexts/CopilotContext', () => ({
  useCopilot: () => ({
    recordProposalOutcome: vi.fn(),
    recordProposalProgressSettled: vi.fn(),
    recordProposalStalled: vi.fn(),
    sendMessage: vi.fn(),
    config: null,
    isLoading: false,
    conversationId: null,
  }),
}))

// The card reads the user's role to phrase a 403; stub it (not exercised by
// these success-path dispatch tests).
vi.mock('@/contexts/AuthContext', () => ({
  useAuth: () => ({ user: null }),
}))

// useNavigate is exercised post-success; jsdom + MemoryRouter handle it.

// ─── Helpers ─────────────────────────────────────────────────────────

function renderCard(proposal: ActionProposal) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <ActionProposalCard proposal={proposal} toolCallId="tc-1" />
      </QueryClientProvider>
    </MemoryRouter>,
  )
}

function baseProposal(
  action: ActionProposal['action'],
  params: Record<string, unknown>,
  targetType = 'deployments',
): ActionProposal {
  return {
    kind: 'action_proposal',
    version: 1,
    action,
    target: { type: targetType, namespace: 'default', name: 'api' },
    params,
    summary: `${action} summary`,
    rationale: 'rationale',
    risk: 'medium',
    reversible: true,
  }
}

beforeEach(() => {
  Object.values(apiCalls).forEach((m) => m.mockReset())
})

// ─── Tests ───────────────────────────────────────────────────────────

// formatProposalParam regression — the in-vivo bug that prompted this
// test: set_resources / set_image / set_env params are arrays of
// objects (containers, images), and the default String(v) rendered
// them as the literal string "[object Object]" on the card. The
// formatter must summarize the rows in a one-line, human-readable
// form.
describe('formatProposalParam', () => {
  it('containers array renders comma-separated container names', () => {
    const got = formatParam('containers', [
      { container: 'api', requests: { cpu: '100m' } },
      { container: 'sidecar', limits: { memory: '256Mi' } },
    ])
    expect(got).toBe('api, sidecar')
  })

  it('images array renders container→image pairs', () => {
    const got = formatParam('images', [
      { container: 'api', image: 'nginx:1.25' },
      { container: 'sidecar', image: 'acme/sidecar:v3' },
    ])
    expect(got).toBe('api→nginx:1.25, sidecar→acme/sidecar:v3')
  })

  it('empty array reports "(empty)" instead of nothing', () => {
    expect(formatParam('containers', [])).toBe('(empty)')
  })

  it('primitives still String() through unchanged', () => {
    expect(formatParam('replicas', 3)).toBe('3')
    expect(formatParam('force', true)).toBe('true')
    expect(formatParam('reason', 'oomkilled')).toBe('oomkilled')
  })

  it('plain object reports field count instead of [object Object]', () => {
    expect(formatParam('whatever', { a: 1, b: 2 })).toBe('(2 fields)')
  })

  it('unknown array key falls back to count summary', () => {
    expect(formatParam('unknownArr', [{ x: 1 }, { x: 2 }, { x: 3 }])).toBe('3 items')
    expect(formatParam('unknownArr', [{ x: 1 }])).toBe('1 item')
  })
})

describe('ActionProposalCard dispatch — new propose_* arms', () => {
  it('set_resources calls setResourcesResource with copilot_proposal source', async () => {
    apiCalls.setResourcesResource.mockResolvedValue({
      status: 'patched',
      toResources: [{ container: 'api' }],
      fromResources: [{ container: 'api' }],
      resource: null,
    })
    const containers = [
      {
        container: 'api',
        requests: { cpu: '100m', memory: '128Mi' },
        limits: { memory: '256Mi' },
      },
    ]
    const proposal = baseProposal('set_resources', { containers })
    renderCard(proposal)

    await userEvent.click(screen.getByRole('button', { name: /Execute/i }))

    await waitFor(() => expect(apiCalls.setResourcesResource).toHaveBeenCalledTimes(1))
    expect(apiCalls.setResourcesResource).toHaveBeenCalledWith(
      'deployments',
      'default',
      'api',
      containers,
      expect.objectContaining({ source: 'copilot_proposal' }),
    )
  })

  it('set_image calls setImageResource with the requested images', async () => {
    apiCalls.setImageResource.mockResolvedValue({
      status: 'patched',
      fromImages: [{ container: 'api', image: 'acme/api:v1' }],
      toImages: [{ container: 'api', image: 'acme/api:v2' }],
      resource: null,
    })
    const images = [{ container: 'api', image: 'acme/api:v2' }]
    const proposal = baseProposal('set_image', { images })
    renderCard(proposal)

    await userEvent.click(screen.getByRole('button', { name: /Execute/i }))

    await waitFor(() => expect(apiCalls.setImageResource).toHaveBeenCalledTimes(1))
    expect(apiCalls.setImageResource).toHaveBeenCalledWith(
      'deployments',
      'default',
      'api',
      images,
      expect.objectContaining({ source: 'copilot_proposal' }),
    )
  })

  it('set_env calls setEnvResource and forwards triggerRollout from params', async () => {
    apiCalls.setEnvResource.mockResolvedValue({
      status: 'patched',
      fromEnv: [],
      toEnv: [],
      triggerRollout: true,
      resource: null,
    })
    const containers = [
      {
        container: 'api',
        env: [
          { name: 'LOG_LEVEL', action: 'set', value: 'info' },
          { name: 'OLD_FLAG', action: 'remove' },
        ],
      },
    ]
    const proposal = baseProposal('set_env', { containers, triggerRollout: true })
    renderCard(proposal)

    await userEvent.click(screen.getByRole('button', { name: /Execute/i }))

    await waitFor(() => expect(apiCalls.setEnvResource).toHaveBeenCalledTimes(1))
    const call = apiCalls.setEnvResource.mock.calls[0]
    expect(call[0]).toBe('deployments')
    expect(call[1]).toBe('default')
    expect(call[2]).toBe('api')
    expect(call[3]).toEqual({ containers, triggerRollout: true })
    expect(call[4]).toEqual(expect.objectContaining({ source: 'copilot_proposal' }))
  })

  it('patch_hpa calls patchHpaBounds with just the bounds the LLM provided', async () => {
    apiCalls.patchHpaBounds.mockResolvedValue({
      status: 'patched',
      fromBounds: { minReplicas: 1, maxReplicas: 3 },
      toBounds: { minReplicas: 1, maxReplicas: 10 },
      resource: null,
    })
    // Only maxReplicas — minReplicas should NOT be forwarded.
    const proposal = baseProposal('patch_hpa', { maxReplicas: 10 }, 'hpas')
    renderCard(proposal)

    await userEvent.click(screen.getByRole('button', { name: /Execute/i }))

    await waitFor(() => expect(apiCalls.patchHpaBounds).toHaveBeenCalledTimes(1))
    expect(apiCalls.patchHpaBounds).toHaveBeenCalledWith(
      'default',
      'api',
      { minReplicas: undefined, maxReplicas: 10 },
      expect.objectContaining({ source: 'copilot_proposal' }),
    )
  })

  it('patch_hpa throws when both bounds are missing (defensive)', async () => {
    const proposal = baseProposal('patch_hpa', {}, 'hpas')
    renderCard(proposal)

    await userEvent.click(screen.getByRole('button', { name: /Execute/i }))

    // No api call should have been issued; the error path renders the
    // "Retry" link, but the assertion that matters is the api.* mock
    // was never called.
    await waitFor(() => {
      expect(apiCalls.patchHpaBounds).not.toHaveBeenCalled()
    })
  })
})
