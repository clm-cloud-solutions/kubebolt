import { describe, it, expect } from 'vitest'
import { routeScope, isGlobalScope, survivesDeadCluster, CATCH_ALL_ROUTE, SCOPE_TABLE } from './scope'

// El test que evita que la tabla de ámbitos derive.
//
// La deriva ya ocurrió con la lista que ROUTE_SCOPE sustituye: acumuló una
// entrada muerta (`/settings`) y tardó en incluir `/security`, y mientras tanto
// esa pantalla se sustituía por "Cluster unreachable" aunque la API sabía
// responderla. El fallo no fue de criterio — fue que nadie se enteró.
//
// Por eso este test no comprueba opiniones: lee las rutas REALMENTE
// REGISTRADAS del código fuente y exige que cada una esté declarada. Añadir una
// ruta nueva sin decidir su altitud pone el suite en rojo en el mismo commit.

// Las fuentes se leen por el pipeline de Vite (`?raw`) y no por `node:fs`: el
// tsconfig del front no lleva los tipos de Node, así que un `readFileSync`
// pasa los tests pero rompe `npm run build`. Y de regalo resuelve la costura
// OSS/EE — si `ee/registry.tsx` no existe, el glob devuelve `{}` en vez de
// petar, que es justo lo que hará la homologación.
const APP_SRC = import.meta.glob('../App.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>
const EE_SRC = import.meta.glob('../ee/registry.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

/**
 * Extrae los `path="..."` de las fuentes de rutas.
 *
 * Sólo se queda con los ABSOLUTOS. Los relativos (`approvals` dentro de
 * `<Route path="/autopilot">`) no se pueden clasificar sueltos —su altitud es
 * la del padre— y el emparejamiento por prefijo ya los cubre: `/autopilot` está
 * declarado, así que `/autopilot/approvals` resuelve igual.
 */
const ALL_PATHS = [...Object.values(APP_SRC), ...Object.values(EE_SRC)].flatMap((src) =>
  [...src.matchAll(/path="([^"]+)"/g)].map((m) => m[1]).filter((p) => p.startsWith('/')),
)

/** Sustituye `:param` por un segmento concreto para poder resolver el path. */
function concrete(path: string): string {
  return path.replace(/:[^/]+/g, 'x')
}

describe('ROUTE_SCOPE — exhaustividad sobre las rutas registradas', () => {
  it('encuentra las rutas del código (si no, el resto del test no prueba nada)', () => {
    // Guardia contra el peor modo de fallo de un test que lee fuentes: que el
    // regex deje de casar y todo pase en verde sobre una lista vacía.
    expect(ALL_PATHS.length).toBeGreaterThan(40)
    expect(ALL_PATHS).toContain('/fleet')
    expect(ALL_PATHS).toContain('/pods')
  })

  it('toda ruta registrada está declarada explícitamente en la tabla', () => {
    const declared = new Set<string>([
      ...SCOPE_TABLE.GLOBAL_ROUTES,
      ...SCOPE_TABLE.CLUSTER_ROUTES,
      ...SCOPE_TABLE.PUBLIC_ROUTES,
    ])

    const missing = ALL_PATHS.filter((path) => {
      if (path === CATCH_ALL_ROUTE) return false // cubierto a propósito
      if (declared.has(path)) return false
      // Una sub-ruta cuenta como cubierta si su prefijo de segmento lo está:
      // `/security/runtime` hereda de `/security`, `/admin/ai` de `/admin`.
      return ![...declared].some((prefix) => path.startsWith(prefix + '/'))
    })

    expect(missing, `rutas sin ámbito declarado: ${missing.join(', ')}`).toEqual([])
  })

  it('ninguna ruta declarada es un prefijo muerto', () => {
    // El otro lado del mismo problema: `/settings` sobrevivió años en la lista
    // vieja sin que existiera ninguna ruta con ese path. Una entrada que no
    // protege nada engaña a quien la lee sobre qué superficies hay.
    const registered = ALL_PATHS.map(concrete)
    const dead = [
      ...SCOPE_TABLE.GLOBAL_ROUTES,
      ...SCOPE_TABLE.CLUSTER_ROUTES,
      ...SCOPE_TABLE.PUBLIC_ROUTES,
    ].filter(
      (prefix) =>
        prefix !== '/' &&
        !registered.some((p) => p === prefix || p.startsWith(prefix + '/')),
    )
    expect(dead, `prefijos declarados que ninguna ruta usa: ${dead.join(', ')}`).toEqual([])
  })
})

describe('routeScope', () => {
  it('clasifica las superficies globales', () => {
    expect(routeScope('/home')).toBe('global')
    expect(routeScope('/fleet')).toBe('global')
    expect(routeScope('/security')).toBe('global')
    expect(routeScope('/security/runtime')).toBe('global')
    expect(routeScope('/admin/agents')).toBe('global')
    // /account y /platform son superficies EE: su registry las declara, y el
    // test de la edición las cubre.
  })

  it('clasifica las superficies de cluster', () => {
    expect(routeScope('/')).toBe('cluster')
    expect(routeScope('/capacity')).toBe('cluster')
    expect(routeScope('/cost')).toBe('cluster')
    expect(routeScope('/pods')).toBe('cluster')
    expect(routeScope('/map')).toBe('cluster')
    // /autopilot es EE; lo declara su registry.
  })

  it('clasifica las públicas, que no tienen chrome que esconder', () => {
    // La categoría que destapó este mismo test: se montan fuera de RequireAuth
    // y de Layout. Clasificarlas como cluster haría que S5 intentara montarles
    // el WebSocket de recursos de un cluster que aún no hay.
    expect(routeScope('/login')).toBe('public')
    // /signup, /onboarding y /accept-invite son EE; los declara su registry.
    expect(isGlobalScope('/login')).toBe(false)
  })

  it('el detalle de recurso cae en el comodín y es de cluster', () => {
    // `/:type/:namespace/:name` casa con cualquier primer segmento, así que es
    // lo único que resuelve por default en vez de por tabla.
    expect(routeScope('/pods/default/api-7c9f')).toBe('cluster')
    expect(routeScope('/persistentvolumeclaims/data/pgdata')).toBe('cluster')
  })

  it('la raíz no se comporta como prefijo', () => {
    // Con emparejamiento por substring, '/' casaría con TODO y la tabla
    // entera daría 'cluster'.
    expect(routeScope('/')).toBe('cluster')
    expect(routeScope('/home')).toBe('global')
  })

  it('casa por segmento, no por substring', () => {
    // '/podsomething' no es '/pods'. Sin el separador, un recurso futuro cuyo
    // nombre empiece igual que uno global heredaría el ámbito equivocado.
    expect(routeScope('/homelab')).toBe('cluster')
    expect(routeScope('/fleetwide')).toBe('cluster')
  })

  it('la barra final no cambia el ámbito', () => {
    expect(routeScope('/fleet/')).toBe('global')
    expect(routeScope('/pods/')).toBe('cluster')
  })

  it('administrar clusters es trabajo de cluster, no de flota', () => {
    // Fleet es la superficie global —mirar la flota entera—; `/clusters` es la
    // de operarla, así que vive abajo. Los dos ítems existían en el mismo grupo
    // y se confundían por eso.
    expect(routeScope('/clusters')).toBe('cluster')
    expect(routeScope('/fleet')).toBe('global')
  })
})

describe('survivesDeadCluster', () => {
  it('cubre las 7 entradas vivas de PLATFORM_ROUTE_PREFIXES', () => {
    // La octava, /settings, no tenía ruta. Si alguna cambiara de respuesta, la
    // cascada de empty-states del cluster volvería a comerse una página que la
    // API sabe responder.
    // (/account y /platform, las globales de EE, las cubre el test de esa edición.)
    for (const p of ['/clusters', '/admin/access', '/home', '/fleet', '/security']) {
      expect(survivesDeadCluster(p), p).toBe(true)
    }
  })

  it('/clusters sobrevive AUNQUE sea de ámbito de cluster', () => {
    // Las dos preguntas se separaron justo por este caso: el ámbito dice en qué
    // menú vives, y esto dice si tienes algo que enseñar con el cluster caído.
    // /clusters es la salida de emergencia — la pantalla a la que vas cuando el
    // activo no responde—, así que taparla con "Cluster unreachable" dejaría al
    // usuario encerrado sin forma de cambiar de cluster.
    expect(routeScope('/clusters')).toBe('cluster')
    expect(survivesDeadCluster('/clusters')).toBe(true)
  })

  it('una pantalla que SÍ lee del cluster no sobrevive', () => {
    expect(survivesDeadCluster('/pods')).toBe(false)
    expect(survivesDeadCluster('/')).toBe(false)
    expect(survivesDeadCluster('/capacity')).toBe(false)
  })
})
