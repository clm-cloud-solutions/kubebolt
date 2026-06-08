import { useState } from 'react'
import { SlidersHorizontal, Bell, ScrollText } from 'lucide-react'
import { AdminHub } from './AdminHub'
import { GeneralSettingsTab } from './settings/GeneralSettingsTab'
import { NotificationsSettingsTab } from './settings/NotificationsSettingsTab'
import { BootedWithModal } from './settings/BootedWithModal'

// System — instance-wide configuration: general settings + notifications, plus
// the boot-snapshot inspector that used to live in the Settings header.
export function SystemHub() {
  const [bootOpen, setBootOpen] = useState(false)

  return (
    <>
      <AdminHub
        rightSlot={
          <button
            type="button"
            onClick={() => setBootOpen(true)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border shrink-0"
            title="Inspect the KUBEBOLT_* env vars the process saw at boot"
          >
            <ScrollText className="w-3.5 h-3.5" />
            Boot snapshot
          </button>
        }
        tabs={[
          {
            key: 'general',
            label: 'General',
            Icon: SlidersHorizontal,
            title: 'General',
            subtitle: 'Display name and the default data-refresh interval.',
            render: () => <GeneralSettingsTab />,
          },
          {
            key: 'notifications',
            label: 'Notifications',
            Icon: Bell,
            title: 'Notifications',
            subtitle: 'Where KubeBolt sends alerts and at what severity.',
            render: () => <NotificationsSettingsTab />,
          },
        ]}
      />
      {bootOpen && <BootedWithModal onClose={() => setBootOpen(false)} />}
    </>
  )
}
