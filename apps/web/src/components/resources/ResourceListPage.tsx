import { useState, useMemo, useEffect } from 'react'
import { useParams, useSearchParams, Link } from 'react-router-dom'
import { type ColumnDef } from '@tanstack/react-table'
import { ChevronLeft, ChevronRight, Filter, X, SearchX, Inbox } from 'lucide-react'
import { ResourceTypeIcon } from '@/utils/resourceIcons'
import { useResources } from '@/hooks/useResources'
import { ResourceTable } from './ResourceTable'
import { RestartHistorySparkline } from './RestartHistorySparkline'
import { FilterBar } from './FilterBar'
import { StatusBadge } from './StatusBadge'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { EmptyState } from '@/components/shared/EmptyState'
import { PermissionDenied } from '@/components/shared/PermissionDenied'
import { ApiError } from '@/services/api'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { formatAge } from '@/utils/formatters'
import { ResourceUsageCell } from '@/components/shared/ResourceUsageCell'
import { EndpointHealthCell } from './EndpointHealthCell'
import type { ResourceItem } from '@/types/kubernetes'

const PAGE_SIZE = 50
const MAX_KEYS_DISPLAY = 8

function TruncatedKeys({ value }: { value: string }) {
  const keys = value.split(',').map(k => k.trim()).filter(Boolean)
  if (keys.length === 0) return <span className="font-mono text-[11px] text-kb-text-tertiary">—</span>
  const shown = keys.slice(0, MAX_KEYS_DISPLAY)
  const remaining = keys.length - shown.length
  return (
    <span className="font-mono text-[11px] text-kb-text-secondary leading-relaxed">
      {shown.join(', ')}
      {remaining > 0 && <span className="text-kb-text-tertiary"> +{remaining} more</span>}
    </span>
  )
}

const resourceLabels: Record<string, string> = {
  pods: 'Pods',
  deployments: 'Deployments',
  statefulsets: 'StatefulSets',
  daemonsets: 'DaemonSets',
  jobs: 'Jobs',
  cronjobs: 'CronJobs',
  services: 'Services',
  ingresses: 'Ingresses',
  networkpolicies: 'Network Policies',
  gateways: 'Gateways',
  httproutes: 'HTTPRoutes',
  endpoints: 'Endpoints',
  pvcs: 'Persistent Volume Claims',
  pvs: 'Persistent Volumes',
  storageclasses: 'Storage Classes',
  configmaps: 'ConfigMaps',
  secrets: 'Secrets',
  hpas: 'Horizontal Pod Autoscalers',
}

// Sort key for CPU/Memory columns: absolute usage (millicores / bytes).
// We initially used cpuPercent/memoryPercent to surface "who's closest to
// limit", but it diverged from what the cell actually shows (bytes), so
// a 7.8 GiB pod with a generous limit ranked below a 358 MiB pod with a
// tight limit when sorting desc — confusing. Match the visible value.
function cpuSortValue(item: ResourceItem): number {
  return Number(item.cpuUsage ?? 0)
}
function memSortValue(item: ResourceItem): number {
  return Number(item.memoryUsage ?? 0)
}

// NodeCell: shows the node name as a link to the node detail (primary
// click) plus a filter affordance (secondary click) that scopes the
// current list to pods on that node via the ?node= query param. Hovering
// any pod row reveals the icon — discoverable but unobtrusive. Reusable
// for any future "filter by this column value" interaction.
function NodeCell({ node }: { node: string }) {
  const [searchParams, setSearchParams] = useSearchParams()
  if (!node) return <span className="text-[11px] font-mono text-kb-text-tertiary">—</span>
  const active = searchParams.get('node') === node
  return (
    <span className="inline-flex items-center gap-1 group">
      <Link to={`/nodes/_/${node}`} className="text-[11px] font-mono text-status-info hover:underline">
        {node}
      </Link>
      <button
        type="button"
        title={active ? 'Already filtering by this node' : `Filter pods on ${node}`}
        disabled={active}
        onClick={(e) => {
          e.preventDefault()
          const next = new URLSearchParams(searchParams)
          next.set('node', node)
          setSearchParams(next)
        }}
        className={`p-0.5 rounded transition-opacity ${
          active
            ? 'opacity-40 cursor-default'
            : 'opacity-0 group-hover:opacity-100 text-kb-text-tertiary hover:text-status-info hover:bg-kb-elevated'
        }`}
      >
        <Filter className="w-3 h-3" />
      </button>
    </span>
  )
}

