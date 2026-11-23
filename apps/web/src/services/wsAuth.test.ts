import { describe, it, expect } from 'vitest'
import { wsProtocols, WS_PROTOCOL } from './wsAuth'

describe('wsProtocols', () => {
  it('offers the fixed protocol and the token as unpadded base64url', () => {
    const jwt = 'eyJhbGciOiJIUzI1NiJ9.eyJ1aWQiOiJ1MSJ9.c2lnbmF0dXJl'
    const [proto, bearer] = wsProtocols(jwt)
    expect(proto).toBe(WS_PROTOCOL)
    expect(bearer.startsWith('base64url.bearer.kubebolt.io.')).toBe(true)
    const b64 = bearer.slice('base64url.bearer.kubebolt.io.'.length)
    expect(b64).toMatch(/^[A-Za-z0-9_-]+$/)
    const padded = b64.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (b64.length % 4)) % 4)
    expect(atob(padded)).toBe(jwt)
  })

  it('offers only the fixed protocol without a token', () => {
    expect(wsProtocols(null)).toEqual([WS_PROTOCOL])
  })
})
