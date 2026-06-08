import { Lock } from 'lucide-react'

interface EnterpriseFeatureNoticeProps {
  /** Short headline. Defaults to the standard upgrade title. */
  title?: string
  /** One- or two-sentence explanation of what the feature unlocks. */
  message: string
  className?: string
}

/**
 * EnterpriseFeatureNotice is the standard "this needs SaaS/EE" panel. Render it
 * when the backend returns the `requires_ee` boundary (see isRequiresEE) or to
 * pre-empt an action that OSS doesn't support (e.g. multiple orgs/teams). Uses
 * only existing tokens — no new colors or fonts.
 */
export function EnterpriseFeatureNotice({
  title = 'Available in SaaS & Enterprise',
  message,
  className = '',
}: EnterpriseFeatureNoticeProps) {
  // Matches the Settings info-banner style (clear blue accent + border) so the
  // notice reads at a glance instead of washing out against the page.
  return (
    <div className={`flex items-start gap-2 rounded-xl border border-status-info-dim bg-status-info-dim/30 p-4 text-xs text-status-info ${className}`}>
      <Lock className="w-4 h-4 shrink-0 mt-0.5" />
      <div className="min-w-0">
        <div className="font-semibold mb-0.5">{title}</div>
        <div className="text-kb-text-secondary leading-relaxed">{message}</div>
      </div>
    </div>
  )
}
