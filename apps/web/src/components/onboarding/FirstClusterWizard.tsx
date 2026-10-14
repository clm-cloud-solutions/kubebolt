import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { ArrowLeft, ArrowRight, Check, Copy, Eye, EyeOff, Laptop, Loader2, Server, Settings2 } from 'lucide-react'
import { api, type AgentInstallConfig, type AgentIssueTokenResponse } from '@/services/api'
import type { ClusterInfo } from '@/types/kubernetes'
import { Modal } from '@/components/shared/Modal'
import { AddClusterWizard } from '@/components/admin/AddClusterWizard'
import { AddKubeconfigModal } from '@/components/admin/AddKubeconfigModal'
import { useSwitchCluster } from '@/hooks/useSwitchCluster'
import {
  agentAddressProblem,
  buildFirstClusterScript,
  LOCAL_BACKEND_URL,
  LOCAL_CLUSTER_TOOLS,
  type LocalClusterTool,
} from '@/utils/agentInstall'

// FirstClusterWizard — the add-cluster flow while no cluster is registered.
//
// The full AddClusterWizard asks for every agent knob up front, and its
// command could be copied before the token existed. This one asks for a name
// and the address the agent dials, issues the token itself, hands over ONE
// block to paste and stays with the person until the agent reports. The full
// wizard and the kubeconfig import are one click away for anyone who needs
// them.

type Where = 'own' | 'local'
type Step = 1 | 2 | 3

interface Props {
  onClose: () => void
  /** Open on "a cluster on my laptop" — Home's "Try it on my laptop". */
  initialWhere?: Where
}

const NAME_RE = /^[a-z0-9]([a-z0-9._-]{0,48}[a-z0-9])?$/i
// After this long without an agent, show what to check. The agent registers
// ~30 s after its pod starts; two minutes covers the image pull.
const HELP_AFTER_S = 120
// Past this the poll slows down. It does not stop: an agent that registers
// minutes later still lands here.
const TIMEOUT_S = 300

