// Browser-tab title resolution from the current route.
//
// Layout.tsx subscribes to location.pathname and calls titleForPath() to
// pick a human-readable label that becomes the document title in the
// format "<label> · KubeBolt". Centralising the mapping here means a new
// route only needs one entry in ROUTE_TITLES instead of touching every
// page component to wire up a useDocumentTitle hook.
//
// Adding a new route: drop it in ROUTE_TITLES with its display name.
// Adding a dynamic title that depends on fetched data (e.g. cluster
// display name): derive it inside the page component and call
// document.title there directly — this map is for static-label routes.

// Title shown on the home route (/). The Overview IS the landing page of
// the app, so its tab title mirrors the marketing site's full product
// name + tagline instead of the per-page "<label> · KubeBolt" format —
// a tab opened on the dashboard should read as the product, not as one
// section among many. Kept in sync with the <title> in index.html for
// the pre-hydration render.
export const HOME_DOCUMENT_TITLE = 'KubeBolt - Open-source Kubernetes operations'

const ROUTE_TITLES: Record<string, string> = {
  '/capacity':      'Capacity',
  '/reliability':   'Reliability',
  '/insights':      'Insights',
  '/map':           'Cluster Map',

  // Resource lists
  '/pods':            'Pods',
  '/nodes':           'Nodes',
  '/deployments':     'Deployments',
  '/statefulsets':    'StatefulSets',
  '/daemonsets':      'DaemonSets',
  '/jobs':            'Jobs',
  '/cronjobs':        'CronJobs',
  '/services':        'Services',
  '/ingresses':       'Ingresses',
  '/gateways':        'Gateways',
  '/httproutes':      'HTTPRoutes',
  '/endpoints':       'Endpoints',
  '/pvcs':            'Persistent Volume Claims',
  '/pvs':             'Persistent Volumes',
  '/storageclasses':  'Storage Classes',
  '/configmaps':      'ConfigMaps',
  '/secrets':         'Secrets',
  '/hpas':            'Horizontal Pod Autoscalers',

  // Cluster / platform pages
  '/clusters':        'Clusters',
  '/namespaces':      'Namespaces',
  '/events':          'Events',
  '/rbac':            'RBAC',
  '/settings':        'Settings',

  // Admin
  '/admin/users':           'Users',
  '/admin/agent-tokens':    'Agent Tokens',
  '/admin/ingest-limits':   'Ingest Limits',
  '/admin/notifications':   'Notifications',
  '/admin/copilot-usage':   'Copilot Usage',
  '/admin/integrations':    'Integrations',
  '/admin/teams':           'Teams',
  '/admin/service-accounts':'Service Accounts',
  '/admin/authentication':  'Authentication',

  // Auth
  '/login': 'Sign in',
}

// titleForPath returns the page label for a given pathname, or undefined
// when the path doesn't match any known route (caller falls back to the
// product name alone).
export function titleForPath(pathname: string): string | undefined {
  if (ROUTE_TITLES[pathname]) return ROUTE_TITLES[pathname]

  // Resource detail route: /:type/:namespace/:name — the operator most
  // wants to see the resource NAME in the tab so a row of tabs reads as
  // distinct resources, not as a row of "Resource detail · KubeBolt".
  // Decoding handles namespaces / names that came through the router
  // with URL-encoded characters.
  const parts = pathname.split('/').filter(Boolean)
  if (parts.length === 3) {
    try {
      return decodeURIComponent(parts[2])
    } catch {
      return parts[2]
    }
  }

  return undefined
}

// resolveDocumentTitle returns the final string the browser shows in the
// tab for a given pathname. The home route (/) gets the full marketing
// title; every other route uses the "<label> · KubeBolt" format with
// the page label leading (most variable, helps tab disambiguation when
// many are open) and the product name as a stable suffix.
export function resolveDocumentTitle(pathname: string): string {
  if (pathname === '/') return HOME_DOCUMENT_TITLE
  const label = titleForPath(pathname)
  const PRODUCT = 'KubeBolt'
  return label ? `${label} · ${PRODUCT}` : PRODUCT
}
