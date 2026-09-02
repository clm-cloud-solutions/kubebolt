import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// usePlan — the org's resolved tier, plus the comparison the plan-aware HOME
// needs to decide what to render versus what to tease.
//
// FAILS OPEN, deliberately. /account/plan is a multi-tenant surface: in OSS
// (and any self-hosted install without the plan machinery) it 404s or 403s, and
// there is no subscription to gate on. Treating that as "Free" would paint
// upsell veils over features the operator already has — the worst possible
// outcome for a self-hosted user who paid nothing precisely because everything
// is included. So an unresolved plan unlocks everything; only an explicitly
// reported tier gates.

export type PlanTier = 'free' | 'team' | 'business' | 'enterprise' | 'ee_self_hosted'

// Rank drives `atLeast`. ee_self_hosted sits at the top: a self-hosted
// Enterprise licence is not a lesser tier than SaaS Enterprise.
const RANK: Record<string, number> = {
  free: 0,
  team: 1,
  business: 2,
  enterprise: 3,
  ee_self_hosted: 3,
}

export interface PlanInfo {
  /** Resolved tier, or null when the backend doesn't gate (OSS / self-hosted). */
  tier: PlanTier | null
  /** Display label for the badge. */
  label: string
  /** True while we don't yet know — render neutral, never a lock. */
  isLoading: boolean
  /** Whether the org is at or above a tier. True when ungated. */
  atLeast: (t: PlanTier) => boolean
  /** Convenience: the plan gates something the org doesn't have. */
  isFree: boolean
  /**
   * Días de retención del plan, o undefined cuando no hay caps (OSS: el
   * cliente guarda lo que quiera). Fija hasta dónde puede mirar atrás una
   * comparación — no es una decisión de producto, es que el dato no existe.
   */
  retentionDays?: number
  /**
   * Estado de la suscripción tal cual lo reporta Stripe (active, past_due,
   * unpaid, canceled…), o undefined cuando no hay ninguna: OSS, self-hosted, y
   * toda org en Free.
   *
   * Se pasa sin traducir a propósito. Un estado que no hayamos visto antes
   * llega igual a la UI en vez de aplanarse a "desconocido", que es como se
   * pierde justo el caso raro que había que mirar.
   */
  subscriptionStatus?: string
  /** Fin del periodo pagado: fecha de renovación si está al día, y fecha
   *  límite si el cobro está fallando. */
  currentPeriodEnd?: string
  /**
   * El cobro está fallando y el cliente puede arreglarlo.
   *
   * Stripe reintenta durante semanas antes de rendirse, así que esto NO
   * significa "ya no es cliente": significa que hay una ventana para actuar
   * —normalmente una tarjeta caducada— y que hasta ahora esa ventana pasaba
   * entera sin que nadie se lo dijera. No se corta nada; se avisa.
   */
  paymentFailing: boolean
}

// Estados en los que el cobro falló pero la suscripción sigue viva. `unpaid` es
// el final del embudo de reintentos de Stripe: aún recuperable, pero es el
// último aviso antes de que se cancele.
const PAYMENT_FAILING = new Set(['past_due', 'unpaid'])

const LABEL: Record<string, string> = {
  free: 'Free',
  team: 'Team',
  business: 'Business',
  enterprise: 'Enterprise',
  ee_self_hosted: 'Enterprise',
}

export function usePlan(): PlanInfo {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['account-plan'],
    queryFn: api.getAccountPlan,
    staleTime: 5 * 60_000,
    // A missing endpoint is an answer ("this build doesn't gate"), not a
    // transient failure worth hammering.
    retry: false,
  })

  const raw = data?.plan?.toLowerCase() ?? ''
  // OSS seeds its single tenant with plan "self-hosted" (auth/tenants_store.go).
  // That is an edition, not a tier: nothing gates, so it resolves to the same
  // "ungated" null a missing endpoint does — otherwise an unknown string would
  // rank at 0 and paint Free's veils over an install that owns everything.
  const tier = (isError || !raw || raw === 'self-hosted' ? null : raw) as PlanTier | null

  return {
    tier,
    label: tier ? (LABEL[tier] ?? tier) : '',
    isLoading: isLoading && !isError,
    atLeast: (t: PlanTier) => {
      if (tier === null) return true // ungated build — everything is available
      return (RANK[tier] ?? 0) >= (RANK[t] ?? 0)
    },
    isFree: tier === 'free',
    // OSS has no per-plan caps and no subscription: /account/plan returns
    // { id, name, plan, limits } only. The EE build reads caps.maxRetentionDays
    // and the Stripe subscription here; the fields stay on PlanInfo so the
    // Home page compiles identically in both editions.
    retentionDays: undefined,
    subscriptionStatus: undefined,
    currentPeriodEnd: undefined,
    paymentFailing: PAYMENT_FAILING.has(''),
  }
}
