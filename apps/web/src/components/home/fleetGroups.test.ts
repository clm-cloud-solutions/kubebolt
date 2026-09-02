import { describe, it, expect } from 'vitest'
import { buildRows, sortRows, groupByTeam } from './fleetGroups'
import type { ClusterInfo } from '@/types/kubernetes'
import type { FleetClusterRollup } from '@/hooks/useFleetRollup'

// El valor de esta vista está en el ORDEN y en no inventar números. Un
// desglose alfabético obliga a leerlo entero para encontrar el problema —que es
// justo lo que existe para evitar— y un 0 donde falta el dato convierte «aún no
// lo sabemos» en «cluster vacío».

function cluster(over: Partial<ClusterInfo> & { context: string }): ClusterInfo {
  return {
    name: over.context,
    server: 'https://x',
    active: false,
    status: 'connected',
    ...over,
  } as ClusterInfo
}

const rollup = (over: Partial<FleetClusterRollup> = {}): FleetClusterRollup =>
  ({ pods: 10, nodes: 2, costMonthly: 100, ...over }) as FleetClusterRollup

describe('buildRows', () => {
  it('un cluster en error es crítico y lo dice con palabras', () => {
    const [r] = buildRows([cluster({ context: 'a', status: 'error' })], {})
    expect(r.health).toBe('crit')
    expect(r.reason).toBe('unreachable')
  })

  it('un agente que SE FUE es aviso, no error', () => {
    // Puede ser simplemente un agente que se ausentó un momento; tratarlo como
    // fallo duro pondría en rojo media flota tras un despliegue.
    // `lastSeen` es lo que lo distingue de un contexto nunca dado de alta: sale
    // de la fila de membresía durable, así que sólo existe si alguien contactó.
    const [r] = buildRows(
      [cluster({ context: 'a', status: 'disconnected', agentConnected: false, lastSeen: '2026-08-07T10:00:00Z' })],
      {},
    )
    expect(r.health).toBe('warn')
    expect(r.reason).toBe('no agent connected')
  })

  it('un contexto del kubeconfig NUNCA dado de alta no pide atención', () => {
    // Un kubeconfig de trabajo trae docenas de contextos a los que uno
    // simplemente tiene acceso. Contarlos como avería llenaba la portada de «no
    // agent connected» —10 de 12 en la flota real— y ahogaba las dos filas que
    // sí pedían algo. Es inventario, y la copia lo dice.
    const [r] = buildRows(
      [cluster({ context: 'eks-prod', status: 'disconnected', source: 'file' })],
      {},
    )
    expect(r.health).toBe('ok')
    expect(r.reason).toBe('not connected yet')
  })

  it('agente conectado que no manda nada = aviso, aunque diga connected', () => {
    // El fallo más caro de todos: la lista lo pinta sano y sus paneles salen
    // vacíos, así que el usuario culpa a la UI.
    const [r] = buildRows(
      [cluster({ context: 'a', clusterId: 'u1', agentConnected: true })],
      { u1: rollup({ pods: null, nodes: null, costMonthly: null }) },
    )
    expect(r.silent).toBe(true)
    expect(r.health).toBe('warn')
    expect(r.reason).toBe('no metrics arriving')
  })

  it('un cluster sin UID no se une al roll-up y deja null, no 0', () => {
    // Nunca conectado / UID sin resolver: no hay con qué unir. Un 0 diría que
    // el cluster está vacío, que es una afirmación distinta y falsa.
    const [r] = buildRows([cluster({ context: 'a' })], { u1: rollup() })
    expect(r.pods).toBeNull()
    expect(r.nodes).toBeNull()
    expect(r.costMonthly).toBeNull()
  })

  it('cruza los hallazgos por cluster', () => {
    const [r] = buildRows([cluster({ context: 'a', clusterId: 'u1' })], { u1: rollup() }, { u1: { critical: 3, high: 9 } })
    expect(r.critical).toBe(3)
    expect(r.high).toBe(9)
  })
})

describe('salud vs enlace — no se vuelven a fundir', () => {
  it('un cluster que reporta pero tiene warnings NO sale sano', () => {
    // El bug exacto que originó todo: Fleet decía «Healthy» de un cluster cuyo
    // Overview decía «warning», porque una miraba el cable y la otra el estado.
    const [r] = buildRows(
      [cluster({ context: 'a', clusterId: 'u1', agentConnected: true })],
      { u1: rollup() },
      undefined,
      { u1: { warning: 3 } },
    )
    expect(r.health).toBe('ok') // el enlace está en pie…
    expect(r.verdict).toBe('warning') // …y el cluster no está sano
  })

  it('un cluster sano cuyo agente se fue conserva su salud', () => {
    // La otra mitad: perder el enlace no vuelve enfermo al cluster. Son dos
    // hechos distintos y la fila tiene que poder decir los dos.
    const [r] = buildRows(
      [cluster({ context: 'a', clusterId: 'u1', status: 'disconnected', agentConnected: false, lastSeen: '2026-08-07T10:00:00Z' })],
      {},
      undefined,
      { u1: {} },
    )
    expect(r.health).toBe('warn') // el cable está caído…
    expect(r.verdict).toBe('healthy') // …pero lo último que supimos es que estaba bien
  })

  it('sin resumen de insights el veredicto es "unknown", no "healthy"', () => {
    const [r] = buildRows([cluster({ context: 'a', clusterId: 'u1' })], {}, undefined, undefined)
    expect(r.verdict).toBe('unknown')
  })
})