// hasMetrics is derived from the presence of cpuUsage/memoryUsage
// fields on the row payload — backend only sets them when the
// metrics-server actually had a sample. This lets the cell render
// a literal "0" instead of "—" when usage is genuinely zero (e.g.
// SUCCEEDED Job pod whose container terminated), avoiding the
// asymmetry where Memory shows the last-cached value but CPU
// looks like "no data".
function rowHasMetrics(item: ResourceItem): boolean {
  return (
    (item as unknown as { cpuUsage?: number }).cpuUsage !== undefined ||
    (item as unknown as { memoryUsage?: number }).memoryUsage !== undefined
  )
}

function CpuCell({ item }: { item: ResourceItem }) {
  return (
    <ResourceUsageCell
      usage={Number(item.cpuUsage ?? 0)}
      request={Number(item.cpuRequest ?? 0)}
      limit={Number(item.cpuLimit ?? 0)}
      percent={Number(item.cpuPercent ?? 0)}
      type="cpu"
      hasMetrics={rowHasMetrics(item)}
    />
  )
}

function MemCell({ item }: { item: ResourceItem }) {
  return (
    <ResourceUsageCell
      usage={Number(item.memoryUsage ?? 0)}
      request={Number(item.memoryRequest ?? 0)}
      limit={Number(item.memoryLimit ?? 0)}
      percent={Number(item.memoryPercent ?? 0)}
      type="memory"
      hasMetrics={rowHasMetrics(item)}
    />
  )
}

