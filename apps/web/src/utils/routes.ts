// Shared route helpers — kept tiny on purpose, since the routes
// table itself lives in App.tsx and we only centralize the small
// set of derived predicates that multiple components need to
// agree on.

// Dashboard sub-routes — the three lenses on the same "monitoring
// the cluster" surface. All three should mark the Sidebar's
// Overview item AND the Topbar's Dashboard pill as active, since
// from the user's perspective they're on the dashboard regardless
// of which sub-tab. NavLink's built-in `end` prop only matches
// exact paths, so we drive the active state from this list
// instead.
//
// Append future sub-tabs here when they land (e.g. /costs,
// /storage). Must stay in sync with the SubTab entries in
// DashboardSubTabs.
export const DASHBOARD_PATHS = ['/', '/capacity', '/reliability'] as const

export function isDashboardPath(pathname: string): boolean {
  return (DASHBOARD_PATHS as readonly string[]).includes(pathname)
}
