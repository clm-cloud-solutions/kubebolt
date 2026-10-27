import { describe, it, expect } from 'vitest'
import { kindOf, toolTitle, workloadOf } from './toolTitles'

// The examples are the design's ("Etiquetas: hoy → propuesta").
describe('toolTitle', () => {
  it('says what is read, and what about', () => {
    expect(toolTitle('get_cluster_overview')).toEqual({ title: 'Cluster overview' })
    expect(toolTitle('get_workload_pods', { type: 'deployments', namespace: 'tienda', name: 'catalog-api' })).toEqual({
      title: 'Pods · catalog-api',
      chip: 'tienda',
    })
    expect(toolTitle('get_pod_logs', { namespace: 'tienda', name: 'catalog-api-f958778f8-4n5dj', since: '30m' })).toEqual({
      title: 'Logs · catalog-api',
      chip: '30 min',
    })
    expect(toolTitle('get_resource_describe', { type: 'externalsecrets', namespace: 'tienda', name: 'orders-db-credentials' })).toEqual({
      title: 'Describe · ExternalSecret',
      chip: 'orders-db-credentials',
    })
    expect(toolTitle('list_resources', { type: 'networkpolicies' })).toEqual({ title: 'NetworkPolicies', chip: 'all namespaces' })
    expect(toolTitle('get_events', { namespace: 'shop' })).toEqual({ title: 'Events', chip: 'shop' })
    expect(toolTitle('search_resources', { q: 'catalog' })).toEqual({ title: 'Search · catalog' })
    expect(toolTitle('query_metrics', { query: 'up', range: '6h' })).toEqual({ title: 'Metrics', chip: '6 h' })
    expect(toolTitle('get_findings', { severity: 'CRITICAL' })).toEqual({ title: 'Security findings', chip: 'CRITICAL' })
    expect(toolTitle('offer_cluster_switch', { cluster: 'prod-eu', question: 'q', established: 'e' })).toEqual({
      title: 'Cluster switch · prod-eu',
    })
  })

  it('says which metrics of which target, and over what window', () => {
    expect(
      toolTitle('get_workload_metrics', {
        kind: 'Deployment',
        namespace: 'shop-backend',
        name: 'catalog-api',
        metrics: ['cpu', 'memory'],
        range: '6h',
      }),
    ).toEqual({ title: 'CPU, memory · catalog-api', chip: '6 h' })
    expect(
      toolTitle('get_workload_metrics', {
        kind: 'Pod',
        namespace: 'shop',
        name: 'catalog-api-f958778f8-vg55l',
        metrics: ['network_rx', 'network_tx'],
      }),
    ).toEqual({ title: 'Network · catalog-api', chip: '15 min' })
    expect(toolTitle('get_workload_metrics', { kind: 'Node', name: 'ip-10-0-1-23', metrics: ['filesystem'], range: '24h' })).toEqual({
      title: 'Disk · ip-10-0-1-23',
      chip: '24 h',
    })
  })

  it('names a cloud read by its service and operation', () => {
    expect(toolTitle('mcp__aws__call', { service: 'cloudtrail', operation: 'LookupEvents', region: 'us-east-2' })).toEqual({
      title: 'CloudTrail · LookupEvents',
      chip: 'us-east-2',
    })
    expect(
      toolTitle('mcp__azure__monitor', { command: 'monitor_activitylog_list', parameters: { 'resource-group': 'MC_shop_eastus2' } }),
    ).toEqual({ title: 'Monitor · activitylog list', chip: 'MC_shop_eastus2' })
    expect(toolTitle('mcp__gcp__logging_list_log_entries', { parent: 'projects/tienda-lab' })).toEqual({
      title: 'Cloud Logging · list log entries',
      chip: 'projects/tienda-lab',
    })
    expect(toolTitle('mcp__gcp__sqladmin_get_instance', {}).title).toBe('Cloud SQL · get instance')
  })

  it('reads an unknown tool as its name in words', () => {
    expect(toolTitle('get_something_new')).toEqual({ title: 'get something new' })
  })
})

describe('helpers', () => {
  it('names a pod by its workload', () => {
    expect(workloadOf('catalog-api-f958778f8-vg55l')).toBe('catalog-api')
    expect(workloadOf('node-exporter-x2k4q')).toBe('node-exporter')
    expect(workloadOf('redis-0')).toBe('redis-0')
  })

  it('names a kind by its resource type', () => {
    expect(kindOf('deployments')).toBe('Deployment')
    expect(kindOf('persistentvolumeclaims')).toBe('PVC')
    expect(kindOf('widgets')).toBe('Widget')
    expect(kindOf('policies')).toBe('Policy')
  })
})