function getColumns(resourceType: string): ColumnDef<ResourceItem, unknown>[] {
  const base: ColumnDef<ResourceItem, unknown>[] = [
    {
      accessorKey: 'name',
      header: 'Name',
      cell: (info) => {
        const item = info.row.original
        const ns = item.namespace || '_'
        return (
          <Link
            to={`/${resourceType}/${ns}/${info.getValue() as string}`}
            className="font-medium text-status-info hover:underline transition-colors"
          >
            {info.getValue() as string}
          </Link>
        )
      },
    },
    {
      accessorKey: 'namespace',
      header: 'Namespace',
      cell: (info) => (
        <span className="text-[10px] font-mono text-kb-text-tertiary">{(info.getValue() as string) || '—'}</span>
      ),
    },
    {
      accessorKey: 'status',
      header: 'Status',
      cell: (info) => <StatusBadge status={info.getValue() as string} />,
    },
  ]

  // Pods
  if (resourceType === 'pods') {
    base.push(
      {
        accessorKey: 'ready',
        header: 'Ready',
        cell: (info) => {
          const val = String(info.getValue() ?? '0/0')
          const [r, t] = val.split('/')
          const ok = r === t && t !== '0'
          return <StatusBadge status={ok ? 'Running' : 'Warning'} label={val} />
        },
      },
      {
        id: 'cpu',
        header: 'CPU',
        accessorFn: cpuSortValue,
        sortingFn: 'basic',
        cell: (info) => <CpuCell item={info.row.original} />,
      },
      {
        id: 'memory',
        header: 'Memory',
        accessorFn: memSortValue,
        sortingFn: 'basic',
        cell: (info) => <MemCell item={info.row.original} />,
      },
      {
        accessorKey: 'restarts',
        header: 'Restarts',
        cell: (info) => {
          const v = Number(info.getValue() ?? 0)
          const item = info.row.original
          // The component owns both the count and the recency icon —
          // count color depends on recency analysis, not on lifetime
          // alone, so a stable-now pod with high lifetime doesn't
          // scream red.
          return (
            <RestartHistorySparkline
              namespace={String(item.namespace ?? '')}
              pod={String(item.name ?? '')}
              variant="badge"
              lifetimeCount={v}
            />
          )
        },
      },
      {
        accessorKey: 'ip',
        header: 'IP',
        cell: (info) => <span className="text-[11px] font-mono text-kb-text-secondary">{String(info.getValue() ?? '—')}</span>,
      },
      {
        accessorKey: 'nodeName',
        header: 'Node',
        cell: (info) => <NodeCell node={(info.getValue() as string) || ''} />,
      }
    )
  }

  // Deployments, StatefulSets, DaemonSets
  if (['deployments', 'statefulsets', 'daemonsets'].includes(resourceType)) {
    base.push(
      {
        id: 'ready',
        header: 'Ready',
        cell: (info) => {
          const item = info.row.original
          const ready = item.readyReplicas ?? item.ready ?? 0
          const total = item.replicas ?? item.desired ?? 0
          const ok = Number(ready) >= Number(total)
          return (
            <StatusBadge
              status={ok ? 'Running' : 'Warning'}
              label={`${ready}/${total}`}
            />
          )
        },
      },
      {
        id: 'upToDate',
        header: 'Up-to-date',
        cell: (info) => (
          <span className="text-[11px] font-mono text-kb-text-secondary">
            {String(info.row.original.updatedReplicas ?? info.row.original.current ?? '—')}
          </span>
        ),
      },
      {
        id: 'cpu',
        header: 'CPU',
        accessorFn: cpuSortValue,
        sortingFn: 'basic',
        cell: (info) => <CpuCell item={info.row.original} />,
      },
      {
        id: 'memory',
        header: 'Memory',
        accessorFn: memSortValue,
        sortingFn: 'basic',
        cell: (info) => <MemCell item={info.row.original} />,
      }
    )
  }

  // Services
  if (resourceType === 'services') {
    base.push(
      {
        accessorKey: 'type',
        header: 'Type',
        cell: (info) => <span className="text-[11px] font-mono text-kb-text-secondary">{info.getValue() as string}</span>,
      },
      {
        id: 'endpoints',
        header: 'Endpoints',
        // Endpoint health is fetched by EndpointHealthCell via shared
        // TanStack Query cache, so all rows trigger one VM round-trip
        // total. Cell handles the "no data" case (KSM not scraping)
        // by rendering a neutral em-dash — no broken UI on clusters
        // without scrape sidecar.
        cell: (info) => {
          const item = info.row.original
          return (
            <EndpointHealthCell
              namespace={String(item.namespace ?? '')}
              name={String(item.name ?? '')}
              serviceType={String(item.type ?? '')}
              clusterIP={String(item.clusterIP ?? '')}
            />
          )
        },
      },
      {
        accessorKey: 'clusterIP',
        header: 'Cluster IP',
        cell: (info) => <span className="text-[11px] font-mono text-kb-text-secondary">{info.getValue() as string}</span>,
      },
      {
        accessorKey: 'ports',
        header: 'Ports',
        cell: (info) => {
          const ports = info.getValue()
          if (!ports || !Array.isArray(ports)) return <span className="text-[11px] font-mono text-kb-text-tertiary">—</span>
          const formatted = (ports as Array<{ port: number; protocol: string }>)
            .map((p) => `${p.port}/${p.protocol}`)
            .join(', ')
          return <span className="text-[11px] font-mono text-kb-text-secondary">{formatted}</span>
        },
      }
    )
  }

  // Ingresses
  if (resourceType === 'ingresses') {
    base.push(
      { accessorKey: 'hosts', header: 'Hosts' },
      { accessorKey: 'address', header: 'Address' }
    )
  }

  // NetworkPolicies — the operator-actionable signal at list scope
  // is "which pods does this policy gate (selector preview), and
  // what traffic does it claim to cover (policyTypes + rule
  // counts)". A typo'd / orphaned policy stands out because its
  // selector reads as "all pods (catch-all)" but it's surrounded
  // by zero-rule policyTypes — the insights tab will flag the
  // detail; the list view's job is to make the gap visible at a
  // glance.
  if (resourceType === 'networkpolicies') {
    base.push(
      {
        accessorKey: 'podSelector',
        header: 'Pod Selector',
        cell: (info) => {
          const v = String(info.getValue() ?? '—')
          // Catch-all rendered explicitly so the operator can spot
          // namespace-wide policies without opening the detail.
          const isCatchAll = v.startsWith('all pods')
          return (
            <span className={`font-mono text-[11px] ${isCatchAll ? 'text-status-warn' : 'text-kb-text-secondary'}`}>
              {v}
            </span>
          )
        },
      },
      {
        accessorKey: 'policyTypes',
        header: 'Types',
        cell: (info) => {
          const raw = info.getValue() as string[] | undefined
          if (!raw || raw.length === 0) return <span className="text-[11px] text-kb-text-tertiary">—</span>
          return (
            <span className="font-mono text-[11px] text-kb-text-secondary">{raw.join(', ')}</span>
          )
        },
      },
      {
        accessorKey: 'ingressRules',
        header: 'Ingress',
        cell: (info) => (
          <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? 0)}</span>
        ),
      },
      {
        accessorKey: 'egressRules',
        header: 'Egress',
        cell: (info) => (
          <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? 0)}</span>
        ),
      },
    )
  }

  if (resourceType === 'gateways') {
    base.push(
      { accessorKey: 'class', header: 'Class', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'address', header: 'Address', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary truncate block max-w-[250px]">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'listeners', header: 'Listeners', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> }
    )
  }

  if (resourceType === 'httproutes') {
    base.push(
      { accessorKey: 'hostnames', header: 'Hostnames', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'gateway', header: 'Gateway', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'backends', header: 'Backends', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> }
    )
  }

  // Jobs
  if (resourceType === 'jobs') {
    base.push(
      { accessorKey: 'completions', header: 'Completions', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'duration', header: 'Duration', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { id: 'cpu', header: 'CPU', accessorFn: cpuSortValue, sortingFn: 'basic', cell: (info) => <CpuCell item={info.row.original} /> },
      { id: 'memory', header: 'Memory', accessorFn: memSortValue, sortingFn: 'basic', cell: (info) => <MemCell item={info.row.original} /> }
    )
  }

  // CronJobs
  if (resourceType === 'cronjobs') {
    base.push(
      { accessorKey: 'schedule', header: 'Schedule', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      {
        accessorKey: 'lastSchedule',
        header: 'Last Run',
        // Append "ago" so the value reads as elapsed time, not as
        // a duration (which would be ambiguous next to the cron
        // syntax in the Schedule column). Hover shows the absolute
        // timestamp via title.
        cell: (info) => {
          const v = info.getValue() as string | undefined
          if (!v) return <span className="font-mono text-[11px] text-kb-text-tertiary">never</span>
          const row = info.row.original as unknown as { lastScheduleTime?: string }
          const tipDate = row.lastScheduleTime ? new Date(row.lastScheduleTime).toLocaleString() : ''
          return (
            <span className="font-mono text-[11px] text-kb-text-secondary" title={tipDate}>
              {v} ago
            </span>
          )
        },
      },
      // Suspended badge column — surfaces the suspend flag right
      // next to the schedule so the operator scans for paused crons
      // without opening the detail page. Empty cell when active so
      // the column doesn't add visual noise on healthy crons.
      {
        id: 'suspended',
        header: 'State',
        accessorFn: (row) => (row as unknown as { suspend?: boolean }).suspend === true,
        cell: (info) =>
          info.getValue() ? (
            <span
              className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-status-warn-dim text-status-warn uppercase tracking-wide"
              title="CronJob is suspended — scheduled runs will not fire"
            >
              Suspended
            </span>
          ) : (
            <span
              className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-status-ok-dim text-status-ok uppercase tracking-wide"
              title="CronJob is active — scheduled runs fire on cadence"
            >
              Active
            </span>
          ),
      }
    )
  }

  // PVCs/PVs
  if (['pvcs', 'pvs'].includes(resourceType)) {
    base.push(
      { accessorKey: 'capacity', header: 'Capacity', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'storageClass', header: 'Storage Class', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> }
    )
  }

  // ConfigMaps
  if (resourceType === 'configmaps') {
    base.push({
      accessorKey: 'keys',
      header: 'Keys',
      cell: (info) => <TruncatedKeys value={String(info.getValue() ?? '—')} />,
    })
  }

  // Secrets
  if (resourceType === 'secrets') {
    base.push(
      { accessorKey: 'type', header: 'Type', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'keys', header: 'Keys', cell: (info) => <TruncatedKeys value={String(info.getValue() ?? '—')} /> }
    )
  }

  // HPAs
  if (resourceType === 'hpas') {
    base.push(
      { accessorKey: 'minReplicas', header: 'Min', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'maxReplicas', header: 'Max', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> },
      { accessorKey: 'currentReplicas', header: 'Current', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> }
    )
  }

  // Always add age
  base.push({
    accessorKey: 'createdAt',
    header: 'Age',
    cell: (info) => {
      const val = info.getValue() as string
      return (
        <span className="text-[10px] font-mono text-kb-text-tertiary">
          {val ? formatAge(val) : (info.row.original.age as string) || '—'}
        </span>
      )
    },
  })

  return base
}

interface ResourceListPageProps {
  resourceType?: string
}

export function ResourceListPage({ resourceType: propType }: ResourceListPageProps) {
  const params = useParams<{ type: string }>()
  const resourceType = propType || params.type || 'pods'

  // URL drives the filter state for both `namespace` and `node` so
  // deep links land on a pre-filtered list and bookmarks are
  // shareable. `namespace` is set from NamespaceTiles on the
  // dashboard; `node` is set by NodeCell's filter button below and
  // cleared via the chip under the FilterBar. Search and page stay
  // local — they're transient interactions, not shareable views.
  const [searchParams, setSearchParams] = useSearchParams()
  const namespace = searchParams.get('namespace') ?? ''
  const node = searchParams.get('node') || ''
  const setNamespace = (v: string) => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (v) next.set('namespace', v)
        else next.delete('namespace')
        return next
      },
      { replace: true },
    )
  }
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [page, setPage] = useState(1)

  function resetPage() { setPage(1) }

  // Debounce search: update query param 300ms after last keystroke
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedSearch(search)
      resetPage()
    }, 300)
    return () => clearTimeout(timer)
  }, [search])

  // Reset to page 1 whenever the node filter changes — otherwise we'd
  // request "page 5 of an empty filtered list" and show nothing.
  useEffect(() => { resetPage() }, [node])

  const { data, isLoading, error, refetch, dataUpdatedAt, isFetching } = useResources(resourceType, {
    namespace: namespace || undefined,
    search: debouncedSearch || undefined,
    node: node || undefined,
    page,
    limit: PAGE_SIZE,
  })

  const columns = useMemo(() => getColumns(resourceType), [resourceType])

  const namespaces = useMemo(() => {
    if (!data?.items) return []
    const ns = new Set(data.items.map((i) => i.namespace).filter(Boolean))
    return Array.from(ns).sort()
  }, [data?.items])

  if (isLoading && !data) return <LoadingSpinner />
  if (error) {
    if (error instanceof ApiError && error.status === 403) {
      return <PermissionDenied resourceType={resourceType} />
    }
    return <ErrorState message={error.message} onRetry={() => refetch()} />
  }

  const items = data?.items || []
  const total = data?.total ?? items.length
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const label = resourceLabels[resourceType] || resourceType

  return (
    <div>
      <div className="flex items-center gap-3 mb-4">
        <div className="flex items-center gap-2">
          <ResourceTypeIcon type={resourceType} />
          <h1 className="text-lg font-semibold text-kb-text-primary">{label}</h1>
        </div>
        <span className="text-[10px] font-mono px-2.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary">
          {total} total
        </span>
        <div className="ml-auto">
          <DataFreshnessIndicator
            dataUpdatedAt={dataUpdatedAt}
            isFetching={isFetching}
          />
        </div>
      </div>
      <FilterBar
        namespaces={namespaces}
        selectedNamespace={namespace}
        onNamespaceChange={(v) => { setNamespace(v); resetPage() }}
        search={search}
        onSearchChange={setSearch}
        total={items.length}
        resourceName={label.toLowerCase()}
      />
      {node && (
        <div className="flex items-center gap-2 mb-2 px-1">
          <span className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Filter:</span>
          <button
            type="button"
            onClick={() => {
              const next = new URLSearchParams(searchParams)
              next.delete('node')
              setSearchParams(next)
            }}
            title="Clear node filter"
            className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full border border-status-info/40 bg-status-info-dim/30 text-[11px] font-mono text-status-info hover:bg-status-info-dim/50 transition-colors"
          >
            node: {node}
            <X className="w-3 h-3" />
          </button>
        </div>
      )}
      <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
        {items.length === 0 ? (() => {
          // Distinguish "filtered down to nothing" from "really nothing".
          // Filtered → tell the operator WHICH filters caused it and offer
          // a one-click recovery; un-filtered → neutral "no X in this
          // cluster" with no CTA (there's nothing for them to undo).
          const hasFilters = !!(search || namespace || node)
          const filterParts: string[] = []
          if (search) filterParts.push(`name matching "${search}"`)
          if (namespace) filterParts.push(`namespace ${namespace}`)
          if (node) filterParts.push(`node ${node}`)
          const clearFilters = () => {
            if (search) setSearch('')
            if (namespace) { setNamespace(''); resetPage() }
            if (node) {
              const next = new URLSearchParams(searchParams)
              next.delete('node')
              setSearchParams(next)
            }
          }
          return hasFilters ? (
            <EmptyState
              icon={<SearchX className="w-10 h-10" />}
              title={`No ${label.toLowerCase()} match these filters`}
              message={filterParts.join(' · ')}
              action={
                <button
                  type="button"
                  onClick={clearFilters}
                  className="px-3 py-1.5 text-xs font-medium bg-kb-elevated text-kb-text-primary rounded-md border border-kb-border hover:border-kb-border-active transition-colors"
                >
                  Clear filters
                </button>
              }
            />
          ) : (
            <EmptyState
              icon={<Inbox className="w-10 h-10" />}
              title={`No ${label.toLowerCase()} in this cluster`}
              message="When resources of this type get created, they will show up here."
            />
          )
        })() : (
          <ResourceTable data={items} columns={columns} resourceType={resourceType} />
        )}
      </div>
      {totalPages > 1 && (
        // Centered so the floating Kobi button (bottom-right) doesn't
        // overlap the Next-page control. Counter + controls share the
        // center row; right side stays clear for the Kobi sigil.
        <div className="flex items-center justify-center gap-4 mt-3 px-1">
          <span className="text-[11px] font-mono text-kb-text-tertiary">
            {(page - 1) * PAGE_SIZE + 1}–{Math.min(page * PAGE_SIZE, total)} of {total}
          </span>
          <div className="flex items-center gap-1">
            <button
              type="button"
              title="Previous page"
              onClick={() => setPage(p => Math.max(1, p - 1))}
              disabled={page === 1}
              className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
            >
              <ChevronLeft className="w-3.5 h-3.5" />
            </button>
            <span className="text-[11px] font-mono text-kb-text-secondary px-2">
              {page} / {totalPages}
            </span>
            <button
              type="button"
              title="Next page"
              onClick={() => setPage(p => Math.min(totalPages, p + 1))}
              disabled={page === totalPages}
              className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
            >
              <ChevronRight className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
