import { describe, it, expect } from 'vitest'
import { shortToolName, externalTool, connectorDisplayName, connectorShortName, toolLabel } from './toolName'

// Field report: the Executor's timeline rendered
// `mcp__kubebolt_actions__verify_pods_ready` raw while every read showed
// clean. The stripper matched a hard-coded `mcp__kubebolt__`, and the day
// Autopilot's mutations moved to their own MCP server — they had to, two
// entries in `mcpServers` cannot share a key — that pattern stopped matching
// them. Only the Executor showed it, because it is the one agent that calls
// an action.

describe('shortToolName', () => {
  it('strips the reads server prefix', () => {
    expect(shortToolName('mcp__kubebolt__get_pod_logs')).toBe('get_pod_logs')
  })

  it('strips the actions server prefix — the one that regressed', () => {
    expect(shortToolName('mcp__kubebolt_actions__verify_pods_ready')).toBe('verify_pods_ready')
    expect(shortToolName('mcp__kubebolt_actions__patch_deployment_resources'))
      .toBe('patch_deployment_resources')
  })

  it('strips a server key it has never heard of', () => {
    // The point of matching the SHAPE rather than a known list: a third server
    // must not need a change here.
    expect(shortToolName('mcp__something_new__do_a_thing')).toBe('do_a_thing')
  })

  it('takes only the server segment, never part of the tool name', () => {
    // Non-greedy: a tool whose own name contains `__` keeps it.
    expect(shortToolName('mcp__kubebolt__get__weird__name')).toBe('get__weird__name')
  })

  it('leaves a name that is not MCP-prefixed alone', () => {
    expect(shortToolName('Read')).toBe('Read')
    expect(shortToolName('verify_pods_ready')).toBe('verify_pods_ready')
  })
})

// Cloud connector tools reach Kobi's panel as `mcp__<connector>__<tool>` and
// Autopilot's timeline as `mcp__kubebolt__cloud_<connector>_<tool>`. Before
// this helper both rendered through name.replace(/_/g, ' ') as
// "mcp  aws  call" — unreadable, and it hid the one fact the operator needs:
// that Kobi left the cluster to read this.
describe('externalTool', () => {
  it('reads the Kobi spelling', () => {
    expect(externalTool('mcp__aws__call')).toEqual({
      connectorId: 'aws',
      tool: 'call',
      label: 'AWS · call',
      consumer: 'kobi',
    })
    expect(externalTool('mcp__gcp__sqladmin_get_instance')).toEqual({
      connectorId: 'gcp',
      tool: 'sqladmin_get_instance',
      label: 'GCP · sqladmin get instance',
      consumer: 'kobi',
    })
    expect(externalTool('mcp__azure__monitor')?.label).toBe('Azure · monitor')
    expect(externalTool('mcp__azure__group_resource_list')?.tool).toBe('group_resource_list')
  })

  it('reads the Autopilot spelling served through KubeBolt\'s own MCP server', () => {
    expect(externalTool('mcp__kubebolt__cloud_aws_call')).toEqual({
      connectorId: 'aws',
      tool: 'call',
      label: 'AWS · call',
      consumer: 'autopilot',
    })
    expect(externalTool('mcp__kubebolt__cloud_gcp_monitoring_list_timeseries')?.tool)
      .toBe('monitoring_list_timeseries')
    expect(externalTool('mcp__kubebolt__cloud_azure_sql')?.connectorId).toBe('azure')
  })

  it('never mistakes KubeBolt\'s own tools for an external read', () => {
    // Same server key as the Autopilot cloud tools — the prefix alone is not enough.
    expect(externalTool('mcp__kubebolt__get_pod_logs')).toBeNull()
    expect(externalTool('mcp__kubebolt__get_external_connections')).toBeNull()
    expect(externalTool('mcp__kubebolt_actions__verify_pods_ready')).toBeNull()
    expect(externalTool('get_pod_logs')).toBeNull()
    expect(externalTool('get_external_connections')).toBeNull()
  })

  it('drops a provider prefix the tool itself still carries', () => {
    // AWS spells its own tools aws___<name>; an older binder forwarded them as is.
    expect(externalTool('mcp__aws__aws___search_documentation')?.tool).toBe('search_documentation')
  })

  it('labels a connector it has never heard of by its id', () => {
    expect(externalTool('mcp__datadog__query_metrics')?.label).toBe('Datadog · query metrics')
    expect(externalTool('mcp__kubebolt__cloud_datadog_query')?.connectorId).toBe('datadog')
  })

  it('rejects shapes with nothing after the connector', () => {
    expect(externalTool('mcp__kubebolt__cloud_aws')).toBeNull()
    expect(externalTool('mcp__kubebolt__cloud_')).toBeNull()
    expect(externalTool('cloud_aws')).toBeNull()
  })

  it('reads the Autopilot family without its server prefix too', () => {
    // KubeBolt's own MCP server names them cloud_<connector>_<tool>.
    expect(externalTool('cloud_aws_call')).toEqual({
      connectorId: 'aws',
      tool: 'call',
      label: 'AWS · call',
      consumer: 'autopilot',
    })
    expect(externalTool('cloud_gcp_sqladmin_get_instance')?.label).toBe('GCP · sqladmin get instance')
  })
})

// P4: Autopilot's timeline rendered cloud reads as `cloud_aws_call` — the
// server prefix stripped, nothing more — while Kobi's cards said "AWS · call".
describe('toolLabel', () => {
  it('labels a cloud read by its provider, in either spelling', () => {
    expect(toolLabel('mcp__kubebolt__cloud_aws_call')).toBe('AWS · call')
    expect(toolLabel('cloud_azure_monitor')).toBe('Azure · monitor')
  })

  it('strips the server prefix from everything else, as before', () => {
    expect(toolLabel('mcp__kubebolt__get_pod_logs')).toBe('get_pod_logs')
    expect(toolLabel('mcp__kubebolt_actions__verify_pods_ready')).toBe('verify_pods_ready')
  })
})

describe('connector names', () => {
  it('is short in labels and long in prose', () => {
    expect(connectorShortName('gcp')).toBe('GCP')
    expect(connectorDisplayName('gcp')).toBe('Google Cloud')
    expect(connectorDisplayName('aws')).toBe('AWS')
    expect(connectorDisplayName('grafana')).toBe('Grafana')
  })
})
