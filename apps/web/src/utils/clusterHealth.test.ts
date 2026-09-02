import { describe, it, expect } from 'vitest'
import { healthFromInsights, healthLabel, fleetHealthSummary } from './clusterHealth'

// Este módulo existe por un bug concreto: Fleet decía «Healthy» de un cluster
// cuyo propio Overview decía «warning». Lo que se prueba aquí es que el
// veredicto sea UNO y que la ausencia de dato no se disfrace de salud.

describe('healthFromInsights', () => {
  it('un crítico manda sobre todo lo demás', () => {
    expect(healthFromInsights({ critical: 1, warning: 9, info: 30 })).toBe('critical')
  })

  it('warnings sin críticos degradan a warning', () => {
    // El caso exacto del choque: el Overview decía «3 warning insights» y Fleet
    // lo pintaba verde.
    expect(healthFromInsights({ warning: 3 })).toBe('warning')
  })

  it('info NO degrada', () => {
    // Son observaciones. Contarlas pintaría de ámbar media flota de forma
    // permanente, y un color permanente deja de mirarse.
    expect(healthFromInsights({ info: 15 })).toBe('healthy')
  })

  it('cero de todo es sano', () => {
    expect(healthFromInsights({ critical: 0, warning: 0, info: 0 })).toBe('healthy')
    expect(healthFromInsights({})).toBe('healthy')
  })

  it('sin dato es "unknown", NUNCA sano', () => {
    // La distinción que más se pierde al pintar tablas: «lo miramos y está
    // bien» vs «no lo hemos mirado». Afirmar salud sobre un cluster que nadie
    // evaluó es la mentira que este módulo evita.
    expect(healthFromInsights(undefined)).toBe('unknown')
  })
})

describe('healthLabel', () => {
  it('lleva el NÚMERO, como pide el diseño', () => {
    // «2 warnings» y «11 warnings» piden respuestas distintas; una insignia que
    // sólo dijera WARNING obligaría a entrar para saber cuánto.
    expect(healthLabel('warning', { warning: 2 })).toBe('2 WARNINGS')
    expect(healthLabel('critical', { critical: 7 })).toBe('7 CRITICAL')
  })

  it('singulariza', () => {
    expect(healthLabel('warning', { warning: 1 })).toBe('1 WARNING')
    expect(healthLabel('critical', { critical: 1 })).toBe('1 CRITICAL')
  })

  it('sano y sin datos se distinguen a simple vista', () => {
    expect(healthLabel('healthy')).toBe('HEALTHY')
    expect(healthLabel('unknown')).toBe('NO DATA')
  })
})

describe('fleetHealthSummary', () => {
  it('cuenta CLUSTERS, no insights', () => {
    // «1 de 3 clusters» es la unidad en la que se actúa. Sumar insights
    // mezclaría uno con once avisos leves y otro con un crítico.
    const summary = fleetHealthSummary(['a', 'b', 'c'], {
      a: { warning: 11 },
      b: { critical: 1 },
      c: {},
    })
    expect(summary).toEqual({ healthy: 1, warning: 1, critical: 1, unknown: 0 })
  })

  it('separa los que no tienen datos en vez de darlos por sanos', () => {
    // Permite decir «2 sin datos» en lugar de fingir que la flota entera está
    // evaluada — que es lo que haría contarlos como healthy.
    const summary = fleetHealthSummary(['a', 'b', 'c'], { a: {} })
    expect(summary).toEqual({ healthy: 1, warning: 0, critical: 0, unknown: 2 })
  })

  it('sin resumen entero, todo es desconocido', () => {
    // Persistencia deshabilitada. La flota se pinta igual, sin insignias de
    // salud — no en verde.
    expect(fleetHealthSummary(['a', 'b'], undefined)).toEqual({
      healthy: 0, warning: 0, critical: 0, unknown: 2,
    })
  })
})
