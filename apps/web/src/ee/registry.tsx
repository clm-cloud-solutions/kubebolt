import type { ReactNode } from 'react'
import type { NavItem } from '@/components/layout/Sidebar'

// EE extension registry — the community (OSS) build ships these empty.
// The Enterprise build (kubebolt-ee) overrides THIS file to inject Autopilot's
// routes and pinned nav item. Keeping App.tsx and Sidebar.tsx referencing this
// module is what lets those two files stay byte-identical between OSS and EE:
// the edition-specific content lives here, not as edits to the shared files.

// Extra <Route> elements injected into <Routes> (App.tsx). A fragment of
// <Route>s or null; React Router flattens fragments.
export const eeRoutes: ReactNode = null

// Public (pre-auth) EE routes — signup/onboarding. Empty in OSS.
export const eePublicRoutes: ReactNode = null

// Extra items appended to the sidebar's "Pinned" section (Sidebar.tsx).
export const eePinnedNavItems: NavItem[] = []

// Sign-in panel copy. The OSS build lists what OSS ships; the Enterprise build
// overrides this module and names Autopilot. Keeping the strings here (not in
// AuthShell) is what lets AuthShell.tsx stay byte-identical across editions.
export const authFeatures: string[] = [
  'Kobi, your AI copilot for Kubernetes',
  'Investigate incidents & apply the fix you approve',
  'Security & compliance from your own scanners',
  'Every cluster on one screen',
]
export const authTagline =
  'Kobi, your AI copilot, investigates incidents and proposes the fix you approve. Metrics, logs, topology, security and cost across every cluster.'

// Route prefixes the edition adds to the scope table (utils/scope.ts) — the
// altitude of every EE-only surface, declared where the surface's routes are.
// OSS registers none: /account, /platform, /autopilot and the mail-driven
// public flows (/signup, /onboarding, /forgot-password, /reset-password,
// /accept-invite) exist only in the Enterprise build. The scope test walks the
// routes actually registered and fails on a prefix nothing uses, so a stale
// entry here cannot survive.
export const eeRouteScopes: { global: string[]; cluster: string[]; public: string[] } = {
  global: [],
  cluster: [],
  public: [],
}

// Per-environment insight tuning (the Environment tuning tab of Admin →
// Insights and the «Adjust rule» deep link into it) needs the cluster
// environment classification, which is Enterprise billing metadata. OSS
// keeps the global layer only: the tab is hidden and «Adjust rule» lands on
// the Rules tab.
export const eeInsightEnvironments = false
