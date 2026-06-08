import { Bot, BarChart3 } from 'lucide-react'
import { AdminHub } from './AdminHub'
import { CopilotSettingsTab } from './settings/CopilotSettingsTab'
import { CopilotUsagePage } from './CopilotUsagePage'

// AI (Kobi) — the AI copilot: provider/model configuration and token usage.
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
      ]}
    />
  )
}
