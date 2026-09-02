import { NavLink } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Bug, ShieldCheck, SlidersHorizontal, Radio, KeyRound } from 'lucide-react'

// SecuritySubTabs splits Security into the five questions it actually answers.
//
// They were one ranked list, and that was the mistake. A CVE with CVSS 7.5 and a
// CIS control are not comparable: you do not triage one against the other, they
// are fixed by different actions, and in many organizations they are not even
// read by the same person. Sorting them together forces an equivalence that does
// not exist.
//
// It also produced a counting bug rather than merely an ugly page. A compliance
// control's "N failing" IS the sum of workload checks we store individually, so
// one flat list added the same problem twice. Splitting by CONCERN removes that
// structurally — compliance counts controls, vulnerabilities count CVEs, and
// nothing sums across them.
//
//   Vulnerabilities  what am I running that is vulnerable   → rebuild the image
//   Configuration    how are my workloads configured        → edit the manifest
//   Permissions      who can do what in this cluster        → edit the Role
//   Compliance       do I meet the standard                 → depends on control
//   Runtime          what is happening right now            → respond
//
// Permissions sits apart from Configuration even though both end in "edit a
// manifest": the subject is a Role rather than a workload, and the question —
// who holds which power — is a different person's call in most organizations.
// Folding it in would also have mixed Roles into a table built around workloads.
//
// Same underline-active grammar as DashboardSubTabs, for the same reason: these
// are lenses on one surface, not separate destinations.
export function SecuritySubTabs() {
  return (
    <nav className="flex items-center gap-1 border-b border-kb-border -mt-1 mb-4">
      <SubTab to="/security" end icon={<Bug className="w-3.5 h-3.5" />}>
        Vulnerabilities
      </SubTab>
      <SubTab to="/security/configuration" icon={<SlidersHorizontal className="w-3.5 h-3.5" />}>
        Configuration
      </SubTab>
      <SubTab to="/security/permissions" icon={<KeyRound className="w-3.5 h-3.5" />}>
        Permissions
      </SubTab>
      <SubTab to="/security/compliance" icon={<ShieldCheck className="w-3.5 h-3.5" />}>
        Compliance
      </SubTab>
      <SubTab to="/security/runtime" icon={<Radio className="w-3.5 h-3.5" />}>
        Runtime
      </SubTab>
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
