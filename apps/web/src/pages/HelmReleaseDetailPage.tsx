import { useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Package, ArrowLeft, Loader2, AlertTriangle } from 'lucide-react'
import { api } from '@/services/api'
import { HelmStatusBadge } from '@/pages/ApplicationsPage'

type Tab = 'overview' | 'values' | 'manifest' | 'history' | 'dependencies' | 'related'

// HelmReleaseDetailPage shows one Helm release read-only (Sprint 4). Conforms
// to the shared ResourceDetailPage design (space-y-4, Section + InfoField,
// matching tab bar) and adds a Related tab linking to the release's rendered
// objects.
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
        <Section title="Release Information">
          <div className="grid grid-cols-2 gap-x-8 gap-y-4">
            <InfoField label="Status"><HelmStatusBadge status={data.status} /></InfoField>
            <InfoField label="Revision">{data.revision}</InfoField>
            <InfoField label="Chart">{data.chart} {data.chartVersion}</InfoField>
            <InfoField label="App version">{data.appVersion || '—'}</InfoField>
            <InfoField label="Namespace">{data.namespace}</InfoField>
            <InfoField label="Updated">{data.updated ? new Date(data.updated).toLocaleString() : '—'}</InfoField>
            <InfoField label="First deployed">
              {data.firstDeployed ? new Date(data.firstDeployed).toLocaleString() : '—'}
            </InfoField>
            {data.description ? <InfoField label="Description">{data.description}</InfoField> : null}
          </div>
          {data.notes ? (
            <div className="mt-5">
              <div className="text-[10px] uppercase tracking-wide text-kb-text-tertiary mb-1">Notes</div>
              <CodeBlock text={data.notes} />
            </div>
          ) : null}
        </Section>
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
        <Section title="Rendered Manifest">
          <CodeBlock text={data.manifest || ''} />
        </Section>
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
  PodDisruptionBudget: 'pdbs',
}

interface ManifestObject {
  kind: string
  name: string
  namespace?: string
}

// parseManifestObjects does a lightweight, dependency-free parse of a
// Helm-rendered multi-doc manifest: split on '---' document separators and,
// per doc, pull the top-level `kind:` and the first metadata `name:` /
// `namespace:` (the metadata block sits at the top of each doc). Best-effort —
// good enough to link to the release's objects without a YAML library.
function parseManifestObjects(manifest: string): ManifestObject[] {
  const out: ManifestObject[] = []
  const docs = manifest.split(/^---\s*$/m)
  for (const doc of docs) {
    const kindM = doc.match(/^kind:\s*(\S+)\s*$/m)
    if (!kindM) continue
    const nameM = doc.match(/^\s{2,}name:\s*["']?([^"'\n]+?)["']?\s*$/m)
    if (!nameM) continue
    const nsM = doc.match(/^\s{2,}namespace:\s*["']?([^"'\n]+?)["']?\s*$/m)
    out.push({
      kind: kindM[1],
      name: nameM[1].trim(),
      namespace: nsM ? nsM[1].trim() : undefined,
    })
  }
  return out
}
