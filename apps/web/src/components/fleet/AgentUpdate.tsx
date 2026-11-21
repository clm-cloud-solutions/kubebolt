import { useState } from 'react'
import { ArrowUpCircle, Check, Copy, ExternalLink } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import type { AgentUpdate } from '@/types/kubernetes'

// The «agent out of date» notice (debt-24). KubeBolt never upgrades an agent:
// it is the customer's Helm release, often under GitOps. It says how far
// behind the cluster's oldest live agent is, why that matters when it does,
// and the command to run.

// agentVersionLabel names the version: "1.4.8", or "before 1.4.4" for an agent
// that does not report its own (every agent before 1.4.4 sends 1.0.0).
export function agentVersionLabel(u: AgentUpdate): string {
  return u.version || (u.before ? `before ${u.before}` : '')
}

// The same in a chip, where space is short: "<1.4.4".
function agentVersionShort(u: AgentUpdate): string {
  return u.version || (u.before ? `<${u.before}` : '')
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
      className={`flex items-center gap-1 max-w-full min-w-0 px-1.5 py-0.5 rounded text-[9px] font-mono whitespace-nowrap transition-colors ${
        recommended
          ? 'bg-kobi-st-warn-dim text-kobi-st-warn hover:opacity-80'
          : 'bg-kb-elevated text-kb-text-secondary hover:text-kb-text-primary'
      }`}
    >
      <ArrowUpCircle className="w-2.5 h-2.5 shrink-0" aria-hidden="true" />
      {/* Truncates inside a narrow card instead of running past its edge. */}
      <span className="truncate">
        Agent {agentVersionShort(update)} · {recommended ? 'update recommended' : 'update available'}
      </span>
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
      title={`Agent ${agentVersionLabel(update)} has problems ${update.latest} fixes. Clusters shows why and the Helm command.`}
      className="inline-flex items-center gap-1 shrink-0 whitespace-nowrap px-1.5 py-0.5 rounded text-[9px] font-mono bg-kobi-st-warn-dim text-kobi-st-warn"
    >
      <ArrowUpCircle className="w-2.5 h-2.5" aria-hidden="true" />
      Agent {agentVersionShort(update)}
    </span>
  )
}

// AgentUpdateModal — why, and how: the reasons for this version, the Helm
// command, and what to change when the agent was installed differently.
export function AgentUpdateModal({ clusterName, update, onClose }: { clusterName: string; update: AgentUpdate; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  // The command comes from the server (agentversion.UpgradeCommand), the same
  // Kobi gives.
  const command = update.upgrade ?? ''
  const recommended = update.level === 'recommended'
  const label = agentVersionLabel(update)

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
      title={clusterName}
      onClose={onClose}
      size="md"
    >
      <div className="px-5 py-4 flex flex-col gap-4 text-xs text-kb-text-secondary">
        <p className="font-mono text-[12px] text-kb-text-primary">
          Agent {label} → {update.latest}
        </p>
        {update.before && (
          <p>
            This agent does not report its own version: every agent before {update.before} sends 1.0.0. Applications
            shows the installed chart (the Helm release kubebolt-agent).
          </p>
        )}
        {recommended ? (
          <div>
            <p className="text-kb-text-primary">Agent {label} has problems that newer releases fix:</p>
            <ul className="mt-2 flex flex-col gap-1.5 list-disc pl-4">
              {(update.reasons ?? []).map((r) => (
                <li key={r}>{r}</li>
              ))}
            </ul>
          </div>
        ) : (
          <p className="text-kb-text-primary">
            Agent {update.latest} is available. Nothing in {label} needs fixing now: upgrade when it suits you.
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
            {/* Seen live: the context was another cluster's, and the command
                pasted as is would have upgraded the wrong one. */}
            <li className="text-kb-text-primary">
              It runs against kubectl&apos;s current context: check it is this cluster (<code className="font-mono">kubectl config current-context</code>) or add <code className="font-mono">--kube-context &lt;name&gt;</code>.
            </li>
            <li>Installed with another release name or namespace? Change <code className="font-mono text-kb-text-primary">kubebolt-agent</code> and <code className="font-mono text-kb-text-primary">kubebolt-system</code>.</li>
            <li>
              <code className="font-mono text-kb-text-primary">--reset-then-reuse-values</code> keeps your settings and takes the new chart&apos;s defaults (Helm 3.14 or later; on older Helm use <code className="font-mono text-kb-text-primary">--reuse-values</code>). If it was installed with a full copy of the chart&apos;s values file, those old defaults count as settings and stay: <code className="font-mono text-kb-text-primary">helm get values kubebolt-agent -n kubebolt-system</code> shows what it keeps.
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
