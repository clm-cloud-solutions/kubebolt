import { describe, it, expect, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ToolCallCard, formatDigest } from './ToolCallCard'
import { MCP_CONNECTORS_KEY } from '@/components/admin/mcp/ConnectorBits'

// user-event fires its events through its own copy of @testing-library/dom
// (10.x), which React Testing Library 14 — built on 9.x — never hooks up to
// act(): the updates an event causes land outside act and React warns.
// Going through act settles each interaction before the next assertion.
const click = (el: Element) => act(() => userEvent.click(el))

// A cloud connector read used to render as "mcp  aws  call": the card spelled
// the wire name with underscores turned into spaces. It now says whose cloud
// and what was read, and offers the AWS CLI call to reproduce it. AWS's one
// `call` tool is named by its operation, so the label is the provider alone.

function renderCard(props: Parameters<typeof ToolCallCard>[0]) {
  return render(
    <MemoryRouter>
      <ToolCallCard {...props} />
    </MemoryRouter>,
  )
}

describe('ToolCallCard — cloud connector tools', () => {
  it('labels the call by provider and names the API call', () => {
    renderCard({
      name: 'mcp__aws__call',
      input: { service: 'rds', operation: 'DescribeDBInstances', region: 'us-east-1' },
      result: '{}',
    })
    expect(screen.getByText('AWS')).toBeInTheDocument()
    expect(screen.getByText('RDS · DescribeDBInstances')).toBeInTheDocument()
    expect(screen.getByText('us-east-1')).toBeInTheDocument()
    expect(screen.queryByText(/mcp/)).not.toBeInTheDocument()
  })

  it('offers the AWS CLI equivalent, and the connector link only to admins', async () => {
    const { unmount } = renderCard({
      name: 'mcp__aws__call',
      input: { service: 'rds', operation: 'DescribeDBInstances' },
      result: '{}',
    })
    await click(screen.getByRole('button', { name: /AWS/ }))
    // No region in the call and none known here: the command says where it ran.
    expect(screen.getByText(/aws rds describe-db-instances$/)).toHaveTextContent(/connector's region/)
    expect(screen.queryByRole('link', { name: /connector/ })).not.toBeInTheDocument()
    unmount()

    renderCard({
      name: 'mcp__aws__call',
      input: { service: 'rds', operation: 'DescribeDBInstances' },
      result: '{}',
      canManageConnectors: true,
    })
    await click(screen.getByRole('button', { name: /AWS/ }))
    expect(screen.getByRole('link', { name: /Open AWS connector/ })).toHaveAttribute(
      'href',
      '/admin/ai?tab=connectors&connector=aws',
    )
  })

  it('names the connector region an admin already has loaded (P9)', async () => {
    const qc = new QueryClient()
    qc.setQueryData(MCP_CONNECTORS_KEY, {
      connectors: [{ id: 'aws', config: { fields: { region: 'eu-west-1' }, secretsConfigured: {}, secretsUnreadable: [] } }],
    })
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <ToolCallCard
            name="mcp__aws__call"
            input={{ service: 'rds', operation: 'DescribeDBInstances' }}
            result="{}"
            canManageConnectors
          />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    await click(screen.getByRole('button', { name: /AWS/ }))
    expect(screen.getByText('aws rds describe-db-instances --region eu-west-1')).toBeInTheDocument()
  })

  it('names KubeBolt\'s own tools by what they read', () => {
    renderCard({ name: 'get_pod_logs', input: { namespace: 'shop', name: 'checkout-7d9f8c6b5-x2k4q', since: '30m' }, result: 'ok' })
    expect(screen.getByText('Logs · checkout')).toBeInTheDocument()
    expect(screen.getByText('30 min')).toBeInTheDocument()
    // The wire name and the long summary stay in the tooltip.
    expect(screen.getByText('Logs · checkout')).toHaveAttribute('title', expect.stringContaining('get_pod_logs'))
  })
})

describe('ToolCallCard — get_external_connections', () => {
  const result = JSON.stringify({
    sourcePresent: true,
    destinations: [{
      ip: '57.165.100.6',
      inFleet: { cluster: 'kubeshop-back', kind: 'Service', namespace: 'shop-backend', name: 'orders-api', sameCluster: false },
    }],
  })
  const input = { namespace: 'shop-frontend', workload: 'shop-web' }

  it('names the other cluster\'s workload once the answer is in', () => {
    renderCard({ name: 'get_external_connections', input, result })
    expect(screen.getByText('External connections · shop-web')).toBeInTheDocument()
    // The chip truncates in a narrow panel; its tooltip keeps it whole.
    expect(screen.getByText('→ kubeshop-back · shop-backend/orders-api')).toHaveAttribute(
      'title',
      '→ kubeshop-back · shop-backend/orders-api',
    )
  })

  it('reads nothing from an errored call', () => {
    renderCard({ name: 'get_external_connections', input, result, isError: true })
    expect(screen.getByText('shop-frontend')).toBeInTheDocument()
    expect(screen.queryByText(/kubeshop-back/)).not.toBeInTheDocument()
  })
})

describe('ToolCallCard — how the call ended', () => {
  it('runs with the operation named, then says how long it took', () => {
    const { rerender } = renderCard({
      name: 'mcp__aws__call',
      input: { service: 'cloudtrail', operation: 'LookupEvents' },
    })
    // Live: the input arrives with the call, so the card names it while it runs.
    expect(screen.getByText('CloudTrail · LookupEvents')).toBeInTheDocument()
    expect(screen.queryByText(/ s$|ms$/)).not.toBeInTheDocument()

    rerender(
      <MemoryRouter>
        <ToolCallCard
          name="mcp__aws__call"
          input={{ service: 'cloudtrail', operation: 'LookupEvents' }}
          result="{}"
          status="ok"
          durationMs={2380}
        />
      </MemoryRouter>,
    )
    expect(screen.getByText('2.4 s')).toBeInTheDocument()
  })

  it('says what the icon alone would not', () => {
    // A call that read nothing gets a word by its icon; one that did not
    // read gets a line under its title instead.
    const cases: Array<[string, string]> = [
      ['empty', 'empty'],
      ['not_found', 'not found'],
      ['denied', 'Denied'],
      ['timeout', 'Timed out'],
      ['not_sent', 'Not sent'],
      ['agent_offline', 'Agent disconnected — no live reads until it reconnects'],
      ['unsupported', 'Not available for this kind'],
    ]
    for (const [status, word] of cases) {
      const { unmount } = renderCard({ name: 'get_events', input: {}, result: '{}', status, isError: status !== 'empty' && status !== 'not_found' })
      expect(screen.getByText(word)).toBeInTheDocument()
      unmount()
    }
  })

  it('keeps an error collapsed until asked', async () => {
    renderCard({
      name: 'get_pod_logs',
      input: { namespace: 'shop', name: 'checkout-1' },
      result: '{"error":"agentConnected: false — tell the operator to reconnect the agent"}',
      isError: true,
      status: 'error',
    })
    expect(screen.queryByText(/tell the operator/)).not.toBeInTheDocument()
    await click(screen.getByRole('button', { name: /Logs · checkout-1/ }))
    expect(screen.getByText(/tell the operator/)).toBeInTheDocument()
  })

  it('counts its own time while it runs past a few seconds', () => {
    vi.useFakeTimers()
    try {
      renderCard({ name: 'get_resource_describe', input: { type: 'pods', namespace: 'trivy-system', name: 'scan-1' } })
      expect(screen.queryByText(/ s$/)).not.toBeInTheDocument()
      act(() => {
        vi.advanceTimersByTime(12_000)
      })
      expect(screen.getByText('12 s')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('stops spinning when the turn ends without its result', () => {
    renderCard({ name: 'get_events', input: {}, stopped: true })
    expect(screen.getByText('not finished')).toBeInTheDocument()
    expect(document.querySelector('.animate-spin')).toBeNull()
  })

  it('paints a call that left the cluster in its cloud\'s colour', () => {
    const { container } = renderCard({ name: 'mcp__aws__call', input: { service: 'rds', operation: 'DescribeDBInstances' }, result: '{}', status: 'ok' })
    expect((container.firstChild as HTMLElement).style.borderLeftColor).toBe('var(--kobi-cloud-aws)')
    expect(screen.getByText('AWS').style.color).toBe('var(--kobi-cloud-aws-text)')
  })
})

describe('ToolCallCard — digest', () => {
  it('says what came back under the title, once it is back', () => {
    const props = {
      name: 'mcp__aws__call',
      input: { service: 'ec2', operation: 'DescribeRouteTables' },
      digest: '1 table · 1 route: 10.42.0.0/16 → local',
    }
    const { rerender } = renderCard(props)
    expect(screen.queryByText(/1 table/)).not.toBeInTheDocument()
    rerender(
      <MemoryRouter>
        <ToolCallCard {...props} result="{}" status="ok" durationMs={1600} />
      </MemoryRouter>,
    )
    expect(screen.getByText('1 table · 1 route: 10.42.0.0/16 → local')).toBeInTheDocument()
  })

  it("shows the server's times in the operator's zone", () => {
    const now = new Date('2026-10-22T23:00:00Z')
    const today = formatDigest('rarest first: DeleteRoute @2026-10-22T22:05:12Z', now)
    expect(today).not.toContain('@')
    expect(today).toMatch(/DeleteRoute \d{2}:\d{2}$/)
    const earlier = formatDigest('latest x @2026-10-01T10:00:00Z', now)
    expect(earlier).toMatch(/latest x \S+ \d+ \d{2}:\d{2}$|latest x \d+ \S+ \d{2}:\d{2}$/)
    expect(formatDigest('no times here', now)).toBe('no times here')
  })
})

describe('ToolCallCard — a call that did not read', () => {
  it('says why on its own line, in the colour of how it ended', () => {
    renderCard({
      name: 'mcp__aws__call',
      input: { service: 'kms', operation: 'DescribeKey' },
      result: '{}',
      isError: true,
      status: 'denied',
      reason: 'AccessDenied for kms:DescribeKey',
      durationMs: 800,
    })
    const line = screen.getByText('Denied — AccessDenied for kms:DescribeKey')
    expect(line.className).toContain('text-kobi-st-warn')
    // The word by the icon would only repeat it.
    expect(screen.queryByText('denied')).not.toBeInTheDocument()
  })

  it('says how long a call waited before it timed out', () => {
    renderCard({ name: 'get_resource_describe', input: { type: 'pods', name: 'scan-1' }, result: '{}', isError: true, status: 'timeout', durationMs: 90_011 })
    expect(screen.getByText('Timed out after 90 s')).toBeInTheDocument()
  })
})
