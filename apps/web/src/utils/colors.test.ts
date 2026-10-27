import { describe, expect, it } from 'vitest'
import { getDotColor, getStatusBgColor } from './colors'

describe('a Deployment scaled to 0', () => {
  it('is neutral, never the green of Running', () => {
    expect(getStatusBgColor('Scaled to 0')).toBe('bg-kb-elevated text-kb-text-tertiary')
    expect(getStatusBgColor('Running')).toBe('bg-status-ok-dim text-status-ok')
    expect(getDotColor('Scaled to 0')).not.toBe(getDotColor('Running'))
  })
})
