import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import { Boxes, ChevronRight } from 'lucide-react'
import { api } from '@/services/api'
import { parseClusterDisplayName } from '@/utils/cluster'
import { CloudProviderIcon, providerLabel } from '@/components/shared/CloudProviderIcon'
import type { ClusterInfo } from '@/types/kubernetes'
import type { TeamBrief } from '@/types/auth'
import type { FleetClusterRollup } from '@/hooks/useFleetRollup'
import { buildRows, groupByTeam, sortRows, type FleetRow } from './fleetGroups'
import { healthLabel, HEALTH_RAIL_CLASS, type InsightCounts } from '@/utils/clusterHealth'

// FleetBreakdown — qué clusters están peor, y de quién son.
//
// La capacidad que aparece al pasar de Free a Team. No es un panel bloqueado
// que se desvela: es una vista que sólo tiene sentido a la escala que el plan
// desbloquea. Con los 2 clusters de Free, «2 clusters · all reporting» ya lo
// dice todo; con doce, esa misma línea no dice nada y hace falta saber cuáles
// piden algo. Y el agrupado por equipo directamente no existe en Free, que
// tiene 3 usuarios y en la práctica un solo equipo.
//
// Deliberadamente NO duplica el panel de atención de arriba. Aquel responde
// «¿qué tengo que hacer?» con filas accionables; éste responde «¿cómo está
// repartida mi flota?». Si los dos dijeran lo mismo sobraría uno.

// El punto pinta la SALUD del cluster, no el enlace: es la primera lectura de
// la fila y tiene que coincidir con lo que dice el Overview de ese cluster.
// El estado del enlace no se pierde — sale escrito en la señal («unreachable»,
// «no metrics arriving»), que es donde una palabra distingue lo que un color
// no puede.

// Cuántas filas enseña cada equipo antes de remitir a Fleet.
//
// Cinco entra en una tarjeta sin scroll y basta para reconocer el patrón de un
// equipo. Más no informa: si un equipo tiene doce clusters con problemas, el
// dato es «doce», no la lista — y la lista ya existe, a un clic.
const ROWS_PER_GROUP = 5

function num(v: number | null): string {
  return v === null ? '—' : Math.round(v).toLocaleString()
}

function money(v: number | null): string {
  return v === null ? '—' : `$${Math.round(v).toLocaleString()}`
}

export function FleetBreakdown({
  clusters,
  byCluster,
  bySeverityCluster,
  insightsByCluster,
  teams,
}: {
  clusters: ClusterInfo[]
  byCluster: Record<string, FleetClusterRollup>
  bySeverityCluster?: Record<string, Record<string, number>>
  /** Insights activos por cluster — la salud REAL, misma fuente que Overview. */
  insightsByCluster?: Record<string, InsightCounts>
  teams: TeamBrief[]
}) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  // Entrar a un cluster desde aquí es lo MISMO que hace Fleet: cambiar el
  // activo y navegar a su portada. Se replica el patrón en vez de inventar otro
  // porque el switch tiene una secuencia que importa —cancelar el overview en
  // vuelo antes de refrescar, o React Query devuelve el del cluster anterior—
  // y una segunda versión sin ese cuidado enseñaría datos del cluster de antes.
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
    onSuccess: () => {
      queryClient.invalidateQueries()
      navigate('/')
    },
    onError: () => queryClient.invalidateQueries(),
  })

  const rows = buildRows(clusters, byCluster, bySeverityCluster, insightsByCluster)
  const groups = groupByTeam(rows, teams)

  const open = (c: ClusterInfo) => {
    if (c.active) navigate('/')
    else switchMutation.mutate(c.context)
  }

  return (
    <section>
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-sm font-semibold text-kb-text-primary flex items-center gap-2">
          <span className="w-5 h-5 rounded-md bg-kb-accent-light flex items-center justify-center text-kb-accent">
            <Boxes className="w-3 h-3" />
          </span>
          Your fleet
        </h2>
        <span className="text-[11px] font-mono text-kb-text-tertiary">
          {rows.length} {rows.length === 1 ? 'cluster' : 'clusters'} · worst first
        </span>
      </div>

      {/* Un bloque POR EQUIPO en rejilla, no una tabla de ancho completo.
          A ~1900px una fila de cuatro columnas obliga al ojo a cruzar un vacío
          entre el nombre y las cifras: es una tabla sin serlo. En bloques de
          media pantalla el nombre y su señal caben juntos, el equipo pasa a ser
          la unidad —que es de lo que trata este corte— y escala igual con 2
          equipos que con 6, donde una lista plana ya sería scroll. */}
      {/* items-start: sin él la rejilla estira los dos bloques a la altura del
          más alto, y un equipo con doce clusters dejaba al de al lado como una
          tarjeta de dos filas con medio metro de vacío debajo. */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-3 items-start">
        {groups.map((g) => (
          <div key={g.teamId || '_unassigned'} className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
            <div className="px-3.5 py-2 border-b border-kb-border flex items-center justify-between gap-2">
              <span className="text-[10px] font-mono uppercase tracking-[0.1em] text-kb-text-secondary truncate">
                {g.teamName}
              </span>
              {/* Se cuentan CLUSTERS que piden atención, no incidencias: «2 de
                  5» es la unidad en la que un responsable piensa, y sumar
                  avisos mezclaría un cluster caído con otro cuyo agente sólo se
                  ausentó. En calma sólo el recuento — «1 healthy» repetido en
                  cada bloque es ruido que enseña a no mirar la esquina. */}
              <span className="shrink-0 text-[10px] font-mono tabular-nums">
                {g.summary.needAttention > 0 ? (
                  <span className="text-status-warn">
                    {g.summary.needAttention}/{g.summary.clusters} need attention
                  </span>
                ) : (
                  <span className="text-kb-text-tertiary">
                    {g.summary.clusters} {g.summary.clusters === 1 ? 'cluster' : 'clusters'}
                  </span>
                )}
                {g.summary.critical > 0 && (
                  <span className="text-status-error"> · {g.summary.critical} crit</span>
                )}
              </span>
            </div>
            {/* Home muestra una MUESTRA, no el inventario.
                Con 14 clusters el bloque de sin-asignar traía doce filas y
                convertía la portada en una lista de scroll — que es el trabajo
                de Fleet, no el de la página que responde «qué me necesita».
                Las filas ya vienen peor-primero, así que las que se cortan son
                siempre las que menos piden; y el pie dice cuántas faltan en vez
                de esconderlas, con la puerta a Fleet al lado. */}
            <div className="divide-y divide-kb-border">
              {g.rows.slice(0, ROWS_PER_GROUP).map((r) => (
                <Row
                  key={r.cluster.context}
                  row={r}
                  disabled={switchMutation.isPending}
                  onOpen={() => open(r.cluster)}
                />
              ))}
            </div>
            {g.rows.length > ROWS_PER_GROUP && (
              <Link
                to="/fleet"
                className="block px-3.5 py-2 border-t border-kb-border text-[10px] font-mono text-kb-text-tertiary hover:text-kb-accent transition-colors"
              >
                +{g.rows.length - ROWS_PER_GROUP} more in Fleet →
              </Link>
            )}
          </div>
        ))}
      </div>
    </section>
  )
}

