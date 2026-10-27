import { describe, it, expect } from 'vitest'
import { presentTool, cliOperation, externalToolSummary } from './toolPresenters'

describe('presentTool — get_operational_episodes', () => {
  it('names the window so the operator sees Kobi\'s intent collapsed', () => {
    expect(presentTool('get_operational_episodes', { sinceHours: 6 }).summary)
      .toBe('last 6h · operational bursts')
  })

  it('falls back to the default window when the model omits it', () => {
    expect(presentTool('get_operational_episodes', {}).summary)
      .toBe('last 24h · operational bursts')
    // A nonsense value must not leak into the chip as "last NaNh".
    expect(presentTool('get_operational_episodes', { sinceHours: 'ayer' }).summary)
      .toBe('last 24h · operational bursts')
  })

  it('lands on the Bursts view, carrying the window Kobi asked for', () => {
    expect(presentTool('get_operational_episodes', { sinceHours: 168 }).link)
      .toEqual({ href: '/insights?view=bursts&range=7d', label: 'Open Bursts' })
    expect(presentTool('get_operational_episodes', { sinceHours: 720 }).link?.href)
      .toBe('/insights?view=bursts&range=30d')
  })

  it('opens on the default window rather than inventing a range the view ignores', () => {
    // 24h IS the view's default, so it needs no param.
    expect(presentTool('get_operational_episodes', { sinceHours: 24 }).link?.href)
      .toBe('/insights?view=bursts')
    // 13h is not a preset: the view cannot honour it, so do not pretend.
    expect(presentTool('get_operational_episodes', { sinceHours: 13 }).link?.href)
      .toBe('/insights?view=bursts')
    expect(presentTool('get_operational_episodes', {}).link?.href)
      .toBe('/insights?view=bursts')
  })

  it('still lists the arguments, so a wrong window is visible before the conclusion is', () => {
    expect(presentTool('get_operational_episodes', { sinceHours: 168 }).inputLines)
      .toEqual([{ key: 'sinceHours', value: '168' }])
  })
})

describe('presentTool — insight history', () => {
  it('surfaces the window and the scope, since those change what came back', () => {
    expect(presentTool('get_insight_episodes', { sinceHours: 72, cluster: 'all', rule: 'crash-loop' }).summary)
      .toBe('last 72h · all clusters · crash-loop')
    expect(presentTool('get_insight_episodes', {}).summary).toBe('last 24h')
  })

  it('links the list to History and the detail to that exact episode', () => {
    expect(presentTool('get_insight_episodes', {}).link)
      .toEqual({ href: '/insights?view=history', label: 'Open Insight History' })
    expect(presentTool('get_insight_episode', { id: 'abc12345-6789' }).link)
      .toEqual({ href: '/insights/episodes/abc12345-6789', label: 'Open episode' })
  })

  it('offers no episode link without an id, rather than a route to nowhere', () => {
    expect(presentTool('get_insight_episode', {}).link).toBeUndefined()
    expect(presentTool('get_insight_episode', {}).summary).toBe('one episode')
  })
})

