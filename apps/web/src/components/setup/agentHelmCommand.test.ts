import { describe, expect, it } from 'vitest'
import type { AgentInstallDefaults } from '@/services/api'
import {
  BACKEND_URL_PLACEHOLDER,
  buildSetupAgentHelmCommand,
  resolveSetupAgentTarget,
  setupAgentBlocked,
} from './agentHelmCommand'

const inCluster: AgentInstallDefaults = {
  deploymentMode: 'in-cluster',
  selfNamespace: 'kubebolt',
  internalBackendUrl: 'kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090',
  agentNamespace: 'kubebolt-system',
}

describe('resolveSetupAgentTarget', () => {
  it('in-cluster: dials the discovered agent-ingest Service and installs next to KubeBolt', () => {
    expect(resolveSetupAgentTarget(inCluster)).toEqual({
      backendUrl: 'kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090',
      namespace: 'kubebolt',
      inferred: true,
    })
  })

  it('in-cluster under a non-default release/namespace keeps the discovered values', () => {
    const t = resolveSetupAgentTarget({
      ...inCluster,
      selfNamespace: 'observability',
      internalBackendUrl: 'kb-kubebolt-agent-ingest.observability.svc.cluster.local:9090',
    })
    expect(t.backendUrl).toBe('kb-kubebolt-agent-ingest.observability.svc.cluster.local:9090')
    expect(t.namespace).toBe('observability')
  })

  it('external with a reachable endpoint uses it, in the agent namespace', () => {
    expect(
      resolveSetupAgentTarget({ deploymentMode: 'external', externalEndpoint: 'kubebolt.example.com:9090', agentNamespace: 'kubebolt-system' }),
    ).toEqual({ backendUrl: 'kubebolt.example.com:9090', namespace: 'kubebolt-system', inferred: true })
  })

  it('without topology hints falls back to a visible placeholder, never an empty backendUrl', () => {
    for (const d of [undefined, { deploymentMode: 'external', agentNamespace: 'kubebolt-system' } as AgentInstallDefaults]) {
      const t = resolveSetupAgentTarget(d)
      expect(t.backendUrl).toBe(BACKEND_URL_PLACEHOLDER)
      expect(t.inferred).toBe(false)
    }
  })
})

describe('buildSetupAgentHelmCommand', () => {
  const target = resolveSetupAgentTarget(inCluster)

  it('always sets backendUrl — the chart marks it required', () => {
    const cmd = buildSetupAgentHelmCommand({ target, rbacMode: 'reader' })
    expect(cmd).toContain('--set backendUrl=kubebolt-agent-ingest.kubebolt.svc.cluster.local:9090')
    expect(cmd).toContain('--namespace kubebolt --create-namespace')
    expect(cmd).toContain('--set rbac.mode=reader')
    expect(cmd).not.toContain('auth.')
  })

  it('renders the chosen permission tier', () => {
    expect(buildSetupAgentHelmCommand({ target, rbacMode: 'metrics' })).toContain('--set rbac.mode=metrics')
  })

  it('wires the token with the chart keys that exist (auth.ingestToken.existingSecret)', () => {
    const cmd = buildSetupAgentHelmCommand({ target, rbacMode: 'operator', tokenSecretName: 'kubebolt-agent-token' })
    expect(cmd).toContain('--set auth.mode=ingest-token')
    expect(cmd).toContain('--set auth.ingestToken.existingSecret=kubebolt-agent-token')
    expect(cmd).not.toContain('auth.ingestTokenSecret')
  })

  it('is a copy-pasteable multi-line shell command', () => {
    const lines = buildSetupAgentHelmCommand({ target, rbacMode: 'reader' }).split('\n')
    expect(lines[0]).toMatch(/^helm install kubebolt-agent oci:\/\/ghcr\.io\/.+ \\$/)
    lines.slice(0, -1).forEach((l) => expect(l.endsWith(' \\')).toBe(true))
    expect(lines[lines.length - 1].endsWith('\\')).toBe(false)
  })
})

describe('setupAgentBlocked', () => {
  it('refuses operator mode without a token', () => {
    expect(setupAgentBlocked('operator', false)).toBe(true)
    expect(setupAgentBlocked('operator', true)).toBe(false)
  })
  it('lets metrics and reader through without a token', () => {
    expect(setupAgentBlocked('metrics', false)).toBe(false)
    expect(setupAgentBlocked('reader', false)).toBe(false)
  })
})
