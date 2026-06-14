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

// Extra items appended to the sidebar's "Pinned" section (Sidebar.tsx).
export const eePinnedNavItems: NavItem[] = []
