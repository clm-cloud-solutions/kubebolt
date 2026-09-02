import type { ClusterInfo } from '@/types/kubernetes'
import type { TeamBrief } from '@/types/auth'
import type { FleetClusterRollup } from '@/hooks/useFleetRollup'
import { healthFromInsights, type HealthVerdict, type InsightCounts } from '@/utils/clusterHealth'

// La lógica del desglose de flota de Home, aparte del render para poder
// probarla: lo que decide qué ve el usuario es el ORDEN y el agrupado, no el
// JSX.
//
// Por qué esto es capacidad de plan y no una tabla más: con los 2 clusters de
// Free, «2 clusters · all reporting» ya lo dice todo y un desglose sería la
// misma frase ocupando quince veces más sitio. Con doce clusters esa línea no
// dice nada — hace falta saber CUÁLES están peor y de quién son. La vista sólo
// tiene sentido a la escala que el plan de pago desbloquea, así que crece con
// él en vez de aparecer capada.

// El estado del ENLACE con el cluster: si reporta, no si está sano.
//
// La distinción no es cosmética — confundirlas ya produjo que Fleet dijera
// «Healthy» de un cluster cuyo propio Overview decía «warning». La salud la
// calcula el backend con los insights activos (crash-loops, OOM, presión) y
// necesita un connector vivo; esta vista se pinta sin conectar a ninguno, así
// que sólo puede hablar del cable.
export type ClusterHealth = 'ok' | 'warn' | 'crit'

export interface FleetRow {
  cluster: ClusterInfo
  health: ClusterHealth
  /** Motivo cuando no está sano — se imprime, no se deduce del color. */
  reason: string
  pods: number | null
  nodes: number | null
  costMonthly: number | null
  /** Variación del gasto frente a la ventana del plan; null = sin referencia. */
  costDelta: number | null
  critical: number
  high: number
  /** true cuando el agente vive pero no llega ni una muestra. */
  silent: boolean
  /** Salud REAL del cluster (insights activos). Ver utils/clusterHealth. */
  verdict: HealthVerdict
  /** Los conteos que sostienen el veredicto, para etiquetar con su número. */
  insights?: InsightCounts
}

export interface FleetGroup {
  /** id del equipo, o '' para los clusters sin dueño. */
  teamId: string
  teamName: string
  rows: FleetRow[]
  /** Resumen del equipo — ver summarize(). */
  summary: FleetGroupSummary
}

export interface FleetGroupSummary {
  clusters: number
  /** Clusters que no están sanos, sea por caídos o por mudos. */
  needAttention: number
  critical: number
  high: number
}

/**
 * summarize — lo que un equipo tiene, en una línea.
 *
 * Es la mitad que hace legible el corte por equipo: sin esto hay que leer
 * todas las filas de un grupo para saber si ese equipo está bien, y con seis
 * equipos eso es leer la lista entera — exactamente lo que el desglose
 * pretendía evitar.
 *
 * Cuenta CLUSTERS que piden atención, no incidencias: «2 de 5 clusters» es la
 * unidad en la que un responsable piensa y actúa, mientras que sumar avisos
 * mezcla un cluster caído con otro que sólo tiene el agente ausente.
 */
export function summarize(rows: FleetRow[]): FleetGroupSummary {
  return {
    clusters: rows.length,
    // `health !== 'ok'` ya excluye los nunca dados de alta (healthOf los trata
    // como ok), así que «10/12 need attention» pasa a contar sólo lo que de
    // verdad se rompió.
    needAttention: rows.filter((r) => r.health !== 'ok').length,
    critical: rows.reduce((n, r) => n + r.critical, 0),
    high: rows.reduce((n, r) => n + r.high, 0),
  }
}

/**
 * neverOnboarded — un contexto del kubeconfig que nunca se dio de alta.
 *
 * Un kubeconfig de trabajo trae docenas de contextos de clusters a los que
 * simplemente se tiene acceso. Contarlos como «piden atención» convertía la
 * portada en un muro de «no agent connected» —10 de 12 en la flota real del
 * operador— y ahogaba las dos filas que sí importaban.
 *
 * La distinción honesta no es «tiene agente» sino «LLEGÓ a tenerlo»: `lastSeen`
 * sale de la fila de membresía durable, así que existe exactamente cuando algún
 * agente contactó alguna vez. Sin ella y sin ser agent-proxy, esto es
 * inventario, no una avería.
 *
 * Un cluster que SÍ estuvo conectado y ahora no sigue pidiendo atención — que es
 * el caso que de verdad hay que ver.
 */
function neverOnboarded(c: ClusterInfo): boolean {
  return !c.agentConnected && !c.lastSeen && c.source !== 'agent-proxy'
}

// Mismo criterio que FleetPage: `error` es el único fallo duro que reporta la
// lista; cualquier otra forma de no-conectado es aviso, porque puede ser
// simplemente un agente que se ausentó un momento. Sólo conectividad — ver el
// comentario de ClusterHealth.
function healthOf(c: ClusterInfo): ClusterHealth {
  if (c.status === 'error') return 'crit'
  if (c.status === 'connected' || c.agentConnected) return 'ok'
  // Nunca dado de alta: no es un aviso, es un contexto del kubeconfig.
  if (neverOnboarded(c)) return 'ok'
  return 'warn'
}

