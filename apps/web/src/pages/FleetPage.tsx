import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { DollarSign } from 'lucide-react'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { parseClusterDisplayName } from '@/utils/cluster'
import { StripCard } from '@/components/dashboard/StripCard'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { ResourceTypeIcon } from '@/utils/resourceIcons'
import { useFleetRollup, type FleetClusterRollup } from '@/hooks/useFleetRollup'
import { AddClusterButton } from '@/components/admin/AddClusterButton'
import { CloudProviderIcon, providerLabel } from '@/components/shared/CloudProviderIcon'
import {
  fleetHealthSummary,
  healthFromInsights,
  healthLabel,
  HEALTH_BADGE_CLASS,
  HEALTH_RAIL_CLASS,
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

// In-card label/value pair. Deliberately one tier below the KPI strip's label
// token (10px / 0.09em) — mixing them flattens the two levels of the page.
//
// `hint` follows the same principle as metricsState below: a dash alone reads as
// "we lost your data", when the honest meaning is often "this number needs an
// add-on you haven't installed". Pass it only when the value is absent AND the
// reason is actionable — never as a permanent caption, which would compete with
// the number it sits under.
function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="flex flex-col">
      <span className="text-[9px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
        {label}
      </span>
      <span className="text-sm font-semibold tabular-nums text-kb-text-primary mt-0.5">{value}</span>
      {hint && (
        <span className="text-[9px] font-mono text-kb-text-tertiary leading-tight">{hint}</span>
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

function ClusterCard({
  cluster,
  rollup,
  findings,
  teamName,
  insights,
  onOpen,
}: {
  cluster: ClusterInfo
  rollup?: FleetClusterRollup
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
  return (
    <button
      type="button"
      onClick={onOpen}
      // `flex flex-col` + cuerpo elástico: la rejilla estira las tarjetas a la
      // misma altura, y sin esto una tarjeta con menos contenido —un cluster sin
      // agente, que no tiene rejilla de cifras— repartía el espacio sobrante y
      // bajaba su título respecto al de al lado. El nombre es el ancla con la
      // que se recorre la fila, así que tiene que estar SIEMPRE a la misma
      // altura; lo que sobra se acumula en el hueco de las cifras, que es donde
      // el vacío significa algo.
      className="relative text-left flex flex-col h-full bg-kb-card border border-kb-border rounded-[10px] p-4 pl-[1.1rem] overflow-hidden hover:bg-kb-card-hover transition-colors"
    >
      <span className={`absolute left-0 top-0 bottom-0 w-[3px] ${HEALTH_RAIL_CLASS[verdict]}`} aria-hidden />

      <div className="flex items-start justify-between gap-2 mb-2.5">
        <div className="min-w-0">
          <div className="text-[13px] font-semibold text-kb-text-primary truncate">
            {parseClusterDisplayName(cluster)}
          </div>
          {/* La identidad del cluster, que el diseño pedía y no teníamos: dónde
              corre y sobre qué versión. Llega en la lista desde la caché de
              perfiles del backend, así que se pinta sin conectar a ninguno.
              Cuando el perfil aún no se ha resuelto cae al par
              entorno/modo que siempre conocemos, en vez de dejar la línea
              vacía. */}
          <div className="flex items-center gap-1.5 text-[10px] font-mono text-kb-text-tertiary truncate">
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
        {/* La insignia vuelve a decir SALUD, como pide el diseño, y ahora es
            verdad: sale de /insights/summary, el mismo store del que el
            Overview calcula la suya. El estado del ENLACE no desaparece —baja
            al pie, junto al latido del agente, que es su sitio natural.
            Un cluster sin evaluar dice «NO DATA» y no «HEALTHY»: afirmar salud
            sobre algo que nadie ha mirado es el bug que esto viene a cerrar. */}
        <span
          className={`shrink-0 text-[9px] font-mono font-semibold uppercase tracking-[0.03em] px-1.5 py-0.5 rounded ${HEALTH_BADGE_CLASS[verdict]}`}
          title={
            verdict === 'unknown'
              ? 'No insight data for this cluster yet'
              : 'Active insights — same source as the cluster Overview'
          }
        >
          {healthLabel(verdict, insights)}
        </span>
      </div>

      {/* Three stats today; the Security slice adds Findings + CIS for six.
          Keep the count a multiple of three so the grid never leaves a hole. */}
      <div className="flex-1">
      {metrics.has ? (
        <div className="grid grid-cols-3 gap-x-3 gap-y-2">
          <Stat label="Pods" value={count(rollup?.pods ?? null)} />
          <Stat label="Nodes" value={count(rollup?.nodes ?? null)} />
          {/* Only cost carries a hint: it is the one stat that depends on an
              optional integration. Pods and Nodes come from the agent itself,
              so a dash there really does mean the data is missing.
              El delta va de HINT y no de valor: el gasto es la cifra que se
              lee, y el movimiento el contexto que la explica. */}
          <Stat
            label="Cost/mo"
            value={money(rollup?.costMonthly ?? null)}
            hint={
              rollup?.costMonthly == null
                ? 'needs OpenCost'
                : deltaHint(rollup?.costDelta ?? null)
            }
          />
          {/* Findings: el conteo del plan que tenga la org. En Free son CVEs y
              secretos; en Team suma configuración y RBAC; en Business,
              compliance y runtime. Mismo campo, más cobertura al subir — sin
              un solo `if` por tier.
              NO se pinta CIS%: arranca en Business, así que en la mayoría de
              las tarjetas sería un hueco permanente. */}
          <Stat
            label="Findings"
            value={findings ? String((findings.critical ?? 0) + (findings.high ?? 0)) : '—'}
            hint={findings?.critical ? `${findings.critical} critical` : undefined}
          />
        </div>
      ) : (
        <div className="text-[10px] font-mono text-kb-text-tertiary py-1">{metrics.reason}</div>
      )}
      </div>

      <div className="flex items-center gap-2 mt-3 pt-2.5 border-t border-kb-border text-[10px] font-mono text-kb-text-tertiary">
        <span
          className={`w-1.5 h-1.5 rounded-full ${cluster.agentConnected ? 'bg-status-ok' : 'bg-kb-text-tertiary'}`}
        />
        <span className="truncate">
          {cluster.agentConnected
            ? `Agent live · ${timeAgo(cluster.lastSeen)}`
            : LINK_LABEL[health]}
        </span>
        {/* El equipo dueño. Es la pregunta «¿a quién le toca esto?», que en una
            flota con varios equipos precede a cualquier otra — y el dato ya
            viajaba en la lista sin que nadie lo pintara aquí.
            Un equipo del que el usuario no es miembro NO se nombra: enseñar su
            id filtraría la existencia de un equipo ajeno. */}
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

  // Reparto de salud de la flota, para el KPI. Cuenta CLUSTERS y no insights:
  // «1 de 3» es la unidad en la que se actúa, y sumar avisos mezclaría uno con
  // once leves y otro con un crítico.
  const fleetHealth = fleetHealthSummary(
    clusters.map((c) => c.clusterId).filter((id): id is string => !!id),
    insightSummary?.bySeverityCluster,
  )

  const connected = clusters.filter((c) => healthOf(c) === 'ok').length
  const attention = clusters.length - connected
  const agentsLive = clusters.filter((c) => c.agentConnected).length
  const anyCrit = clusters.some((c) => healthOf(c) === 'crit')

  // A local kubeconfig routinely holds a dozen contexts — every cluster the
  // operator can reach, not every cluster they monitor. Lead with the ones
  // actually reporting so the page opens on signal instead of on a wall of
  // "not connected" cards, and surface the ratio in the subtitle so the gap
  // reads as a fact about the fleet rather than as a broken page.
  const reporting = clusters.filter((c) => metricsState(c, c.clusterId ? rollup.byCluster[c.clusterId] : undefined).has)
  // How much of the fleet the spend figure actually covers. Denominator is
  // `reporting`, not every cluster: one that isn't connected has no cost for an
  // obvious reason, and counting it would make the OpenCost gap look worse than
  // it is. Surfaced only when partial — "2 of 2 clusters" is noise, and a KPI
  // that qualifies itself when nothing is wrong teaches people to ignore the
  // qualifier.
  const costReporting = reporting.filter(
    (c) => c.clusterId && rollup.byCluster[c.clusterId]?.costMonthly != null,
  ).length
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

      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-5 gap-3">
        <StripCard
          hero
          label="Fleet spend"
          icon={<DollarSign className="w-3 h-3" />}
          info={
            <>
              <TooltipHeader right="OpenCost">Fleet spend</TooltipHeader>
              <TooltipRow
                color="#22d68a"
                label="Basis"
                value="Σ node_total_hourly_cost × 730, by cluster_id"
              />
              <TooltipNote>
                Summed across every cluster in the org, not just the active one. Clusters without
                OpenCost contribute nothing.
              </TooltipNote>
            </>
          }
          value={money(rollup.fleetSpendMonthly)}
          valueSuffix={rollup.costAvailable ? '/mo' : undefined}
          sub={
            !rollup.costAvailable
              ? 'no cost data'
              : costReporting < reporting.length
                ? `OpenCost · ${costReporting} of ${reporting.length} clusters`
                : 'OpenCost · run-rate'
          }
        />
        {/* El «3/3 · 1 with warnings» del diseño. La sub-línea prioriza, en
            este orden: clusters con críticos, con warnings, sin enlace, y por
            último los que aún no se han evaluado. Un cluster sin datos NO se
            cuenta como sano ni como problema — se dice aparte, porque «no lo
            sabemos» es una tercera respuesta y esconderla es lo que hacía que
            esta tarjeta pareciera más tranquilizadora de lo que sabía. */}
        <StripCard
          label="Clusters"
          value={String(clusters.length)}
          valueAccent={fleetHealth.critical > 0 ? 'crit' : fleetHealth.warning > 0 ? 'warn' : 'default'}
          sub={
            fleetHealth.critical > 0
              ? `${fleetHealth.critical} with criticals`
              : fleetHealth.warning > 0
                ? `${fleetHealth.warning} with warnings`
                : attention > 0
                  ? `${attention} not reporting`
                  : fleetHealth.unknown > 0
                    ? `${fleetHealth.unknown} not evaluated yet`
                    : 'all healthy'
          }
          subAccent={
            fleetHealth.critical > 0
              ? 'crit'
              : fleetHealth.warning > 0 || attention > 0
                ? 'warn'
                : fleetHealth.unknown > 0
                  ? 'default'
                  : 'ok'
          }
        />
        <StripCard label="Nodes" value={count(rollup.totalNodes)} sub="across fleet" />
        <StripCard label="Pods" value={count(rollup.totalPods)} sub="across fleet" />
        <StripCard
          label="Live agents"
          info={
            <>
              <TooltipHeader right="live">Live agents</TooltipHeader>
              <TooltipRow color="#4c9aff" label="Shows" value="agent channel currently open" />
              <TooltipNote>
                Channel liveness, not cluster reachability — a direct-kubeconfig cluster never
                counts here.
              </TooltipNote>
            </>
          }
          value={String(agentsLive)}
          valueSuffix={`/ ${clusters.length}`}
          valueAccent={agentsLive === clusters.length ? 'default' : 'warn'}
          sub={agentsLive === clusters.length ? 'all reporting' : 'partial coverage'}
          subAccent={agentsLive === clusters.length ? 'ok' : 'warn'}
        />
      </div>

      <div>
        <div className="flex items-center gap-3 border-t border-kb-border pt-4 mb-3">
          <span className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            All clusters
          </span>
          <span className="text-[10px] font-mono text-kb-text-tertiary/70">click to switch</span>
        </div>

        {isLoading && (
          <div className="bg-kb-card border border-kb-border rounded-[10px] px-4 py-8 text-center text-xs text-kb-text-tertiary">
            Loading fleet…
          </div>
        )}

        {!isLoading && clusters.length === 0 && (
          <div className="bg-kb-card border border-kb-border rounded-[10px] px-4 py-8 text-center text-xs text-kb-text-tertiary">
            No clusters yet — connect one from the Clusters page.
          </div>
        )}

        {!isLoading && clusters.length > 0 && view === 'grid' && (
          <div className="grid grid-cols-3 gap-3">
            {ordered.map((c) => (
              <ClusterCard
                key={c.context}
                cluster={c}
                rollup={c.clusterId ? rollup.byCluster[c.clusterId] : undefined}
                findings={c.clusterId ? findings?.bySeverityCluster?.[c.clusterId] : undefined}
                insights={c.clusterId ? insightSummary?.bySeverityCluster?.[c.clusterId] : undefined}
                onOpen={() => switchMutation.mutate(c.context)}
              />
            ))}
          </div>
        )}

        {!isLoading && clusters.length > 0 && view === 'table' && (
          <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
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
