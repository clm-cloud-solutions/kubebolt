import { describe, it, expect } from 'vitest'
import { ApiError } from '@/services/api'
import { describeTestError, describeTestResult } from './testOutcome'

describe('describeTestResult', () => {
  it('reports a pass with the tool count and who KubeBolt is on the other side', () => {
    const o = describeTestResult({
      ok: true,
      tools: 7,
      latencyMs: 312,
      identity: 'arn:aws:sts::123456789012:assumed-role/kb/kobi',
      canary: { tool: 'call', ok: true },
    })
    expect(o).toMatchObject({
      tone: 'ok',
      title: 'Connection verified · 7 tools · 312 ms',
      detail: 'Connected as arn:aws:sts::123456789012:assumed-role/kb/kobi',
    })
  })

  it('does not paint a pass green when the setup still needs attention', () => {
    // UI-2: AWS answered with the account's root user. The test passes, but
    // the warning used to sit in grey under a green check.
    const o = describeTestResult({
      ok: true,
      tools: 5,
      latencyMs: 120,
      canary: { tool: 'call', ok: true },
      warning: "These are the account's root user credentials. Use a dedicated read-only role or profile instead.",
    })
    expect(o.tone).toBe('warn')
    expect(o.title).toBe('Connection verified · 5 tools · 120 ms')
    expect(o.warning).toMatch(/root user/)
  })

  it('does not call it verified when the test read failed', () => {
    const o = describeTestResult({ ok: true, tools: 1, latencyMs: 90, canary: { tool: 'call', ok: false } })
    expect(o.tone).toBe('warn')
    expect(o.title).toBe('Connected · 1 tool, but a test read failed')
  })

  it('keeps the backend sentence and hint when they are already human', () => {
    const o = describeTestResult({
      ok: false,
      tools: 0,
      latencyMs: 0,
      error: 'The role ARN belongs to a different account than the one you named.',
      hint: 'Copy the ARN from the role page.',
    })
    expect(o).toEqual({
      tone: 'error',
      title: 'The role ARN belongs to a different account than the one you named.',
      hint: 'Copy the ARN from the role page.',
      raw: undefined,
    })
  })

  it('translates a raw AssumeRole denial and keeps the original behind a disclosure', () => {
    const raw =
      'operation error STS: AssumeRole, https response error StatusCode: 403, api error AccessDenied: User: arn:aws:iam::1:user/kb is not authorized to perform: sts:AssumeRole'
    const o = describeTestResult({ ok: false, tools: 0, latencyMs: 0, error: raw })
    expect(o.title).toBe("KubeBolt couldn't assume the role.")
    expect(o.hint).toMatch(/trust policy/)
    expect(o.raw).toBe(raw)
  })

  it('translates a network failure', () => {
    const o = describeTestResult({ ok: false, tools: 0, latencyMs: 0, error: 'dial tcp: lookup aws-mcp.us-east-1.api.aws: no such host' })
    expect(o.title).toBe("KubeBolt couldn't reach the provider.")
  })
})

describe('describeTestError', () => {
  it('cools the button down for as long as the backend asks', () => {
    const o = describeTestError(new ApiError(429, 'rate_limited', { error: 'rate_limited', retryAfterSeconds: 4 }))
    expect(o.retryAfterSeconds).toBe(4)
    expect(o.detail).toBe('Try again in 4 s.')
  })

  it('defaults the wait when the backend names none', () => {
    expect(describeTestError(new ApiError(429, 'rate_limited', { error: 'rate_limited' })).retryAfterSeconds).toBe(5)
  })

  it('points a field rejection at the field', () => {
    const o = describeTestError(
      new ApiError(400, 'region must look like us-east-1', { error: 'validation failed', field: 'region', message: 'region must look like us-east-1' }),
    )
    expect(o).toMatchObject({ tone: 'error', field: 'region', title: 'region must look like us-east-1' })
  })

  it('turns a plan gate into the plan to move to', () => {
    const o = describeTestError(
      new ApiError(409, 'requires upgrade', { error: 'requires upgrade', reason: 'requires_upgrade', requiredPlan: 'business', upgradeTo: 'business' }),
    )
    expect(o.upgradePlan).toBe('business')
    expect(o.planGate).toBe(true)
    expect(o.title).toBe('This connector needs the Business plan.')
  })

  // Review fix: with nothing on sale (upgradeTo ''), the outcome fell back to
  // requiredPlan and the drawer drew "Upgrade to Business" into a Billing page
  // where Business cannot be bought. It still names the plan; it no longer
  // offers to sell it.
  it('names the required plan but offers no upgrade when nothing on sale unlocks it', () => {
    const o = describeTestError(
      new ApiError(409, 'requires upgrade', { error: 'requires upgrade', reason: 'requires_upgrade', requiredPlan: 'business', upgradeTo: '' }),
    )
    expect(o.title).toBe('This connector needs the Business plan.')
    expect(o.planGate).toBe(true)
    expect(o.upgradePlan).toBeUndefined()
  })

  it('names no plan when the gate names none', () => {
    const o = describeTestError(new ApiError(409, 'requires upgrade', { error: 'requires upgrade', reason: 'requires_upgrade' }))
    expect(o).toMatchObject({ tone: 'warn', title: "Your plan doesn't include this connector.", planGate: true })
    expect(o.upgradePlan).toBeUndefined()
  })

  it('never headlines a raw server error', () => {
    const o = describeTestError(new ApiError(500, 'runtime error: invalid memory address'))
    expect(o.title).toBe("The test couldn't run.")
    expect(o.raw).toBe('runtime error: invalid memory address')
  })
})