/**
 * La señal de la derecha: UNA, la que manda.
 *
 * La versión anterior alineaba cuatro columnas —motivo, hallazgos, pods/nodos y
 * coste— y a ancho completo eso deja de ser una fila para ser una tabla, con el
 * ojo cruzando un vacío para emparejar nombre y número. Aquí se elige por
 * prioridad: si el cluster no responde eso es lo único que importa; si responde
 * pero tiene críticos, esos; si no, su tamaño, que es la lectura tranquila.
 *
 * Los hechos tranquilos —tamaño, gasto, latido— bajan a su propia línea (ver
 * facts): mezclados aquí competían con la alarma, que es lo único que decide si
 * esta fila te interesa ahora.
 */
function signal(row: FleetRow): { text: string; className: string } {
  if (row.reason) {
    return {
      text: row.reason,
      className: row.health === 'crit' ? 'text-status-error' : 'text-status-warn',
    }
  }
  // Salud del cluster ANTES que sus hallazgos: un crash-loop activo pide una
  // respuesta hoy, y un CVE crítico pide una planificada. El orden de la señal
  // es el orden en que hay que atenderlo.
  if (row.verdict === 'critical' || row.verdict === 'warning') {
    return {
      text: healthLabel(row.verdict, row.insights).toLowerCase(),
      className: row.verdict === 'critical' ? 'text-status-error' : 'text-status-warn',
    }
  }
  if (row.critical > 0) return { text: `${row.critical} crit`, className: 'text-status-error' }
  if (row.high > 0) return { text: `${row.high} high`, className: 'text-status-warn' }
  // Sano y sin hallazgos: la fila no necesita etiqueta. El punto verde y la
  // línea de hechos de abajo ya lo dicen, y un «healthy» en cada fila es la
  // clase de texto tranquilizador que enseña a no leer esa columna.
  return { text: '', className: '' }
}

