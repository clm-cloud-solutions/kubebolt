// ROUTE_SCOPE — de qué ALTITUD es cada ruta (S4 del plan de navegación de dos
// ámbitos, `strategy/design/kubebolt-two-scope-navigation.md`).
//
// El producto tiene dos alturas y hasta ahora ninguna estaba declarada:
//
//   global  — la flota / la organización. No hay "cluster activo" que valga.
//   cluster — el cluster que el switcher tiene seleccionado ahora mismo.
//
// Que una superficie sea de una u otra NO es cuestión de gusto: lo dice su
// backend (§1 del doc, "el clasificador").
//
//   cluster-scope  ⇔  está dentro de `requireConnector`, o llama
//                     activeClusterUID() / Connector() / MetricsOnlyClusterID()
//   global-scope   ⇔  se llavea SÓLO por activeTenantID()
//
// Hasta ahora la altitud se INFERÍA de la ruta en tres sitios distintos, con
// tres reglas distintas, y por eso derivó:
//
//   1. `PLATFORM_ROUTE_PREFIXES` en Layout — la lista que decide si una página
//      se sustituye por la cascada de empty-states del cluster. Acumuló una
//      entrada muerta (`/settings`, sin ninguna ruta registrada) y llegó tarde
//      a dos superficies: `/security` estuvo un tiempo fuera y la pantalla se
//      tragaba una página que la API sí sabía responder.
//   2. `pathname === '/fleet'` en el Topbar, para el alcance de ⌘K.
//   3. `DASHBOARD_PATHS`, acoplado a `/` (ver utils/routes.ts).
//
// Una tabla explícita mata las tres. Este módulo es sólo la tabla y su
// consulta — el gating de chrome (esconder el switcher, el pill LIVE, los
// port-forwards, desmontar el WebSocket de recursos) llega en S5 y consume
// esto. S4 no cambia nada visual.
//
// **La red de seguridad es el test, no el default.** `scope.test.ts` recorre
// las rutas realmente registradas en App.tsx y en ee/registry.tsx y falla si
// alguna no está declarada aquí. Sin ese test cualquier default es una
// trampa: dar `cluster` por defecto reproduce el bug de `/security` (la
// página desaparece cuando el cluster no responde), y dar `global` deja a una
// lista de recursos sin sus empty-states y soltando un 503 en crudo.

// `public` es una tercera altitud que el doc no nombra porque razona sobre la
// app autenticada, pero existe en el código: login, recuperación de contraseña,
// aceptar invitación, signup y onboarding se montan FUERA de RequireAuth y
// FUERA de Layout — no tienen sidebar, ni switcher, ni WebSocket, ni tenant
// resuelto todavía. La encontró el test de exhaustividad en su primera pasada.
//
// Merece valor propio en vez de colarse como `cluster`: en S5 el gating de
// chrome se escribe como "monta el WebSocket de recursos si el ámbito es
// cluster", y una pantalla de login clasificada como cluster sería justo el
// tipo de suposición que revienta lejos de donde se escribió.
import { useLocation } from 'react-router-dom'
import { eeRouteScopes } from '@/ee/registry'

export type RouteScope = 'global' | 'cluster' | 'public'

// GLOBAL — se leen con el tenant del usuario y nunca tocan un connector.
// Ninguna depende del cluster activo, así que un cluster caído no debe
// sustituirlas por "Cluster unreachable".
const GLOBAL_ROUTES = [
  '/home', // portada: usePlan, listClusters, useFleetRollup, listFindings
  '/fleet', // implementación de referencia del modelo (scope=fleet)
  '/security', // + /security/{configuration,permissions,compliance,runtime}
  '/admin', // access · agents · ai · system · api-tokens
  // La edición añade las suyas: /account (plan & usage) y /platform (consola
  // del operador) sólo existen en EE, y se declaran en su registry.
  ...eeRouteScopes.global,
] as const

// CLUSTER — dentro de `requireConnector`, o resuelven el cluster activo.
//
// Las cuatro que viven FUERA del guard y aun así son cluster-scope existen
// para que un cluster metrics-only degrade en vez de dar 503; el doc las trata
// como lista cerrada y el guard en Go (findings/routes) es quien lo vigila.
const CLUSTER_ROUTES = [
  '/', // Overview — exacta, ver rootIsCluster abajo
  '/capacity',
  '/reliability',
  '/cost',
  '/insights',
  '/events',
  '/namespaces',
  '/rbac',
  '/map',
  '/applications',
  // Gestionar clusters (alta, renombrado, borrado) es trabajo DE cluster:
  // Fleet es la superficie global de la flota y ésta es la de administrarla,
  // así que vive con el resto del menú de cluster. Es además la única ruta de
  // este ámbito que NO lee del cluster activo — ver CLUSTER_INDEPENDENT abajo.
  '/clusters',
  // Autopilot (EE): el servicio corre contra el cluster activo (un solo
  // cluster, ver el proxy de WS-6), así que su hub baja con él — lo declara el
  // registry de la edición.
  ...eeRouteScopes.cluster,
  // Las 25 listas de recursos. Se escriben una por una a propósito: derivar
  // el ámbito de "no es ninguna de las globales" es exactamente la inferencia
  // por string que esta tabla existe para eliminar.
  '/pods',
  '/nodes',
  '/deployments',
  '/statefulsets',
  '/daemonsets',
  '/jobs',
  '/cronjobs',
  '/services',
  '/ingresses',
  '/networkpolicies',
  '/ciliumnetworkpolicies',
  '/ciliumclusterwidenetworkpolicies',
  '/pdbs',
  '/certificates',
  '/argocdapps',
  '/vpas',
  '/serviceaccounts',
  '/gateways',
  '/httproutes',
  '/endpoints',
  '/pvcs',
  '/pvs',
  '/storageclasses',
  '/configmaps',
  '/secrets',
  '/hpas',
] as const