export function FirstClusterWizard({ onClose, initialWhere = 'own' }: Props) {
  const [alternative, setAlternative] = useState<'advanced' | 'kubeconfig' | null>(null)
  const [step, setStep] = useState<Step>(1)
  const [where, setWhere] = useState<Where>(initialWhere)
  const [tool, setTool] = useState<LocalClusterTool['id']>('kind')
  const [name, setName] = useState(initialWhere === 'local' ? 'my-laptop' : '')
  const [addressEdit, setAddressEdit] = useState<string | null>(null)
  const [token, setToken] = useState<AgentIssueTokenResponse | null>(null)
  const [issueError, setIssueError] = useState<string | null>(null)
  const [showToken, setShowToken] = useState(false)
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const [copiedOnce, setCopiedOnce] = useState(false)
  const [startedAt, setStartedAt] = useState<number | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const [baseline, setBaseline] = useState<Set<string> | null>(null)
  const [connected, setConnected] = useState<ClusterInfo | null>(null)

  const { data: defaults } = useQuery({
    queryKey: ['agent-install-defaults'],
    queryFn: () => api.getAgentInstallDefaults(),
    staleTime: 30_000,
  })
  // An operator-configured ingest URL is authoritative (TLS on). Otherwise
  // nothing tells this backend which of its addresses another cluster can
  // dial, so the person confirms one; the suggestion follows the "where"
  // choice until they type their own.
  const hostedMode = !!defaults?.agentIngestUrl
  const suggested = defaults?.externalEndpoint || (where === 'local' ? LOCAL_BACKEND_URL : '')
  const backendUrl = (hostedMode ? defaults?.agentIngestUrl : addressEdit ?? suggested)?.trim() ?? ''
  const addressProblem = hostedMode ? null : agentAddressProblem(backendUrl)

  const waiting = step >= 2 && !!token?.token && !connected
  const elapsed = startedAt ? Math.floor((now - startedAt) / 1000) : 0
  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    enabled: !!token?.token,
    refetchInterval: waiting ? (elapsed < TIMEOUT_S ? 5_000 : 15_000) : false,
  })

  // The clusters that already existed when the token was issued; whatever
  // shows up after that is the one being connected.
  useEffect(() => {
    if (!clusters || !token?.token) return
    if (!baseline) {
      setBaseline(new Set(clusters.map((c) => c.context)))
      return
    }
    if (!connected) {
      const fresh = clusters.find((c) => !baseline.has(c.context))
      if (fresh) setConnected(fresh)
    }
  }, [clusters, token, baseline, connected])

  useEffect(() => {
    if (step !== 3 || connected) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [step, connected])

  const issue = useMutation({
    mutationFn: () =>
      api.issueAgentTokenAndMaterializeSecret({
        label: `first-cluster ${name.trim()} ${new Date().toISOString().slice(0, 10)}`,
      }),
    onSuccess: (resp) => {
      setIssueError(null)
      setToken(resp)
    },
    onError: (err) => setIssueError(err instanceof Error ? err.message : String(err)),
  })

  const cfg: AgentInstallConfig = useMemo(
    () => ({
      backendUrl,
      clusterName: name.trim(),
      rbacMode: 'reader',
      hubbleEnabled: false,
      authMode: 'ingest-token',
      tlsEnabled: hostedMode,
    }),
    [backendUrl, name, hostedMode],
  )
  const script = token?.token ? buildFirstClusterScript(cfg, token.token) : ''
  const masked = token?.token ? script.replace(token.token, '•'.repeat(24)) : ''
  const localTool = LOCAL_CLUSTER_TOOLS.find((t) => t.id === tool)!

  function copy(text: string, key: string) {
    const done = () => {
      setCopiedKey(key)
      setTimeout(() => setCopiedKey((k) => (k === key ? null : k)), 2000)
      if (key === 'script') setCopiedOnce(true)
    }
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(() => copyViaTextarea(text, done))
      return
    }
    copyViaTextarea(text, done)
  }

  function goInstall() {
    setStep(2)
    if (!token && !issue.isPending) issue.mutate()
  }

  function goLive() {
    setStep(3)
    if (!startedAt) setStartedAt(Date.now())
  }

  // Switching lands on the new cluster's Overview (the hook navigates); the
  // modal closes once the switch went through.
  const switchCluster = useSwitchCluster()

  if (alternative === 'advanced') return <AddClusterWizard onClose={onClose} />
  if (alternative === 'kubeconfig') return <AddKubeconfigModal onClose={onClose} />

  const nameValid = NAME_RE.test(name.trim())

  return (
    <Modal badge="First cluster" title="Connect your first cluster" onClose={onClose} size="xl">
      <div className="px-6 pt-4 pb-1">
        <Stepper step={step} />
      </div>

      {step === 1 && (
        <div className="px-6 py-4 space-y-5">
          <div className="space-y-1.5">
            <label htmlFor="fc-name" className="text-[11px] font-mono uppercase tracking-wider text-kb-text-tertiary">
              Name your cluster
            </label>
            <input
              id="fc-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter' && nameValid && !addressProblem) goInstall() }}
              placeholder="production-eu"
              className="w-full px-3 py-2 rounded-lg bg-kb-card border border-kb-border text-sm text-kb-text-primary focus:outline-none focus:ring-1 focus:ring-kb-accent"
            />
            <p className={`text-[11px] ${name && !nameValid ? 'text-status-warn' : 'text-kb-text-tertiary'}`}>
              {name && !nameValid
                ? 'Letters, numbers, dots, dashes and underscores, starting and ending with a letter or number.'
                : 'This is how it shows up in KubeBolt. You can change it later.'}
            </p>
          </div>

          <div className="space-y-2">
            <div className="text-[11px] font-mono uppercase tracking-wider text-kb-text-tertiary">Where does it run?</div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
              <WhereCard
                active={where === 'own'}
                onClick={() => setWhere('own')}
                icon={<Server className="w-4 h-4" />}
                title="A cluster I already have"
                text="EKS, AKS, GKE, OpenShift, on-prem… any cluster your kubectl can reach."
              />
              <WhereCard
                active={where === 'local'}
                onClick={() => { setWhere('local'); if (!name) setName('my-laptop') }}
                icon={<Laptop className="w-4 h-4" />}
                title="A cluster on my laptop"
                text="No cluster at hand? Create one with kind, k3d or minikube in about two minutes."
              />
            </div>
          </div>

          {defaults && !hostedMode && (
            <div className="space-y-1.5">
              <label htmlFor="fc-backend" className="text-[11px] font-mono uppercase tracking-wider text-kb-text-tertiary">
                Where the agent reaches KubeBolt
              </label>
              <input
                id="fc-backend"
                value={backendUrl}
                onChange={(e) => setAddressEdit(e.target.value)}
                placeholder="kubebolt.example.com:9090"
                spellCheck={false}
                className="w-full px-3 py-2 rounded-lg bg-kb-card border border-kb-border text-sm font-mono text-kb-text-primary focus:outline-none focus:ring-1 focus:ring-kb-accent"
              />
              <p className={`text-[11px] leading-relaxed ${backendUrl && addressProblem ? 'text-status-warn' : 'text-kb-text-tertiary'}`}>
                {backendUrl && addressProblem
                  ? addressProblem
                  : where === 'local'
                    ? `${LOCAL_BACKEND_URL} is this machine as kind, k3d and minikube see it on Docker Desktop; on Linux, use its address on the Docker network. KubeBolt's port 9090 has to be published there — the single container needs -p 9090:9090.`
                    : "KubeBolt's agent port (9090) as that cluster sees it: a hostname, a LoadBalancer address or a node IP with its NodePort."}
              </p>
            </div>
          )}
        </div>
      )}

      {step === 2 && (
        <div className="px-6 py-4 space-y-4">
          {issue.isPending && (
            <div className="flex items-center gap-2 text-[12px] text-kb-text-secondary">
              <Loader2 className="w-4 h-4 animate-spin" /> Preparing your install command…
            </div>
          )}

          {issueError && (
            <div className="rounded-lg border border-status-error/40 bg-status-error-dim/15 p-3 text-[12px] text-kb-text-secondary space-y-2">
              <div>We couldn't prepare the install command: {issueError}</div>
              <div className="flex items-center gap-3">
                <button type="button" onClick={() => issue.mutate()} className="text-kb-accent underline underline-offset-2">
                  Try again
                </button>
                <button type="button" onClick={() => setAlternative('advanced')} className="text-kb-accent underline underline-offset-2">
                  Advanced setup
                </button>
              </div>
            </div>
          )}

          {token?.token && (
            <>
              {where === 'local' && (
                <div className="space-y-2">
                  <div className="flex items-center justify-between gap-3 flex-wrap">
                    <div className="text-[12px] font-medium text-kb-text-primary">1 · Create the cluster</div>
                    <div className="flex gap-1 p-0.5 rounded-lg bg-kb-elevated" role="tablist" aria-label="Local cluster tool">
                      {LOCAL_CLUSTER_TOOLS.map((t) => (
                        <button
                          key={t.id}
                          type="button"
                          role="tab"
                          aria-selected={tool === t.id}
                          onClick={() => setTool(t.id)}
                          className={`px-2.5 py-1 rounded-md text-[11px] font-mono transition-colors ${
                            tool === t.id ? 'bg-kb-card text-kb-text-primary shadow-sm' : 'text-kb-text-tertiary hover:text-kb-text-secondary'
                          }`}
                        >
                          {t.label}
                        </button>
                      ))}
                    </div>
                  </div>
                  <CodeBlock text={localTool.create} copied={copiedKey === 'create'} onCopy={() => copy(localTool.create, 'create')} />
                  <p className="text-[11px] text-kb-text-tertiary">
                    Needs {localTool.needs} running. Don't have {localTool.label}?{' '}
                    <a href={localTool.install} target="_blank" rel="noopener noreferrer" className="text-kb-accent underline underline-offset-2">
                      Install it
                    </a>
                    . Already running Kubernetes in Docker Desktop, OrbStack or Rancher Desktop? Skip this and use that context.
                  </p>
                </div>
              )}

              <div className="space-y-2">
                <div className="text-[12px] font-medium text-kb-text-primary">
                  {where === 'local' ? '2 · Install the agent' : 'Run this in a terminal where kubectl points at your cluster'}
                </div>
                <div className="rounded-lg border border-kb-border bg-kb-elevated overflow-hidden">
                  <div className="flex items-center justify-between gap-2 px-3 py-1.5 border-b border-kb-border">
                    <span className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Install block · bash / zsh</span>
                    <div className="flex items-center gap-1">
                    <button
                      type="button"
                      onClick={() => setShowToken((s) => !s)}
                      className="p-1.5 rounded text-kb-text-tertiary hover:text-kb-text-primary"
                      title={showToken ? 'Hide the token' : 'Show the token'}
                    >
                      {showToken ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                    </button>
                    <button
                      type="button"
                      onClick={() => copy(script, 'script')}
                      className="flex items-center gap-1 px-2.5 py-1 rounded-md bg-kb-accent text-white text-[11px] font-medium hover:bg-kb-accent/90"
                    >
                      {copiedKey === 'script' ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                      {copiedKey === 'script' ? 'Copied' : 'Copy all'}
                    </button>
                    </div>
                  </div>
                  <pre className="text-[11px] font-mono p-3 overflow-x-auto text-kb-text-primary leading-relaxed whitespace-pre">
                    {showToken ? script : masked}
                  </pre>
                </div>
                <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
                  Your token is already inside and is shown only once — Copy all takes the real value even while it's hidden.
                  The agent is read-only and dials out to <span className="font-mono">{backendUrl}</span>
                  {hostedMode ? ' over HTTPS' : ''}; you need <span className="font-mono">kubectl</span>,{' '}
                  <span className="font-mono">helm</span> and rights to create a namespace and a ClusterRole.
                </p>
              </div>

              <button
                type="button"
                onClick={() => setAlternative('advanced')}
                className="w-full flex items-center justify-between gap-3 px-3 py-2.5 rounded-lg border border-kb-border text-left text-[12px] text-kb-text-secondary hover:bg-kb-card-hover"
              >
                <span className="flex items-center gap-2">
                  <Settings2 className="w-3.5 h-3.5" /> Advanced setup · RBAC level, Hubble, Prometheus, OpenCost, resources
                </span>
                <span className="text-[11px] font-mono text-kb-text-tertiary">defaults work for most clusters</span>
              </button>
            </>
          )}
        </div>
      )}

      {step === 3 && (
        <div className="px-6 py-4 space-y-4">
          <div className="text-[14px] font-semibold text-kb-text-primary">
            {connected ? `${connected.displayName || connected.name} is connected` : `Waiting for ${name.trim()}`}
          </div>
          <div className="space-y-2.5">
            <CheckRow state="done" label="Install command ready" />
            {/* Selected by hand instead of Copy all still runs: a connected agent means it ran. */}
            <CheckRow state={copiedOnce || connected ? 'done' : 'todo'} label="Command copied" />
            <CheckRow
              state={connected ? 'done' : 'run'}
              label={connected ? 'Agent connected — the cluster is registered' : 'Waiting for the agent to connect'}
              meta={connected ? undefined : fmtElapsed(elapsed)}
            />
          </div>

          {!connected && elapsed >= HELP_AFTER_S && (
            <div className="rounded-lg border border-kb-border bg-kb-card p-3 space-y-2">
              <div className="text-[12px] font-medium text-kb-text-primary">Taking longer than usual? Check the agent:</div>
              <CodeBlock
                text={'kubectl -n kubebolt-system get pods\nkubectl -n kubebolt-system logs ds/kubebolt-agent --tail=20'}
                copied={copiedKey === 'diag'}
                onCopy={() => copy('kubectl -n kubebolt-system get pods\nkubectl -n kubebolt-system logs ds/kubebolt-agent --tail=20', 'diag')}
              />
              <ul className="text-[11px] text-kb-text-tertiary list-disc pl-4 space-y-0.5">
                <li>Pods in <span className="font-mono">CreateContainerConfigError</span>: the token Secret is missing — run the whole block again.</li>
                <li>Logs say the connection is refused or times out: the cluster can't reach <span className="font-mono">{backendUrl}</span>.</li>
                <li>Nothing in <span className="font-mono">kubebolt-system</span>: kubectl was pointing at another cluster.</li>
              </ul>
              <p className="text-[11px] text-kb-text-tertiary">We keep checking — the cluster shows up here as soon as the agent reports.</p>
            </div>
          )}

          {connected && (
            <button
              type="button"
              onClick={() => switchCluster.mutate(connected.context, { onSuccess: () => onClose() })}
              disabled={switchCluster.isPending}
              className="inline-flex items-center gap-2 px-4 py-2 rounded-lg bg-kb-accent text-white text-[13px] font-medium hover:bg-kb-accent/90 disabled:opacity-60"
            >
              {switchCluster.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <ArrowRight className="w-4 h-4" />}
              Open {connected.displayName || connected.name}
            </button>
          )}
        </div>
      )}

      <div className="px-6 py-3 border-t border-kb-border flex items-center justify-between gap-2">
        <div className="flex items-center">
          {step > 1 && !connected && (
            <button
              type="button"
              onClick={() => setStep((s) => (s - 1) as Step)}
              className="inline-flex items-center gap-1 px-3 py-1.5 text-[12px] text-kb-text-secondary hover:text-kb-text-primary"
            >
              <ArrowLeft className="w-3.5 h-3.5" /> Back
            </button>
          )}
          {step === 1 && (
            <>
              <button type="button" onClick={() => setAlternative('advanced')} className="px-3 py-1.5 text-[12px] text-kb-text-tertiary hover:text-kb-text-secondary">
                Advanced setup
              </button>
              <button type="button" onClick={() => setAlternative('kubeconfig')} className="px-3 py-1.5 text-[12px] text-kb-text-tertiary hover:text-kb-text-secondary">
                Import a kubeconfig
              </button>
            </>
          )}
        </div>
        <div className="flex items-center gap-2">
          {step === 1 && (
            <button
              type="button"
              disabled={!defaults || !nameValid || !!addressProblem}
              onClick={goInstall}
              className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg bg-kb-accent text-white text-[12px] font-medium hover:bg-kb-accent/90 disabled:opacity-40"
            >
              Continue <ArrowRight className="w-3.5 h-3.5" />
            </button>
          )}
          {step === 2 && (
            <button
              type="button"
              disabled={!token?.token}
              onClick={goLive}
              className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg bg-kb-accent text-white text-[12px] font-medium hover:bg-kb-accent/90 disabled:opacity-40"
            >
              I ran it <ArrowRight className="w-3.5 h-3.5" />
            </button>
          )}
          {step === 3 && (
            <button type="button" onClick={onClose} className="px-3 py-1.5 text-[12px] text-kb-text-secondary hover:text-kb-text-primary">
              {connected ? 'Close' : 'Close — keep it running'}
            </button>
          )}
        </div>
      </div>
    </Modal>
  )
}

function Stepper({ step }: { step: Step }) {
  const steps = ['Name it', 'Install', 'Go live']
  return (
    <ol className="flex items-center gap-2 text-[12px]" aria-label="Progress">
      {steps.map((label, i) => {
        const n = (i + 1) as Step
        const done = n < step
        const cur = n === step
        return (
          <li key={label} className="flex items-center gap-2">
            {i > 0 && <span className="w-6 h-px bg-kb-border" aria-hidden />}
            <span
              className={`w-5 h-5 rounded-full grid place-items-center text-[10px] font-mono border ${
                done ? 'bg-status-ok border-status-ok text-kb-bg' : cur ? 'border-kb-accent text-kb-accent' : 'border-kb-border text-kb-text-tertiary'
              }`}
            >
              {done ? <Check className="w-3 h-3" /> : n}
            </span>
            <span className={cur ? 'text-kb-text-primary font-medium' : 'text-kb-text-tertiary'} aria-current={cur ? 'step' : undefined}>
              {label}
            </span>
          </li>
        )
      })}
    </ol>
  )
}

function WhereCard({ active, onClick, icon, title, text }: { active: boolean; onClick: () => void; icon: ReactNode; title: string; text: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={`text-left rounded-lg border p-3 space-y-1 transition-colors ${
        active ? 'border-kb-accent bg-kb-accent-light/40' : 'border-kb-border hover:bg-kb-card-hover'
      }`}
    >
      <div className={`flex items-center gap-2 text-[13px] font-medium ${active ? 'text-kb-text-primary' : 'text-kb-text-secondary'}`}>
        <span className={active ? 'text-kb-accent' : 'text-kb-text-tertiary'}>{icon}</span>
        {title}
      </div>
      <div className="text-[11px] text-kb-text-tertiary leading-relaxed">{text}</div>
    </button>
  )
}

function CodeBlock({ text, copied, onCopy }: { text: string; copied: boolean; onCopy: () => void }) {
  return (
    <div className="relative">
      <pre className="text-[11px] font-mono bg-kb-elevated border border-kb-border rounded-lg p-3 pr-12 overflow-x-auto text-kb-text-primary whitespace-pre">{text}</pre>
      <button
        type="button"
        onClick={onCopy}
        className="absolute top-2 right-2 p-1.5 rounded text-kb-text-tertiary hover:text-kb-text-primary"
        title="Copy"
      >
        {copied ? <Check className="w-3.5 h-3.5 text-kb-accent" /> : <Copy className="w-3.5 h-3.5" />}
      </button>
    </div>
  )
}

function CheckRow({ state, label, meta }: { state: 'done' | 'run' | 'todo'; label: string; meta?: string }) {
  return (
    <div className="flex items-center gap-3 text-[12px]">
      <span
        className={`w-5 h-5 rounded-full grid place-items-center shrink-0 ${
          state === 'done' ? 'bg-status-ok text-kb-bg' : state === 'run' ? 'text-kb-accent' : 'border border-kb-border'
        }`}
      >
        {state === 'done' ? <Check className="w-3 h-3" /> : state === 'run' ? <Loader2 className="w-4 h-4 animate-spin" /> : null}
      </span>
      <span className={state === 'todo' ? 'text-kb-text-tertiary' : 'text-kb-text-primary'}>{label}</span>
      {meta && <span className="ml-auto font-mono text-[11px] text-kb-text-tertiary">{meta}</span>}
    </div>
  )
}

function fmtElapsed(s: number) {
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

function copyViaTextarea(text: string, done: () => void) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'absolute'
  ta.style.left = '-9999px'
  document.body.appendChild(ta)
  ta.select()
  try {
    document.execCommand('copy')
    done()
  } catch {
    /* the person can still select the text by hand */
  } finally {
    document.body.removeChild(ta)
  }
}
