import { describe, it, expect, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MCP_CONNECTORS_KEY } from '@/components/admin/mcp/ConnectorBits'
import { CloudBoundary, GuideLine, InvestigationStrip, InvestigationSummary, TurnFailure } from './InvestigationMarks'

describe('CloudBoundary', () => {
  it('says where the investigation leaves the cluster', () => {
    render(<CloudBoundary connectorId="aws" input={{ service: 'ec2', operation: 'DescribeRouteTables', region: 'us-east-2' }} />)
    expect(screen.getByText('Leaves the cluster →')).toBeInTheDocument()
    expect(screen.getByText('AWS').style.color).toBe('var(--kobi-cloud-aws-text)')
    expect(screen.getByText('us-east-2 · read-only')).toBeInTheDocument()
  })

  it("names the connector's region only to an admin who has it loaded", () => {
    const qc = new QueryClient()
    qc.setQueryData(MCP_CONNECTORS_KEY, {
      connectors: [{ id: 'aws', config: { fields: { region: 'eu-west-1' }, secretsConfigured: {}, secretsUnreadable: [] } }],
    })
    const { unmount } = render(
      <QueryClientProvider client={qc}>
        <CloudBoundary connectorId="aws" input={{ service: 'ec2', operation: 'DescribeVpcs' }} />
      </QueryClientProvider>,
    )
    expect(screen.getByText('read-only')).toBeInTheDocument()
    unmount()
    render(
      <QueryClientProvider client={qc}>
        <CloudBoundary connectorId="aws" input={{ service: 'ec2', operation: 'DescribeVpcs' }} canManageConnectors />
      </QueryClientProvider>,
    )
    expect(screen.getByText('eu-west-1 · read-only')).toBeInTheDocument()
  })
})

describe('InvestigationSummary', () => {
  const run = {
    answerId: 'ans',
    stepIds: ['a0', 'u0'],
    elapsedMs: 72_000,
    reads: 5,
    byCloud: [['aws', 3]] as Array<[string, number]>,
    guides: 1,
    unread: 2,
    charts: 0,
    proposals: 0,
    proposalIds: [],
  }

  it('says how long it took and what it read', () => {
    render(<InvestigationSummary run={run} open={false} onToggle={() => {}} />)
    for (const text of ['Investigation', '1:12', '5 reads', '3 in AWS', '· 1 guide', '· 2 could not be read', 'Show steps']) {
      expect(screen.getByText(text)).toBeInTheDocument()
    }
  })

  it('opens and closes the steps', async () => {
    const onToggle = vi.fn()
    const { rerender } = render(<InvestigationSummary run={run} open={false} onToggle={onToggle} />)
    await act(() => userEvent.click(screen.getByRole('button', { name: /Show steps/ })))
    expect(onToggle).toHaveBeenCalledOnce()
    rerender(<InvestigationSummary run={run} open onToggle={onToggle} />)
    expect(screen.getByRole('button', { name: /Hide steps/ })).toHaveAttribute('aria-expanded', 'true')
  })

  it('leaves out what did not happen', () => {
    render(<InvestigationSummary run={{ ...run, byCloud: [], guides: 0, unread: 0, reads: 1, elapsedMs: undefined }} open={false} onToggle={() => {}} />)
    expect(screen.getByText('1 read')).toBeInTheDocument()
    expect(screen.queryByText(/could not be read|guide| in |chart|proposal|Paused/)).not.toBeInTheDocument()
  })

  it('counts the charts and proposals the answer brings', () => {
    render(<InvestigationSummary run={{ ...run, charts: 1, proposals: 2, proposalIds: ['p'] }} open={false} onToggle={() => {}} />)
    expect(screen.getByText('· 1 chart')).toBeInTheDocument()
    expect(screen.getByText('· 2 proposals')).toBeInTheDocument()
  })

  it('says in amber that it paused at the step limit', () => {
    render(<InvestigationSummary run={{ ...run, pausedAtLimit: 20 }} open={false} onToggle={() => {}} />)
    const paused = screen.getByText(/Paused at the step limit · 20 steps/)
    expect(paused.className).toContain('text-kobi-st-warn')
  })
})

describe('TurnFailure', () => {
  it('says the question could not finish, after how long, and why', () => {
    render(<TurnFailure message="The AI provider is overloaded. Try again in a minute." afterMs={58_000} />)
    expect(screen.getByText("Kobi couldn't finish")).toBeInTheDocument()
    expect(screen.getByText('0:58')).toBeInTheDocument()
    expect(screen.getByText('The AI provider is overloaded. Try again in a minute.').className).toContain('text-kobi-st-error')
  })
})

describe('GuideLine', () => {
  it('shows a guide as a line, not a read', () => {
    const { rerender } = render(
      <GuideLine call={{ id: 'g', name: 'load_skill', input: { name: 'aws-investigation' } }} running />,
    )
    expect(screen.getByText('Guide ·')).toBeInTheDocument()
    expect(screen.getByText('aws-investigation')).toBeInTheDocument()
    expect(screen.getByLabelText('Loading')).toBeInTheDocument()
    rerender(
      <GuideLine
        call={{ id: 'g', name: 'load_skill', input: { name: 'aws-investigation' } }}
        result={{ toolCallId: 'g', content: '{"error":"x"}', status: 'error', isError: true }}
        running={false}
      />,
    )
    expect(screen.getByText('· not loaded')).toBeInTheDocument()
  })
})

describe('InvestigationStrip', () => {
  it('says how the question goes, and stops it', async () => {
    const onStop = vi.fn()
    render(
      <InvestigationStrip
        live={{ startedAt: new Date(Date.now() - 37_000), reads: 13, byCloud: [['aws', 1]], cloudsNow: ['aws'] }}
        onStop={onStop}
      />,
    )
    for (const text of ['Investigating', '0:37', '· 13 reads · 1 in AWS', 'AWS now']) {
      expect(screen.getByText(text)).toBeInTheDocument()
    }
    const badge = screen.getByText('AWS now')
    expect(badge.style.color).toBe('var(--kobi-cloud-aws-text)')
    expect(badge.className).toContain('animate-pulse')
    await act(() => userEvent.click(screen.getByRole('button', { name: /Stop/ })))
    expect(onStop).toHaveBeenCalledOnce()
  })

  it('keeps the badge of every cloud the question read, still between its calls', () => {
    render(
      <InvestigationStrip
        live={{ startedAt: new Date(), reads: 12, byCloud: [['aws', 5]], cloudsNow: [] }}
        onStop={() => {}}
      />,
    )
    const badge = screen.getByText('AWS')
    expect(badge.style.background).toContain('--kobi-cloud-aws')
    expect(badge.className).not.toContain('animate-pulse')
  })
})