describe('presentTool — security findings', () => {
  it('says whether Kobi asked for the posture or for rows', () => {
    expect(presentTool('get_findings', {}).summary).toBe('posture')
    expect(presentTool('get_findings', { severity: 'critical', image: 'reg/app:v1' }).summary)
      .toBe('critical · reg/app:v1')
  })

  it('flags the widened scope and the history view, which change what came back', () => {
    expect(presentTool('get_findings', { cluster: 'all' }).summary).toBe('posture · all clusters')
    expect(presentTool('get_findings', { status: 'resolved' }).summary).toBe('posture · resolved')
  })

  it('links to the Security pillar', () => {
    expect(presentTool('get_findings', {}).link).toEqual({ href: '/security', label: 'Open Security' })
  })

  it('names the lens of the workload ranking and links to that lens', () => {
    const p = presentTool('get_finding_workloads', { group: 'configuration', severity: 'high', cluster: 'all' })
    expect(p.summary).toBe('configuration by workload · high · all clusters')
    expect(p.link).toEqual({ href: '/security/configuration', label: 'Open Security' })
    expect(presentTool('get_finding_workloads', {}).summary).toBe('by workload')
    expect(presentTool('get_finding_workloads', { group: 'rbac' }).link?.href).toBe('/security/permissions')
  })

  it('right-sizing links to the Capacity screen it shares an engine with', () => {
    const p = presentTool('get_right_sizing', { namespace: 'shop' })
    expect(p.summary).toBe('7d P95 · shop')
    expect(p.link?.href).toBe('/capacity')
  })

  it('coverage and query_metrics say what they are', () => {
    expect(presentTool('get_coverage', {}).summary).toBe('what KubeBolt can see')
    const q = presentTool('query_metrics', { query: 'sum(up)', range: '6h' })
    expect(q.summary).toBe('PromQL · last 6h')
    expect(q.command).toBe('sum(up)')
    expect(presentTool('query_metrics', { query: 'up' }).summary).toBe('PromQL · now')
  })

  it('runtime events name their window and link to the Runtime lens', () => {
    const p = presentTool('get_runtime_events', { priority: 'Critical', cluster: 'all' })
    expect(p.summary).toBe('last 24h · Critical · all clusters')
    expect(p.link?.href).toBe('/security/runtime')
  })

  it('recent deploys give the kubectl equivalent and link to Capacity', () => {
    const p = presentTool('get_recent_deploys', { sinceHours: 6, namespace: 'shop' })
    expect(p.summary).toBe('last 6h · shop')
    expect(p.command).toContain('-n shop')
    expect(p.link?.href).toBe('/capacity')
  })

  it('shows which finding the detail re-read', () => {
    expect(presentTool('get_finding_detail', { fingerprint: 'abcdef0123456789' }).summary).toBe('finding abcdef01')
    expect(presentTool('get_finding_detail', {}).summary).toBe('one finding')
  })
})

describe('presentTool — fleet', () => {
  it('marks itself as the cross-cluster tool and lands on Fleet', () => {
    const p = presentTool('get_fleet_summary', {})
    expect(p.summary).toBe('every cluster')
    expect(p.link).toEqual({ href: '/fleet', label: 'Open Fleet' })
    // It takes no arguments, so an Input table would be an empty box.
    expect(p.inputLines).toEqual([])
  })
})

