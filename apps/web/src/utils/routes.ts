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
// Append future sub-tabs here when they land (e.g. /storage). Must
// stay in sync with the SubTab entries in DashboardSubTabs.
export const DASHBOARD_PATHS = ['/', '/capacity', '/reliability', '/cost'] as const

export function isDashboardPath(pathname: string): boolean {
  return (DASHBOARD_PATHS as readonly string[]).includes(pathname)
}

// canonicalListRoute maps the backend's full resource kind (the
// "type" field carried in URLs, audit logs, and Kobi proposal
// targets) to the alias the frontend's list routes are registered
// under in App.tsx. Without this, opening a PVC detail at
// /persistentvolumeclaims/.../... and then deleting lands the
// post-delete navigate(`/${type}`) on a route that doesn't exist.
//
// Add an entry here whenever a new resource kind is exposed both by
// its full apiserver kind AND by a short alias in the route table.
// Resource types whose URL form already matches their list route
// (deployments, pods, services, etc.) don't need an entry — they
// fall through unchanged.
const TYPE_ALIAS_FOR_ROUTE: Record<string, string> = {
  persistentvolumeclaims: 'pvcs',
  persistentvolumes: 'pvs',
  horizontalpodautoscalers: 'hpas',
}

export function canonicalListRoute(resourceType: string): string {
  return TYPE_ALIAS_FOR_ROUTE[resourceType] ?? resourceType
}

// ─── Dónde aterriza alguien que acaba de entrar ──────────────────────────────
//
// Home, no la portada del primer cluster.
//
// Overview responde "¿cómo está ESTE cluster?", que sólo puedes formular cuando
// ya sabes cuál mirar; Home responde la de antes: "¿qué me necesita?". Y "el
// primero de la lista" no es el que importa — es el que quedó primero por orden
// de alta, así que cada sesión empezaba mirando datos de un cluster que nadie
// eligió, con la autoridad que da estar en la pantalla de inicio.
//
// Lo peor era el alta: una org recién creada no tiene clusters, así que los
// tres flujos de entrada (signup, OAuth, wizard) desembocaban en "No clusters
// configured" — un estado de error como PRIMERA pantalla del producto. Home
// está hecho para ese caso y lleva el CTA de conectar.
//
// Coste asumido: quien tiene un solo cluster paga un clic por sesión. A cambio
// Home sigue diciéndole si el agente se calló, si no llegan métricas, qué
// hallazgos críticos tiene y qué hizo Autopilot mientras no miraba.
//
// Deliberadamente NO depende del número de clusters: cambiaría solo el día que
// alguien conecta el segundo, y "depende" es la peor respuesta que puede dar el
// soporte a "¿dónde entro?".
//
// Esto NO mueve ninguna URL. `/` sigue siendo Overview; lo que cambia es el
// destino por defecto tras autenticarse. La promoción de `/` (S8 del plan de
// dos ámbitos) rompe marcadores y va aparte.
export const POST_AUTH_LANDING = '/home'

// Rutas que nunca son un destino válido de vuelta: aterrizar ahí tras
// autenticarse manda al usuario de vuelta al formulario que acaba de rellenar.
const NOT_A_DESTINATION = ['/login', '/signup', '/onboarding', '/forgot-password', '/reset-password', '/accept-invite']

/**
 * landingAfterAuth — a dónde ir después de autenticarse.
 *
 * Respeta el enlace profundo: si a alguien lo mandó al login una URL concreta
 * (RequireAuth guarda el pathname en `state.from`), vuelve a ESA. Sólo cambia
 * el DEFAULT, que es lo que se decidió.
 */
export function landingAfterAuth(from?: string | null): string {
  // `//host` empieza por `/` pero es una URL relativa al protocolo. React Router
  // la resuelve contra el mismo origen, así que hoy no saca a nadie de la app —
  // se rechaza igual porque el valor viene del estado del cliente y esta función
  // es la que alguien copiará el día que el destino sí se pase a `location`.
  if (!from || !from.startsWith('/') || from.startsWith('//')) return POST_AUTH_LANDING
  if (NOT_A_DESTINATION.some((p) => from === p || from.startsWith(p + '/'))) {
    return POST_AUTH_LANDING
  }
  return from
}
