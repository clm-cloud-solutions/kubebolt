import { KeyRound, Activity, Puzzle, Gauge } from 'lucide-react'
import { AdminHub } from './AdminHub'
import { AgentTokensPage } from './AgentTokensPage'
import { IngestActivityPage } from './IngestActivityPage'
import { IntegrationsPage } from './IntegrationsPage'
import { IngestSettingsTab } from './settings/IngestSettingsTab'

// Agents & Ingest — everything about getting data into KubeBolt: agent auth
// tokens, watching agents connect, external integrations, and the fleet config.
export function AgentsHub() {
  return (
    <AdminHub
      tabs={[
        { key: 'tokens', label: 'Agent Tokens', Icon: KeyRound, render: () => <AgentTokensPage /> },
        { key: 'activity', label: 'Activity', Icon: Activity, render: () => <IngestActivityPage /> },
        { key: 'integrations', label: 'Integrations', Icon: Puzzle, render: () => <IntegrationsPage /> },
        {
          key: 'config',
          label: 'Configuration',
          Icon: Gauge,
          title: 'Agents & Ingest',
          subtitle: 'Channel security, rate limiting, auto-registration, and remote_write.',
          render: () => <IngestSettingsTab />,
        },
      ]}
    />
  )
}
