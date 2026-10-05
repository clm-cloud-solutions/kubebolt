import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { parseClusterDisplayName } from '@/utils/cluster'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { ResourceTypeIcon } from '@/utils/resourceIcons'
import { useFleetRollup, type FleetClusterRollup } from '@/hooks/useFleetRollup'
import { useFleetPodsTrend } from '@/hooks/useFleetPodsTrend'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'
import { FleetKpis } from '@/components/fleet/FleetKpis'
import { KPI_COLOR, Sparkline } from '@/components/shared/kpi/MiniCharts'
import { AddClusterButton } from '@/components/admin/AddClusterButton'
import { CloudProviderIcon, providerLabel } from '@/components/shared/CloudProviderIcon'
import {
  healthFromInsights,
  healthLabel,
  HEALTH_BADGE_CLASS,
  type HealthVerdict,
  type InsightCounts,
} from '@/utils/clusterHealth'
import type { ClusterInfo } from '@/types/kubernetes'

// FleetPage (E2 A1) — the altitude-1 view: every cluster in the org at a
// glance, painted in the app's own list-page grammar rather than the mockup's
// standalone HTML.
//
// Clusters render as CARDS by default because a card carries a health accent
// plus a roll-up block, which is the actual question here ("which cluster needs
// me?"); a table optimizes for scanning one column, so it stays one click away
// for wide fleets. There is no in-page search: the Topbar's ⌘K palette already
// opens fleet-scoped on this route.
//
// Data comes from two independent sources, joined on cluster_id:
//   - /clusters — identity, status, agent liveness (no cluster connection)
//   - useFleetRollup — cost/nodes/pods aggregated across the org from VM
// Anything the roll-up can't answer renders "—" rather than a confident zero.
//
// KNOWN DEVIATIONS from the mockup, both waiting on other slices:
//   - Findings / CIS per card need the Security slice (A2) — the
//     FindingsStore isn't in develop yet. The stats grid flows, so they drop
//     into the empty cells when it lands.
//   - "EKS · us-east-1 · v1.29" needs provider/region/version on ClusterInfo.
//     Connector.CloudProfile() computes them but only for a CONNECTED cluster,
//     and Fleet deliberately renders without connecting. Showing environment +
//     mode instead, which we do know for every cluster.

type FleetView = 'grid' | 'table'
// Narrowest a cluster card can be before its four figures crowd each other.
const CARD_MIN_WIDTH = 360
const VIEW_KEY = 'kb-fleet-view'

function readView(): FleetView {
  try {
    return localStorage.getItem(VIEW_KEY) === 'table' ? 'table' : 'grid'
  } catch {
    return 'grid'
  }
}

