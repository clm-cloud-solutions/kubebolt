// La salud de un cluster a partir de sus insights activos.
//
// Vive aparte porque Fleet, Home y cualquier vista futura tienen que llegar al
// MISMO veredicto. El bug que originó esto fue justo lo contrario: Fleet decía
// «Healthy» del cluster cuyo Overview decía «warning», porque cada una medía
// algo distinto llamándolo igual.
//
// Son DOS preguntas y conviene no volver a fundirlas nunca:
//
//   enlace  → ¿reporta? (status + agentConnected). Lo sabe la lista de
//             clusters sin conectar a ninguno.
//   salud   → ¿está bien lo que corre dentro? (insights activos). Lo sabe el
//             store, que ahora se lee por flota vía /insights/summary.
//
// Un cluster puede estar reportando y enfermo, o sano y con el agente ausente.
// Mezclarlas pierde exactamente la mitad que el operador necesita.

export type HealthVerdict = 'healthy' | 'warning' | 'critical' | 'unknown'

export interface InsightCounts {
  critical?: number
  warning?: number
  info?: number
}

/**
 * healthFromInsights — el mismo escalón que aplica el backend en GetHealth:
 * un crítico activo manda sobre cualquier score, y un warning degrada a
 * warning. `info` NO degrada: son observaciones, y contarlas pintaría de ámbar
 * media flota permanentemente hasta que nadie mire el color.
 *
 * `undefined` (no `{}`) significa que no hay dato de ese cluster — sin
 * persistencia, o un cluster que nunca ha sido evaluado. Devuelve 'unknown',
 * que el UI debe pintar como ausencia y jamás como «sano»: afirmar salud sobre
 * un cluster que nadie ha mirado es la mentira que este módulo existe para
 * evitar.
 */
export function healthFromInsights(counts?: InsightCounts): HealthVerdict {
  if (!counts) return 'unknown'
  if ((counts.critical ?? 0) > 0) return 'critical'
  if ((counts.warning ?? 0) > 0) return 'warning'
  return 'healthy'
}

/**
 * healthLabel — lo que se imprime en la insignia.
 *
 * El diseño de Fleet pide «HEALTHY» y «2 WARNINGS»: el número forma parte del
 * mensaje porque «2 warnings» y «11 warnings» piden respuestas distintas, y una
 * insignia que sólo dijera «WARNING» obligaría a entrar para saber cuánto.
 */
export function healthLabel(verdict: HealthVerdict, counts?: InsightCounts): string {
  switch (verdict) {
    case 'critical': {
      const n = counts?.critical ?? 0
      return n === 1 ? '1 CRITICAL' : `${n} CRITICAL`
    }
    case 'warning': {
      const n = counts?.warning ?? 0
      return n === 1 ? '1 WARNING' : `${n} WARNINGS`
    }
    case 'healthy':
      return 'HEALTHY'
    default:
      // Ni «healthy» ni un hueco: el operador tiene que poder distinguir
      // «lo miramos y está bien» de «no lo hemos mirado».
      return 'NO DATA'
  }
}

export const HEALTH_BADGE_CLASS: Record<HealthVerdict, string> = {
  healthy: 'bg-status-ok-dim text-status-ok',
  warning: 'bg-status-warn-dim text-status-warn',
  critical: 'bg-status-error-dim text-status-error',
  unknown: 'bg-kb-elevated text-kb-text-tertiary',
}

export const HEALTH_RAIL_CLASS: Record<HealthVerdict, string> = {
  healthy: 'bg-status-ok',
  warning: 'bg-status-warn',
  critical: 'bg-status-error',
  unknown: 'bg-kb-text-tertiary',
}

/**
 * fleetHealthSummary — cuántos clusters de un conjunto no están sanos.
 *
 * Alimenta el «1 with warnings» del KPI de flota que pide el diseño. Cuenta
 * CLUSTERS y no insights: «1 de 3 clusters» es la unidad en la que se actúa,
 * mientras que sumar insights mezcla un cluster con once avisos leves y otro
 * con un crítico, y da un número más grande que dice menos.
 *
 * Los 'unknown' no cuentan como problema —no sabemos que lo tengan— pero se
 * devuelven aparte para que la UI pueda decir «2 sin datos» en vez de fingir
 * que la flota entera está evaluada.
 */
export function fleetHealthSummary(
  clusterIds: string[],
  bySeverityCluster?: Record<string, InsightCounts>,
): { healthy: number; warning: number; critical: number; unknown: number } {
  const out = { healthy: 0, warning: 0, critical: 0, unknown: 0 }
  for (const id of clusterIds) {
    out[healthFromInsights(bySeverityCluster?.[id])]++
  }
  return out
}
