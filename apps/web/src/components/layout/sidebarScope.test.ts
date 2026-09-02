import { describe, it, expect } from 'vitest'
import { routeScope } from '@/utils/scope'

// La invariante del split: TODO ítem de un menú apunta a una ruta de la altitud
// de ese menú.
//
// Sin ella el split se rompe de la peor forma posible: pulsas un enlace y el
// menú entero cambia bajo el cursor, así que el siguiente clic —que ibas a dar
// a ciegas, porque el ítem estaba ahí hace medio segundo— cae en otra cosa. Y
// no es hipotético: `eePinnedNavItems` trae Autopilot (cluster) y Plan & usage
// (global) en la MISMA lista, de modo que meterla entera en cualquiera de los
// dos menús habría plantado justo ese enlace.
//
// El test lee los ítems de la fuente en vez de importar el componente: el
// módulo arrastra JSX, iconos y el registry de la edición, y montar todo eso
// para comprobar una tabla de strings es pagar un render por una lista.
const SIDEBAR_SRC = Object.values(
  import.meta.glob('./Sidebar.tsx', { query: '?raw', import: 'default', eager: true }),
)[0] as string

/**
 * Extrae los `path:` de una sección declarada entre `const <name>` y el `]`
 * final de nivel cero. Suficiente para tablas literales, que es lo que son.
 */
function pathsOfConst(name: string): string[] {
  const start = SIDEBAR_SRC.indexOf(`const ${name}`)
  if (start === -1) throw new Error(`no encuentro ${name} en Sidebar.tsx`)
  // Corta en la siguiente declaración de nivel cero.
  const rest = SIDEBAR_SRC.slice(start + 1)
  const end = rest.search(/\n(const|export|function) /)
  const block = end === -1 ? rest : rest.slice(0, end)
  return [...block.matchAll(/path: '([^']+)'/g)].map((m) => m[1])
}

const clusterPaths = pathsOfConst('clusterSections')
const globalPaths = pathsOfConst('globalSections')
const adminPaths = pathsOfConst('adminItems')
// `platformItems` (la consola cross-org) sólo existe en el Sidebar de EE; el
// test de esa edición la cubre.

describe('split del sidebar — cada menú sólo lleva rutas de su altitud', () => {
  it('las tablas se leen (si no, el resto no prueba nada)', () => {
    expect(clusterPaths.length).toBeGreaterThan(25)
    expect(globalPaths.length).toBeGreaterThanOrEqual(3)
    expect(adminPaths.length).toBeGreaterThanOrEqual(4)
  })

  it('el menú de cluster no enlaza nada global', () => {
    const wrong = clusterPaths.filter((p) => routeScope(p) !== 'cluster')
    expect(wrong, `rutas globales en el menú de cluster: ${wrong.join(', ')}`).toEqual([])
  })

  it('el menú global no enlaza nada de cluster', () => {
    // Administración se pinta dentro del menú global, así que entra en la
    // misma comprobación.
    const all = [...globalPaths, ...adminPaths]
    const wrong = all.filter((p) => routeScope(p) !== 'global')
    expect(wrong, `rutas de cluster en el menú global: ${wrong.join(', ')}`).toEqual([])
  })

  it('ningún ítem está en los dos menús', () => {
    // Duplicar un enlace haría que el rail marcara activo el mismo ítem en dos
    // alturas, y con eso el menú deja de decirte dónde estás.
    const dupes = clusterPaths.filter((p) => globalPaths.includes(p))
    expect(dupes, `ítems duplicados: ${dupes.join(', ')}`).toEqual([])
  })

  it('las superficies que suben a global ya no están abajo', () => {
    // Las tres que movió el split. Si alguna reapareciera en el menú de
    // cluster, volvería el síntoma que lo motivó: Fleet y Security apagándose
    // en un cluster metrics-only pese a no necesitar connector.
    for (const p of ['/home', '/fleet', '/security']) {
      expect(clusterPaths, p).not.toContain(p)
      expect(globalPaths).toContain(p)
    }
  })

  it('Fleet arriba y Clusters abajo — mirar la flota vs operarla', () => {
    // El reparto que más se confunde, porque los dos ítems vivían en el mismo
    // grupo: Fleet es la vista global de todos los clusters; /clusters es la
    // pantalla de darlos de alta, renombrarlos y borrarlos, que es trabajo de
    // cluster.
    expect(globalPaths).toContain('/fleet')
    expect(clusterPaths).toContain('/clusters')
  })
})
