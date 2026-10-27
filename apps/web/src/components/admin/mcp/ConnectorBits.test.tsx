import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import type { McpConnector } from '@/services/api'
import {
  ConnectorStatusBadge,
  PlanGateLink,
  PlanUpgradeCta,
  effectiveStatus,
  planInvitation,
  safeContactUrl,
  statusSentence,
  storedModeLabel,
} from './ConnectorBits'

const iamRole = { id: 'assume-role', label: 'IAM role', description: '', selfHostedOnly: false, fields: [] }
// The stored mode of the fixture, offered: a connector whose install lists no
// mode at all is another state (below), not the default one.
const ambient = { id: 'ambient', label: "This machine's credentials", description: '', selfHostedOnly: true, fields: [] }

function connector(over: Partial<McpConnector> = {}): McpConnector {
  return {
    id: 'aws',
    name: 'Amazon Web Services',
    category: 'cloud',
    description: '',
    tags: [],
    docsURL: '',
    minPlan: 'team',
    entitled: true,
    status: 'enabled',
    enabled: true,
    paused: false,
    kobiMinRole: 'editor',
    mode: 'ambient',
    authModes: [ambient],
    families: [],
    never: [],
    tools: { kobi: [], autopilot: [] },
    config: { fields: { region: 'us-east-1' }, secretsConfigured: {}, secretsUnreadable: [] },
    verifiedAt: new Date(Date.now() - 60_000).toISOString(),
    verifiedTools: 5,
    setupGuide: { steps: [] },
    ...over,
  }
}

describe('effectiveStatus', () => {
  it('reads a paused connector as paused even with an error on record (P2)', () => {
    // Pausing is what an admin does to a connector that keeps failing; the
    // error stays on record, and used to win — no Resume, "Needs attention".
    expect(effectiveStatus(connector({ status: 'error', paused: true, lastError: 'The provider rejected the credentials.' })))
      .toBe('paused')
    expect(effectiveStatus(connector({ status: 'error' }))).toBe('error')
  })

  it('keeps the plan lock above everything', () => {
    expect(effectiveStatus(connector({ entitled: false, paused: true, authModes: [iamRole] }))).toBe('plan')
  })

  it('never reads as an upgrade when the install offers no way to sign in (P6)', () => {
    // Azure on a multi-tenant install, for an org below its plan: no plan
    // turns it on here, and it is not "available" either — it says so.
    const notOffered = connector({
      entitled: false,
      status: 'plan',
      upgradeTo: 'team',
      mode: '',
      enabled: false,
      authModes: [],
      verifiedAt: undefined,
    })
    expect(effectiveStatus(notOffered)).toBe('unoffered')
    expect(statusSentence(notOffered)).toEqual({ tone: 'muted', text: 'Not available on this installation' })
    render(<ConnectorStatusBadge connector={notOffered} />)
    expect(screen.getByText('Not offered')).toBeInTheDocument()
    expect(screen.queryByText('Available')).not.toBeInTheDocument()
    expect(screen.queryByText('Upgrade')).not.toBeInTheDocument()
  })

  // Wave 6 verification: a configuration stored before the install stopped
  // offering the connector read "Saved, not enabled yet" and offered Set up.
  // The stored switches say what the org chose; the server's status ('plan'
  // below the plan, 'available' when never switched on) does not.
  describe('a stored configuration the install no longer offers', () => {
    const stored = (over: Partial<McpConnector>) =>
      connector({ id: 'azure', mode: 'sidecar', authModes: [], lastError: undefined, ...over })

    it('reads switched on as needing attention, whatever the server status says', () => {
      for (const status of ['plan', 'error', 'available'] as const) {
        const c = stored({ status, enabled: true, entitled: status !== 'plan' })
        expect(effectiveStatus(c)).toBe('error')
        expect(statusSentence(c)).toEqual({ tone: 'error', text: "Saved, but it can't be used on this installation" })
      }
    })

    it('reads paused as paused, and saved-but-off as not offered — never "not enabled yet"', () => {
      expect(effectiveStatus(stored({ status: 'paused', enabled: true, paused: true }))).toBe('paused')
      const off = stored({ status: 'available', enabled: false })
      expect(effectiveStatus(off)).toBe('unoffered')
      expect(statusSentence(off)).toEqual({ tone: 'muted', text: "Saved, but it can't be used on this installation" })
      for (const c of [off, stored({ status: 'plan', enabled: true, entitled: false })]) {
        expect(statusSentence(c)?.text).not.toMatch(/not enabled/i)
      }
    })

    it('names a stored mode the catalog no longer lists in words, not by its id', () => {
      expect(storedModeLabel(stored({}))).toBe("A method this installation doesn't offer")
      expect(storedModeLabel(connector())).toBe("This machine's credentials")
    })
  })
})

