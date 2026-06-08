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

  // Admin — grouped domain hubs
  '/admin/access':    'Access',
  '/admin/agents':    'Agents & Ingest',
  '/admin/ai':        'AI (Kobi)',
  '/admin/system':    'System',
  '/admin/api-tokens': 'API Tokens',

  // Auth
  '/login': 'Sign in',
}

// Short type prefix used in resource detail tab titles. Matches the
// kubectl alias an operator already reads in their terminal so the tab
// title is instantly classifiable as "this is a Deployment" / "this
// is a Service" without parsing the name itself. Types without an
// entry fall through to the URL segment as-is.
const TYPE_ABBREVIATIONS: Record<string, string> = {
  pods:             'pod',
  nodes:            'node',
  deployments:      'deploy',
  statefulsets:     'sts',
  daemonsets:       'ds',
  replicasets:      'rs',
  jobs:             'job',
  cronjobs:         'cj',
  services:         'svc',
  ingresses:        'ing',
  gateways:         'gw',
  httproutes:       'httproute',
  endpoints:        'ep',
  endpointslices:   'eps',
  pvcs:             'pvc',
  pvs:              'pv',
  storageclasses:   'sc',
  configmaps:       'cm',
  secrets:          'secret',
  hpas:             'hpa',
  namespaces:       'ns',
}

// titleForPath returns the page label for a given pathname, or undefined
// when the path doesn't match any known route (caller falls back to the
// product name alone).
export function titleForPath(pathname: string): string | undefined {
  if (ROUTE_TITLES[pathname]) return ROUTE_TITLES[pathname]

  // Resource detail route: /:type/:namespace/:name — format as
  // "<type-abbrev>/<name>" (kubectl-style: deploy/demo-load,
  // pod/api-server, svc/coredns). The type prefix disambiguates a
  // row of detail tabs that would otherwise read as bare resource
  // names, and gives any title-based usage analytics a clean token
  // to count which resource type the operator visited.
  // Decoding handles namespaces / names that came through the router
  // with URL-encoded characters.
  const parts = pathname.split('/').filter(Boolean)
  if (parts.length === 3) {
    const type = parts[0]
    let name: string
    try { name = decodeURIComponent(parts[2]) } catch { name = parts[2] }
    const abbr = TYPE_ABBREVIATIONS[type] ?? type
    return `${abbr}/${name}`
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