function timeAgo(iso?: string | null): string {
  if (!iso) return '—'
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (secs < 60) return `${Math.floor(secs)}s ago`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`
  return `${Math.floor(secs / 86400)}d ago`
}

function money(v: number | null): string {
  if (v === null) return '—'
  return `$${Math.round(v).toLocaleString()}`
}

// Cabecera de columna. Existe para que las nueve celdas de <thead> no repitan
// la misma cadena de clases, que es como una de ellas acaba desalineada.
function Th({
  children,
  right,
  className = '',
}: {
  children: React.ReactNode
  right?: boolean
  className?: string
}) {
  return (
    <th
      className={`px-3 py-2.5 text-[10px] font-mono font-medium uppercase tracking-[0.08em] text-kb-text-secondary ${
        right ? 'text-right' : ''
      } ${className}`}
    >
      {children}
    </th>
  )
}

function count(v: number | null): string {
  return v === null ? '—' : Math.round(v).toLocaleString()
}

/**
 * El movimiento del gasto como pista bajo la cifra.
 *
 * Se calla por debajo del 2%: el coste oscila solo con el reciclaje de nodos y
 * los precios spot, así que un «▲1%» permanente enseña a ignorar la flecha
 * justo antes de que aparezca la que importa.
 *
 * Y se calla del todo sin referencia —cluster nuevo, o retención más corta que
 * la ventana de comparación— en vez de imprimir 0%, que afirmaría que el gasto
 * no se movió cuando la verdad es que no había con qué compararlo.
 */
function deltaHint(delta: number | null): string | undefined {
  if (delta === null || Math.abs(delta) < 0.02) return undefined
  return `${delta > 0 ? '▲' : '▼'}${Math.abs(Math.round(delta * 100))}% vs before`
}

// El estado del ENLACE con el cluster, no su salud. Ver LINK_LABEL.
type Health = 'ok' | 'warn' | 'crit'

// El enlace está en pie cuando podemos alcanzarlo (connected) o su agente está
// enviando. "error" es el único fallo duro que reporta la lista; cualquier otra
// forma de no-conectado es aviso — puede ser simplemente un agente que se
// ausentó un momento.
//
// NO dice nada sobre si los workloads del cluster están bien: eso lo calcula
// GetHealth en el backend a partir de los insights, y exige un connector vivo.
function healthOf(c: ClusterInfo): Health {
  if (c.status === 'error') return 'crit'
  if (c.status === 'connected' || c.agentConnected) return 'ok'
  return 'warn'
}

// Lo que esta insignia mide es si el cluster REPORTA, no si está sano.
//
// Decía «Healthy», y eso chocaba de frente con el Overview del mismo cluster
// diciendo «warning»: son dos preguntas distintas con la misma palabra. El
// Overview puntúa la SALUD —checks del plano de control más los insights
// activos: crash-loops, OOM, presión de memoria— y para eso necesita un
// connector vivo con sus informers. La flota se pinta a propósito sin conectar
// a ninguno, así que aquí sólo se sabe si el enlace está en pie.
//
// El enum ya venía mezclando los dos vocabularios —«Healthy» habla del cluster,
// «Unreachable» del cable— y esa mezcla es exactamente lo que produjo la
// contradicción. Ahora las tres palabras hablan del cable, y la salud del
// cluster se lee donde se calcula.
const LINK_LABEL: Record<Health, string> = {
  ok: 'Reporting',
  warn: 'Unreachable',
  crit: 'Error',
}

// In-card figure: the number in the KPI cards' face, one size down, with its
// label underneath in mono — the site's "figure, then what it is" order.
//
// `hint` follows the same principle as metricsState below: a dash alone reads as
// "we lost your data", when the honest meaning is often "this number needs an
// add-on you haven't installed". Pass it only when the value is absent AND the
// reason is actionable — never as a permanent caption, which would compete with
// the number it sits under.
function Figure({ label, value, hint, hintColor }: { label: string; value: string; hint?: string; hintColor?: string }) {
  return (
    <div className="flex flex-col min-w-0">
      <span className="font-display text-[22px] font-semibold leading-none tracking-[-0.03em] tabular-nums text-kb-text-primary">
        {value}
      </span>
      <span className="mt-1.5 text-[10.5px] font-mono text-kb-text-tertiary truncate">{label}</span>
      {hint && (
        <span
          className="text-[10px] font-mono text-kb-text-tertiary truncate"
          style={hintColor ? { color: hintColor } : undefined}
        >
          {hint}
        </span>
      )}
    </div>
  )
}

// A cluster contributes numbers only when we know its kube-system UID AND that
// UID appears in the roll-up. Both halves fail routinely and for DIFFERENT
// reasons, which is why the card says which one rather than printing three
// dashes: a kubeconfig holding eleven contexts will list eleven clusters here,
// and most of them are simply not part of the monitored fleet.
function metricsState(
  cluster: ClusterInfo,
  rollup?: FleetClusterRollup,
): { has: boolean; reason: string } {
  if (!cluster.clusterId) {
    // We have never resolved this context's UID — it has not been connected in
    // this session and was never cached, so there is nothing to join on.
    return { has: false, reason: 'Not connected yet' }
  }
  const any = rollup && (rollup.pods !== null || rollup.nodes !== null || rollup.costMonthly !== null)
  if (!any) {
    // Known cluster, no series: nothing is shipping into VictoriaMetrics for it.
    return { has: false, reason: 'No metrics reporting' }
  }
  return { has: true, reason: '' }
}

const VERDICT_TINT: Record<HealthVerdict, string> = {
  healthy: KPI_COLOR.ok,
  warning: KPI_COLOR.warn,
  critical: KPI_COLOR.err,
  unknown: 'var(--kb-text-tertiary)',
}

function ClusterCard({
  cluster,
  rollup,
  podTrend,
  findings,
  teamName,
  insights,
  onOpen,
}: {
  cluster: ClusterInfo
  rollup?: FleetClusterRollup
  /** Pods over the last 24h (useFleetPodsTrend); undefined = no series. */
  podTrend?: number[]
  /** Conteo por severidad de ESTE cluster; undefined = sin escáneres. */
  findings?: Record<string, number>
  /** Nombre del equipo dueño; vacío cuando no hay equipos o no se resuelve. */
  teamName?: string
  /** Insights ACTIVOS de este cluster; undefined = sin evaluar. */
  insights?: InsightCounts
  onOpen: () => void
}) {
  const health = healthOf(cluster)
  const verdict = healthFromInsights(insights)
  const metrics = metricsState(cluster, rollup)
  const tint = VERDICT_TINT[verdict]
  const offline = cluster.source === 'agent-proxy' && !cluster.agentConnected
  return (
    <button
      type="button"
      onClick={onOpen}
      // `flex flex-col` + cuerpo elástico: la rejilla estira las tarjetas a la
      // misma altura, y el nombre —el ancla con la que se recorre la fila— se
      // queda SIEMPRE a la misma altura. Lo que sobra lo absorbe el cuerpo, que
      // en una tarjeta sin datos es el bloque punteado que lo explica, no un
      // hueco.
      className="kb-panel kb-panel-hover text-left flex flex-col h-full p-5 min-w-0"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-[15px] font-semibold text-kb-text-primary truncate">
            {parseClusterDisplayName(cluster)}
          </div>
          {/* La identidad del cluster: dónde corre y sobre qué versión. Llega en
              la lista desde la caché de perfiles del backend, así que se pinta
              sin conectar a ninguno; sin perfil cae al par entorno/modo. */}
          <div className="mt-1 flex items-center gap-1.5 text-[10.5px] font-mono text-kb-text-tertiary min-w-0">
            {cluster.cloudProvider && (
              <CloudProviderIcon provider={cluster.cloudProvider} className="w-3 h-3 shrink-0" />
            )}
            <span className="truncate">
              {[
                providerLabel(cluster.cloudProvider),
                cluster.region,
                cluster.kubernetesVersion,
                cluster.mode || 'full',
              ]
                .filter(Boolean)
                .join(' · ')}
            </span>
          </div>
        </div>
        {/* SALUD, del mismo store que el Overview (/insights/summary). Un
            cluster sin evaluar dice «NO DATA» y no «HEALTHY»: afirmar salud
            sobre algo que nadie ha mirado es el bug que esto vino a cerrar. El
            estado del ENLACE vive al pie, junto al latido del agente. */}
        <span
          className="shrink-0 inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[10px] font-mono uppercase tracking-[0.08em]"
          style={{ color: tint, background: `color-mix(in srgb, ${tint} 12%, transparent)` }}
          title={
            verdict === 'unknown'
              ? 'No insight data for this cluster yet'
              : 'Active insights — same source as the cluster Overview'
          }
        >
          <span className="w-1.5 h-1.5 rounded-full" style={{ background: tint }} />
          {healthLabel(verdict, insights).toLowerCase()}
        </span>
      </div>

      <div className="flex-1 flex flex-col justify-center mt-4">
        {metrics.has ? (
          <>
            <div className="grid grid-cols-4 gap-3">
              <Figure label="pods" value={count(rollup?.pods ?? null)} />
              <Figure label="nodes" value={count(rollup?.nodes ?? null)} />
              {/* Sólo el coste lleva pista: es la única cifra que depende de una
                  integración opcional. El delta va de pista y no de valor: el
                  gasto es la cifra que se lee, el movimiento su contexto. */}
              <Figure
                label="/mo"
                value={money(rollup?.costMonthly ?? null)}
                hint={rollup?.costMonthly == null ? 'needs OpenCost' : deltaHint(rollup?.costDelta ?? null)}
              />
              {/* Findings: el conteo del plan que tenga la org (Free: CVEs y
                  secretos; Team suma configuración y RBAC; Business, compliance
                  y runtime). Sin CIS%: arranca en Business y sería un hueco. */}
              <Figure
                label="findings"
                value={findings ? String((findings.critical ?? 0) + (findings.high ?? 0)) : '—'}
                hint={findings?.critical ? `${findings.critical} critical` : undefined}
                hintColor={findings?.critical ? KPI_COLOR.err : undefined}
              />
            </div>
            {podTrend && podTrend.length >= 2 && (
              <div className="mt-4">
                <Sparkline
                  values={podTrend.map(Math.round)}
                  height={30}
                  left={
                    Math.min(...podTrend) === Math.max(...podTrend)
                      ? `pods 24h · steady at ${Math.round(podTrend[0])}`
                      : `pods 24h · ${Math.round(Math.min(...podTrend))}–${Math.round(Math.max(...podTrend))}`
                  }
                />
              </div>
            )}
          </>
        ) : (
          <div className="h-full min-h-[84px] flex flex-col justify-center rounded-xl border border-dashed border-kb-border px-4 py-3">
            <div className="text-[13px] text-kb-text-secondary">{metrics.reason}</div>
            <div className="mt-1 text-[10.5px] font-mono text-kb-text-tertiary">
              {!cluster.clusterId
                ? 'open it once to start reading it'
                : offline
                  ? 'its numbers return when the agent reconnects'
                  : 'install the agent to see pods, nodes and cost'}
            </div>
          </div>
        )}
      </div>

      <div className="flex items-center gap-2 mt-4 pt-3 border-t border-kb-border text-[10.5px] font-mono text-kb-text-tertiary">
        <span
          className={`w-1.5 h-1.5 rounded-full ${cluster.agentConnected ? 'bg-status-ok' : offline ? 'bg-status-warn' : 'bg-kb-text-tertiary'}`}
        />
        <span className="truncate">
          {cluster.agentConnected
            ? `Agent live · ${timeAgo(cluster.lastSeen)}`
            : offline
              ? // An agent-proxy cluster with no live agent is OFFLINE, not
                // "Reporting" (in-vivo 2026-09-15): it stays registered and
                // keeps its stored data, but the link is down — say so, and
                // keep the last contact so the gap is legible.
                `Agent offline · ${timeAgo(cluster.lastSeen)}`
              : LINK_LABEL[health]}
        </span>
        {/* El equipo dueño — «¿a quién le toca esto?». Un equipo del que el
            usuario no es miembro NO se nombra: su id filtraría su existencia. */}
        {teamName && <span className="ml-auto shrink-0 truncate max-w-[45%]">{teamName}</span>}
      </div>
    </button>
  )
}

export function FleetPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { hasRole } = useAuth()
  // Mismo criterio que ClustersPage: dar de alta es de admin de org.
  const canManage = hasRole('admin')
  const [view, setView] = useState<FleetView>(readView)

  const {
    data: allClusters = [],
    isLoading,
    dataUpdatedAt,
    isFetching,
  } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    // Matches ClustersPage. Without it the DataFreshnessIndicator below counts
    // up forever against data that never refreshes (the app sets
    // refetchOnWindowFocus:false globally).
    refetchInterval: 30_000,
  })

  // OSS is single-tenant: there is no team lens, so the fleet is every cluster
  // the backend lists. The EE build narrows this to the active team here, the
  // same way Topbar's switcher and ClustersPage do.
  const clusters = allClusters

  // Totals must cover the SAME clusters as the list. The backend roll-up is
  // org-scoped (tenant_id) and has no team dimension, so the ids are passed
  // down and the sums folded from the per-cluster rows.
  const rollup = useFleetRollup(
    clusters.length > 0,
    clusters.map((c) => c.clusterId).filter((id): id is string => !!id),
  )
  // One 24h pods line per card — the same cached series Home sums.
  const podTrends = useFleetPodsTrend(clusters.length > 0)
  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()

  // Hallazgos por cluster. MISMA queryKey que Home y que la página de Security,
  // así que las tres comparten una sola petición y no pueden discrepar en el
  // número que enseñan del mismo cluster.
  const { data: findings } = useQuery({
    queryKey: ['findings', '', '', ''],
    queryFn: () => api.listFindings(),
    retry: false,
  })

  // Estado REAL por cluster. Misma queryKey que Home para compartir caché, y
  // MISMO origen que la salud del Overview — por eso ya no pueden discrepar.
  const { data: insightSummary } = useQuery({
    queryKey: ['insights-summary'],
    queryFn: api.getInsightsSummary,
    refetchInterval: 60_000,
    retry: false,
  })

  const connected = clusters.filter((c) => healthOf(c) === 'ok').length
  const attention = clusters.length - connected

  // A local kubeconfig routinely holds a dozen contexts — every cluster the
  // operator can reach, not every cluster they monitor. Lead with the ones
  // actually reporting so the page opens on signal instead of on a wall of
  // "not connected" cards, and surface the ratio in the subtitle so the gap
  // reads as a fact about the fleet rather than as a broken page.
  const reporting = clusters.filter((c) => metricsState(c, c.clusterId ? rollup.byCluster[c.clusterId] : undefined).has)
  // Lo peor primero, en las DOS vistas — comparten este orden a propósito:
  // cambiar de Grid a Table no debería reordenar la flota bajo el cursor.
  //
  // Antes sólo separaba «reporta métricas» de «no reporta» y dejaba el resto en
  // orden de kubeconfig, que es el orden de alta: el cluster con criticals podía
  // quedar el último. Ahora manda la salud, luego los hallazgos, y el nombre
  // desempata para que dos clusters igual de sanos no bailen entre refrescos.
  const HEALTH_ORDER: Record<string, number> = { critical: 0, warning: 1, unknown: 2, healthy: 3 }
  const ordered = [...clusters].sort((a, b) => {
    const av = healthFromInsights(a.clusterId ? insightSummary?.bySeverityCluster?.[a.clusterId] : undefined)
    const bv = healthFromInsights(b.clusterId ? insightSummary?.bySeverityCluster?.[b.clusterId] : undefined)
    if (HEALTH_ORDER[av] !== HEALTH_ORDER[bv]) return HEALTH_ORDER[av] - HEALTH_ORDER[bv]

    // A igual salud, el que no reporta métricas sube: sus paneles saldrán
    // vacíos y eso es lo siguiente que hay que mirar.
    const am = metricsState(a, a.clusterId ? rollup.byCluster[a.clusterId] : undefined).has
    const bm = metricsState(b, b.clusterId ? rollup.byCluster[b.clusterId] : undefined).has
    if (am !== bm) return am ? 1 : -1

    const ac = a.clusterId ? (findings?.bySeverityCluster?.[a.clusterId]?.critical ?? 0) : 0
    const bc = b.clusterId ? (findings?.bySeverityCluster?.[b.clusterId]?.critical ?? 0) : 0
    if (ac !== bc) return bc - ac

    return parseClusterDisplayName(a).localeCompare(parseClusterDisplayName(b))
  })

  function chooseView(next: FleetView) {
    setView(next)
    try {
      localStorage.setItem(VIEW_KEY, next)
    } catch {
      // Private mode / quota — the choice just won't survive a reload.
    }
  }

  // Opening a cluster is a DESCENT from the account/fleet altitude into one
  // cluster, so it must feel like every other switch in the app. Carries the
  // ['switch-cluster'] mutationKey deliberately: Layout watches it with
  // useIsMutating to raise the "Connecting to cluster" overlay, and a plain
  // async function — which is what this was — never triggers it, so the page
  // sat frozen with no feedback while the switch ran. Mirrors ClustersPage:
  // stay pending until the new cluster's overview has settled, so there is no
  // double spinner and no flash of the previous cluster's name.
  const switchMutation = useMutation({
    mutationKey: ['switch-cluster'],
    mutationFn: async (context: string) => {
      let switchErr: unknown = null
      try {
        await api.switchCluster(context)
      } catch (e) {
        switchErr = e
      }
      await queryClient.cancelQueries({ queryKey: ['cluster-overview'] })
      await queryClient.refetchQueries({ queryKey: ['cluster-overview'] })
      if (switchErr) throw switchErr
    },
    onMutate: (context: string) => {
      queryClient.setQueryData(['clusters'], (old: ClusterInfo[] | undefined) =>
        old?.map((c) => ({ ...c, active: c.context === context })),
      )
    },
    onSuccess: () => {
      queryClient.invalidateQueries()
      navigate('/')
    },
    onError: () => {
      queryClient.invalidateQueries()
    },
  })

  return (
    <div className="space-y-5">
      <div className="mb-4">
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <ResourceTypeIcon type="fleet" />
            <h1 className="text-lg font-semibold text-kb-text-primary">Fleet</h1>
          </div>
          <span className="text-[10px] font-mono px-2.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary">
            {clusters.length} total
          </span>
          <div className="ml-auto flex items-center gap-3">
            <div
              className="flex items-center gap-0.5 rounded-md border border-kb-border bg-kb-card p-0.5"
              role="tablist"
              aria-label="Fleet view"
            >
              {(['grid', 'table'] as FleetView[]).map((v) => (
                <button
                  key={v}
                  type="button"
                  role="tab"
                  aria-selected={view === v}
                  onClick={() => chooseView(v)}
                  className={`px-2.5 py-1 text-[10px] font-mono uppercase tracking-[0.06em] rounded transition-colors ${
                    view === v
                      ? 'bg-kb-accent/15 text-kb-accent font-semibold'
                      : 'text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary'
                  }`}
                >
                  {v}
                </button>
              ))}
            </div>
            <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
            {/* El alta REAL, no un enlace a la pantalla que la tiene.
                Mandaba a `/clusters`, que tras el split es de ámbito de
                cluster: el botón principal de la pantalla de flota cambiaba el
                menú entero bajo el cursor y te dejaba a dos clics del wizard
                que querías. Ahora abre el mismo desplegable y los mismos dos
                modales que Clusters — literalmente el mismo componente. */}
            <AddClusterButton canManage={canManage} label="Connect cluster" />
          </div>
        </div>
        <p className="text-xs text-kb-text-tertiary mt-0.5">
          {reporting.length} reporting metrics · {connected} connected · {attention} need attention
        </p>
      </div>

      {/* The headline row in the site's card anatomy (FleetKpis → KpiCard),
          the same family as Home and the cluster Overview: the fleet's shape —
          health per cluster, spend over 7 days, pods per cluster, agent
          coverage — instead of five label-and-number strips. */}
      <FleetKpis
        clusters={clusters}
        rollup={rollup}
        insights={insightSummary?.bySeverityCluster}
        reporting={reporting.length}
        canManage={canManage}
      />

      <div>
        <div className="flex items-center gap-3 border-t border-kb-border pt-4 mb-3">
          <span className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            All clusters
          </span>
          <span className="text-[10px] font-mono text-kb-text-tertiary">click to switch</span>
        </div>

        {isLoading && (
          <div className="kb-panel px-4 py-8 text-center text-xs text-kb-text-tertiary">
            Loading fleet…
          </div>
        )}

        {!isLoading && clusters.length === 0 && (
          <div className="kb-panel px-4 py-8 text-center text-xs text-kb-text-tertiary">
            No clusters yet — connect one from the Clusters page.
          </div>
        )}

        {!isLoading && clusters.length > 0 && view === 'grid' && (
          <div
            ref={gridRef}
            className={`grid gap-4 ${gridWidth ? '' : 'grid-cols-1 lg:grid-cols-2 2xl:grid-cols-3'}`}
            style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, CARD_MIN_WIDTH, 16, [3, 2, 1])}, minmax(0, 1fr))` } : undefined}
          >
            {ordered.map((c) => (
              <ClusterCard
                key={c.context}
                cluster={c}
                rollup={c.clusterId ? rollup.byCluster[c.clusterId] : undefined}
                podTrend={c.clusterId ? podTrends.get(c.clusterId)?.map(([, v]) => v) : undefined}
                findings={c.clusterId ? findings?.bySeverityCluster?.[c.clusterId] : undefined}
                insights={c.clusterId ? insightSummary?.bySeverityCluster?.[c.clusterId] : undefined}
                onOpen={() => switchMutation.mutate(c.context)}
              />
            ))}
          </div>
        )}

        {!isLoading && clusters.length > 0 && view === 'table' && (
          <div className="kb-panel overflow-hidden">
            <table className="w-full text-left">
              {/* Una tabla se lee por COLUMNAS, así que puede llevar más que
                  la tarjeta, no menos — y llevaba menos: sin salud real, sin
                  proveedor ni versión, sin hallazgos, sin delta de coste y sin
                  equipo. Quien elegía «Table» perdía la mitad de lo que acababa
                  de ver en «Grid», que es lo contrario de lo que un cambio de
                  vista promete.
                  Las columnas de identidad se ocultan primero al estrechar
                  (Platform en <lg, Team en <xl): lo que un operador compara en
                  una flota son los NÚMEROS, y el nombre siempre se queda. */}
              <thead>
                <tr className="border-b border-kb-border">
                  <Th>Cluster</Th>
                  <Th className="hidden lg:table-cell">Platform</Th>
                  <Th>Health</Th>
                  <Th right>Findings</Th>
                  <Th right>Pods</Th>
                  <Th right>Nodes</Th>
                  <Th right>Cost/mo</Th>
                  <Th>Agent</Th>
                </tr>
              </thead>
              <tbody>
                {ordered.map((c) => {
                  const health = healthOf(c)
                  const r = c.clusterId ? rollup.byCluster[c.clusterId] : undefined
                  const ins = c.clusterId ? insightSummary?.bySeverityCluster?.[c.clusterId] : undefined
                  const verdict = healthFromInsights(ins)
                  const sev = c.clusterId ? findings?.bySeverityCluster?.[c.clusterId] : undefined
                  const crit = sev?.critical ?? 0
                  const totalFindings = crit + (sev?.high ?? 0)
                  return (
                    <tr
                      key={c.context}
                      onClick={() => switchMutation.mutate(c.context)}
                      className="border-b border-kb-border last:border-0 hover:bg-kb-card-hover cursor-pointer transition-colors"
                    >
                      <td className="px-3 py-2.5">
                        <div className="text-xs text-kb-text-primary">{parseClusterDisplayName(c)}</div>
                        <div className="text-[10px] font-mono text-kb-text-tertiary">
                          {c.mode || 'full'}
                        </div>
                      </td>

                      {/* Proveedor · región · versión. El icono identifica antes
                          que el texto al recorrer la columna. */}
                      <td className="px-3 py-2.5 hidden lg:table-cell">
                        <div className="flex items-center gap-1.5 text-[10px] font-mono text-kb-text-secondary">
                          {c.cloudProvider && (
                            <CloudProviderIcon provider={c.cloudProvider} className="w-3 h-3 shrink-0" />
                          )}
                          <span className="truncate">
                            {[providerLabel(c.cloudProvider), c.region, c.kubernetesVersion]
                              .filter(Boolean)
                              .join(' · ') || '—'}
                          </span>
                        </div>
                      </td>

                      {/* Salud REAL, la misma que el Overview — no el estado del
                          enlace, que ahora vive en la columna Agent. */}
                      <td className="px-3 py-2.5">
                        <span
                          className={`px-2 py-0.5 rounded-full text-[10px] font-mono ${HEALTH_BADGE_CLASS[verdict]}`}
                        >
                          {healthLabel(verdict, ins)}
                        </span>
                      </td>

                      <td className="px-3 py-2.5 text-[10px] font-mono tabular-nums text-right">
                        {sev ? (
                          <>
                            <span className="text-kb-text-secondary">{totalFindings}</span>
                            {crit > 0 && <span className="text-status-error"> · {crit} crit</span>}
                          </>
                        ) : (
                          <span className="text-kb-text-tertiary" title="No security scanners on this cluster">
                            —
                          </span>
                        )}
                      </td>

                      <td className="px-3 py-2.5 text-[10px] font-mono tabular-nums text-right text-kb-text-secondary">
                        {count(r?.pods ?? null)}
                      </td>
                      <td className="px-3 py-2.5 text-[10px] font-mono tabular-nums text-right text-kb-text-secondary">
                        {count(r?.nodes ?? null)}
                      </td>

                      {/* Same "say why, don't just dash" idea as the grid card,
                          but as a title: a table is scanned down a column, so
                          repeating "needs OpenCost" on every row would be noise
                          where the card shows it once. */}
                      <td
                        className="px-3 py-2.5 text-[10px] font-mono tabular-nums text-right text-kb-text-secondary"
                        title={r?.costMonthly == null ? 'Needs the OpenCost integration' : undefined}
                      >
                        {money(r?.costMonthly ?? null)}
                        {deltaHint(r?.costDelta ?? null) && (
                          <span
                            className={
                              (r?.costDelta ?? 0) > 0 ? 'text-status-warn ml-1' : 'text-status-ok ml-1'
                            }
                          >
                            {(r?.costDelta ?? 0) > 0 ? '▲' : '▼'}
                            {Math.abs(Math.round((r?.costDelta ?? 0) * 100))}%
                          </span>
                        )}
                      </td>

                      {/* El estado del ENLACE. Un cluster puede estar sano y con
                          el agente ausente, o reportando y enfermo: son dos
                          hechos y la fila tiene que poder decir los dos. */}
                      <td className="px-3 py-2.5 text-[10px] font-mono">
                        {c.agentConnected ? (
                          <span className="text-status-ok">live · {timeAgo(c.lastSeen)}</span>
                        ) : (
                          <span
                            className={health === 'crit' ? 'text-status-error' : 'text-kb-text-tertiary'}
                          >
                            {LINK_LABEL[health].toLowerCase()}
                          </span>
                        )}
                      </td>


                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}
