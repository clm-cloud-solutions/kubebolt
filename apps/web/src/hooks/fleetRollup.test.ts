import { describe, it, expect } from 'vitest'
import { deltaWindowDays } from './useFleetRollup'

// La ventana de comparación no es una decisión de producto que se pueda
// discutir: con 15 días guardados NO se puede comparar contra hace 30. El plan
// que compra retención compra el horizonte del delta, y este test fija que la
// regla salga de ahí y no de un número escrito a mano en un componente.

describe('deltaWindowDays', () => {
  it('Free (15d) sólo puede mirar 7 días atrás', () => {
    expect(deltaWindowDays(15)).toBe(7)
  })

  it('Team (30d) alcanza el mes', () => {
    expect(deltaWindowDays(30)).toBe(30)
  })

  it('Business (90d) también, sin pedir más de lo que hace falta', () => {
    // Podría comparar contra el trimestre, pero «vs mes pasado» es la lectura
    // que un responsable de coste reconoce; 90 días no mejora la decisión.
    expect(deltaWindowDays(90)).toBe(30)
  })

  it('sin caps —OSS, self-hosted— asume histórico suficiente', () => {
    // El cliente guarda lo que quiera en su propio disco. Degradar a 7 días
    // ahí sería aplicar un límite de SaaS a quien no lo tiene.
    expect(deltaWindowDays(undefined)).toBe(30)
    expect(deltaWindowDays(0)).toBe(30)
  })

  it('una retención más corta que la ventana corta nunca pide más de lo que hay', () => {
    // El caso que produciría un delta permanentemente vacío sin que nada lo
    // explique: pedir 30d contra 7 de retención.
    for (const days of [1, 3, 7, 14]) {
      expect(deltaWindowDays(days)).toBeLessThanOrEqual(Math.max(7, days))
    }
  })
})
