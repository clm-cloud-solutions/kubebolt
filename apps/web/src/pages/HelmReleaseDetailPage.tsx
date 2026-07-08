import { useState, useMemo, useRef, useEffect } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  Package, ArrowLeft, Loader2, AlertTriangle,
  Search, X, ChevronDown, ChevronRight, Clipboard, Check, ExternalLink, Boxes,
} from 'lucide-react'
import { api } from '@/services/api'
import { HelmStatusBadge } from '@/pages/ApplicationsPage'
import { YamlViewer } from '@/components/shared/YamlViewer'
import { ResourceTypeIcon } from '@/utils/resourceIcons'
import { formatAge } from '@/utils/formatters'

type Tab = 'overview' | 'values' | 'manifest' | 'history' | 'dependencies' | 'related'

// HelmReleaseDetailPage shows one Helm release read-only (Sprint 4). Conforms
// to the shared ResourceDetailPage design (space-y-4, Section + InfoField,
// matching tab bar) and adds a Related tab linking to the release's rendered
// objects.
export function HelmReleaseDetailPage() {
  const { namespace = '', name = '' } = useParams()
  const [tab, setTab] = useState<Tab>('overview')

  // Cluster-scoped: the same release coords can exist in another cluster, so key
  // by the ACTIVE cluster — otherwise switching shows the previous cluster's
  // release while the live (over-the-agent-proxy) Secrets List refetches.
  const { data: clusters } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })
  const activeCluster = clusters?.find((c) => c.active)?.context ?? 'none'

  const { data, isLoading, error } = useQuery({
    queryKey: ['helm-release', activeCluster, namespace, name],
    queryFn: () => api.getHelmRelease(namespace, name),
    refetchInterval: 30_000,
  })

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 text-sm text-kb-text-secondary">
        <Loader2 className="w-4 h-4 animate-spin" /> Loading release…
      </div>
    )
  }
  if (error || !data) {
    return (
      <div className="space-y-4">
        <BackLink />
        <div className="flex items-start gap-2 text-sm text-status-error">
          <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
          <span>
            Helm release {namespace}/{name} not found.
          </span>
        </div>
      </div>
    )
  }

  const related = data.manifest ? parseManifestObjects(data.manifest) : []
  const tabs: { id: Tab; label: string; show: boolean }[] = [
    { id: 'overview', label: 'Overview', show: true },
    { id: 'values', label: 'Values', show: true },
    { id: 'manifest', label: 'Manifest', show: !!data.manifest },
    { id: 'related', label: `Related${related.length ? ` (${related.length})` : ''}`, show: related.length > 0 },
    { id: 'history', label: `History${data.history ? ` (${data.history.length})` : ''}`, show: !!data.history?.length },
    { id: 'dependencies', label: 'Dependencies', show: !!data.dependencies?.length },
  ]

  return (
    <div className="space-y-4">
      <BackLink />
      <div>
        <div className="flex items-center gap-2">
          <Package className="w-5 h-5 text-kb-accent" />
          <h1 className="text-lg font-semibold text-kb-text-primary">{data.name}</h1>
          <HelmStatusBadge status={data.status} />
        </div>
        <div className="text-xs text-kb-text-tertiary font-mono mt-0.5">
          {data.namespace} · {data.chart} {data.chartVersion}
          {data.appVersion ? ` · app ${data.appVersion}` : ''} · rev {data.revision}
        </div>
      </div>

      <div className="flex gap-1 border-b border-kb-border">
        {tabs.filter((t) => t.show).map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={`px-3 py-2 text-xs font-semibold transition-colors relative ${
              tab === t.id
                ? 'text-kb-accent border-b-2 border-kb-accent -mb-px'
                : 'text-kb-text-secondary hover:text-kb-text-primary'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === 'overview' && (
        <div className="space-y-4">
          {/* Status Overview — stat cards, mirrors the workload detail. */}
          <Section title="Status Overview">
            <div className="grid grid-cols-4 gap-6">
              <Stat label="Status">
                <div className="flex items-center gap-2">
                  <span className={`w-2.5 h-2.5 rounded-full ${statusDot(data.status)}`} />
                  {data.status}
                </div>
              </Stat>
              <Stat label="Revision">{data.revision}</Stat>
              <Stat label="Chart">{data.chart} {data.chartVersion}</Stat>
              <Stat label="App version">{data.appVersion || '—'}</Stat>
            </div>
          </Section>

          <Section title="Release Information">
            <div className="grid grid-cols-2 gap-x-8 gap-y-4">
              <InfoField label="Namespace">{data.namespace}</InfoField>
              <InfoField label="Chart">{data.chart} {data.chartVersion}</InfoField>
              <InfoField label="App version">{data.appVersion || '—'}</InfoField>
              <InfoField label="Updated">
                {data.updated ? `${new Date(data.updated).toLocaleString()} (${formatAge(data.updated)})` : '—'}
              </InfoField>
              <InfoField label="First deployed">
                {data.firstDeployed ? new Date(data.firstDeployed).toLocaleString() : '—'}
              </InfoField>
              {data.description ? <InfoField label="Description">{data.description}</InfoField> : null}
            </div>
            {/* Counts — at-a-glance "how big / how much history". */}
            <div className="grid grid-cols-3 gap-x-8 gap-y-4 mt-5 pt-4 border-t border-kb-border">
              <InfoField label="Resources">{related.length}</InfoField>
              <InfoField label="Dependencies">{data.dependencies?.length ?? 0}</InfoField>
              <InfoField label="Revisions">{data.history?.length ?? 0}</InfoField>
            </div>
          </Section>

          {data.notes ? (
            <Section title="Notes">
              <CodeBlock text={data.notes} />
            </Section>
          ) : null}
        </div>
      )}

      {tab === 'values' && (
        <Section title="Values">
          <CodeBlock
            text={
              data.values && Object.keys(data.values).length
                ? JSON.stringify(data.values, null, 2)
                : '# No user-supplied values (chart defaults in effect)'
            }
          />
        </Section>
      )}

      {tab === 'manifest' && (
        <ManifestTab manifest={data.manifest || ''} releaseNamespace={data.namespace} />
      )}

      {tab === 'related' && (
        <Section title="Resources in this release">
          <table className="w-full text-[11px]">
            <thead>
              <tr className="text-kb-text-tertiary text-left">
                <th className="pb-2 font-normal">Kind</th>
                <th className="pb-2 font-normal">Name</th>
                <th className="pb-2 font-normal">Namespace</th>
              </tr>
            </thead>
            <tbody>
              {related.map((o, i) => {
                const route = kindToRoute[o.kind]
                const ns = o.namespace || data.namespace
                return (
                  <tr key={`${o.kind}-${o.name}-${i}`} className="border-t border-kb-border">
                    <td className="py-2">
                      <span className="px-2 py-0.5 rounded text-[9px] font-medium bg-status-info text-white">
                        {o.kind}
                      </span>
                    </td>
                    <td className="py-2">
                      {route ? (
                        <Link
                          to={`/${route}/${ns || '_'}/${o.name}`}
                          className="text-status-info hover:underline font-mono text-[11px]"
                        >
                          {o.name}
                        </Link>
                      ) : (
                        <span className="font-mono text-kb-text-secondary">{o.name}</span>
                      )}
                    </td>
                    <td className="py-2 text-kb-text-tertiary font-mono text-[10px]">{ns || '—'}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </Section>
      )}

      {tab === 'history' && (
        <Section title="Revision History">
          <table className="w-full text-[11px]">
            <thead>
              <tr className="text-kb-text-tertiary text-left">
                <th className="pb-2 font-normal">Rev</th>
                <th className="pb-2 font-normal">Status</th>
                <th className="pb-2 font-normal">Chart</th>
                <th className="pb-2 font-normal">Updated</th>
                <th className="pb-2 font-normal">Description</th>
              </tr>
            </thead>
            <tbody>
              {data.history?.map((h) => (
                <tr key={h.revision} className="border-t border-kb-border">
                  <td className="py-2 font-mono">{h.revision}</td>
                  <td className="py-2"><HelmStatusBadge status={h.status} /></td>
                  <td className="py-2 text-kb-text-secondary font-mono">{h.chartVersion}</td>
                  <td className="py-2 text-kb-text-tertiary">{h.updated ? new Date(h.updated).toLocaleString() : '—'}</td>
                  <td className="py-2 text-kb-text-secondary">{h.description || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {tab === 'dependencies' && (
        <Section title="Chart Dependencies">
          <ul className="flex flex-col gap-1.5 text-[12px]">
            {data.dependencies?.map((d, i) => (
              <li key={i} className="flex items-center gap-2">
                <span className="font-medium text-kb-text-primary">{d.name}</span>
                {d.version ? <span className="font-mono text-xs text-kb-text-secondary">{d.version}</span> : null}
                {d.repository ? (
                  <span className="font-mono text-[11px] text-kb-text-tertiary truncate">{d.repository}</span>
                ) : null}
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  )
}

function BackLink() {
  return (
    <Link
      to="/applications"
      className="inline-flex items-center gap-1 text-xs text-kb-text-secondary hover:text-kb-accent"
    >
      <ArrowLeft className="w-3.5 h-3.5" /> Applications
    </Link>
  )
}

// Section + InfoField mirror the shared ResourceDetailPage primitives (same
// classes) so the Helm detail page matches the rest of the app.
function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-5">
      <div className="text-sm font-semibold text-kb-text-primary mb-4">{title}</div>
      {children}
    </div>
  )
}

function InfoField({ label, children }: { label: string; children: React.ReactNode }) {
  if (children === undefined || children === null || children === '') return null
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wide text-kb-text-tertiary mb-0.5">{label}</div>
      <div className="text-[12px] text-kb-text-primary">{children}</div>
    </div>
  )
}

// Stat is the more prominent label/value card used in the Status Overview
// row (mirrors the workload detail's StatusOverview cells).
function Stat({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wide text-kb-text-tertiary mb-1">{label}</div>
      <div className="text-sm text-kb-text-primary font-medium">{children}</div>
    </div>
  )
}

// statusDot maps a Helm release status to the colored-dot class used in the
// Status Overview (same green/amber/red convention as workloads).
function statusDot(status: string): string {
  const s = status.toLowerCase()
  if (s === 'deployed') return 'bg-status-ok'
  if (s === 'failed') return 'bg-status-error'
  if (s.startsWith('pending') || s === 'uninstalling') return 'bg-status-warn'
  return 'bg-kb-text-tertiary'
}

function CodeBlock({ text }: { text: string }) {
  return (
    <pre className="bg-kb-bg border border-kb-border rounded-md p-3 text-xs font-mono text-kb-text-primary overflow-auto max-h-[60vh] whitespace-pre-wrap break-words">
      {text}
    </pre>
  )
}

// kindToRoute maps the kinds a Helm chart typically renders to their resource
// list/detail route, so the Related tab links to them. Kinds without a route
// (e.g. ClusterRole) render as plain text.
const kindToRoute: Record<string, string> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
  Pod: 'pods',
  Service: 'services',
  Ingress: 'ingresses',
  ConfigMap: 'configmaps',
  Secret: 'secrets',
  ServiceAccount: 'serviceaccounts',
  Job: 'jobs',
  CronJob: 'cronjobs',
  PersistentVolumeClaim: 'pvcs',
  HorizontalPodAutoscaler: 'hpas',
  NetworkPolicy: 'networkpolicies',
  CiliumNetworkPolicy: 'ciliumnetworkpolicies',
  CiliumClusterwideNetworkPolicy: 'ciliumclusterwidenetworkpolicies',
  PodDisruptionBudget: 'pdbs',
}

interface ManifestObject {
  kind: string
  name: string
  namespace?: string
}

// ManifestDoc is the per-resource superset the Manifest explorer needs: the
// same identity fields as ManifestObject plus the doc's verbatim YAML, the
// optional Helm "# Source:" path, and a stable gap-free index (key into the
// docs array for React keys + selection — never the raw name, which can
// collide across kinds/namespaces).
interface ManifestDoc extends ManifestObject {
  source?: string
  yaml: string
  idx: number
}

// parseManifestDocs does a lightweight, dependency-free parse of a
// Helm-rendered multi-doc manifest: split on '---' document separators and,
// per doc, pull the top-level `kind:`, the first metadata `name:`/`namespace:`
// (the metadata block sits at the top of each doc), the optional leading
// "# Source: chart/templates/x.yaml" comment, and keep the doc's raw YAML so
// each resource can be rendered on its own. The kind+name guard naturally
// drops the leading empty split segment and bare-comment fragments. Best-
// effort — good enough without a YAML library.
function parseManifestDocs(manifest: string): ManifestDoc[] {
  const out: ManifestDoc[] = []
  const docs = manifest.split(/^---\s*$/m)
  let idx = 0
  for (const doc of docs) {
    const kindM = doc.match(/^kind:\s*(\S+)\s*$/m)
    if (!kindM) continue
    const nameM = doc.match(/^\s{2,}name:\s*["']?([^"'\n]+?)["']?\s*$/m)
    if (!nameM) continue
    const nsM = doc.match(/^\s{2,}namespace:\s*["']?([^"'\n]+?)["']?\s*$/m)
    const srcM = doc.match(/^#\s*Source:\s*(.+?)\s*$/m)
    out.push({
      kind: kindM[1],
      name: nameM[1].trim(),
      namespace: nsM ? nsM[1].trim() : undefined,
      source: srcM ? srcM[1].trim() : undefined,
      // Verbatim per-doc slice, trimmed of leading/trailing blank lines only —
      // the "# Source:" line stays as line 1; not re-prefixed with '---' since
      // YamlViewer renders it standalone.
      yaml: doc.replace(/^\n+/, '').replace(/\n+$/, ''),
      idx: idx++,
    })
  }
  return out
}

// parseManifestObjects stays as a thin map-down over parseManifestDocs so the
// Related tab keeps consuming exactly {kind,name,namespace} — byte-for-byte
// the same behaviour, single source of truth for the regexes.
function parseManifestObjects(manifest: string): ManifestObject[] {
  return parseManifestDocs(manifest).map(({ kind, name, namespace }) => ({ kind, name, namespace }))
}

// KIND_ORDER groups the rail the way an operator scans a cluster: workloads
// first, then networking, then config/storage, then RBAC, then everything else
// alphabetically. Mirrors the rough top-to-bottom order of the Sidebar.
const KIND_ORDER = [
  'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Pod', 'Job', 'CronJob',
  'Service', 'Ingress', 'Gateway', 'HTTPRoute', 'NetworkPolicy',
  'ConfigMap', 'Secret', 'PersistentVolumeClaim', 'PersistentVolume', 'StorageClass',
  'ServiceAccount', 'Role', 'RoleBinding', 'ClusterRole', 'ClusterRoleBinding',
]
function kindRank(kind: string): number {
  const i = KIND_ORDER.indexOf(kind)
  return i >= 0 ? i : KIND_ORDER.length
}

interface KindGroupData {
  kind: string
  slug: string
  rows: ManifestDoc[]
}

// Rail width is user-resizable: drag the divider either way. Widen the rail to
// read long resource names (helm release + chart names concatenate into long
// identifiers that truncate at the default), or shrink it to give the YAML
// pane more room. Clamped to [RAIL_MIN, RAIL_MAX] so neither pane collapses;
// opens at RAIL_DEFAULT. Persisted across sessions.
const RAIL_MIN = 130
const RAIL_MAX = 520
const RAIL_DEFAULT = 260
const RAIL_WIDTH_KEY = 'kb-helm-manifest-rail-width'

// ManifestTab — browse the release's rendered resources one at a time instead
// of one giant YAML blob. Left rail = KubeBolt's own resource-list grammar
// (collapsible per-Kind groups with the tinted ResourceTypeIcon + count); right
// pane = a resource header (icon · Kind · ns/name + copy + the signature
// "Open live object" deep-link) over the shared YamlViewer scoped to that one
// document. Keyboard: ↑/↓ move selection, "/" focuses search.
function ManifestTab({ manifest, releaseNamespace }: { manifest: string; releaseNamespace: string }) {
  const docs = useMemo(() => parseManifestDocs(manifest), [manifest])
  const [selectedIdx, setSelectedIdx] = useState(0)
  const [query, setQuery] = useState('')
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set())
  const searchRef = useRef<HTMLInputElement>(null)
  const selectedRowRef = useRef<HTMLButtonElement>(null)

  // Resizable rail (drag the divider to widen the YAML pane).
  const [railWidth, setRailWidth] = useState<number>(() => {
    const stored = Number(localStorage.getItem(RAIL_WIDTH_KEY))
    return stored >= RAIL_MIN && stored <= RAIL_MAX ? stored : RAIL_DEFAULT
  })
  const [dragging, setDragging] = useState(false)
  const dragRef = useRef<{ startX: number; startW: number } | null>(null)
  const clampRail = (w: number) => Math.min(RAIL_MAX, Math.max(RAIL_MIN, w))

  // Reset selection when the manifest changes (navigating between releases).
  useEffect(() => { setSelectedIdx(0); setQuery('') }, [manifest])

  // Live drag: track the pointer on window so the resize keeps following even
  // when the cursor outruns the 8px handle. dragRef holds the gesture origin so
  // the move handler stays correct without re-subscribing on every width tick.
  useEffect(() => {
    if (!dragging) return
    function onMove(e: MouseEvent) {
      if (!dragRef.current) return
      setRailWidth(clampRail(dragRef.current.startW + (e.clientX - dragRef.current.startX)))
    }
    function onUp() { setDragging(false); dragRef.current = null }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
  }, [dragging])

  // Persist the chosen width so the layout sticks across sessions.
  useEffect(() => { localStorage.setItem(RAIL_WIDTH_KEY, String(railWidth)) }, [railWidth])

  // Group by kind, KIND_ORDER then alpha; rows alpha by name within a kind.
  const groups = useMemo<KindGroupData[]>(() => {
    const byKind = new Map<string, ManifestDoc[]>()
    for (const d of docs) {
      const arr = byKind.get(d.kind) ?? []
      arr.push(d)
      byKind.set(d.kind, arr)
    }
    return [...byKind.entries()]
      .map(([kind, rows]) => ({
        kind,
        slug: kindToRoute[kind] ?? '__unmapped',
        rows: [...rows].sort((a, b) => a.name.localeCompare(b.name)),
      }))
      .sort((a, b) => kindRank(a.kind) - kindRank(b.kind) || a.kind.localeCompare(b.kind))
  }, [docs])

  const q = query.trim().toLowerCase()
  const matches = (d: ManifestDoc) =>
    !q || `${d.name} ${d.kind} ${d.namespace ?? ''}`.toLowerCase().includes(q)

  // Visible groups after filter; a query force-expands matching groups and
  // hides zero-match ones.
  const visibleGroups = useMemo(
    () =>
      groups
        .map((g) => ({ ...g, rows: g.rows.filter(matches) }))
        .filter((g) => g.rows.length > 0),
    [groups, q],
  )
  const isOpen = (kind: string) => !!q || !collapsed.has(kind)
  // Flattened display order across OPEN groups — the order ↑/↓ walks.
  const flatRows = useMemo(
    () => visibleGroups.filter((g) => isOpen(g.kind)).flatMap((g) => g.rows),
    [visibleGroups, collapsed, q],
  )
  const matchCount = visibleGroups.reduce((n, g) => n + g.rows.length, 0)

  const selected = docs[selectedIdx] ?? docs[0]

  // Keep the selected row in view when arrow-keying through a long rail.
  useEffect(() => {
    selectedRowRef.current?.scrollIntoView({ block: 'nearest' })
  }, [selectedIdx])

  function toggle(kind: string) {
    setCollapsed((prev) => {
      const next = new Set(prev)
      next.has(kind) ? next.delete(kind) : next.add(kind)
      return next
    })
  }

  function startDrag(e: React.MouseEvent) {
    e.preventDefault()
    dragRef.current = { startX: e.clientX, startW: railWidth }
    setDragging(true)
  }
  // Keyboard resize when the divider is focused — ←/→ nudge by 16px.
  function onHandleKey(e: React.KeyboardEvent) {
    if (e.key === 'ArrowLeft') { e.preventDefault(); setRailWidth((w) => clampRail(w - 16)) }
    else if (e.key === 'ArrowRight') { e.preventDefault(); setRailWidth((w) => clampRail(w + 16)) }
  }

  function onListKeyDown(e: React.KeyboardEvent) {
    if (e.key === '/') { e.preventDefault(); searchRef.current?.focus(); return }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    if (flatRows.length === 0) return
    const cur = flatRows.findIndex((d) => d.idx === selectedIdx)
    const next =
      e.key === 'ArrowDown'
        ? Math.min(flatRows.length - 1, cur < 0 ? 0 : cur + 1)
        : Math.max(0, cur < 0 ? 0 : cur - 1)
    setSelectedIdx(flatRows[next].idx)
  }

  if (docs.length === 0) {
    return (
      <Section title="Rendered Manifest">
        <div className="flex flex-col items-center justify-center py-12 text-center">
          <Boxes className="w-8 h-8 text-kb-text-tertiary mb-2" />
          <div className="text-[12px] text-kb-text-secondary">This release rendered no resources.</div>
        </div>
      </Section>
    )
  }

  return (
    <Section title="Rendered Manifest">
      <div className={`flex h-[calc(100vh-360px)] min-h-[320px] ${dragging ? 'cursor-col-resize select-none' : ''}`}>
        {/* ── Left rail: search + kind-grouped resource list ──────── */}
        <div
          style={{ width: railWidth }}
          className="shrink-0 bg-kb-bg border border-kb-border rounded-lg flex flex-col h-full overflow-hidden"
        >
          <div className="p-2 border-b border-kb-border relative">
            <Search className="w-3.5 h-3.5 text-kb-text-tertiary absolute left-3.5 top-1/2 -translate-y-1/2 pointer-events-none" />
            <input
              ref={searchRef}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Filter resources…"
              className="w-full pl-7 pr-12 py-1.5 text-[11px] bg-kb-card border border-kb-border rounded-md text-kb-text-primary placeholder:text-kb-text-tertiary focus:border-kb-border-active focus:outline-none"
            />
            {q && (
              <>
                <span className="text-[10px] font-mono text-kb-text-tertiary absolute right-7 top-1/2 -translate-y-1/2 tabular-nums">
                  {matchCount}/{docs.length}
                </span>
                <button
                  onClick={() => { setQuery(''); searchRef.current?.focus() }}
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-kb-text-tertiary hover:text-kb-text-primary"
                  title="Clear filter"
                >
                  <X className="w-3 h-3" />
                </button>
              </>
            )}
          </div>

          <div
            role="listbox"
            tabIndex={0}
            onKeyDown={onListKeyDown}
            className="flex-1 overflow-y-auto px-1 py-1 space-y-0.5 focus:outline-none"
          >
            {visibleGroups.map((g) => {
              const open = isOpen(g.kind)
              return (
                <div key={g.kind}>
                  <button
                    onClick={() => toggle(g.kind)}
                    className="w-full flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-kb-card-hover text-left"
                  >
                    {open ? (
                      <ChevronDown className="w-3 h-3 text-kb-text-tertiary shrink-0" />
                    ) : (
                      <ChevronRight className="w-3 h-3 text-kb-text-tertiary shrink-0" />
                    )}
                    <ResourceTypeIcon type={g.slug} className="w-3.5 h-3.5 shrink-0" />
                    <span className="text-[11px] font-semibold text-kb-text-primary truncate">{g.kind}</span>
                    <span className="ml-auto px-1.5 rounded bg-kb-elevated text-[10px] text-kb-text-tertiary tabular-nums shrink-0">
                      {g.rows.length}
                    </span>
                  </button>
                  {open && (
                    <div className="mt-0.5 space-y-px">
                      {g.rows.map((d) => {
                        const isSel = d.idx === selectedIdx
                        return (
                          <button
                            key={d.idx}
                            ref={isSel ? selectedRowRef : undefined}
                            onClick={() => setSelectedIdx(d.idx)}
                            className={`w-full flex items-center gap-2 pl-7 pr-2 py-1 rounded-md border-l-2 transition-colors ${
                              isSel
                                ? 'bg-kb-accent-light border-kb-accent text-kb-accent'
                                : 'border-transparent text-kb-text-secondary hover:bg-kb-card-hover'
                            }`}
                          >
                            <span
                              className={`w-1.5 h-1.5 rounded-full shrink-0 ${
                                isSel ? 'bg-kb-accent' : 'border border-kb-text-tertiary'
                              }`}
                            />
                            <span className="font-mono text-[11px] truncate flex-1 text-left">{d.name}</span>
                            <NamespaceChip namespace={d.namespace} />
                          </button>
                        )
                      })}
                    </div>
                  )
                  }
                </div>
              )
            })}
            {matchCount === 0 && (
              <div className="px-2 py-6 text-center text-[11px] text-kb-text-tertiary">No resources match.</div>
            )}
          </div>
        </div>

        {/* ── Drag divider: widen the YAML pane by shrinking the rail ─ */}
        <div
          role="separator"
          aria-orientation="vertical"
          tabIndex={0}
          onMouseDown={startDrag}
          onKeyDown={onHandleKey}
          title="Drag to resize"
          className="group relative w-2 mx-1 shrink-0 cursor-col-resize flex items-stretch justify-center rounded focus:outline-none focus-visible:ring-1 focus-visible:ring-kb-accent"
        >
          <div className={`w-px transition-colors ${dragging ? 'bg-kb-accent' : 'bg-kb-border group-hover:bg-kb-border-active'}`} />
        </div>

        {/* ── Right detail: resource header + scoped YAML ──────────── */}
        {selected && (
          <div className="flex-1 min-w-0 flex flex-col border border-kb-border rounded-lg overflow-hidden h-full">
            <div className="px-3 py-2 border-b border-kb-border bg-kb-card-hover flex flex-col gap-1.5">
              <div className="flex items-center gap-2 min-w-0">
                <ResourceTypeIcon type={kindToRoute[selected.kind] ?? '__unmapped'} className="w-4 h-4 shrink-0" />
                <span className="text-[12px] font-semibold text-kb-text-primary shrink-0">{selected.kind}</span>
                <span className="text-kb-text-tertiary shrink-0">·</span>
                <span className="text-[11px] font-mono truncate min-w-0">
                  {selected.namespace && <span className="text-kb-text-tertiary">{selected.namespace}/</span>}
                  <span className="text-kb-text-primary">{selected.name}</span>
                </span>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-[10px] text-kb-text-tertiary tabular-nums">
                  {selected.yaml.split('\n').length} lines
                </span>
                <div className="ml-auto flex items-center gap-1.5">
                  <CopyButton text={selected.yaml} />
                  <LiveLink doc={selected} releaseNamespace={releaseNamespace} />
                </div>
              </div>
            </div>
            <YamlViewer text={selected.yaml} heightClass="flex-1 min-h-0" />
          </div>
        )}
      </div>
      <div className="mt-2 text-[10px] text-kb-text-tertiary">↑/↓ move · / search · drag the divider to widen the YAML</div>
    </Section>
  )
}

// NamespaceChip — namespace pill, or a dim "cluster" pill for cluster-scoped
// objects (no namespace). Honest framing: this is the rendered template, so
// the chip is identity, not live placement.
function NamespaceChip({ namespace }: { namespace?: string }) {
  return (
    <span
      className={`px-1.5 rounded text-[9px] font-mono shrink-0 ${
        namespace ? 'bg-kb-elevated text-kb-text-tertiary' : 'bg-kb-elevated/60 text-kb-text-tertiary/70 italic'
      }`}
    >
      {namespace || 'cluster'}
    </span>
  )
}

// CopyButton — copies the selected doc's YAML, swapping Clipboard→Check for ~2s.
function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard?.writeText(text).then(() => {
          setCopied(true)
          setTimeout(() => setCopied(false), 2000)
        }).catch(() => {})
      }}
      className="inline-flex items-center gap-1 px-2 py-0.5 text-[10px] rounded border border-kb-border text-kb-text-secondary hover:bg-kb-card-hover hover:text-kb-text-primary transition-colors"
      title="Copy this resource's YAML"
    >
      {copied ? <Check className="w-3 h-3 text-status-ok" /> : <Clipboard className="w-3 h-3" />}
      {copied ? 'Copied' : 'Copy'}
    </button>
  )
}

// LiveLink — the signature value-add: bridge the rendered manifest to the LIVE
// cluster object via the same kindToRoute + route format the Related tab uses.
// Kinds with no route (e.g. ClusterRole) render nothing.
function LiveLink({ doc, releaseNamespace }: { doc: ManifestDoc; releaseNamespace: string }) {
  const route = kindToRoute[doc.kind]
  if (!route) return null
  const ns = doc.namespace || releaseNamespace || '_'
  return (
    <Link
      to={`/${route}/${ns}/${doc.name}`}
      className="inline-flex items-center gap-1 px-2 py-0.5 text-[10px] rounded text-status-info hover:underline"
      title="Open the live cluster object"
    >
      <ExternalLink className="w-3 h-3" />
      Open live object
    </Link>
  )
}
