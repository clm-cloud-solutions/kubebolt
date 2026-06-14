import { Users, UsersRound, KeyRound } from 'lucide-react'
import { AdminHub } from './AdminHub'
import { UsersPage } from './UsersPage'
import { TeamsPage } from './TeamsPage'
import { AuthSettingsTab } from './settings/AuthSettingsTab'

// Access — identity & sign-in: who can use KubeBolt and how they authenticate.
export function AccessHub() {
  return (
    <AdminHub
      tabs={[
        { key: 'users', label: 'Users', Icon: Users, render: () => <UsersPage /> },
        { key: 'teams', label: 'Teams', Icon: UsersRound, render: () => <TeamsPage /> },
        {
          key: 'authentication',
          label: 'Authentication',
          Icon: KeyRound,
          title: 'Authentication',
          subtitle: 'How users sign in to KubeBolt.',
          render: () => <AuthSettingsTab />,
        },
      ]}
    />
  )
}
