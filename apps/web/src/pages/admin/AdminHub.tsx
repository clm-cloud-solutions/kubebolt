import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { useSearchParams } from 'react-router-dom'

export interface AdminHubTab {
  key: string
  label: string
  Icon: LucideIcon
  /**
   * Optional title rendered above the content. Use it for tabs whose content
   * has no header of its own (the Settings-style tabs); omit it for full pages
   * that already render their own header (Users, Teams, …) so we never stack
   * two titles.
   */
  title?: string
  subtitle?: string
  render: () => ReactNode
}

/**
 * AdminHub is the shared shell for the grouped Administration hubs (Access,
 * Agents & Ingest, AI, System). It renders an underline tab bar — the same
 * style as the former Settings page — and the active tab's content. The active
 * tab is URL-driven (`?tab=`) so each sub-view is linkable and the sidebar /
 * deep links can target a specific tab.
 */
export function AdminHub({ tabs, rightSlot }: { tabs: AdminHubTab[]; rightSlot?: ReactNode }) {
  const [params, setParams] = useSearchParams()
  const active = tabs.find((t) => t.key === params.get('tab')) ?? tabs[0]

  return (
    // pb-24 mirrors the old Settings page: keeps a form's action bar clear of
    // the floating Copilot toggle on tall tabs.
    <div className="pb-24">
      <div className="border-b border-kb-border mb-5 flex items-center justify-between gap-4">
        <div className="flex items-center gap-1">
          {tabs.map((tab) => {
            const isActive = tab.key === active.key
            return (
              <button
                key={tab.key}
                type="button"
                onClick={() => setParams({ tab: tab.key })}
                className={`flex items-center gap-2 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-colors ${
                  isActive
                    ? 'border-kb-accent text-kb-accent'
                    : 'border-transparent text-kb-text-secondary hover:text-kb-text-primary'
                }`}
              >
                <tab.Icon className="w-3.5 h-3.5" />
                {tab.label}
              </button>
            )
          })}
        </div>
        {rightSlot && <div className="pb-2 shrink-0">{rightSlot}</div>}
      </div>

      {active.title && (
        <div className="mb-5">
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <active.Icon className="w-5 h-5" />
            {active.title}
          </h1>
          {active.subtitle && <p className="text-xs text-kb-text-tertiary mt-0.5">{active.subtitle}</p>}
        </div>
      )}

      <div>{active.render()}</div>
    </div>
  )
}
