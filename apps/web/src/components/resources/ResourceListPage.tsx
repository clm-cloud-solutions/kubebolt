import { useState, useMemo, useEffect } from 'react'
import { useParams, Link } from 'react-router-dom'
import { type ColumnDef } from '@tanstack/react-table'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useResources } from '@/hooks/useResources'
import { ResourceTable } from './ResourceTable'
import { FilterBar } from './FilterBar'
import { StatusBadge } from './StatusBadge'
import { UsageBar } from './UsageBar'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { PermissionDenied } from '@/components/shared/PermissionDenied'
import { ApiError } from '@/services/api'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { formatAge, formatCPU, formatMemory } from '@/utils/formatters'
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

function CpuCell({ item }: { item: ResourceItem }) {
  const usage = Number(item.cpuUsage ?? 0)
  const percent = Number(item.cpuPercent ?? 0)
  if (usage === 0 && percent === 0) return <span className="text-kb-text-tertiary text-[11px] font-mono">—</span>
  return (
    <div className="flex items-center gap-1.5">
      <div className="w-14">
        <UsageBar percent={Math.max(percent, usage > 0 ? 2 : 0)} height={4} />
      </div>
      <span className="text-[11px] font-mono text-kb-text-secondary">{formatCPU(usage)}</span>
    </div>
  )
}

function MemCell({ item }: { item: ResourceItem }) {
  const usage = Number(item.memoryUsage ?? 0)
  const percent = Number(item.memoryPercent ?? 0)
  if (usage === 0 && percent === 0) return <span className="text-kb-text-tertiary text-[11px] font-mono">—</span>
  return (
    <div className="flex items-center gap-1.5">
      <div className="w-14">
        <UsageBar percent={Math.max(percent, usage > 0 ? 2 : 0)} height={4} />
      </div>
      <span className="text-[11px] font-mono text-kb-text-secondary">{formatMemory(usage)}</span>
    </div>
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
        id: 'cpu',
        header: 'CPU',
        cell: (info) => <CpuCell item={info.row.original} />,
      },
      {
        id: 'memory',
        header: 'Memory',
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
        cell: (info) => <CpuCell item={info.row.original} />,
      },
      {
        id: 'memory',
        header: 'Memory',
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
      { accessorKey: 'duration', header: 'Duration', cell: (info) => <span className="font-mono text-[11px] text-kb-text-secondary">{String(info.getValue() ?? '—')}</span> }
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

  const [namespace, setNamespace] = useState('')
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

  const { data, isLoading, error, refetch, dataUpdatedAt, isFetching } = useResources(resourceType, {
    namespace: namespace || undefined,
    search: debouncedSearch || undefined,
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
      <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
        <ResourceTable data={items} columns={columns} />
      </div>
      {totalPages > 1 && (
        <div className="flex items-center justify-between mt-3 px-1">
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
