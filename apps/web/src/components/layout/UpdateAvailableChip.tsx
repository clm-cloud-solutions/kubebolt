import { useState } from 'react'
import { Sparkles, ExternalLink, X } from 'lucide-react'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'

// UpdateAvailableChip renders a discreet pill in the Topbar when a
// newer stable KubeBolt release is available on GitHub. Clicking the
// version opens the release notes in a new tab; clicking X dismisses
// the chip for that specific version only (a future v1.14.0 will
// re-surface because the localStorage key includes the version).
//
// Renders nothing when:
//   - the backend hasn't responded yet
//   - the operator disabled update-check (admin toggle or env var)
//   - the running binary already matches the latest stable
//   - the user dismissed this exact version
//
// Pattern mirrors PortForwardIndicator in the same file — only
// occupies space when there's something to show.

const dismissKeyFor = (version: string) => `kb-dismissed-update-${version}`

export function UpdateAvailableChip() {
  const data = useUpdateCheck()
  const [, forceRerender] = useState(0)

  if (!data || !data.enabled || !data.isUpdateAvailable || !data.latestVersion) {
    return null
  }

  const dismissed = typeof window !== 'undefined' &&
    window.localStorage.getItem(dismissKeyFor(data.latestVersion)) === '1'
  if (dismissed) return null

  const handleDismiss = (e: React.MouseEvent) => {
    e.stopPropagation()
    try {
      window.localStorage.setItem(dismissKeyFor(data.latestVersion!), '1')
    } catch {}
    forceRerender(n => n + 1)
  }

  return (
    <div className="flex items-center gap-1 pl-2 pr-1 py-1 rounded-md bg-status-info-dim text-status-info hover:bg-status-info/20 transition-colors">
      <a
        href={data.releaseUrl}
        target="_blank"
        rel="noopener noreferrer"
        title={`Release notes for ${data.latestVersion}`}
        className="flex items-center gap-1.5"
      >
        <Sparkles className="w-3.5 h-3.5" />
        <span className="text-[10px] font-mono font-medium uppercase tracking-[0.08em]">
          Update {data.latestVersion}
        </span>
        <ExternalLink className="w-3 h-3" />
      </a>
      <button
        type="button"
        onClick={handleDismiss}
        title="Dismiss until next release"
        className="p-0.5 rounded hover:bg-status-info/30 transition-colors"
      >
        <X className="w-3 h-3" />
      </button>
    </div>
  )
}