// PUBLIC — se montan fuera de RequireAuth y de Layout. No hay chrome que
// esconder ni cluster que resolver; el onboarding además existe precisamente
// porque una org recién creada todavía no tiene ninguno.
const PUBLIC_ROUTES = [
  '/login',
  // Los flujos que dependen de correo —recuperar contraseña, aceptar
  // invitación, signup, onboarding— son de la edición multi-org.
  ...eeRouteScopes.public,
] as const

// El detalle de recurso se registra como `/:type/:namespace/:name`, un
// comodín que casa con CUALQUIER primer segmento. No hay prefijo que lo
// exprese, así que es lo único que cae en el default — y por eso el default
// es `cluster`. Declarado aquí para que el test lo reconozca como cubierto a
// propósito y no como un olvido.
export const CATCH_ALL_ROUTE = '/:type/:namespace/:name'

// Prefijos ordenados de más largo a más corto, para que `/platform/settings`
// gane a `/platform` si algún día divergen. Se calcula una vez.
const ORDERED: ReadonlyArray<readonly [string, RouteScope]> = [
  ...GLOBAL_ROUTES.map((p) => [p, 'global'] as const),
  ...CLUSTER_ROUTES.filter((p) => p !== '/').map((p) => [p, 'cluster'] as const),
  ...PUBLIC_ROUTES.map((p) => [p, 'public'] as const),
].sort((a, b) => b[0].length - a[0].length)

/**
 * routeScope — la altitud de un pathname.
 *
 * Casa por prefijo de SEGMENTO, no por substring: `/podsomething` no es
 * `/pods`. La raíz se compara exacta, porque como prefijo casaría con todo.
 */
export function routeScope(pathname: string): RouteScope {
  // Normaliza la barra final para que `/fleet/` y `/fleet` no discrepen.
  const path = pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname
  if (path === '' || path === '/') return 'cluster' // Overview
  for (const [prefix, scope] of ORDERED) {
    if (path === prefix || path.startsWith(prefix + '/')) return scope
  }
  // Sólo llega aquí el comodín del detalle de recurso. Ver CATCH_ALL_ROUTE.
  return 'cluster'
}

/**
 * isGlobalScope — ¿en qué menú y con qué chrome se pinta esta pantalla?
 */
export function isGlobalScope(pathname: string): boolean {
  return routeScope(pathname) === 'global'
}

// Rutas de ámbito de CLUSTER que aun así no leen del cluster activo.
//
// Son dos preguntas distintas que conviene no volver a fundir:
//
//   ámbito                → en qué menú vives y qué chrome te rodea
//   ¿lees el cluster?     → si el cluster está caído, ¿tienes algo que enseñar?
//
// Coinciden en todas las rutas menos ésta. `/clusters` es la pantalla de
// administrar clusters —dar de alta, renombrar, borrar—, así que su sitio es el
// menú de cluster; pero su contenido sale de la lista de contextos de la org, no
// de ningún connector. Y es la SALIDA DE EMERGENCIA: la pantalla a la que vas
// justamente cuando el cluster activo no responde. Si la cascada de
// empty-states la tapara con "Cluster unreachable", el usuario quedaría
// encerrado en un cluster roto sin forma de cambiar de aire.
const CLUSTER_INDEPENDENT = ['/clusters']

/**
 * survivesDeadCluster — si esta pantalla debe renderizarse aunque el cluster
 * activo esté inalcanzable. Es la pregunta que respondía
 * `PLATFORM_ROUTE_PREFIXES` en Layout, ahora con su excepción declarada en vez
 * de escondida dentro de una lista de prefijos.
 */
export function survivesDeadCluster(pathname: string): boolean {
  if (routeScope(pathname) === 'global') return true
  return CLUSTER_INDEPENDENT.some((p) => pathname === p || pathname.startsWith(p + '/'))
}

/** Sólo para el test de exhaustividad. */
export const SCOPE_TABLE = { GLOBAL_ROUTES, CLUSTER_ROUTES, PUBLIC_ROUTES } as const

/**
 * useScope — la altitud de la pantalla actual.
 *
 * El ámbito es función de la RUTA, no del estado: el cluster activo persiste al
 * salir a global, así que volver a entrar te devuelve donde estabas. Derivarlo
 * del estado —"¿hay cluster seleccionado?"— daría siempre `cluster` y sería
 * justo la confusión que el modelo separa (§5 del plan).
 */
export function useScope(): RouteScope {
  const { pathname } = useLocation()
  return routeScope(pathname)
}
