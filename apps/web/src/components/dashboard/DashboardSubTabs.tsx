import { NavLink } from 'react-router-dom'
import type { ReactNode } from 'react'
import { LayoutDashboard, Gauge, Activity } from 'lucide-react'
import { useHubbleAvailable } from '@/hooks/useHubbleAvailable'

// DashboardSubTabs is the sub-navigation bar shown at the top of the
// dashboard surfaces (Overview, Capacity, Reliability). Sits BELOW
// the Topbar's primary toggle (Dashboard / Cluster Map), not in it —
// Cluster Map is a different mode of looking at the cluster
// (topology), while these are sub-views of the same "monitoring"
// mode.
//
// Reliability is conditional: surfaces only when Hubble is shipping
// flows into VM (L3/L4 baseline — `useHubbleAvailable` since
// 1.11/Item 3). The tab stays visible on clusters where Hubble runs
// but L7 isn't exposed (GKE managed Dataplane V2) so the operator
// gets actionable copy explaining the limitation — see
// `HubbleL7UnavailablePlaceholder` in ReliabilityPage.tsx. Hide
// only when no Hubble at all, to avoid a tab that yields nothing.
//
// Visual: underline-active pattern with a small lucide icon ahead
// of each label. Icons are 14px so they don't compete with the
// underline for visual weight — the active tab is still primarily
// signaled by the underline + text color, with the icon as a
// secondary identifier matching the Sidebar's icon-per-item rhythm.
// Border-bottom on the nav itself + per-tab border lifted via
// -mb-px so the active tab's underline merges into the nav's
// bottom edge instead of stacking awkwardly.
export function DashboardSubTabs() {
  const { available: hubbleAvailable } = useHubbleAvailable()
  return (
    <nav className="flex items-center gap-1 border-b border-kb-border -mt-1 mb-4">
      <SubTab to="/" end icon={<LayoutDashboard className="w-3.5 h-3.5" />}>
        Overview
      </SubTab>
      <SubTab to="/capacity" icon={<Gauge className="w-3.5 h-3.5" />}>
        Capacity
      </SubTab>
      {hubbleAvailable && (
        <SubTab to="/reliability" icon={<Activity className="w-3.5 h-3.5" />}>
          Reliability
        </SubTab>
      )}
    </nav>
  )
}

function SubTab({
  to,
  end,
  icon,
  children,
}: {
  to: string
  end?: boolean
  icon: ReactNode
  children: ReactNode
}) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        `flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-colors ${
          isActive
            ? 'border-kb-accent text-kb-text-primary'
            : 'border-transparent text-kb-text-tertiary hover:text-kb-text-secondary'
        }`
      }
    >
      <span className="shrink-0">{icon}</span>
      <span>{children}</span>
    </NavLink>
  )
}