const HEALTH_RANK: Record<ClusterHealth, number> = { crit: 0, warn: 1, ok: 2 }

/**
 * buildRows — una fila por cluster, con su salud y sus números.
 *
 * Todo lo numérico es nullable a propósito: un cluster recién dado de alta, o
 * uno cuyo UID todavía no se ha resuelto, no tiene con qué unirse al roll-up.
 * Un 0 ahí se lee como «cluster vacío» y no como «aún no lo sabemos», que es la
 * distinción que más se pierde al pintar tablas.
 */
export function buildRows(
  clusters: ClusterInfo[],
  byCluster: Record<string, FleetClusterRollup>,
  bySeverityCluster?: Record<string, Record<string, number>>,
  /** Insights activos por cluster — la salud REAL, de /insights/summary. */
  insightsByCluster?: Record<string, InsightCounts>,
): FleetRow[] {
  return clusters.map((c) => {
    const r = c.clusterId ? byCluster[c.clusterId] : undefined
    const sev = c.clusterId ? bySeverityCluster?.[c.clusterId] : undefined
    const ins = c.clusterId ? insightsByCluster?.[c.clusterId] : undefined
    const health = healthOf(c)
    // Un agente conectado que no manda nada es un fallo propio, y de los caros:
    // el cluster parece sano en la lista y sus paneles salen vacíos. Se marca
    // como aviso aunque el estado diga «connected».
    const silent = health === 'ok' && !!c.clusterId && !!r && r.pods === null && r.nodes === null
    return {
      cluster: c,
      health: silent ? 'warn' : health,
      reason:
        health === 'crit'
          ? 'unreachable'
          : health === 'warn'
            ? 'no agent connected'
            : silent
              ? 'no metrics arriving'
              : neverOnboarded(c)
                // Dice lo que ES y lo que se puede hacer, en vez de un
                // «no agent connected» que suena a avería.
                ? 'not connected yet'
                : '',
      pods: r?.pods ?? null,
      nodes: r?.nodes ?? null,
      costMonthly: r?.costMonthly ?? null,
      costDelta: r?.costDelta ?? null,
      critical: sev?.critical ?? 0,
      high: sev?.high ?? 0,
      silent,
      verdict: healthFromInsights(ins),
      insights: ins,
    }
  })
}

/**
 * sortRows — lo peor primero, que es el único orden que sirve en una portada.
 *
 * Alfabético obligaría a leer la lista entera para encontrar el problema, y a
 * esta escala eso es justo lo que la vista existe para evitar. Desempata por
 * hallazgos críticos y, al final, por nombre, para que dos clusters igual de
 * sanos no bailen de sitio entre refrescos.
 */
export function sortRows(rows: FleetRow[]): FleetRow[] {
  return [...rows].sort((a, b) => {
    const h = HEALTH_RANK[a.health] - HEALTH_RANK[b.health]
    if (h !== 0) return h
    if (b.critical !== a.critical) return b.critical - a.critical
    if (b.high !== a.high) return b.high - a.high
    return (a.cluster.displayName || a.cluster.name).localeCompare(b.cluster.displayName || b.cluster.name)
  })
}

/**
 * groupByTeam — agrupa por equipo dueño, con los grupos también peor-primero.
 *
 * Devuelve UN grupo sin nombre cuando no hay equipos que mostrar (org de un
 * solo equipo, o clusters sin asignar): así el render no necesita saber si
 * estamos en una org con equipos, sólo pinta cabecera de grupo cuando hay más
 * de uno. Los clusters sin dueño van al final — son un pendiente
 * administrativo, no un equipo.
 */
export function groupByTeam(rows: FleetRow[], teams: TeamBrief[]): FleetGroup[] {
  const nameById = new Map(teams.map((t) => [t.id, t.name]))
  const groups = new Map<string, FleetGroup>()

  for (const row of rows) {
    const id = row.cluster.ownerTeamId || ''
    if (!groups.has(id)) {
      groups.set(id, {
        teamId: id,
        // Un equipo cuyo id no resuelve —el usuario no es miembro— NO se
        // nombra: enseñar «Team a3f9…» filtra la existencia de un equipo
        // ajeno, y decir sólo «Otro equipo» dice lo justo.
        teamName: id ? (nameById.get(id) ?? 'Another team') : 'Unassigned',
        rows: [],
        summary: { clusters: 0, needAttention: 0, critical: 0, high: 0 },
      })
    }
    groups.get(id)!.rows.push(row)
  }

  const out = [...groups.values()].map((g) => ({ ...g, rows: sortRows(g.rows), summary: summarize(g.rows) }))
  out.sort((a, b) => {
    // Sin dueño siempre al final, por mal que esté: es trabajo de
    // administración y no compite con un cluster caído de un equipo real.
    if (!a.teamId !== !b.teamId) return a.teamId ? -1 : 1
    const worst = (g: FleetGroup) => Math.min(...g.rows.map((r) => HEALTH_RANK[r.health]))
    const w = worst(a) - worst(b)
    if (w !== 0) return w
    return a.teamName.localeCompare(b.teamName)
  })
  return out
}
