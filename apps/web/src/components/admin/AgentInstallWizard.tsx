import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Check, Loader2, KeyRound } from 'lucide-react'
import { api, type AgentInstallConfig, type AgentIssueTokenResponse, type Integration } from '@/services/api'
import { Modal } from '@/components/shared/Modal'
import { AgentConfigFields, agentConfigBlocked } from '@/components/admin/AgentConfigFields'

interface Props {
  integration: Integration
  onClose: () => void
}

// AgentInstallWizard installs the agent into a cluster the BACKEND can reach
// (it applies the manifests via installIntegration). The full config surface
// lives in the shared AgentConfigFields, so this wizard and the copy-paste
// AddClusterWizard never drift on which knobs they expose.
export function AgentInstallWizard({ integration: _integration, onClose }: Props) {
  const qc = useQueryClient()
  const [cfg, setCfg] = useState<AgentInstallConfig>({
    backendUrl: '',
    clusterName: '',
    hubbleEnabled: false,
    rbacMode: 'reader',
  })
  const [nodeSelector, setNodeSelector] = useState<Array<{ k: string; v: string }>>([])
  const [advancedOpen, setAdvancedOpen] = useState(false)

  const { data: authInfo } = useQuery({
    queryKey: ['agent-auth-info'],
    queryFn: () => api.getAgentAuthInfo(),
    staleTime: 30_000,
  })

  const { data: defaults } = useQuery({
    queryKey: ['agent-install-defaults'],
    queryFn: () => api.getAgentInstallDefaults(),
    staleTime: 30_000,
  })

  useEffect(() => {
    if (!defaults) return
    setCfg((prev) => ({
      ...prev,
      backendUrl: prev.backendUrl || defaults.internalBackendUrl || '',
      namespace: prev.namespace || defaults.agentNamespace,
    }))
  }, [defaults])

  const [issuedToken, setIssuedToken] = useState<AgentIssueTokenResponse | null>(null)
  const [issueError, setIssueError] = useState<string | null>(null)
  const [selectedTenantId, setSelectedTenantId] = useState<string>('')

  useEffect(() => {
    if (selectedTenantId || !authInfo?.tenants?.length) return
    const firstActive = authInfo.tenants.find((t) => !t.disabled)
    if (firstActive) setSelectedTenantId(firstActive.id)
  }, [authInfo, selectedTenantId])

  const [conflict, setConflict] = useState<{ kind: string; namespace?: string; name: string; reason: string } | null>(null)

  const issueToken = useMutation({
    mutationFn: () =>
      api.issueAgentTokenAndMaterializeSecret({
        tenantId: selectedTenantId,
        namespace: cfg.namespace?.trim() || 'kubebolt-system',
        secretName: cfg.authTokenSecret?.trim() || 'kubebolt-agent-token',
        label: `agent-install ${new Date().toISOString().slice(0, 10)}`,
      }),
    onSuccess: (resp) => {
      setIssueError(null)
      setIssuedToken(resp)
      setCfg((prev) => ({ ...prev, authTokenSecret: resp.secretName }))
    },
    onError: (err) => {
      setIssuedToken(null)
      setIssueError(err instanceof Error ? err.message : String(err))
    },
  })

  const mut = useMutation({
    mutationFn: (body: AgentInstallConfig) => api.installIntegration('agent', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['integrations'] })
      onClose()
    },
    onError: async (err: Error) => {
      const payload = (err as unknown as { payload?: { conflict?: { Kind: string; Namespace: string; Name: string; Reason: string } } }).payload
      if (payload?.conflict) {
        setConflict({ kind: payload.conflict.Kind, namespace: payload.conflict.Namespace, name: payload.conflict.Name, reason: payload.conflict.Reason })
      }
    },
  })

  function submit(e: React.FormEvent) {
    e.preventDefault()
    setConflict(null)
    const ns: Record<string, string> = {}
    for (const { k, v } of nodeSelector) {
      const key = k.trim()
      if (!key) continue
      ns[key] = v
    }
    const tls = cfg.hubbleRelayTls?.existingSecret?.trim()
      ? { existingSecret: cfg.hubbleRelayTls.existingSecret.trim(), serverName: cfg.hubbleRelayTls.serverName?.trim() || undefined }
      : undefined
    const res = cfg.resources
    const hasAnyRes = !!(res?.cpuRequest || res?.cpuLimit || res?.memoryRequest || res?.memoryLimit)
    const trimmed: AgentInstallConfig = {
      ...cfg,
      backendUrl: cfg.backendUrl.trim(),
      clusterName: cfg.clusterName?.trim() || undefined,
      imageTag: cfg.imageTag?.trim() || undefined,
      imageRepo: cfg.imageRepo?.trim() || undefined,
      namespace: cfg.namespace?.trim() || undefined,
      hubbleRelayAddress: cfg.hubbleRelayAddress?.trim() || undefined,
      hubbleRelayTls: tls,
      priorityClassName: cfg.priorityClassName?.trim() || undefined,
      nodeSelector: Object.keys(ns).length > 0 ? ns : undefined,
      resources: hasAnyRes ? res : undefined,
    }
    if (!trimmed.backendUrl) return
    mut.mutate(trimmed)
  }

  // Per-wizard token UI: this flow materializes the Secret in the cluster the
  // backend manages, so it offers a one-click "generate + create Secret".
  const tokenSlot = (
    <>
      {(authInfo?.tenants?.length ?? 0) > 0 && (
        <div className="pt-2 space-y-2">
          {(authInfo?.tenants?.length ?? 0) > 1 && (
            <select
              value={selectedTenantId}
              onChange={(e) => setSelectedTenantId(e.target.value)}
              className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
            >
              {authInfo!.tenants.map((t) => (
                <option key={t.id} value={t.id} disabled={t.disabled}>{t.name}{t.disabled ? ' (disabled)' : ''}</option>
              ))}
            </select>
          )}
          <button
            type="button"
            onClick={() => issueToken.mutate()}
            disabled={!selectedTenantId || issueToken.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-card hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {issueToken.isPending ? (<><Loader2 className="w-3.5 h-3.5 animate-spin" /> Issuing…</>) : (<><KeyRound className="w-3.5 h-3.5" /> Generate token + create Secret</>)}
          </button>
          {issuedToken && (
            <div className="flex items-start gap-2 px-2.5 py-2 rounded bg-status-ok-dim border border-status-ok/30">
              <Check className="w-3.5 h-3.5 text-status-ok shrink-0 mt-0.5" />
              <div className="text-[11px] text-kb-text-primary">
                <div className="font-semibold">Secret <code className="font-mono">{issuedToken.secretName}</code> ready in <code className="font-mono">{issuedToken.namespace}</code></div>
                <div className="text-kb-text-secondary mt-0.5">Token <code className="font-mono">{issuedToken.tokenPrefix}…</code> stored under key <code className="font-mono">token</code>. The wizard will reference this Secret on Install.</div>
              </div>
            </div>
          )}
          {issueError && (
            <div className="flex items-start gap-2 px-2.5 py-2 rounded bg-status-error-dim border border-status-error/30">
              <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
              <div className="text-[11px] text-status-error">{issueError}</div>
            </div>
          )}
        </div>
      )}
      <p className="text-[11px] text-kb-text-tertiary">
        Use the button above to generate a token + create the Secret in one click, or point at a Secret you've already created.
      </p>
    </>
  )

  const blocked = agentConfigBlocked(cfg, authInfo)

  return (
    <Modal badge="Install" title="KubeBolt Agent" onClose={onClose} size="xl">
      <form onSubmit={submit} className="flex-1 overflow-y-auto p-5 space-y-5">
        <AgentConfigFields
          cfg={cfg}
          setCfg={setCfg}
          nodeSelector={nodeSelector}
          setNodeSelector={setNodeSelector}
          authInfo={authInfo}
          advancedOpen={advancedOpen}
          setAdvancedOpen={setAdvancedOpen}
          tokenSlot={tokenSlot}
        />

        {conflict && (
          <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-warn-dim border border-status-warn/30">
            <AlertTriangle className="w-4 h-4 text-status-warn shrink-0 mt-0.5" />
            <div className="text-[11px] text-kb-text-primary">
              <div className="font-semibold mb-0.5">Install conflict</div>
              <div>{conflict.kind}{conflict.namespace ? ` ${conflict.namespace}/` : ' '}{conflict.name}: {conflict.reason}</div>
              <div className="mt-1 text-kb-text-secondary">KubeBolt won't overwrite resources it didn't create. Uninstall the existing one first, or edit it in place with <code className="font-mono">helm upgrade</code>.</div>
            </div>
          </div>
        )}
        {mut.isError && !conflict && (
          <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-error-dim border border-status-error/30">
            <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
            <div className="text-[11px] text-status-error">{(mut.error as Error).message}</div>
          </div>
        )}
      </form>

      <div className="flex items-center justify-end gap-2 px-5 py-3 border-t border-kb-border shrink-0">
        <button type="button" onClick={onClose} className="px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors">Cancel</button>
        <button
          type="button"
          onClick={submit}
          disabled={!cfg.backendUrl.trim() || mut.isPending || blocked}
          title={blocked ? 'Pick Ingest Token — operator/enforced auth requires it before installing' : undefined}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-accent hover:bg-kb-accent-hover text-kb-on-accent text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {mut.isPending ? (<><Loader2 className="w-3.5 h-3.5 animate-spin" /> Installing…</>) : mut.isSuccess ? (<><Check className="w-3.5 h-3.5" /> Installed</>) : (<>Install agent</>)}
        </button>
      </div>
    </Modal>
  )
}