describe('presentTool — cloud connector tools', () => {
  it('names the AWS API call and the region in the chip', () => {
    const p = presentTool('mcp__aws__call', {
      service: 'rds',
      operation: 'DescribeDBInstances',
      region: 'us-east-1',
      params: { DBInstanceIdentifier: 'prod-db' },
    })
    expect(p.summary).toBe('rds.DescribeDBInstances · us-east-1')
    expect(p.command).toBe(
      `aws rds describe-db-instances --region us-east-1 --cli-input-json '{"DBInstanceIdentifier":"prod-db"}'`,
    )
  })

  it('names the connector region a call without one ran in, or says it did (P9)', () => {
    // The gateway fills in the connector's region; the operator's CLI would
    // default to its own and check the wrong one in silence.
    const p = presentTool('mcp__aws__call', { service: 'cloudwatch', operation: 'GetMetricData' })
    expect(p.summary).toBe('cloudwatch.GetMetricData')
    expect(p.command).toBe(
      "# Ran in the connector's region: add --region <region> if your CLI default differs.\naws cloudwatch get-metric-data",
    )
    const known = presentTool(
      'mcp__aws__call',
      { service: 'cloudwatch', operation: 'GetMetricData' },
      { connectorRegion: (id) => (id === 'aws' ? 'eu-west-1' : undefined) },
    )
    expect(known.command).toBe('aws cloudwatch get-metric-data --region eu-west-1')
    // The model's own region wins over the connector's.
    expect(
      presentTool('mcp__aws__call', { service: 'rds', operation: 'DescribeDBInstances', region: 'us-west-2' }, {
        connectorRegion: () => 'eu-west-1',
      }).command,
    ).toBe('aws rds describe-db-instances --region us-west-2')
  })

  it('spells S3 API calls the way the AWS CLI does (P9)', () => {
    // `aws s3` is the file commands; `aws s3 get-bucket-location` is an error.
    expect(
      presentTool('mcp__aws__call', { service: 's3', operation: 'GetBucketLocation', region: 'us-east-1', params: { Bucket: 'media' } })
        .command,
    ).toBe(`aws s3api get-bucket-location --region us-east-1 --cli-input-json '{"Bucket":"media"}'`)
    // The chip keeps the API's own name.
    expect(presentTool('mcp__aws__call', { service: 's3', operation: 'ListBuckets' }).summary).toBe('s3.ListBuckets')
  })

  it('offers no shell command for a value that is not a plain service or operation', () => {
    // Model-written strings land on the operator's clipboard; a quoted-but-odd
    // command is still something they might paste into a shell.
    expect(presentTool('mcp__aws__call', { service: 'rds; rm -rf ~', operation: 'DescribeDBInstances' }).command).toBe('')
    expect(presentTool('mcp__aws__call', { service: 'rds', operation: 'Describe$(id)' }).command).toBe('')
    expect(presentTool('mcp__aws__call', { service: 'rds', operation: 'DescribeDBInstances', region: 'us-east-1 && x' }).command).toBe('')
  })

  it('quotes the parameters so a single quote cannot break out', () => {
    const p = presentTool('mcp__aws__call', { service: 'logs', operation: 'StartQuery', region: 'us-east-1', params: { queryString: "it's" } })
    expect(p.command).toBe(`aws logs start-query --region us-east-1 --cli-input-json '{"queryString":"it'\\''s"}'`)
  })

  it('reads the Autopilot spelling of the same tool', () => {
    expect(presentTool('mcp__kubebolt__cloud_aws_call', { service: 'elasticache', operation: 'DescribeCacheClusters' }).summary)
      .toBe('elasticache.DescribeCacheClusters')
  })

  it('summarises Azure router calls by command and subject', () => {
    expect(presentTool('mcp__azure__sql', {
      intent: 'check the server',
      command: 'sql_server_show',
      parameters: { server: 'prod-sql', 'resource-group': 'rg' },
    }).summary).toBe('sql_server_show · prod-sql')
    expect(presentTool('mcp__azure__monitor', { learn: true }).summary).toBe('listing commands')
  })

  it('summarises Azure direct tools by their top-level parameters (P10)', () => {
    // subscription_list, group_list and group_resource_list take their
    // arguments at the top level, not under `parameters`.
    expect(presentTool('mcp__azure__group_resource_list', { 'resource-group': 'rg-prod' }).summary).toBe('rg-prod')
    expect(presentTool('mcp__kubebolt__cloud_azure_group_resource_list', { 'resource-group': 'rg-prod' }).summary)
      .toBe('rg-prod')
    expect(presentTool('mcp__azure__subscription_list', {}).summary).toBe('')
  })

  it('names the resource of a call addressed by resource ID by its last segment (P10)', () => {
    expect(presentTool('mcp__azure__resourcehealth', {
      command: 'resourcehealth_availability-status_get',
      parameters: { resourceId: '/subscriptions/s/resourceGroups/rg/providers/Microsoft.Cache/redis/r-1' },
    }).summary).toBe('resourcehealth_availability-status_get · r-1')
    expect(presentTool('mcp__azure__role', {
      command: 'role_assignment_list',
      parameters: { scope: '/subscriptions/s/resourceGroups/rg-app' },
    }).summary).toBe('role_assignment_list · rg-app')
  })

  it('summarises Google calls by the tail of the resource path', () => {
    expect(presentTool('mcp__gcp__sqladmin_get_instance', { project: 'p', instance: 'prod-db' }).summary).toBe('prod-db')
    expect(presentTool('mcp__gcp__monitoring_list_timeseries', { name: 'projects/acme/locations/global' }).summary)
      .toBe('locations/global')
  })

  it('links admins to the connector, and nobody else', () => {
    expect(presentTool('mcp__aws__call', { service: 'rds', operation: 'DescribeDBInstances' }).link).toBeUndefined()
    expect(presentTool('mcp__gcp__sqladmin_get_instance', {}, { canManageConnectors: true }).link).toEqual({
      href: '/admin/ai?tab=connectors&connector=gcp',
      label: 'Open Google Cloud connector',
    })
  })

  it('leaves KubeBolt\'s own reads to their presenters', () => {
    expect(presentTool('get_topology').link?.href).toBe('/map')
  })
})

