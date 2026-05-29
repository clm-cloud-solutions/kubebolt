import { useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Package, ArrowLeft, Loader2, AlertTriangle } from 'lucide-react'
import { api } from '@/services/api'
import { HelmStatusBadge } from '@/pages/ApplicationsPage'

type Tab = 'overview' | 'values' | 'manifest' | 'history' | 'dependencies'

// HelmReleaseDetailPage shows one Helm release read-only (Sprint 4): values,
// rendered manifest, revision history, chart dependencies. No write actions.
export function HelmReleaseDetailPage() {
  const { namespace = '', name = '' } = useParams()
  const [tab, setTab] = useState<Tab>('overview')

  const { data, isLoading, error } = useQuery({
    queryKey: ['helm-release', namespace, name],
    queryFn: () => api.getHelmRelease(namespace, name),
    refetchInterval: 30_000,
  })

  if (isLoading) {
    return (
      <div className="p-6 flex items-center gap-2 text-sm text-kb-text-secondary">
        <Loader2 className="w-4 h-4 animate-spin" /> Loading release…
      </div>
    )
  }
  if (error || !data) {
    return (
      <div className="p-6 max-w-3xl mx-auto">
        <BackLink />
        <div className="flex items-start gap-2 text-sm text-status-error mt-4">
          <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
          <span>Helm release {namespace}/{name} not found.</span>
        </div>
      </div>
    )
  }

  const tabs: { id: Tab; label: string; show: boolean }[] = [
    { id: 'overview', label: 'Overview', show: true },
    { id: 'values', label: 'Values', show: true },
    { id: 'manifest', label: 'Manifest', show: !!data.manifest },
    { id: 'history', label: `History${data.history ? ` (${data.history.length})` : ''}`, show: !!data.history?.length },
    { id: 'dependencies', label: 'Dependencies', show: !!data.dependencies?.length },
  ]

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <BackLink />
      <div className="flex items-center gap-2 mt-3 mb-1">
        <Package className="w-5 h-5 text-kb-accent" />
        <h1 className="text-lg font-semibold text-kb-text-primary">{data.name}</h1>
        <HelmStatusBadge status={data.status} />
      </div>
      <div className="text-xs text-kb-text-secondary mb-4 font-mono">
        {data.namespace} · {data.chart} {data.chartVersion}
        {data.appVersion ? ` · app ${data.appVersion}` : ''} · rev {data.revision}
      </div>

      <div className="flex gap-1 border-b border-kb-border mb-4">
        {tabs.filter((t) => t.show).map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={`px-3 py-1.5 text-xs font-medium border-b-2 -mb-px transition-colors ${
              tab === t.id
                ? 'border-kb-accent text-kb-text-primary'
                : 'border-transparent text-kb-text-secondary hover:text-kb-text-primary'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === 'overview' && (
        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm max-w-2xl">
          <Field label="Status"><HelmStatusBadge status={data.status} /></Field>
          <Field label="Revision">{data.revision}</Field>
          <Field label="Chart">{data.chart} {data.chartVersion}</Field>
          <Field label="App version">{data.appVersion || '—'}</Field>
          <Field label="Namespace">{data.namespace}</Field>
          <Field label="Updated">{data.updated ? new Date(data.updated).toLocaleString() : '—'}</Field>
          <Field label="First deployed">{data.firstDeployed ? new Date(data.firstDeployed).toLocaleString() : '—'}</Field>
          {data.description ? <Field label="Description" wide>{data.description}</Field> : null}
          {data.notes ? (
            <div className="col-span-2 mt-2">
              <div className="text-[11px] uppercase tracking-wider text-kb-text-tertiary mb-1">Notes</div>
              <CodeBlock text={data.notes} />
            </div>
          ) : null}
        </dl>
      )}

      {tab === 'values' && (
        <CodeBlock
          text={data.values && Object.keys(data.values).length ? JSON.stringify(data.values, null, 2) : '# No user-supplied values (chart defaults in effect)'}
        />
      )}

      {tab === 'manifest' && <CodeBlock text={data.manifest || ''} />}

      {tab === 'history' && (
        <div className="border border-kb-border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-kb-elevated text-kb-text-secondary text-[11px] uppercase tracking-wider">
              <tr>
                <th className="text-left font-medium px-3 py-2">Rev</th>
                <th className="text-left font-medium px-3 py-2">Status</th>
                <th className="text-left font-medium px-3 py-2">Chart</th>
                <th className="text-left font-medium px-3 py-2">Updated</th>
                <th className="text-left font-medium px-3 py-2">Description</th>
              </tr>
            </thead>
            <tbody>
              {data.history?.map((h) => (
                <tr key={h.revision} className="border-t border-kb-border">
                  <td className="px-3 py-2 font-mono text-xs">{h.revision}</td>
                  <td className="px-3 py-2"><HelmStatusBadge status={h.status} /></td>
                  <td className="px-3 py-2 text-kb-text-secondary font-mono text-xs">{h.chartVersion}</td>
                  <td className="px-3 py-2 text-kb-text-tertiary text-xs">{h.updated ? new Date(h.updated).toLocaleString() : '—'}</td>
                  <td className="px-3 py-2 text-kb-text-secondary text-xs">{h.description || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {tab === 'dependencies' && (
        <ul className="flex flex-col gap-1.5 text-sm">
          {data.dependencies?.map((d, i) => (
            <li key={i} className="flex items-center gap-2 border border-kb-border rounded px-3 py-2">
              <span className="font-medium text-kb-text-primary">{d.name}</span>
              {d.version ? <span className="font-mono text-xs text-kb-text-secondary">{d.version}</span> : null}
              {d.repository ? <span className="font-mono text-[11px] text-kb-text-tertiary truncate">{d.repository}</span> : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function BackLink() {
  return (
    <Link to="/applications" className="inline-flex items-center gap-1 text-xs text-kb-text-secondary hover:text-kb-accent">
      <ArrowLeft className="w-3.5 h-3.5" /> Applications
    </Link>
  )
}

function Field({ label, children, wide }: { label: string; children: React.ReactNode; wide?: boolean }) {
  return (
    <div className={wide ? 'col-span-2' : ''}>
      <dt className="text-[11px] uppercase tracking-wider text-kb-text-tertiary">{label}</dt>
      <dd className="text-kb-text-primary mt-0.5">{children}</dd>
    </div>
  )
}

function CodeBlock({ text }: { text: string }) {
  return (
    <pre className="bg-kb-bg border border-kb-border rounded-lg p-3 text-xs font-mono text-kb-text-primary overflow-auto max-h-[60vh] whitespace-pre-wrap break-words">
      {text}
    </pre>
  )
}
