// The API applies the agent-proxy connect deadline and stuck-agent watchdog
// as idle budgets, never below the request timeout plus this headroom
// (config.EffectiveIdleBudgetSeconds). Mirrors IdleBudgetHeadroomSeconds.
export const IDLE_BUDGET_HEADROOM_SECONDS = 5

export interface RaisedBudget {
  name: 'Connect deadline' | 'Stuck-agent watchdog'
  configured: number
  effective: number
}

/** The value the API runs for a configured idle budget. 0 (off) stays 0. */
export function effectiveIdleBudget(configured: number, request: number): number {
  if (!(configured > 0) || !(request > 0)) return configured
  return Math.max(configured, request + IDLE_BUDGET_HEADROOM_SECONDS)
}

/** The budgets in the form that the API would raise, with what it would run. */
export function idleBudgetsRaised(form: {
  connectTimeoutSeconds: string
  stuckTimeoutSeconds: string
  requestTimeoutSeconds: string
}): RaisedBudget[] {
  const request = parseInt(form.requestTimeoutSeconds, 10)
  const out: RaisedBudget[] = []
  const budgets: [RaisedBudget['name'], string][] = [
    ['Connect deadline', form.connectTimeoutSeconds],
    ['Stuck-agent watchdog', form.stuckTimeoutSeconds],
  ]
  for (const [name, raw] of budgets) {
    const configured = parseInt(raw, 10)
    if (isNaN(configured) || isNaN(request)) continue
    const effective = effectiveIdleBudget(configured, request)
    if (effective !== configured) out.push({ name, configured, effective })
  }
  return out
}
