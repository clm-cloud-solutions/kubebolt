import { describe, it, expect } from 'vitest'
import { siteDoc } from './docs'

describe('siteDoc', () => {
  it('builds a kubebolt.io page with the trailing slash the site serves', () => {
    expect(siteDoc('agent')).toBe('https://kubebolt.io/docs/agent/')
  })
  it('adds an anchor', () => {
    expect(siteDoc('cost', 'three-ways-to-connect-opencost')).toBe('https://kubebolt.io/docs/cost/#three-ways-to-connect-opencost')
  })
})
