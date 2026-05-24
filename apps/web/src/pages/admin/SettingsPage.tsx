import { useState } from 'react'
import { Settings, Bot, Gauge, Bell, KeyRound, SlidersHorizontal, ScrollText } from 'lucide-react'
import { CopilotSettingsTab } from './settings/CopilotSettingsTab'
import { IngestSettingsTab } from './settings/IngestSettingsTab'
import { NotificationsSettingsTab } from './settings/NotificationsSettingsTab'
import { AuthSettingsTab } from './settings/AuthSettingsTab'
import { GeneralSettingsTab } from './settings/GeneralSettingsTab'
import { BootedWithModal } from './settings/BootedWithModal'

// Admin → Settings page (spec #09). Tabbed layout so additional
// runtime-configurable domains (Notifications, Ingest fleet defaults,
// Auth modes with restart banner, etc.) land here without reorganising
// the existing per-page admin surfaces — those pages stay where they
// are; this page is the home for config that USED to be env-only.
//
// V1 ships only the Copilot tab. The tabs scaffold is left in place so
// adding more tabs is a one-line append to the TABS list.
//
// Layout follows NotificationsPage conventions: no outer padding (the
// Layout's <main> already supplies it), header on top, then content.

type TabKey = 'general' | 'copilot' | 'ingest' | 'notifications' | 'auth'

interface TabDef {
  key: TabKey
  label: string
  Icon: typeof Bot
}

const TABS: TabDef[] = [
  {
    key: 'general',
    label: 'General',
    Icon: SlidersHorizontal,
  },
  {
    key: 'copilot',
    label: 'AI Copilot',
    Icon: Bot,
  },
  {
    key: 'ingest',
    label: 'Ingest',
    Icon: Gauge,
  },
  {
    key: 'notifications',
    label: 'Notifications',
    Icon: Bell,
  },
  {
    key: 'auth',
    label: 'Auth',
    Icon: KeyRound,
  },
]

export function SettingsPage() {
  const [active, setActive] = useState<TabKey>('general')
  const [bootedWithOpen, setBootedWithOpen] = useState(false)

  return (
    // pb-24 leaves a comfortable gutter at the bottom of the page so
    // the form's action bar (Reset / Cancel / Save) doesn't sit under
    // the floating Copilot toggle in tall forms. Without this the
    // buttons end up at the same screen position as the toggle and
    // become unclickable.
    <div className="pb-24">
      <div className="flex items-center justify-between mb-6 gap-4">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <Settings className="w-5 h-5" />
            Settings
          </h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5">
            Configure KubeBolt at runtime. Values set here override environment variables on the next read; reset any tab to fall back to env defaults.
          </p>
        </div>
        <button
          type="button"
          onClick={() => setBootedWithOpen(true)}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border shrink-0"
          title="Inspect the KUBEBOLT_* env vars the process saw at boot"
        >
          <ScrollText className="w-3.5 h-3.5" />
          Boot snapshot
        </button>
      </div>

      <div className="border-b border-kb-border mb-5 flex items-center gap-1">
        {TABS.map((tab) => {
          const isActive = tab.key === active
          return (
            <button
              key={tab.key}
              type="button"
              onClick={() => setActive(tab.key)}
              className={`flex items-center gap-2 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-colors ${
                isActive
                  ? 'border-kb-accent text-kb-accent'
                  : 'border-transparent text-kb-text-secondary hover:text-kb-text-primary'
              }`}
            >
              <tab.Icon className="w-3.5 h-3.5" />
              {tab.label}
            </button>
          )
        })}
      </div>

      <div>
        {active === 'general' && <GeneralSettingsTab />}
        {active === 'copilot' && <CopilotSettingsTab />}
        {active === 'ingest' && <IngestSettingsTab />}
        {active === 'notifications' && <NotificationsSettingsTab />}
        {active === 'auth' && <AuthSettingsTab />}
      </div>

      {bootedWithOpen && <BootedWithModal onClose={() => setBootedWithOpen(false)} />}
    </div>
  )
}