/** "4s" / "12m" / "3h" — compacto, para la línea de estado del agente. */
function ago(iso?: string): string {
  if (!iso) return '—'
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return `${Math.floor(s)}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

/**
 * facts — la línea tranquila: tamaño, gasto y latido del agente.
 *
 * Es el contenido de las tarjetas del diseño de Fleet, comprimido a una línea
 * porque aquí el bloque es de media pantalla y una rejilla 2×2 por cluster
 * haría la sección más alta que la propia lista de atención.
 *
 * El latido del agente NO estaba y es de lo más útil del diseño original: un
 * cluster «sano» cuyo agente lleva callado dos horas no se distingue de uno
 * vivo por ningún otro dato de esta fila, y es la causa más común de que los
 * paneles salgan vacíos.
 *
 * Lo que NO se puede pintar todavía, y por qué —para que no se pida dos veces—:
 *   · CIS %: `/findings` agrega `byKind` a nivel ORG, no por cluster. Necesita
 *     un `byKindCluster` en el backend, no un cambio de UI.
 *   · «EKS · us-east-1 · v1.29»: provider/region/version viven en
 *     ClusterOverview, que exige cluster CONECTADO; la lista de flota se pinta
 *     a propósito sin conectar a ninguno.
 *   · «▲8% MoM» del gasto: hace falta histórico de coste, que es la misma
 *     pieza que faltaba para los deltas de seguridad.
 */
function facts(row: FleetRow): string {
  const parts: string[] = []
  if (row.pods !== null) parts.push(`${num(row.pods)} pods`)
  if (row.nodes !== null) parts.push(`${num(row.nodes)}n`)
  if (row.costMonthly !== null) {
    // El delta se pega al gasto, no va suelto: «$743 ▲8%» es una frase, y un
    // «▲8%» en su propia columna obliga a adivinar de qué es el 8%.
    parts.push(money(row.costMonthly) + costDeltaSuffix(row.costDelta))
  }
  parts.push(
    row.cluster.agentConnected
      ? `live ${ago(row.cluster.lastSeen)}`
      : row.cluster.lastSeen
        ? `seen ${ago(row.cluster.lastSeen)} ago`
        : 'no agent',
  )
  return parts.join(' · ')
}

/**
 * El movimiento del gasto, o nada.
 *
 * Se omite por debajo del 2%: el coste oscila solo con el reciclaje de nodos y
 * los precios spot, así que un «▲1%» permanente es ruido que enseña a ignorar
 * la flecha justo antes de que aparezca la que importa.
 *
 * Y se omite entero cuando no hay referencia —cluster nuevo, o retención más
 * corta que la ventana—, en vez de imprimir 0%: un cero afirma que el gasto no
 * se movió, y la verdad es que no había con qué comparar.
 */
function costDeltaSuffix(delta: number | null): string {
  if (delta === null || Math.abs(delta) < 0.02) return ''
  return ` ${delta > 0 ? '▲' : '▼'}${Math.abs(Math.round(delta * 100))}%`
}

function Row({ row, disabled, onOpen }: { row: FleetRow; disabled: boolean; onOpen: () => void }) {
  const name = parseClusterDisplayName(row.cluster)
  const s = signal(row)
  return (
    <button
      type="button"
      onClick={onOpen}
      disabled={disabled}
      title={`Open ${name}`}
      className="w-full text-left px-3.5 py-2 flex items-start gap-2.5 hover:bg-kb-card-hover transition-colors disabled:opacity-50 group"
    >
      <span className={`w-1.5 h-1.5 rounded-full shrink-0 mt-[7px] ${HEALTH_RAIL_CLASS[row.verdict]}`} />

      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          {/* El proveedor identifica el cluster antes que su nombre: en una
              flota, «prod-eu» dice poco y un icono de AWS junto a él dice
              dónde vive. Se omite cuando el backend no lo ha resuelto — nunca
              se cae a un proveedor por defecto. */}
          {row.cluster.cloudProvider && (
            <span
              className="shrink-0 text-kb-text-tertiary"
              title={providerLabel(row.cluster.cloudProvider)}
            >
              <CloudProviderIcon provider={row.cluster.cloudProvider} className="w-3.5 h-3.5" />
            </span>
          )}
          <span className="min-w-0 flex-1 text-xs text-kb-text-primary truncate group-hover:text-kb-accent transition-colors">
            {name}
          </span>
          {/* La señal de alarma se queda arriba, junto al nombre: es lo que
              decide si esta fila te interesa. El motivo se IMPRIME en vez de
              deducirse del color — «unreachable» y «no metrics arriving» piden
              acciones distintas y un punto ámbar no las distingue. */}
          <span className={`shrink-0 text-[10px] font-mono tabular-nums ${s.className}`}>
            {s.text}
          </span>
        </span>
        {/* Y debajo los hechos tranquilos, en una sola línea. */}
        <span className="block text-[10px] font-mono text-kb-text-tertiary truncate mt-0.5">
          {[row.cluster.region, row.cluster.kubernetesVersion].filter(Boolean).join(' · ')}
          {(row.cluster.region || row.cluster.kubernetesVersion) && ' · '}
          {facts(row)}
        </span>
      </span>

      <ChevronRight className="w-3 h-3 shrink-0 mt-[5px] text-kb-text-tertiary" />
    </button>
  )
}

export { sortRows }
