import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Users, Plus, Building2, UsersRound } from 'lucide-react'
import { api, isRequiresEE } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { Modal } from '@/components/shared/Modal'
import { EnterpriseFeatureNotice } from '@/components/shared/EnterpriseFeatureNotice'
import type { UserRole } from '@/types/auth'

const ROLE_COLORS: Record<UserRole, string> = {
  admin: 'bg-status-error-dim text-status-error',
  editor: 'bg-status-info-dim text-status-info',
  viewer: 'bg-status-ok-dim text-status-ok',
}

function RoleBadge({ role }: { role: UserRole }) {
  return (
    <span className={`px-2 py-0.5 rounded-full text-[10px] font-mono font-medium uppercase tracking-wider ${ROLE_COLORS[role]}`}>
      {role}
    </span>
  )
}

// --- New team modal — in OSS this always hits the requires_ee boundary and
// swaps to the upgrade notice; in SaaS/EE the same form creates the team. ---

function NewTeamModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [eeBlocked, setEeBlocked] = useState(false)
  const [saving, setSaving] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setSaving(true)
    try {
      await api.createTeam({ name })
      onCreated()
      onClose()
    } catch (err) {
      if (isRequiresEE(err)) {
        setEeBlocked(true)
      } else {
        setError(err instanceof Error ? err.message : 'Failed to create team')
      }
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal badge="New team" title="Create a team" onClose={onClose} size="sm">
      {eeBlocked ? (
        <div className="p-5 space-y-4">
          <EnterpriseFeatureNotice message="Teams let you segment users and clusters by functional or business area. KubeBolt OSS runs a single default team; multiple teams — with cross-team access and per-team roles — are available in KubeBolt SaaS and Enterprise." />
          <div className="flex justify-end">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 transition-colors"
            >
              Got it
            </button>
          </div>
        </div>
      ) : (
        <form onSubmit={handleSubmit} className="p-5 space-y-3">
          {error && <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>}
          <div className="space-y-1">
            <label className="text-[11px] font-medium text-kb-text-secondary">Team name</label>
            <input
              value={name}
              onChange={e => setName(e.target.value)}
              required
              placeholder="e.g. platform"
              className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors"
            />
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 text-xs text-kb-text-secondary hover:text-kb-text-primary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={saving}
              className="px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-50 transition-colors"
            >
              {saving ? 'Creating...' : 'Create team'}
            </button>
          </div>
        </form>
      )}
    </Modal>
  )
}

export function TeamsPage() {
  const { user } = useAuth()
  const [creating, setCreating] = useState(false)

  const { data: teams, isLoading, error, refetch } = useQuery({
    queryKey: ['admin-teams'],
    queryFn: api.listTeams,
  })

  // OSS is single-team: the default team is the one we render. (EE would list
  // many — the members panel here would become per-team selectable.)
  const team = teams?.[0]

  const { data: members } = useQuery({
    queryKey: ['admin-team-members', team?.id],
    queryFn: () => api.listTeamMembers(team!.id),
    enabled: !!team,
  })

  const orgName = user?.org?.name ?? 'default'

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-64">
        <LoadingSpinner size="lg" />
      </div>
    )
  }

  if (error) {
    return <ErrorState message={error instanceof Error ? error.message : 'Failed to load teams'} onRetry={() => refetch()} />
  }

  return (
    <div>
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <UsersRound className="w-5 h-5" />
            Teams
          </h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5">Group users into teams within your organization</p>
        </div>
        <button
          onClick={() => setCreating(true)}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 transition-colors"
        >
          <Plus className="w-3.5 h-3.5" />
          New team
        </button>
      </div>

      {/* OSS boundary hint — at the top, like the info banners on other screens */}
      <EnterpriseFeatureNotice
        className="mb-4"
        message="KubeBolt OSS runs a single organization and a single default team — every user is a member. Multiple teams, cross-team cluster access, and team-only users are available in KubeBolt SaaS and Enterprise."
      />

      {/* Org context */}
      <div className="flex items-center gap-2 mb-4 px-4 py-2.5 bg-kb-card border border-kb-border rounded-lg">
        <Building2 className="w-4 h-4 text-kb-text-tertiary shrink-0" />
        <span className="text-[11px] text-kb-text-tertiary">Organization</span>
        <span className="text-xs font-medium text-kb-text-primary">{orgName}</span>
        <span className="ml-auto text-[10px] text-kb-text-tertiary">
          {teams?.length === 1 ? '1 team' : `${teams?.length ?? 0} teams`}
        </span>
      </div>

      {/* Members of the default team */}
      <div className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
        <div className="flex items-center gap-2 px-4 py-3 border-b border-kb-border">
          <Users className="w-4 h-4 text-kb-text-secondary" />
          <span className="text-xs font-medium text-kb-text-primary">{team?.name ?? 'default'}</span>
          <span className="px-1.5 py-0.5 rounded-full text-[9px] font-mono font-medium uppercase bg-kb-elevated text-kb-text-tertiary">
            {team?.memberCount ?? 0} {team?.memberCount === 1 ? 'member' : 'members'}
          </span>
        </div>
        <table className="w-full">
          <thead>
            <tr className="border-b border-kb-border">
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Login</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Name</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Role</th>
            </tr>
          </thead>
          <tbody>
            {members?.map(m => (
              <tr key={m.userId} className="border-b border-kb-border last:border-b-0 hover:bg-kb-card-hover transition-colors">
                <td className="px-4 py-2.5">
                  <div className="flex items-center gap-2">
                    <div className="w-6 h-6 rounded-full bg-kb-elevated flex items-center justify-center text-[10px] font-mono font-semibold text-kb-text-secondary uppercase">
                      {m.username[0]}
                    </div>
                    <span className="text-xs font-medium text-kb-text-primary">{m.username}</span>
                  </div>
                </td>
                <td className="px-4 py-2.5 text-xs text-kb-text-secondary">{m.name || '-'}</td>
                <td className="px-4 py-2.5"><RoleBadge role={m.role} /></td>
              </tr>
            ))}
            {members?.length === 0 && (
              <tr>
                <td colSpan={3} className="px-4 py-8 text-center text-xs text-kb-text-tertiary">No members in this team</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {creating && (
        <NewTeamModal
          onClose={() => setCreating(false)}
          onCreated={() => refetch()}
        />
      )}
    </div>
  )
}