describe('sortRows — lo peor primero', () => {
  it('ordena por salud antes que por nombre', () => {
    const rows = buildRows(
      [
        cluster({ context: 'aaa-sano' }),
        cluster({ context: 'zzz-roto', status: 'error' }),
        cluster({ context: 'mmm-aviso', status: 'disconnected', agentConnected: false, lastSeen: '2026-08-07T10:00:00Z' }),
      ],
      {},
    )
    expect(sortRows(rows).map((r) => r.cluster.context)).toEqual(['zzz-roto', 'mmm-aviso', 'aaa-sano'])
  })

  it('a igual salud, manda el número de críticos', () => {
    const rows = buildRows(
      [cluster({ context: 'a', clusterId: 'u1' }), cluster({ context: 'b', clusterId: 'u2' })],
      {},
      { u1: { critical: 1 }, u2: { critical: 8 } },
    )
    expect(sortRows(rows).map((r) => r.cluster.context)).toEqual(['b', 'a'])
  })

  it('el orden es estable entre refrescos', () => {
    // Dos clusters igualmente sanos deben caer siempre en el mismo sitio; si
    // no, la lista baila y el usuario pierde el sitio donde estaba mirando.
    const rows = buildRows([cluster({ context: 'b' }), cluster({ context: 'a' })], {})
    expect(sortRows(rows).map((r) => r.cluster.context)).toEqual(['a', 'b'])
    expect(sortRows(rows.slice().reverse()).map((r) => r.cluster.context)).toEqual(['a', 'b'])
  })
})

describe('groupByTeam', () => {
  const teams = [
    { id: 't1', name: 'Platform', role: 'admin' as const },
    { id: 't2', name: 'Data', role: 'viewer' as const },
  ]

  it('agrupa por dueño y pone el equipo con el peor cluster arriba', () => {
    const rows = buildRows(
      [
        cluster({ context: 'p1', ownerTeamId: 't1' }),
        cluster({ context: 'd1', ownerTeamId: 't2', status: 'error' }),
      ],
      {},
    )
    const gs = groupByTeam(rows, teams)
    expect(gs.map((g) => g.teamName)).toEqual(['Data', 'Platform'])
  })

  it('los clusters sin dueño van al final aunque estén rotos', () => {
    // Es un pendiente administrativo, no un equipo; no debe competir por la
    // atención con el cluster caído de alguien.
    const rows = buildRows(
      [cluster({ context: 'x', status: 'error' }), cluster({ context: 'p1', ownerTeamId: 't1' })],
      {},
    )
    const gs = groupByTeam(rows, teams)
    expect(gs[gs.length - 1].teamName).toBe('Unassigned')
  })

  it('un equipo ajeno no se nombra', () => {
    // El id no resuelve porque el usuario no es miembro. Lo que se PINTA es
    // `teamName`, así que la garantía es que ahí nunca caiga el id crudo: un
    // «Team a3f9-…» de cabecera diría que existe un equipo del que no sabe
    // nada, y encima con su identificador.
    //
    // El `teamId` sí sigue en la estructura —es la key de React y la propiedad
    // de dueño— y eso no filtra nada: ya venía en la lista de clusters que el
    // API le sirvió a este mismo usuario. Lo que se comprueba es el render, no
    // la ausencia del dato.
    const rows = buildRows([cluster({ context: 'x', ownerTeamId: 'otro' })], {})
    const [g] = groupByTeam(rows, teams)
    expect(g.teamName).toBe('Another team')
    expect(g.teamName).not.toContain('otro')
  })

  it('una org de un solo equipo produce UN grupo — el render no pinta cabecera', () => {
    const rows = buildRows(
      [cluster({ context: 'a', ownerTeamId: 't1' }), cluster({ context: 'b', ownerTeamId: 't1' })],
      {},
    )
    expect(groupByTeam(rows, teams)).toHaveLength(1)
  })

  it('el resumen cuenta CLUSTERS que piden atención, no incidencias', () => {
    // «2 de 5 clusters» es la unidad en la que un responsable actúa. Sumar
    // avisos mezclaría un cluster caído con otro cuyo agente sólo se ausentó, y
    // daría un número más grande que dice menos.
    const rows = buildRows(
      [
        cluster({ context: 'a', ownerTeamId: 't1', status: 'error', clusterId: 'u1' }),
        cluster({ context: 'b', ownerTeamId: 't1', status: 'disconnected', agentConnected: false, lastSeen: '2026-08-07T10:00:00Z' }),
        cluster({ context: 'c', ownerTeamId: 't1', clusterId: 'u3' }),
      ],
      { u3: rollup() },
      { u1: { critical: 4 }, u3: { critical: 1, high: 7 } },
    )
    const [g] = groupByTeam(rows, teams)
    expect(g.summary).toEqual({ clusters: 3, needAttention: 2, critical: 5, high: 7 })
  })

  it('un equipo sano resume en cero, no en vacío', () => {
    const rows = buildRows([cluster({ context: 'a', ownerTeamId: 't1', clusterId: 'u1' })], { u1: rollup() })
    expect(groupByTeam(rows, teams)[0].summary).toEqual({
      clusters: 1, needAttention: 0, critical: 0, high: 0,
    })
  })

  it('las filas siguen ordenadas peor-primero DENTRO de cada equipo', () => {
    const rows = buildRows(
      [
        cluster({ context: 'sano', ownerTeamId: 't1' }),
        cluster({ context: 'roto', ownerTeamId: 't1', status: 'error' }),
      ],
      {},
    )
    expect(groupByTeam(rows, teams)[0].rows.map((r) => r.cluster.context)).toEqual(['roto', 'sano'])
  })
})
