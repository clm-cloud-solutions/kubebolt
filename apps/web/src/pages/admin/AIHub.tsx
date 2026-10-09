import { Bot, BarChart3, HeartPulse } from 'lucide-react'
import { AdminHub } from './AdminHub'
import { CopilotSettingsTab } from './settings/CopilotSettingsTab'
import { CopilotUsagePage } from './CopilotUsagePage'
import { KobiHealth } from './health/KobiHealth'

// AI (Kobi) — the AI copilot: provider/model configuration, token usage and
// how it is doing (Health).
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
        { key: 'usage', label: 'Usage', Icon: BarChart3, render: () => <CopilotUsagePage /> },
        { key: 'health', label: 'Health', Icon: HeartPulse, render: () => <KobiHealth /> },
      ]}
    />
  )
}
