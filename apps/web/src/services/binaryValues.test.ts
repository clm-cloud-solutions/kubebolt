import { describe, expect, it } from 'vitest'
import { ApiError, binaryValuesOf } from './api'

// The editor offers "Keep as binary" only when the server named the keys it
// refused because their value decodes to binary.
describe('binaryValuesOf', () => {
  it('names the keys the server refused as binary', () => {
    const e = new ApiError(400, '"password" under data decodes to binary data', { binaryValues: ['password', 'username'] })
    expect(binaryValuesOf(e)).toEqual(['password', 'username'])
  })

  it('is null for any other refusal or error', () => {
    expect(binaryValuesOf(new ApiError(400, 'renamed', { error: 'renamed' }))).toBeNull()
    expect(binaryValuesOf(new ApiError(400, 'empty', { binaryValues: [] }))).toBeNull()
    expect(binaryValuesOf(new Error('network'))).toBeNull()
  })
})
