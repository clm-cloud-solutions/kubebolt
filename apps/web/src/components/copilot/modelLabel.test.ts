import { describe, it, expect } from 'vitest'
import { modelLabel } from './CopilotPanel'

describe('modelLabel', () => {
  it('names Claude models by family and version, others as given', () => {
    expect(modelLabel('claude-haiku-5-5')).toBe('haiku 5.5')
    expect(modelLabel('claude-sonnet-5-5-20261001')).toBe('sonnet 5.5')
    expect(modelLabel('gpt-5-mini')).toBe('gpt-5-mini')
    expect(modelLabel(undefined)).toBe('default')
  })
})