describe('presentTool — get_external_connections', () => {
  it('scopes the chip and falls back to the default window', () => {
    expect(presentTool('get_external_connections', { namespace: 'shop', workload: 'checkout' }).summary)
      .toBe('shop/checkout · last 15m')
    expect(presentTool('get_external_connections', { windowMinutes: 60 }).summary)
      .toBe('all namespaces · last 60m')
    expect(presentTool('get_external_connections', {}).link).toEqual({ href: '/map', label: 'Open Cluster Map' })
  })

  // The 2026-09-24 case: shop-web called 57.165.100.6, the LoadBalancer of
  // shop-backend/orders-api in the org's other cluster. The chip names where
  // the answer leads.
  const answer = (rows: { destinations?: unknown[]; dependencyHosts?: unknown[] }) =>
    JSON.stringify({ sourcePresent: true, clusterUIDKnown: true, windowMinutes: 15, destinations: [], ...rows })
  const ordersApi = {
    cluster: 'kubeshop-back', kind: 'Service', namespace: 'shop-backend', name: 'orders-api',
    ports: ['80/TCP'], sameCluster: false,
  }
  const input = { namespace: 'shop-frontend', workload: 'Deployment/shop-web' }

  it('names the workload of another cluster behind an address', () => {
    const result = answer({ destinations: [{ ip: '57.165.100.6', kind: 'ip', confidence: 'low', inFleet: ordersApi }] })
    expect(presentTool('get_external_connections', input, { result }).summary)
      .toBe('shop-frontend/Deployment/shop-web · last 15m · → kubeshop-back · shop-backend/orders-api')
  })

  it('reads configured hosts too, counts the rest once, and skips a hairpin', () => {
    const result = answer({
      destinations: [
        { ip: '10.0.0.9', inFleet: { ...ordersApi, cluster: 'kubeshop-front', name: 'shop-web', sameCluster: true } },
        { ip: '57.165.100.6', inFleet: ordersApi },
      ],
      dependencyHosts: [
        { source: 'env ORDERS_URL', host: '57.165.100.6', inFleet: ordersApi },
        { source: 'env PAY_URL', host: 'pay.example.com', inFleet: { ...ordersApi, cluster: 'payments', namespace: 'pay', name: 'gw', kind: 'Gateway' } },
      ],
    })
    expect(presentTool('get_external_connections', input, { result }).summary)
      .toBe('shop-frontend/Deployment/shop-web · last 15m · → kubeshop-back · shop-backend/orders-api +1')
  })

  it('adds nothing without a match in another cluster, or without a readable answer', () => {
    const plain = 'shop-frontend/Deployment/shop-web · last 15m'
    const hairpinOnly = answer({ destinations: [{ ip: '10.0.0.9', inFleet: { ...ordersApi, sameCluster: true } }] })
    for (const result of [undefined, '', answer({}), hairpinOnly, '{"error":"no cluster"}', 'not json', '[]', 'null']) {
      expect(presentTool('get_external_connections', input, { result }).summary, String(result)).toBe(plain)
    }
  })
})

// P4: Autopilot's timeline shows the same chip as Kobi's card for a cloud
// read, and nothing for KubeBolt's own tools (it has its own summary).
describe('externalToolSummary', () => {
  it('summarises a cloud read in the Autopilot spelling', () => {
    expect(externalToolSummary('mcp__kubebolt__cloud_aws_call', { service: 'rds', operation: 'DescribeDBInstances', region: 'us-east-1' }))
      .toBe('rds.DescribeDBInstances · us-east-1')
    expect(externalToolSummary('cloud_gcp_sqladmin_get_instance', { instance: 'prod-db' })).toBe('prod-db')
  })

  it('stays out of the way for KubeBolt\'s own tools and odd inputs', () => {
    expect(externalToolSummary('mcp__kubebolt__get_pod_logs', { namespace: 'shop' })).toBeUndefined()
    expect(externalToolSummary('mcp__kubebolt__cloud_azure_subscription_list', null)).toBeUndefined()
  })
})

describe('cliOperation', () => {
  it('derives AWS CLI command names, keeping acronyms whole', () => {
    expect(cliOperation('DescribeDBInstances')).toBe('describe-db-instances')
    expect(cliOperation('GetMetricData')).toBe('get-metric-data')
    expect(cliOperation('ListTagsForResource')).toBe('list-tags-for-resource')
    expect(cliOperation('DescribeDBClusterSnapshots')).toBe('describe-db-cluster-snapshots')
  })
})
