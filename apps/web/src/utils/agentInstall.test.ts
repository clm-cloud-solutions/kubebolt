import { describe, it, expect } from 'vitest'
import type { AgentInstallConfig } from '@/services/api'
import { agentAddressProblem, buildFirstClusterScript, buildHelmCommand, LOCAL_BACKEND_URL, LOCAL_CLUSTER_TOOLS, unresolvedPlaceholders } from './agentInstall'

const hosted: AgentInstallConfig = {
  backendUrl: 'agent.kubebolt.io:443',
  clusterName: 'prod-eu',
  rbacMode: 'reader',
  hubbleEnabled: false,
  authMode: 'ingest-token',
  tlsEnabled: true,
}

describe('unresolvedPlaceholders', () => {
  // The bug this pins: both orgs that connected in Sept–Oct copied the command
  // before the token existed. `<token-secret-name>` is a shell redirection, so
  // that paste failed before Helm ran.
  it('lists every <…> marker the command still carries', () => {
    const cmd = buildHelmCommand({ backendUrl: '', authMode: 'ingest-token' }, [])
    expect(unresolvedPlaceholders(cmd).sort()).toEqual(['<BACKEND_URL>', '<cluster-name>', '<token-secret-name>'])
  })

  it('is empty once every value is set', () => {
    const cmd = buildHelmCommand({ ...hosted, authTokenSecret: 'kubebolt-agent-token' }, [])
    expect(unresolvedPlaceholders(cmd)).toEqual([])
  })
})

describe('buildHelmCommand', () => {
  it('keeps `helm install` by default and switches to upgrade --install on request', () => {
    expect(buildHelmCommand(hosted, [])).toMatch(/^helm install kubebolt-agent /)
    expect(buildHelmCommand(hosted, [], { upsert: true })).toMatch(/^helm upgrade --install kubebolt-agent /)
  })
})

describe('buildFirstClusterScript', () => {
  const script = buildFirstClusterScript(hosted, 'kbi_secret123')

  it('is one block with the token inside and nothing left to fill in', () => {
    expect(script).toContain('--from-literal=token=kbi_secret123')
    expect(script).toContain('--set cluster.name=prod-eu')
    expect(script).toContain('--set auth.ingestToken.existingSecret=kubebolt-agent-token')
    expect(script).toContain('--set tls.enabled=true')
    expect(unresolvedPlaceholders(script)).toEqual([])
  })

  // Pasting the block twice (a re-generated token, a second try) must not fail
  // halfway: AlreadyExists on the namespace or the Secret, or "cannot re-use a
  // name" from Helm, would leave the old token in place.
  it('can be run again without failing', () => {
    expect(script).toContain('kubectl create namespace kubebolt-system --dry-run=client -o yaml | kubectl apply -f -')
    expect(script).toMatch(/create secret generic kubebolt-agent-token[\s\S]*--dry-run=client -o yaml \| kubectl apply -f -/)
    expect(script).toContain('helm upgrade --install kubebolt-agent')
  })

  it('uses the namespace and Secret name from the config when set', () => {
    const s = buildFirstClusterScript({ ...hosted, namespace: 'obs', authTokenSecret: 'kb-token' }, 't')
    expect(s).toContain('kubectl create namespace obs')
    expect(s).toContain('-n obs create secret generic kb-token')
    expect(s).toContain('--namespace obs --create-namespace')
  })
})

describe('LOCAL_CLUSTER_TOOLS', () => {
  it('offers kind, k3d and minikube, each with a create command', () => {
    expect(LOCAL_CLUSTER_TOOLS.map((t) => t.id)).toEqual(['kind', 'k3d', 'minikube'])
    for (const t of LOCAL_CLUSTER_TOOLS) expect(t.create).toMatch(/kubebolt-try/)
  })
})

describe('agentAddressProblem', () => {
  it('accepts host:port, IPs and bracketed IPv6', () => {
    for (const a of [LOCAL_BACKEND_URL, 'kubebolt.example.com:9090', '10.0.0.4:30090', '[fd00::4]:9090', ' kubebolt:9090 ']) {
      expect(agentAddressProblem(a)).toBeNull()
    }
  })

  // From inside a cluster a loopback address is the agent's own pod: the
  // install succeeds and the agent never connects.
  it('refuses loopback addresses', () => {
    for (const a of ['localhost:9090', '127.0.0.1:9090', '[::1]:9090', '0.0.0.0:9090']) {
      expect(agentAddressProblem(a)).toMatch(/own pod/)
    }
  })

  it('refuses what the chart cannot take', () => {
    expect(agentAddressProblem('')).not.toBeNull()
    expect(agentAddressProblem('<NODE_IP>:30090')).toMatch(/<…>/)
    expect(agentAddressProblem('https://kubebolt.example.com:9090')).toMatch(/scheme/)
    expect(agentAddressProblem('kubebolt.example.com')).toMatch(/host:port/)
    expect(agentAddressProblem('kubebolt.example.com:99999')).toMatch(/host:port/)
  })
})
