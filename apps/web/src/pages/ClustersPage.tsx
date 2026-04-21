import { useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { Server, Check, ArrowRightLeft, Shield, Activity, Box, Layers, HardDrive, AlertTriangle, Plus, Pencil, Trash2, Upload, FileText, X } from 'lucide-react'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { parseClusterDisplayName } from '@/utils/cluster'
import type { ClusterInfo, ClusterOverview, ClusterHealth } from '@/types/kubernetes'

function HealthDot({ status }: { status: 'connected' | 'disconnected' | 'error' | string }) {
  const color = status === 'connected' ? 'bg-status-ok'
    : status === 'error' ? 'bg-status-error'
    : 'bg-kb-text-tertiary'
  return (
    <span className="relative flex h-2.5 w-2.5">
      {status === 'connected' && (
        <span className={`animate-ping absolute inline-flex h-full w-full rounded-full ${color} opacity-40`} />
      )}
      <span className={`relative inline-flex rounded-full h-2.5 w-2.5 ${color}`} />
    </span>
  )
}

function StatItem({ icon, label, value }: { icon: React.ReactNode; label: string; value: string | number }) {
  return (
    <div className="flex items-center gap-2">
      <div className="text-kb-text-tertiary">{icon}</div>
      <div>
        <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-wider">{label}</div>
        <div className="text-sm font-mono text-kb-text-primary">{value}</div>
      </div>
    </div>
  )
}

function SourceBadge({ source }: { source?: string }) {
  if (source === 'uploaded') {
    return (
      <span className="px-2 py-0.5 rounded-full bg-kb-accent-light text-kb-accent text-[9px] font-mono font-semibold uppercase tracking-wider">
        Uploaded
      </span>
    )
  }
  if (source === 'in-cluster') {
    return (
      <span className="px-2 py-0.5 rounded-full bg-status-info-dim text-status-info text-[9px] font-mono font-semibold uppercase tracking-wider">
        In-Cluster
      </span>
    )
  }
  return null
}

interface ClusterCardProps {
  cluster: ClusterInfo
  overview?: ClusterOverview
  health?: ClusterHealth
  onSwitch: (context: string) => void
  onRename: (cluster: ClusterInfo) => void
  onDelete: (cluster: ClusterInfo) => void
  isSwitching: boolean
  canManage: boolean
}

function ClusterCard({
  cluster,
  overview,
  health,
  onSwitch,
  onRename,
  onDelete,
  isSwitching,
  canManage,
}: ClusterCardProps) {
  const isActive = cluster.active
  const isConnected = cluster.status === 'connected'
  const hasError = cluster.status === 'error'
  const isUploaded = cluster.source === 'uploaded'
  const displayName = parseClusterDisplayName(cluster)

  return (
    <div
      className={`bg-kb-card border rounded-xl p-5 transition-all ${
        isActive && isConnected
          ? 'border-kb-accent/30 ring-1 ring-kb-accent/10'
          : hasError
          ? 'border-status-error/30'
          : 'border-kb-border hover:border-kb-border-active'
      }`}
    >
      {/* Header */}
      <div className="flex items-start justify-between mb-4 gap-2">
        <div className="flex items-center gap-3 min-w-0 flex-1">
          <div className={`w-9 h-9 rounded-lg flex items-center justify-center shrink-0 ${
            isConnected ? 'bg-kb-accent-light' : hasError ? 'bg-status-error-dim' : 'bg-kb-elevated'
          }`}>
            <Server className={`w-4.5 h-4.5 ${
              isConnected ? 'text-kb-accent' : hasError ? 'text-status-error' : 'text-kb-text-tertiary'
            }`} />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5 flex-wrap">
              <span className="text-sm font-semibold text-kb-text-primary truncate">{displayName}</span>
              <SourceBadge source={cluster.source} />
            </div>
            <div className="text-[10px] font-mono text-kb-text-tertiary truncate" title={cluster.context}>{cluster.context}</div>
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {isActive && isConnected ? (
            <span className="flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-kb-accent-light text-kb-accent text-[10px] font-mono">
              <Check className="w-3 h-3" />
              Active
            </span>
          ) : isActive && hasError ? (
            <span className="flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-status-error-dim text-status-error text-[10px] font-mono">
              <AlertTriangle className="w-3 h-3" />
              Error
            </span>
          ) : (
            <button
              onClick={() => onSwitch(cluster.context)}
              disabled={isSwitching}
              className="flex items-center gap-1.5 px-3 py-1 rounded-lg border border-kb-border text-[11px] font-mono text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors disabled:opacity-50"
            >
              <ArrowRightLeft className="w-3 h-3" />
              Switch
            </button>
          )}
        </div>
      </div>

      {/* Server URL */}
      <div className="text-[10px] font-mono text-kb-text-tertiary mb-4 truncate" title={cluster.server}>
        {cluster.server}
      </div>

      {/* Connection error details */}
      {hasError && cluster.error && (
        <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim mb-4">
          <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
          <span className="text-[10px] font-mono text-status-error break-all">{cluster.error}</span>
        </div>
      )}

      {/* Active + connected cluster details */}
      {isActive && isConnected && overview && (
        <>
          <div className="flex items-center gap-2 mb-4 px-3 py-2 rounded-lg bg-kb-bg">
            <HealthDot status={cluster.status} />
            <span className="text-[11px] font-mono text-kb-text-primary capitalize">
              {health?.status || overview?.health?.status || 'connected'}
            </span>
            {health?.score !== undefined && (
              <span className="text-[10px] font-mono text-kb-text-tertiary ml-auto">
                Score: {health.score}/100
              </span>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3 mb-4">
            {overview.kubernetesVersion && (
              <StatItem icon={<Shield className="w-3.5 h-3.5" />} label="Version" value={overview.kubernetesVersion} />
            )}
            {overview.nodes && (
              <StatItem icon={<Server className="w-3.5 h-3.5" />} label="Nodes" value={`${overview.nodes.ready}/${overview.nodes.total}`} />
            )}
            {overview.pods && (
              <StatItem icon={<Box className="w-3.5 h-3.5" />} label="Pods" value={`${overview.pods.ready}/${overview.pods.total}`} />
            )}
            {overview.deployments && (
              <StatItem icon={<Layers className="w-3.5 h-3.5" />} label="Deployments" value={`${overview.deployments.ready}/${overview.deployments.total}`} />
            )}
            {overview.namespaces && (
              <StatItem icon={<Activity className="w-3.5 h-3.5" />} label="Namespaces" value={overview.namespaces.total} />
            )}
            {overview.pvcs && (
              <StatItem icon={<HardDrive className="w-3.5 h-3.5" />} label="PVCs" value={overview.pvcs.total} />
            )}
          </div>
        </>
      )}

      {/* Disconnected cluster */}
      {cluster.status === 'disconnected' && (
        <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-kb-bg mb-3">
          <div className="w-1.5 h-1.5 rounded-full bg-kb-text-tertiary" />
          <span className="text-[11px] font-mono text-kb-text-tertiary">Disconnected</span>
        </div>
      )}

      {/* Management actions (admin only) */}
      {canManage && (
        <div className="flex items-center gap-2 pt-3 border-t border-kb-border">
          <button
            onClick={() => onRename(cluster)}
            className="flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[10px] font-mono text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors"
            title="Rename cluster (display name only)"
          >
            <Pencil className="w-3 h-3" />
            Rename
          </button>
          {isUploaded && (
            <button
              onClick={() => onDelete(cluster)}
              className="flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[10px] font-mono text-status-error hover:bg-status-error-dim transition-colors ml-auto"
              title="Remove uploaded cluster"
            >
              <Trash2 className="w-3 h-3" />
              Delete
            </button>
          )}
        </div>
      )}
    </div>
  )
}

// --- Add Cluster Modal ---

function AddClusterModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [kubeconfig, setKubeconfig] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [uploading, setUploading] = useState(false)

  async function handleFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    if (file.size > 1024 * 1024) {
      setError('File too large (max 1MB)')
      return
    }
    const text = await file.text()
    setKubeconfig(text)
    setError(null)
  }

  async function handleSubmit() {
    if (!kubeconfig.trim()) {
      setError('Please paste a kubeconfig or choose a file')
      return
    }
    setUploading(true)
    setError(null)
    try {
      const result = await api.addCluster(kubeconfig)
      queryClient.invalidateQueries({ queryKey: ['clusters'] })
      onClose()
      console.log(`Added ${result.added.length} cluster context(s):`, result.added)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add cluster')
    } finally {
      setUploading(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="w-full max-w-2xl bg-kb-card border border-kb-border rounded-xl shadow-2xl" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-6 py-4 border-b border-kb-border">
          <div className="flex items-center gap-2">
            <Upload className="w-4 h-4 text-kb-accent" />
            <h2 className="text-sm font-semibold text-kb-text-primary">Add Cluster</h2>
          </div>
          <button onClick={onClose} className="text-kb-text-tertiary hover:text-kb-text-primary">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="p-6 space-y-4">
          <p className="text-xs text-kb-text-secondary">
            Paste the content of a kubeconfig file or choose a file to upload. All contexts in the file will be added.
          </p>

          <div className="flex items-center gap-2">
            <button
              onClick={() => fileInputRef.current?.click()}
              className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-xs text-kb-text-primary transition-colors border border-kb-border"
            >
              <FileText className="w-3.5 h-3.5" />
              Choose file...
            </button>
            <input
              ref={fileInputRef}
              type="file"
              accept=".yaml,.yml,.kubeconfig,text/yaml,application/yaml"
              onChange={handleFile}
              className="hidden"
            />
            <span className="text-[10px] font-mono text-kb-text-tertiary">or paste below</span>
          </div>

          <textarea
            value={kubeconfig}
            onChange={(e) => { setKubeconfig(e.target.value); setError(null) }}
            placeholder="apiVersion: v1&#10;kind: Config&#10;clusters:&#10;  - name: my-cluster&#10;    cluster:&#10;      server: https://..."
            className="w-full h-64 px-3 py-2 rounded-lg bg-kb-bg border border-kb-border text-[11px] font-mono text-kb-text-primary placeholder:text-kb-text-tertiary/50 focus:outline-none focus:border-kb-border-active resize-none"
          />

          {error && (
            <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim">
              <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
              <span className="text-[11px] text-status-error">{error}</span>
            </div>
          )}
        </div>

        <div className="flex items-center justify-end gap-2 px-6 py-4 border-t border-kb-border bg-kb-surface">
          <button
            onClick={onClose}
            className="px-4 py-1.5 rounded-lg text-xs text-kb-text-secondary hover:text-kb-text-primary transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleSubmit}
            disabled={uploading || !kubeconfig.trim()}
            className="flex items-center gap-1.5 px-4 py-1.5 rounded-lg bg-kb-accent text-white text-xs font-medium hover:bg-kb-accent-bright transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {uploading ? 'Uploading...' : 'Add Cluster'}
          </button>
        </div>
      </div>
    </div>
  )
}

// --- Rename Cluster Modal ---

function RenameClusterModal({ cluster, onClose }: { cluster: ClusterInfo; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [displayName, setDisplayName] = useState(cluster.displayName || '')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  async function handleSubmit() {
    setSaving(true)
    setError(null)
    try {
      await api.renameCluster(cluster.context, displayName)
      queryClient.invalidateQueries({ queryKey: ['clusters'] })
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to rename cluster')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="w-full max-w-md bg-kb-card border border-kb-border rounded-xl shadow-2xl" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-6 py-4 border-b border-kb-border">
          <div className="flex items-center gap-2">
            <Pencil className="w-4 h-4 text-kb-accent" />
            <h2 className="text-sm font-semibold text-kb-text-primary">Rename Cluster</h2>
          </div>
          <button onClick={onClose} className="text-kb-text-tertiary hover:text-kb-text-primary">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="p-6 space-y-4">
          <div>
            <label className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider block mb-1.5">Context</label>
            <div className="text-[11px] font-mono text-kb-text-primary px-3 py-2 rounded-lg bg-kb-bg border border-kb-border">
              {cluster.context}
            </div>
          </div>

          <div>
            <label className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider block mb-1.5">Display Name</label>
            <input
              type="text"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="e.g. Production EKS"
              maxLength={100}
              className="w-full px-3 py-2 rounded-lg bg-kb-bg border border-kb-border text-xs text-kb-text-primary placeholder:text-kb-text-tertiary focus:outline-none focus:border-kb-border-active"
              autoFocus
            />
            <p className="text-[10px] text-kb-text-tertiary mt-1.5">
              Leave blank to use the context name. This does not modify your kubeconfig file.
            </p>
          </div>

          {error && (
            <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim">
              <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
              <span className="text-[11px] text-status-error">{error}</span>
            </div>
          )}
        </div>

        <div className="flex items-center justify-end gap-2 px-6 py-4 border-t border-kb-border bg-kb-surface">
          <button onClick={onClose} className="px-4 py-1.5 rounded-lg text-xs text-kb-text-secondary hover:text-kb-text-primary transition-colors">
            Cancel
          </button>
          <button
            onClick={handleSubmit}
            disabled={saving}
            className="px-4 py-1.5 rounded-lg bg-kb-accent text-white text-xs font-medium hover:bg-kb-accent-bright transition-colors disabled:opacity-50"
          >
            {saving ? 'Saving...' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}

// --- Delete Cluster Modal ---

function DeleteClusterModal({ cluster, onClose }: { cluster: ClusterInfo; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [confirmText, setConfirmText] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [deleting, setDeleting] = useState(false)

  const isConfirmed = confirmText === cluster.context

  async function handleDelete() {
    setDeleting(true)
    setError(null)
    try {
      await api.deleteCluster(cluster.context)
      queryClient.invalidateQueries({ queryKey: ['clusters'] })
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete cluster')
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="w-full max-w-md bg-kb-card border border-kb-border rounded-xl shadow-2xl" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-6 py-4 border-b border-kb-border">
          <div className="flex items-center gap-2">
            <Trash2 className="w-4 h-4 text-status-error" />
            <h2 className="text-sm font-semibold text-kb-text-primary">Delete Cluster</h2>
          </div>
          <button onClick={onClose} className="text-kb-text-tertiary hover:text-kb-text-primary">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="p-6 space-y-4">
          <div className="px-3 py-2 rounded-lg bg-status-error-dim/50 border border-status-error/20">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
              <p className="text-[11px] text-kb-text-primary">
                This will remove the context <code className="text-status-error font-mono">{cluster.context}</code> from KubeBolt. The cluster itself is not affected.
              </p>
            </div>
          </div>

          <div>
            <label className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider block mb-1.5">
              Type <code className="text-kb-text-primary">{cluster.context}</code> to confirm
            </label>
            <input
              type="text"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              className="w-full px-3 py-2 rounded-lg bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-status-error font-mono"
              autoFocus
            />
          </div>

          {error && (
            <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim">
              <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
              <span className="text-[11px] text-status-error">{error}</span>
            </div>
          )}
        </div>

        <div className="flex items-center justify-end gap-2 px-6 py-4 border-t border-kb-border bg-kb-surface">
          <button onClick={onClose} className="px-4 py-1.5 rounded-lg text-xs text-kb-text-secondary hover:text-kb-text-primary transition-colors">
            Cancel
          </button>
          <button
            onClick={handleDelete}
            disabled={!isConfirmed || deleting}
            className="flex items-center gap-1.5 px-4 py-1.5 rounded-lg bg-status-error text-white text-xs font-medium hover:bg-status-error/90 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Trash2 className="w-3 h-3" />
            {deleting ? 'Deleting...' : 'Delete'}
          </button>
        </div>
      </div>
    </div>
  )
}

export function ClustersPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { hasRole } = useAuth()
  const canManage = hasRole('admin')

  const [showAddModal, setShowAddModal] = useState(false)
  const [renaming, setRenaming] = useState<ClusterInfo | null>(null)
  const [deleting, setDeleting] = useState<ClusterInfo | null>(null)

  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    refetchInterval: 30_000,
  })

  const { data: overview } = useQuery({
    queryKey: ['cluster-overview'],
    queryFn: api.getOverview,
  })

  const { data: health } = useQuery({
    queryKey: ['cluster-health'],
    queryFn: api.getHealth,
  })

  const switchMutation = useMutation({
    mutationKey: ['switch-cluster'],
    mutationFn: (context: string) => api.switchCluster(context),
    onMutate: (context: string) => {
      queryClient.setQueryData(['clusters'], (old: ClusterInfo[] | undefined) =>
        old?.map(c => ({ ...c, active: c.context === context }))
      )
      queryClient.setQueryData(['cluster-overview'], undefined)
    },
    onSuccess: () => {
      queryClient.invalidateQueries()
      navigate('/')
    },
    onError: () => {
      queryClient.invalidateQueries()
    },
  })

  const sorted = [...(clusters || [])].sort((a, b) => {
    if (a.active) return -1
    if (b.active) return 1
    return a.context.localeCompare(b.context)
  })

  const connectedCount = clusters?.filter(c => c.status === 'connected').length || 0
  const uploadedCount = clusters?.filter(c => c.source === 'uploaded').length || 0
  const isInCluster = clusters?.some(c => c.source === 'in-cluster') || false

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary">Clusters</h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5">
            {connectedCount} connected · {clusters?.length || 0} available
            {uploadedCount > 0 && ` · ${uploadedCount} uploaded`}
          </p>
        </div>
        {canManage && !isInCluster && (
          <button
            onClick={() => setShowAddModal(true)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-accent text-white text-xs font-medium hover:bg-kb-accent-bright transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
            Add Cluster
          </button>
        )}
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {sorted.map((cluster) => (
          <ClusterCard
            key={cluster.context}
            cluster={cluster}
            overview={cluster.active ? overview : undefined}
            health={cluster.active ? health : undefined}
            onSwitch={(ctx) => switchMutation.mutate(ctx)}
            onRename={setRenaming}
            onDelete={setDeleting}
            isSwitching={switchMutation.isPending}
            canManage={canManage}
          />
        ))}
      </div>

      {showAddModal && <AddClusterModal onClose={() => setShowAddModal(false)} />}
      {renaming && <RenameClusterModal cluster={renaming} onClose={() => setRenaming(null)} />}
      {deleting && <DeleteClusterModal cluster={deleting} onClose={() => setDeleting(null)} />}
    </div>
  )
}
