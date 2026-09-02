import { useRef } from 'react'
import { Ban, PackagePlus, Link as LinkIcon, Database } from 'lucide-react'
import type { AgentInstallConfig } from '@/services/api'

// OpenCostModePicker exposes the three ways the KubeBolt Agent can source
// OpenCost cost/allocation metrics (plus "off"), mirroring the RBACModePicker
// card pattern. Lives in the shared AgentConfigFields so both the copy-paste
// helm flow (AddClusterWizard) and the backend-applied install
// (AgentInstallWizard) render the same choice; buildHelmCommand translates the
// selection into --set flags.
//
//   bundled  — opencost.enabled=true (installs the official sub-chart; the
//              agent auto-wires its exporter scraper to it).
//   scrape   — collectors.exporters.opencost=<url> (an OpenCost you already run).
//   promread — agent.promRead.cost.enabled=true. Cost rides the promRead metrics
//              source (mutually exclusive with the scrape sidecar), so picking it
//              also switches metricsSource to 'promread' — one Prometheus feeds
//              both metrics and cost.

type Mode = NonNullable<AgentInstallConfig['opencostMode']>

interface Props {
  cfg: AgentInstallConfig
  setCfg: (c: AgentInstallConfig) => void
}

interface Option {
  mode: Mode
  label: string
  icon: React.ComponentType<{ className?: string }>
  blurb: string
  hint: string
}

const options: Option[] = [
  {
    mode: 'off',
    label: 'Off',
    icon: Ban,
    blurb: 'No cost data. The Cost dashboard stays hidden for this cluster.',
    hint: 'Default',
  },
  {
    mode: 'bundled',
    label: 'Install OpenCost for me',
    icon: PackagePlus,
    blurb: "KubeBolt deploys the official OpenCost sub-chart and scrapes it automatically. Easiest if you don't already run OpenCost.",
    hint: 'opencost.enabled=true',
  },
  {
    mode: 'scrape',
    label: 'Scrape my OpenCost',
    icon: LinkIcon,
    blurb: 'Point the agent at an OpenCost you already run. It scrapes the /metrics endpoint and forwards the cost families.',
    hint: 'collectors.exporters.opencost',
  },
  {
    mode: 'promread',
    label: 'Read from my Prometheus',
    icon: Database,
    blurb: 'Your Prometheus already scrapes OpenCost (AMP / GMP / Azure Monitor, or self-managed). The agent reads cost from it — same source as metrics.',
    hint: 'promRead.cost.enabled=true',
  },
]

const subLabel = 'block text-[10px] font-mono text-kb-text-tertiary mb-1'
const subInput =
  'w-full text-xs font-mono bg-kb-card border border-kb-border rounded px-2 py-1.5 text-kb-text-primary placeholder:text-kb-text-tertiary focus:border-kb-accent outline-none'

