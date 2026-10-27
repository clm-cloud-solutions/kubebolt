import { ApiError, isRequiresUpgrade, planLabel, requiresUpgradePlan, type McpConnectorTestResult } from '@/services/api'

// What the drawer says after "Test connection". The backend already answers
// with a sentence (`error`) and what to check (`hint`); this module is the
// safety net for the failures that still arrive in the provider's own words
// (an STS AccessDenied, a Go dial error) and for the HTTP-level refusals, so
// an admin never reads a stack of provider jargon as the headline. The raw
// text is kept as `raw` and shown behind a disclosure.

export type OutcomeTone = 'ok' | 'warn' | 'error'

export interface TestOutcome {
  tone: OutcomeTone
  title: string
  detail?: string
  hint?: string
  // Something about a passing setup that still needs fixing (the AWS root
  // user). Rendered in the warn tone, not as a quiet hint.
  warning?: string
  raw?: string
  // Set when the backend asked us to wait (429) — the button cools down this long.
  retryAfterSeconds?: number
  // Set when the draft was rejected for one field (400) — the form marks it.
  field?: string
  // Set when the plan does not include the connector (409 requires_upgrade).
  planGate?: boolean
  // The plan on sale that unlocks it (the 409's upgradeTo), and only that:
  // with nothing on sale the title still names the required plan, but the
  // action is the contact address or nothing — never Billing.
  upgradePlan?: string
}

// Provider phrasing → a sentence. Order matters: the first match wins, and
// the specific AssumeRole case must beat the generic permission one.
const PATTERNS: { re: RegExp; title: string; hint?: string }[] = [
  {
    re: /AccessDenied[\s\S]*AssumeRole|not authorized to perform:?\s*sts:AssumeRole/i,
    title: "KubeBolt couldn't assume the role.",
    hint: "Check that the role's trust policy allows KubeBolt's principal with this external ID.",
  },
  {
    re: /ExpiredToken|InvalidClientTokenId|UnrecognizedClientException|NoCredentialProviders|failed to refresh cached credentials/i,
    title: "KubeBolt's own cloud credentials are missing or expired.",
    hint: 'The server KubeBolt runs on needs a valid identity to reach the provider.',
  },
  {
    re: /invalid_grant|invalid_client|unauthorized_client/i,
    title: 'Google rejected the credential.',
    hint: 'Upload a fresh service account key or workload identity configuration.',
  },
  {
    re: /PERMISSION_DENIED|AuthorizationFailed|AccessDenied|is not authorized|forbidden/i,
    title: 'The identity reached the provider but is not allowed to read.',
    hint: 'Grant the read-only permissions listed in the setup guide.',
  },
  {
    re: /context deadline exceeded|timeout|timed out/i,
    title: "The provider didn't answer in time.",
    hint: 'Try again. If it keeps happening, check outbound network access from the KubeBolt server.',
  },
  {
    re: /no such host|connection refused|dial tcp|network is unreachable/i,
    title: "KubeBolt couldn't reach the provider.",
    hint: 'Check outbound network access from the KubeBolt server.',
  },
]

function humanize(message: string): { title: string; hint?: string; raw?: string } {
  for (const p of PATTERNS) {
    if (p.re.test(message)) return { title: p.title, hint: p.hint, raw: message }
  }
  return { title: message }
}

// The plan on sale that clears a plan gate: the 409's upgradeTo, or '' when
// nothing on sale does (or err is no plan gate). Unlike requiresUpgradePlan it
// never falls back to requiredPlan — that one names the plan; this one says
// whether Billing can sell it.
export function planGateUpgradeTo(err: unknown): string {
  if (!isRequiresUpgrade(err)) return ''
  const u = (err as ApiError).payload?.upgradeTo
  return typeof u === 'string' ? u : ''
}

export function describeTestResult(r: McpConnectorTestResult): TestOutcome {
  if (r.ok) {
    const tools = `${r.tools} tool${r.tools === 1 ? '' : 's'}`
    const base = {
      detail: r.identity ? `Connected as ${r.identity}` : undefined,
    }
    // Reaching the MCP endpoint is not the same as reading the account: the
    // canary is one harmless read that proves the second.
    if (r.canary && !r.canary.ok) {
      return {
        tone: 'warn',
        title: `Connected · ${tools}, but a test read failed`,
        hint: r.hint ?? 'The credentials may lack read permissions. Check the policy in the setup guide.',
        ...base,
      }
    }
    // A pass with a warning is still a pass — the title says so — but it is
    // not a green one: the admin has something to fix.
    return {
      tone: r.warning ? 'warn' : 'ok',
      title: `Connection verified · ${tools} · ${r.latencyMs} ms`,
      hint: r.hint,
      warning: r.warning || undefined,
      ...base,
    }
  }
  const h = humanize(r.error || 'The test failed without a reason.')
  return { tone: 'error', title: h.title, hint: r.hint || h.hint, raw: h.raw }
}

export function describeTestError(err: unknown): TestOutcome {
  if (err instanceof ApiError) {
    const payload = err.payload ?? {}
    if (err.status === 429) {
      const n = Number(payload.retryAfterSeconds)
      const wait = Number.isFinite(n) && n > 0 ? Math.ceil(n) : 5
      return {
        tone: 'warn',
        title: 'Too many tests in a row.',
        detail: `Try again in ${wait} s.`,
        retryAfterSeconds: wait,
      }
    }
    if (isRequiresUpgrade(err)) {
      const plan = requiresUpgradePlan(err)
      return {
        tone: 'warn',
        title: plan ? `This connector needs the ${planLabel(plan)} plan.` : "Your plan doesn't include this connector.",
        planGate: true,
        upgradePlan: planGateUpgradeTo(err) || undefined,
      }
    }
    if (err.status === 400) {
      return {
        tone: 'error',
        title: err.message || 'A field is not valid.',
        field: typeof payload.field === 'string' ? payload.field : undefined,
      }
    }
    if (err.status === 404) {
      return { tone: 'error', title: 'Connectors are turned off on this installation.' }
    }
    if (err.status === 503) {
      return { tone: 'error', title: "KubeBolt couldn't run the test right now.", hint: 'Try again in a moment.', raw: err.message }
    }
    const h = humanize(err.message)
    return { tone: 'error', title: h.raw ? h.title : "The test couldn't run.", hint: h.hint, raw: err.message }
  }
  const message = err instanceof Error ? err.message : String(err)
  return { tone: 'error', title: "The test couldn't run.", raw: message }
}
