import { Bot, BarChart3, HeartPulse, Plug } from 'lucide-react'
import { AdminHub } from './AdminHub'
import { CopilotSettingsTab } from './settings/CopilotSettingsTab'
import { CopilotUsagePage } from './CopilotUsagePage'
import { McpConnectorsPage } from './McpConnectorsPage'
import { KobiHealth } from './health/KobiHealth'

// AI (Kobi) — the AI copilot: provider/model configuration, the cloud
// Connectors Kobi reads through, token usage and how it is doing (Health).
export function AIHub() {
  return (
    <AdminHub
      tabs={[
        {
          key: 'config',
          label: 'Configuration',
          Icon: Bot,
          title: 'AI Copilot (Kobi)',
          subtitle: 'Provider, model, and fallback for the AI copilot.',
          render: () => <CopilotSettingsTab />,
        },
        // Connectors are what Kobi may read outside the cluster. Org-wide, so
        // the tab works with no cluster connected;
        // ?tab=connectors&connector=<id> opens one connector's drawer.
        {
          key: 'connectors',
          label: 'Connectors',
          Icon: Plug,
          title: 'Connectors',
          subtitle:
            'Let Kobi read the cloud resources your workloads depend on — databases, caches, queues, load balancers — while it investigates. Curated MCP servers, your credentials, read-only.',
          render: () => <McpConnectorsPage />,
        },
        { key: 'usage', label: 'Usage', Icon: BarChart3, render: () => <CopilotUsagePage /> },
        { key: 'health', label: 'Health', Icon: HeartPulse, render: () => <KobiHealth /> },
      ]}
    />
  )
}
