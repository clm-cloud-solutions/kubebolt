import { useState, useMemo, useEffect } from 'react'
import { useParams, useSearchParams, Link } from 'react-router-dom'
import { type ColumnDef } from '@tanstack/react-table'
import { ChevronLeft, ChevronRight, Filter, X } from 'lucide-react'
import { useResources } from '@/hooks/useResources'
import { ResourceTable } from './ResourceTable'
import { FilterBar } from './FilterBar'
import { StatusBadge } from './StatusBadge'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { PermissionDenied } from '@/components/shared/PermissionDenied'
import { ApiError } from '@/services/api'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { formatAge } from '@/utils/formatters'
import { ResourceUsageCell } from '@/components/shared/ResourceUsageCell'
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

function CpuCell({ item }: { item: ResourceItem }) {
  return (
    <ResourceUsageCell
      usage={Number(item.cpuUsage ?? 0)}
      request={Number(item.cpuRequest ?? 0)}
      limit={Number(item.cpuLimit ?? 0)}
      percent={Number(item.cpuPercent ?? 0)}
      type="cpu"
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
          return (
            <span className={`text-[11px] font-mono ${v > 0 ? 'text-status-error' : 'text-kb-text-secondary'}`}>
              {v}
            </span>
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
      { accessorKey: 'lastSchedule', header: 'Last Run', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> }
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
        <h1 className="text-lg font-semibold text-kb-text-primary">{label}</h1>
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
        <ResourceTable data={items} columns={columns} resourceType={resourceType} />
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