export function OpenCostModePicker({ cfg, setCfg }: Props) {
  const mode: Mode = cfg.opencostMode ?? 'off'
  // Remember the metrics source we override when forcing promRead, so leaving
  // this mode restores it. Without this, picking "Read from my Prometheus" and
  // then changing your mind leaves promRead enabled — and that setting lives in
  // the collapsed Advanced section, so it silently rides along in the command.
  const priorMetricsSource = useRef<AgentInstallConfig['metricsSource']>(undefined)
  // When the metrics source is already promRead there is exactly ONE Prometheus
  // in play, so this section asks for nothing: showing a second box for the same
  // server is an invitation to type it twice and get one of them wrong.
  const promReadActive = cfg.metricsSource === 'promread'
  const promReadUrl = cfg.promRead?.url?.trim() ?? ''

  const select = (next: Mode) => {
    if (next === mode) return
    const patch: AgentInstallConfig = { ...cfg, opencostMode: next }
    if (next === 'promread') {
      // Cost-via-Prometheus only works in the promRead metrics source (mutually
      // exclusive with the scrape sidecar) — switch to it so one Prometheus
      // feeds both metrics and cost, and the command stays valid.
      if (mode !== 'promread') priorMetricsSource.current = cfg.metricsSource
      patch.metricsSource = 'promread'
    } else if (mode === 'promread' && cfg.metricsSource === 'promread') {
      // Leaving cost-via-Prometheus: undo the forced promRead so it doesn't
      // linger unseen in Advanced. Restore what the user had before (or the
      // kubelet default); skip if they changed it themselves meanwhile.
      patch.metricsSource = priorMetricsSource.current ?? 'kubelet'
    }
    setCfg(patch)
  }

  return (
    <div className="space-y-2 p-3 rounded-lg bg-kb-elevated border border-kb-border">
      <div>
        <div className="text-sm text-kb-text-primary font-medium">Cost monitoring (OpenCost)</div>
        <p className="text-[11px] text-kb-text-secondary mt-0.5">
          How KubeBolt gets cost &amp; allocation data. Picking a mode adds the right values to the Helm command below — the Cost dashboard lights up once samples flow.
        </p>
      </div>
      <div className="space-y-1.5 pt-1">
        {options.map((opt) => {
          const selected = mode === opt.mode
          const Icon = opt.icon
          return (
            <button
              key={opt.mode}
              type="button"
              onClick={() => select(opt.mode)}
              className={`w-full text-left flex items-start gap-3 p-2.5 rounded-lg border transition-colors ${
                selected
                  ? 'bg-kb-accent/10 border-kb-accent text-kb-text-primary'
                  : 'bg-kb-card border-kb-border hover:border-kb-border-strong text-kb-text-secondary'
              }`}
            >
              <Icon className={`w-4 h-4 mt-0.5 shrink-0 ${selected ? 'text-kb-accent' : 'text-kb-text-tertiary'}`} />
              <div className="min-w-0 flex-1">
                <span className="text-sm font-medium text-kb-text-primary">{opt.label}</span>
                <p className="text-[11px] text-kb-text-secondary mt-0.5 leading-relaxed">{opt.blurb}</p>
                <div className="mt-1.5 text-[10px] font-mono text-kb-text-tertiary">{opt.hint}</div>
              </div>
              <span
                aria-hidden="true"
                className={`w-3.5 h-3.5 rounded-full mt-1 shrink-0 border-2 ${
                  selected ? 'border-kb-accent bg-kb-accent' : 'border-kb-text-tertiary'
                }`}
              />
            </button>
          )
        })}
      </div>

      {/* Per-mode inputs / notes. */}
      {mode === 'scrape' && (
        <div className="pt-1">
          <label className={subLabel}>
            OpenCost /metrics URL <span className="text-status-error">*</span>
          </label>
          <input
            type="text"
            placeholder="http://opencost.opencost.svc.cluster.local:9003/metrics"
            value={cfg.opencostScrapeUrl ?? ''}
            onChange={(e) => setCfg({ ...cfg, opencostScrapeUrl: e.target.value })}
            className={subInput}
          />
        </div>
      )}

      {mode === 'promread' && (
        <div className="pt-1 space-y-2">
          <div>
            <label className={subLabel}>
              Prometheus URL <span className="text-status-error">*</span>
            </label>
            <input
              type="text"
              placeholder="https://prometheus.monitoring.svc:9090"
              value={cfg.promRead?.url ?? ''}
              onChange={(e) => setCfg({ ...cfg, promRead: { ...(cfg.promRead ?? {}), url: e.target.value } })}
              className={subInput}
            />
          </div>
          <p className="text-[10px] text-kb-text-tertiary leading-relaxed">
            Reads metrics <em>and</em> cost from this Prometheus (metrics source switched to promRead). Auth &amp; more options live under Advanced → Metrics source.
          </p>
        </div>
      )}

      {mode === 'bundled' && (
        promReadActive ? (
          <p className="text-[10px] text-kb-text-tertiary leading-relaxed pt-1">
            OpenCost will query the Prometheus you set under Advanced → Metrics source
            {promReadUrl ? <> (<span className="font-mono">{promReadUrl}</span>)</> : <> — still empty, so fill it there</>}. One Prometheus, entered once.
          </p>
        ) : (
          <div className="pt-1 space-y-2">
            <div>
              <label className={subLabel}>
                Prometheus URL for OpenCost <span className="text-status-error">*</span>
              </label>
              <input
                type="text"
                placeholder="https://prometheus.monitoring.svc:9090"
                value={cfg.opencostPrometheusUrl ?? ''}
                onChange={(e) => setCfg({ ...cfg, opencostPrometheusUrl: e.target.value })}
                className={subInput}
              />
            </div>
            <p className="text-[10px] text-kb-text-tertiary leading-relaxed">
              Required. OpenCost queries a Prometheus to attribute cost to workloads and <em>exits on startup</em> if it cannot reach one — it does not fall back to node pricing. An in-cluster address is recognised by its <span className="font-mono">.svc</span> label; anything else is passed through as an external URL.
            </p>
          </div>
        )
      )}
    </div>
  )
}
