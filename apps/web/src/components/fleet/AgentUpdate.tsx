import { useState } from 'react'
import { ArrowUpCircle, Check, Copy, ExternalLink } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import type { AgentUpdate } from '@/types/kubernetes'

// The «agent out of date» notice (debt-24). KubeBolt never upgrades an agent:
// it is the customer's Helm release, often under GitOps. It says how far
// behind the cluster's oldest live agent is, why that matters when it does,
// and the command to run.

export const AGENT_CHART = 'oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent'

// The release name and namespace the install wizard uses (utils/agentInstall.ts).
export function agentUpgradeCommand(latest: string): string {
  return [
    `helm upgrade kubebolt-agent ${AGENT_CHART} \\`,
    `  --namespace kubebolt-system --version ${latest} --reset-then-reuse-values`,
  ].join('\n')
}

export function agentReleaseNotesUrl(latest: string): string {
  return `https://github.com/clm-cloud-solutions/kubebolt/releases/tag/agent-v${latest}`
}

// needsAttention: only a version behind a fix asks something of the reader.
export function agentUpdateRecommended(u?: AgentUpdate): boolean {
  return u?.level === 'recommended'
}

// AgentUpdateBadge — the chip on a cluster: amber when an upgrade fixes
// something, neutral when a newer release merely exists, nothing when current.
export function AgentUpdateBadge({ update, onOpen }: { update?: AgentUpdate; onOpen: () => void }) {
  if (!update || update.level === 'current') return null
  const recommended = update.level === 'recommended'
  return (
    <button
      type="button"
      onClick={onOpen}
      title={recommended ? 'This agent version has problems a newer release fixes' : `Agent ${update.latest} is available`}
      className={`flex items-center gap-1 px-1.5 py-0.5 rounded text-[9px] font-mono transition-colors ${
        recommended
          ? 'bg-kobi-st-warn-dim text-kobi-st-warn hover:opacity-80'
          : 'bg-kb-elevated text-kb-text-secondary hover:text-kb-text-primary'
      }`}
    >
      <ArrowUpCircle className="w-2.5 h-2.5" aria-hidden="true" />
      Agent {update.version} · {recommended ? 'update recommended' : 'update available'}
    </button>
  )
}

// AgentUpdateTag — the same notice where the whole row is already a button
// (Fleet's cards and table rows): text only, and only when an upgrade fixes
// something. The reasons and the command are on Clusters and Home.
export function AgentUpdateTag({ update }: { update?: AgentUpdate }) {
  if (!agentUpdateRecommended(update) || !update) return null
  return (
    <span
      title={`Agent ${update.version} has problems ${update.latest} fixes. Clusters shows why and the Helm command.`}
      className="inline-flex items-center gap-1 shrink-0 px-1.5 py-0.5 rounded text-[9px] font-mono bg-kobi-st-warn-dim text-kobi-st-warn"
    >
      <ArrowUpCircle className="w-2.5 h-2.5" aria-hidden="true" />
      Agent {update.version}
    </span>
  )
}

// AgentUpdateModal — why, and how: the reasons for this version, the Helm
// command, and what to change when the agent was installed differently.
export function AgentUpdateModal({ clusterName, update, onClose }: { clusterName: string; update: AgentUpdate; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const command = agentUpgradeCommand(update.latest)
  const recommended = update.level === 'recommended'

  async function copy() {
    if (await copyText(command)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  return (
    <Modal
      badge="Agent"
      badgeClass={recommended ? 'text-kobi-st-warn bg-kobi-st-warn-dim' : undefined}
      title={`${clusterName}: agent ${update.version} → ${update.latest}`}
      onClose={onClose}
      size="md"
    >
      <div className="px-5 py-4 flex flex-col gap-4 text-xs text-kb-text-secondary">
        {recommended ? (
          <div>
            <p className="text-kb-text-primary">Agent {update.version} has problems that newer releases fix:</p>
            <ul className="mt-2 flex flex-col gap-1.5 list-disc pl-4">
              {(update.reasons ?? []).map((r) => (
                <li key={r}>{r}</li>
              ))}
            </ul>
          </div>
        ) : (
          <p className="text-kb-text-primary">
            Agent {update.latest} is available. Nothing in {update.version} needs fixing now: upgrade when it suits you.
          </p>
        )}

        <div>
          <p>KubeBolt never upgrades the agent itself: it is your Helm release. Run this against the cluster:</p>
          <div className="mt-2 relative rounded-lg border border-kb-border bg-kb-bg">
            <pre className="px-3 py-2.5 pr-20 font-mono text-[11px] leading-relaxed text-kb-text-primary whitespace-pre-wrap break-all">{command}</pre>
            <button
              type="button"
              onClick={copy}
              className="absolute top-2 right-2 inline-flex items-center gap-1 px-2 py-1 rounded-md border border-kb-border-active bg-kb-card text-[10px] font-medium text-kb-text-primary hover:border-kb-accent hover:text-kb-accent transition-colors"
            >
              {copied ? <Check className="w-3 h-3" aria-hidden="true" /> : <Copy className="w-3 h-3" aria-hidden="true" />}
              {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
          <ul className="mt-2 flex flex-col gap-1 text-[11px]">
            <li>Installed with another release name or namespace? Change <code className="font-mono text-kb-text-primary">kubebolt-agent</code> and <code className="font-mono text-kb-text-primary">kubebolt-system</code>.</li>
            <li>
              <code className="font-mono text-kb-text-primary">--reset-then-reuse-values</code> keeps your settings and takes the new chart&apos;s defaults (Helm 3.14 or later; on older Helm use <code className="font-mono text-kb-text-primary">--reuse-values</code>).
            </li>
            <li>With ArgoCD or Flux, set the chart version to {update.latest} in the Application or HelmRelease instead.</li>
          </ul>
        </div>

        <a
          href={agentReleaseNotesUrl(update.latest)}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 self-start text-[11px] font-medium text-kb-accent hover:underline"
        >
          What changed in {update.latest}
          <ExternalLink className="w-3 h-3" aria-hidden="true" />
        </a>
      </div>
    </Modal>
  )
}

// copyText copies to the clipboard, falling back to a hidden textarea where
// the Clipboard API is refused (an insecure origin, an old browser).
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // fall through to the textarea
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
