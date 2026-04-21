import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Users, Plus, Pencil, Trash2, KeyRound, Search } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import type { AuthUser, UserRole } from '@/types/auth'

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

function formatDate(dateStr?: string) {
  if (!dateStr) return '-'
  const d = new Date(dateStr)
  const now = new Date()
  const diff = now.getTime() - d.getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return d.toLocaleDateString()
}

// --- Create/Edit User Modal ---

interface UserFormModalProps {
  user?: AuthUser
  onClose: () => void
  onSaved: () => void
}

function UserFormModal({ user, onClose, onSaved }: UserFormModalProps) {
  const isEdit = !!user
  const [username, setUsername] = useState(user?.username || '')
  const [email, setEmail] = useState(user?.email || '')
  const [name, setName] = useState(user?.name || '')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<UserRole>(user?.role || 'viewer')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setSaving(true)

    try {
      if (isEdit) {
        await api.updateUser(user!.id, { username, email, name, role })
      } else {
        if (password.length < 8) {
          setError('Password must be at least 8 characters')
          setSaving(false)
          return
        }
        await api.createUser({ username, email, name, password, role })
      }
      onSaved()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save user')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" onClick={onClose}>
      <div className="bg-kb-card border border-kb-border rounded-xl w-full max-w-md p-6 shadow-xl" onClick={e => e.stopPropagation()}>
        <h2 className="text-sm font-semibold text-kb-text-primary mb-4">
          {isEdit ? 'Edit user' : 'New user'}
        </h2>

        <form onSubmit={handleSubmit} className="space-y-3">
          {error && (
            <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>
          )}

          <div className="space-y-1">
            <label className="text-[11px] font-medium text-kb-text-secondary">Username</label>
            <input
              value={username}
              onChange={e => setUsername(e.target.value)}
              required
              className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary focus:outline-none focus:border-kb-accent transition-colors"
            />
          </div>

          <div className="space-y-1">
            <label className="text-[11px] font-medium text-kb-text-secondary">Email</label>
            <input
              type="email"
              value={email}
              onChange={e => setEmail(e.target.value)}
              className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary focus:outline-none focus:border-kb-accent transition-colors"
            />
          </div>

          <div className="space-y-1">
            <label className="text-[11px] font-medium text-kb-text-secondary">Display name</label>
            <input
              value={name}
              onChange={e => setName(e.target.value)}
              className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary focus:outline-none focus:border-kb-accent transition-colors"
            />
          </div>

          {!isEdit && (
            <div className="space-y-1">
              <label className="text-[11px] font-medium text-kb-text-secondary">Password</label>
              <input
                type="password"
                value={password}
                onChange={e => setPassword(e.target.value)}
                required
                minLength={8}
                placeholder="Min. 8 characters"
                className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors"
              />
            </div>
          )}

          <div className="space-y-1">
            <label className="text-[11px] font-medium text-kb-text-secondary">Role</label>
            <select
              value={role}
              onChange={e => setRole(e.target.value as UserRole)}
              className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary focus:outline-none focus:border-kb-accent transition-colors"
            >
              <option value="viewer">Viewer — read-only access</option>
              <option value="editor">Editor — can edit, scale, restart</option>
              <option value="admin">Admin — full access + user management</option>
            </select>
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
              {saving ? 'Saving...' : isEdit ? 'Save changes' : 'Create user'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

// --- Reset Password Modal ---

function ResetPasswordModal({ user, onClose }: { user: AuthUser; onClose: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [success, setSuccess] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (password.length < 8) {
      setError('Password must be at least 8 characters')
      return
    }
    setError(null)
    setSaving(true)
    try {
      await api.resetUserPassword(user.id, password)
      setSuccess(true)
      setTimeout(onClose, 1500)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to reset password')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" onClick={onClose}>
      <div className="bg-kb-card border border-kb-border rounded-xl w-full max-w-sm p-6 shadow-xl" onClick={e => e.stopPropagation()}>
        <h2 className="text-sm font-semibold text-kb-text-primary mb-1">Reset password</h2>
        <p className="text-xs text-kb-text-tertiary mb-4">Set a new password for <strong>{user.username}</strong></p>

        <form onSubmit={handleSubmit} className="space-y-3">
          {error && <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>}
          {success && <div className="px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">Password reset successfully</div>}

          <input
            type="password"
            value={password}
            onChange={e => setPassword(e.target.value)}
            required
            minLength={8}
            placeholder="New password (min. 8 characters)"
            className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors"
          />

          <div className="flex justify-end gap-2">
            <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors">Cancel</button>
            <button type="submit" disabled={saving || success} className="px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-50 transition-colors">
              {saving ? 'Resetting...' : 'Reset password'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

// --- Delete Confirmation Modal ---

function DeleteUserModal({ user, onClose, onDeleted }: { user: AuthUser; onClose: () => void; onDeleted: () => void }) {
  const [confirmName, setConfirmName] = useState('')
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleDelete() {
    setError(null)
    setDeleting(true)
    try {
      await api.deleteUser(user.id)
      onDeleted()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete user')
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" onClick={onClose}>
      <div className="bg-kb-card border border-kb-border rounded-xl w-full max-w-sm p-6 shadow-xl" onClick={e => e.stopPropagation()}>
        <h2 className="text-sm font-semibold text-status-error mb-1">Delete user</h2>
        <p className="text-xs text-kb-text-tertiary mb-4">
          Type <strong className="text-kb-text-primary">{user.username}</strong> to confirm deletion.
        </p>

        {error && <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs mb-3">{error}</div>}

        <input
          value={confirmName}
          onChange={e => setConfirmName(e.target.value)}
          placeholder={user.username}
          className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-status-error transition-colors mb-3"
        />

        <div className="flex justify-end gap-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors">Cancel</button>
          <button
            onClick={handleDelete}
            disabled={confirmName !== user.username || deleting}
            className="px-3 py-1.5 text-xs font-medium text-white bg-status-error rounded-lg hover:bg-status-error/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            {deleting ? 'Deleting...' : 'Delete user'}
          </button>
        </div>
      </div>
    </div>
  )
}

// --- Main UsersPage ---

export function UsersPage() {
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<AuthUser | null>(null)
  const [resettingPw, setResettingPw] = useState<AuthUser | null>(null)
  const [deleting, setDeleting] = useState<AuthUser | null>(null)

  const { data: users, isLoading, error } = useQuery({
    queryKey: ['admin-users'],
    queryFn: api.listUsers,
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['admin-users'] })

  const filtered = users?.filter(u =>
    !search || u.username.toLowerCase().includes(search.toLowerCase()) ||
    u.email.toLowerCase().includes(search.toLowerCase()) ||
    u.name.toLowerCase().includes(search.toLowerCase())
  )

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-64">
        <LoadingSpinner size="lg" />
      </div>
    )
  }

  if (error) {
    return <ErrorState message={error instanceof Error ? error.message : 'Failed to load users'} onRetry={invalidate} />
  }

  return (
    <div>
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <Users className="w-5 h-5" />
            Users
          </h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5">Manage users and their roles in KubeBolt</p>
        </div>
        <button
          onClick={() => setCreating(true)}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 transition-colors"
        >
          <Plus className="w-3.5 h-3.5" />
          New user
        </button>
      </div>

      {/* Search */}
      <div className="relative mb-4">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-kb-text-tertiary" />
        <input
          value={search}
          onChange={e => setSearch(e.target.value)}
          placeholder="Search user by login, email, or name..."
          className="w-full pl-10 pr-4 py-2 text-sm bg-kb-card border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors"
        />
      </div>

      {/* Table */}
      <div className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
        <table className="w-full">
          <thead>
            <tr className="border-b border-kb-border">
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Login</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Email</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Name</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Role</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Last active</th>
              <th className="px-4 py-2.5 text-right text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Actions</th>
            </tr>
          </thead>
          <tbody>
            {filtered?.map(user => (
              <tr key={user.id} className="border-b border-kb-border last:border-b-0 hover:bg-kb-card-hover transition-colors">
                <td className="px-4 py-2.5">
                  <div className="flex items-center gap-2">
                    <div className="w-6 h-6 rounded-full bg-kb-elevated flex items-center justify-center text-[10px] font-mono font-semibold text-kb-text-secondary uppercase">
                      {user.username[0]}
                    </div>
                    <span className="text-xs font-medium text-kb-text-primary">{user.username}</span>
                  </div>
                </td>
                <td className="px-4 py-2.5 text-xs text-kb-text-secondary">{user.email || '-'}</td>
                <td className="px-4 py-2.5 text-xs text-kb-text-secondary">{user.name || '-'}</td>
                <td className="px-4 py-2.5"><RoleBadge role={user.role} /></td>
                <td className="px-4 py-2.5 text-xs text-kb-text-tertiary font-mono">{formatDate(user.lastLoginAt)}</td>
                <td className="px-4 py-2.5">
                  <div className="flex items-center justify-end gap-1">
                    <button
                      onClick={() => setEditing(user)}
                      className="p-1.5 rounded-md text-kb-text-tertiary hover:text-kb-text-primary hover:bg-kb-elevated transition-colors"
                      title="Edit user"
                    >
                      <Pencil className="w-3.5 h-3.5" />
                    </button>
                    <button
                      onClick={() => setResettingPw(user)}
                      className="p-1.5 rounded-md text-kb-text-tertiary hover:text-kb-text-primary hover:bg-kb-elevated transition-colors"
                      title="Reset password"
                    >
                      <KeyRound className="w-3.5 h-3.5" />
                    </button>
                    <button
                      onClick={() => setDeleting(user)}
                      className="p-1.5 rounded-md text-kb-text-tertiary hover:text-status-error hover:bg-status-error-dim transition-colors"
                      title="Delete user"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {filtered?.length === 0 && (
              <tr>
                <td colSpan={6} className="px-4 py-8 text-center text-xs text-kb-text-tertiary">
                  {search ? 'No users match your search' : 'No users found'}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* Modals */}
      {creating && <UserFormModal onClose={() => setCreating(false)} onSaved={invalidate} />}
      {editing && <UserFormModal user={editing} onClose={() => setEditing(null)} onSaved={invalidate} />}
      {resettingPw && <ResetPasswordModal user={resettingPw} onClose={() => setResettingPw(null)} />}
      {deleting && <DeleteUserModal user={deleting} onClose={() => setDeleting(null)} onDeleted={invalidate} />}
    </div>
  )
}