describe('statusSentence', () => {
  it('keeps a verification warning in view after the drawer closes (UI-2)', () => {
    const root = "These are the account's root user credentials. Use a dedicated read-only role or profile instead."
    expect(statusSentence(connector({ verifiedWarning: root }))).toEqual({ tone: 'warn', text: root })
    expect(statusSentence(connector())).toMatchObject({ tone: 'ok', text: expect.stringMatching(/^Verified · 5 tools/) })
  })
})

describe('ConnectorStatusBadge', () => {
  it('writes Enabled in the readable accent green, not the faint status green (UI-8)', () => {
    render(<ConnectorStatusBadge connector={connector()} />)
    const badge = screen.getByText('Enabled')
    expect(badge.className).toContain('text-kb-accent')
    expect(badge.className).not.toContain('text-status-ok')
  })
})

// A plan lock is an invitation, not a footnote: the plan it needs on the
// badge, what upgrading gets in the connector's own name, and one action that
// leads there. It read "Included in the Team plan" in tertiary grey (under AA
// in both themes) next to a bare "Upgrade" button.
describe('a plan-locked connector', () => {
  const locked = (over: Partial<McpConnector> = {}) =>
    connector({
      entitled: false,
      status: 'plan',
      minPlan: 'team',
      upgradeTo: 'team',
      enabled: false,
      mode: '',
      verifiedAt: undefined,
      authModes: [iamRole],
      ...over,
    })

  it('invites to the plan on sale, naming what Kobi gets', () => {
    expect(planInvitation(locked())).toEqual({
      plan: 'team',
      upgradeTo: 'team',
      contactUrl: '',
      headline: 'Included from the Team plan',
      body: 'Upgrade to let Kobi read your AWS resources while it investigates.',
    })
    expect(planInvitation(locked({ id: 'gcp', name: 'Google Cloud' }))?.body).toBe(
      'Upgrade to let Kobi read your Google Cloud resources while it investigates.',
    )
  })

  it('asks the org to contact us when nothing on sale unlocks it', () => {
    expect(planInvitation(locked({ minPlan: 'business', upgradeTo: '' }), 'mailto:sales@example.com')).toEqual({
      plan: 'business',
      upgradeTo: '',
      contactUrl: 'mailto:sales@example.com',
      headline: 'Available on the Business plan',
      body: 'Contact us to let Kobi read your AWS resources while it investigates.',
    })
  })

  // Review fix: "Contact us to …" was written with no link to contact anyone
  // (no KUBEBOLT_UPGRADE_URL, the usage fetch failed or had not landed yet).
  // With nowhere to send the org, the invitation only says which plan has it.
  it('writes "Contact us" only when there is somewhere to contact', () => {
    const businessOnly = locked({ minPlan: 'business', upgradeTo: '' })
    for (const contact of [undefined, '', 'http://example.com/contact', 'javascript:alert(1)']) {
      expect(planInvitation(businessOnly, contact)).toEqual({
        plan: 'business',
        upgradeTo: '',
        contactUrl: '',
        headline: 'Available on the Business plan',
        body: '',
      })
    }
  })

  it('never needs a contact address for a plan on sale', () => {
    expect(planInvitation(locked(), 'mailto:sales@example.com')).toMatchObject({
      upgradeTo: 'team',
      contactUrl: '',
      body: 'Upgrade to let Kobi read your AWS resources while it investigates.',
    })
  })

  it('invites nothing where no upgrade turns it on', () => {
    expect(planInvitation(connector())).toBeNull()
    expect(planInvitation(locked({ authModes: [] }))).toBeNull()
  })

  it('leaves the state line to the invitation', () => {
    expect(statusSentence(locked())).toBeNull()
  })

  it('badges the plan it needs, with a lock', () => {
    const { container } = render(<ConnectorStatusBadge connector={locked({ upgradeTo: 'business' })} />)
    expect(screen.getByText('Business plan')).toBeInTheDocument()
    expect(screen.queryByText('Upgrade')).not.toBeInTheDocument()
    expect(screen.queryByText('Available')).not.toBeInTheDocument()
    expect(container.querySelector('svg')).toHaveClass('lucide-lock')
  })

  it('never badges "Upgrade" when it knows no plan to name', () => {
    render(<ConnectorStatusBadge connector={locked({ minPlan: '', upgradeTo: '' })} />)
    expect(screen.getByText('Not in your plan')).toBeInTheDocument()
    expect(screen.queryByText('Upgrade')).not.toBeInTheDocument()
  })

  describe('PlanUpgradeCta', () => {
    const renderCta = (c: McpConnector, contactUrl?: string) =>
      render(
        <MemoryRouter>
          <PlanUpgradeCta connector={c} contactUrl={contactUrl} className="cta" />
        </MemoryRouter>,
      )

    it('leads to the contact address when nothing is on sale', () => {
      renderCta(locked({ minPlan: 'business', upgradeTo: '' }), 'mailto:sales@example.com')
      expect(screen.getByRole('link', { name: 'Contact us' })).toHaveAttribute('href', 'mailto:sales@example.com')
    })

    it('draws no dead button when there is nowhere to send the org', () => {
      const { container } = renderCta(locked({ minPlan: 'business', upgradeTo: '' }))
      expect(container).toBeEmptyDOMElement()
    })

    it('does not send the org to a plain-http contact page', () => {
      const { container } = renderCta(locked({ minPlan: 'business', upgradeTo: '' }), 'http://example.com/contact')
      expect(container).toBeEmptyDOMElement()
    })

    it('draws nothing for a connector the plan already includes', () => {
      const { container } = renderCta(connector(), 'mailto:sales@example.com')
      expect(container).toBeEmptyDOMElement()
    })
  })

  // The drawer's 409 note and its test result follow the card's rule: the
  // contact address, else nothing.
  describe('PlanGateLink', () => {
    const renderLink = (upgradeTo: string | null | undefined, contactUrl?: string) =>
      render(
        <MemoryRouter>
          <PlanGateLink upgradeTo={upgradeTo} contactUrl={contactUrl} />
        </MemoryRouter>,
      )

    it('otherwise leads to the contact address', () => {
      renderLink('', 'https://example.com/contact')
      expect(screen.getByRole('link', { name: 'Contact us' })).toHaveAttribute('href', 'https://example.com/contact')
    })

    it('otherwise draws nothing', () => {
      for (const contact of [undefined, '', 'http://example.com/contact']) {
        const { container, unmount } = renderLink(null, contact)
        expect(container).toBeEmptyDOMElement()
        unmount()
      }
    })
  })

  it('only sends the org to an https page or a mail address', () => {
    expect(safeContactUrl('https://example.com/contact')).toBe('https://example.com/contact')
    expect(safeContactUrl('mailto:sales@example.com')).toBe('mailto:sales@example.com')
    expect(safeContactUrl('http://example.com/contact')).toBeNull()
    expect(safeContactUrl('javascript:alert(1)')).toBeNull()
    expect(safeContactUrl('')).toBeNull()
    expect(safeContactUrl(undefined)).toBeNull()
  })
})
