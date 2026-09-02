import { describe, it, expect } from 'vitest'
import { shortenNodeName } from './NodesSummaryStrip'

// The bug this replaces: the helper kept the LAST 20 characters, which is right
// for AWS and wrong for every managed pool that puts the identity first. An AKS
// node rendered as "…-30800577-vmss000000" — the visible half is what every node
// in the pool shares, and the pool name was the part that got cut.
describe('shortenNodeName', () => {
  it('keeps the pool name on AKS instead of the shared suffix', () => {
    const got = shortenNodeName('aks-tmpdefault-30800577-vmss000000')
    expect(got).toContain('aks-tmpdefault')
    expect(got).toContain('vmss000000')
    // The generated VMSS hash is the one part nobody reads.
    expect(got).not.toContain('30800577')
  })

  it('distinguishes two nodes of the same pool', () => {
    const a = shortenNodeName('aks-tmpdefault-30800577-vmss000000')
    const b = shortenNodeName('aks-tmpdefault-30800577-vmss000001')
    expect(a).not.toEqual(b)
  })

  it('distinguishes two pools of the same cluster', () => {
    const a = shortenNodeName('aks-default-31740332-vmss000000')
    const b = shortenNodeName('aks-tmpdefault-30800577-vmss000000')
    expect(a).not.toEqual(b)
  })

  it('leaves AWS names alone — they are short once the FQDN suffix is gone', () => {
    expect(shortenNodeName('ip-10-0-1-23.ec2.internal')).toBe('ip-10-0-1-23')
    expect(shortenNodeName('ip-10-0-1-23.compute.internal')).toBe('ip-10-0-1-23')
  })

  it('keeps the cluster name on GKE', () => {
    const got = shortenNodeName('gke-prod-euw1-default-pool-a1b2c3d4-xyz9')
    expect(got.startsWith('gke-prod')).toBe(true)
    expect(got).toContain('xyz9')
  })

  it('never returns more than the budget', () => {
    for (const n of [
      'aks-tmpdefault-30800577-vmss000000',
      'gke-prod-euw1-default-pool-a1b2c3d4-xyz9',
      'averylongsinglesegmentnodenamewithnohyphensatall',
    ]) {
      expect(shortenNodeName(n).length).toBeLessThanOrEqual(26)
    }
  })

  it('handles a name with no separators without dropping both ends', () => {
    const got = shortenNodeName('averylongsinglesegmentnodenamewithnohyphensatall')
    expect(got.startsWith('averylong')).toBe(true)
    expect(got.endsWith('atall')).toBe(true)
  })

  it('short names pass through untouched', () => {
    expect(shortenNodeName('node-1')).toBe('node-1')
  })
})
