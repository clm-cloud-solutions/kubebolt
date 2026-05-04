import { NavLink } from 'react-router-dom'
import type { ReactNode } from 'react'

// DashboardSubTabs is the sub-navigation bar shown at the top of the
// dashboard surfaces (Overview, Capacity, eventually Reliability).
// Sits BELOW the Topbar's primary toggle (Dashboard / Cluster Map),
// not in it — Cluster Map is a different mode of looking at the
// cluster (topology), while Overview / Capacity are sub-views of the
// same "monitoring" mode.
//
// Visual: underline-active pattern, deliberately quieter than the
// Topbar's pill toggle so the hierarchy reads "primary nav up there,
// section nav down here". Border-bottom on the nav itself + per-tab
// border lifted via -mb-px so the active tab's underline merges into
// the nav's bottom edge instead of stacking awkwardly.
export function DashboardSubTabs() {
  return (
    <nav className="flex items-center gap-1 border-b border-kb-border -mt-1 mb-4">
      <SubTab to="/" end>Overview</SubTab>
      <SubTab to="/capacity">Capacity</SubTab>
      {/* Reliability tab will land here when Hubble L7 data is
          detected in VM. Hidden until then — empty Reliability tab
          would be noise, not invitation. */}
    </nav>
  )
}

function SubTab({
  to,
  end,
  children,
}: {
  to: string
  end?: boolean
  children: ReactNode
}) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        `px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-colors ${
          isActive
            ? 'border-kb-accent text-kb-text-primary'
            : 'border-transparent text-kb-text-tertiary hover:text-kb-text-secondary'
        }`
      }
    >
      {children}
    </NavLink>
  )
}
