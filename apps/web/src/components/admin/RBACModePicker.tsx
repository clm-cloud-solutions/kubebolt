import { Activity, ScanEye, ShieldAlert } from 'lucide-react'
import type { AgentInstallConfig } from '@/services/api'

// RBACModePicker is the single source of truth for the agent's
// permission tier choice. Lives in its own file because both
// AgentInstallWizard and AgentConfigureDialog render it identically —
// the alternative was duplicating a 60-line block in two places and
// having them drift on the next change.
//
// Three modes mirror the backend AgentRBACMode constants and the
// helm chart's rbac.mode values 1:1:
//
//   metrics  — privacy-conscious. Agent ships kubelet metrics +
//              Hubble flows; nothing else leaves the cluster.
//   reader   — cluster-wide read. Backend renders inventory through
//              the agent's tunnel; mutations come back 403.
//   operator — cluster-wide read+write. Effectively cluster-admin
//              scoped to the agent SA. Requires auth; the parent
//              dialog enforces that gate.

type Mode = NonNullable<AgentInstallConfig['rbacMode']>

interface Props {
  mode: Mode
  onChange: (mode: Mode) => void
}

interface Option {
  mode: Mode
  label: string
  icon: React.ComponentType<{ className?: string }>
  blurb: string
  proxyHint: string
  authHint: string
  warn?: boolean
}

const options: Option[] = [
  {
    mode: 'metrics',
    label: 'Metrics only',
    icon: Activity,
    blurb: 'Kubelet stats + Hubble flows. No inventory, no kubectl-style operations through this agent.',
    proxyHint: 'Proxy off',
    authHint: 'Auth optional',
  },
  {
    mode: 'reader',
    label: 'Cluster-wide read',
    icon: ScanEye,
    blurb: 'Full read access across all resources via the agent’s tunnel. Mutations rejected (403). The typical install.',
    proxyHint: 'Proxy on (mandatory)',
    authHint: 'Auth recommended',
  },
  {
    mode: 'operator',
    label: 'Cluster-wide read + write',
    icon: ShieldAlert,
    blurb: 'Effectively cluster-admin scoped to the agent ServiceAccount. Exec, scale, restart, delete, YAML edit through the dashboard.',
    proxyHint: 'Proxy on (mandatory)',
    authHint: 'Auth required',
    warn: true,
  },
]

export function RBACModePicker({ mode, onChange }: Props) {
  return (
    <div className="space-y-2 p-3 rounded-lg bg-kb-elevated border border-kb-border">
      <div>
        <div className="text-sm text-kb-text-primary font-medium">Permission tier</div>
        <p className="text-[11px] text-kb-text-secondary mt-0.5">
          What the agent's ServiceAccount can do in this cluster. The proxy + auth toggles below auto-match the choice; advanced overrides live in the Auth and Advanced sections.
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
              onClick={() => onChange(opt.mode)}
              className={`w-full text-left flex items-start gap-3 p-2.5 rounded-lg border transition-colors ${
                selected
                  ? 'bg-kb-accent/10 border-kb-accent text-kb-text-primary'
                  : 'bg-kb-card border-kb-border hover:border-kb-border-strong text-kb-text-secondary'
              }`}
            >
              <Icon className={`w-4 h-4 mt-0.5 shrink-0 ${selected ? 'text-kb-accent' : 'text-kb-text-tertiary'}`} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className={`text-sm font-medium ${selected ? 'text-kb-text-primary' : 'text-kb-text-primary'}`}>
                    {opt.label}
                  </span>
                  {opt.warn && (
                    <span className="text-[9px] font-mono uppercase tracking-wider px-1.5 py-0.5 rounded bg-status-warning/10 text-status-warning border border-status-warning/30">
                      cluster-admin
                    </span>
                  )}
                </div>
                <p className="text-[11px] text-kb-text-secondary mt-0.5 leading-relaxed">{opt.blurb}</p>
                <div className="flex items-center gap-3 mt-1.5 text-[10px] font-mono text-kb-text-tertiary">
                  <span>{opt.proxyHint}</span>
                  <span>·</span>
                  <span>{opt.authHint}</span>
                </div>
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
    </div>
  )
}
