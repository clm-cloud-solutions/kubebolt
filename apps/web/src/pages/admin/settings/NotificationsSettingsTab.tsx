import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Bell,
  Eye,
  EyeOff,
  AlertTriangle,
  CheckCircle2,
  RotateCcw,
  Save,
  Loader2,
  X,
  Send,
  Check,
  Mail,
} from 'lucide-react'
import { api } from '@/services/api'
import type { NotificationsSettingsPutRequest, NotificationsSettingsResponse } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ConfirmDialog } from './ConfirmDialog'

// NotificationsSettingsTab is the editable Settings → Notifications
// surface introduced by spec #09. Replaces the previously read-only
// /admin/notifications page (now deleted) — operators can change every
// channel's destination and the global thresholds without redeploying.
//
// Three blocks of secrets (Slack webhook URL, Discord webhook URL, SMTP
// password) follow the reveal-and-replace pattern: server returns only
// a masked preview, the input is empty by default, typing replaces the
// stored value, leaving blank means "keep current". Same convention as
// CopilotSettingsTab — kept consistent so operators learn one pattern.

interface FormState {
  // Global
  masterEnabled: boolean
  minSeverity: string
  cooldownSeconds: string // string for editable input
  baseURL: string
  includeResolved: boolean

  // Slack
  slackEnabled: boolean
  slackWebhookURL: string // empty = unchanged

  // Discord
  discordEnabled: boolean
  discordWebhookURL: string

  // Email
  emailEnabled: boolean
  emailHost: string
  emailPort: string
  emailUsername: string
  emailPassword: string // empty = unchanged
  emailFrom: string
  emailTo: string // comma-separated for the input; coerced to array on save
  emailDigestMode: string
}

function stateFromResponse(data: NotificationsSettingsResponse): FormState {
  const eff = data.effective
  return {
    masterEnabled: eff.masterEnabled,
    minSeverity: eff.minSeverity || 'warning',
    cooldownSeconds: String(eff.cooldownSeconds ?? 3600),
    baseURL: eff.baseURL || '',
    includeResolved: eff.includeResolved,
    slackEnabled: eff.slackEnabled,
    slackWebhookURL: '',
    discordEnabled: eff.discordEnabled,
    discordWebhookURL: '',
    emailEnabled: eff.emailEnabled,
    emailHost: eff.emailHost || '',
    emailPort: String(eff.emailPort ?? 587),
    emailUsername: eff.emailUsername || '',
    emailPassword: '',
    emailFrom: eff.emailFrom || '',
    emailTo: (eff.emailTo || []).join(', '),
    emailDigestMode: eff.emailDigestMode || 'instant',
  }
}

// Parse the comma-separated emailTo input into a clean string array.
// Empty trimmed entries are dropped so trailing commas don't poison the
// recipient list.
function parseEmailTo(raw: string): string[] {
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
}

function buildPatch(initial: FormState, current: FormState): NotificationsSettingsPutRequest {
  const patch: NotificationsSettingsPutRequest['patch'] = {}
  const globalPatch: NonNullable<NotificationsSettingsPutRequest['patch']>['global'] = {}
  if (current.masterEnabled !== initial.masterEnabled) globalPatch.masterEnabled = current.masterEnabled
  if (current.minSeverity !== initial.minSeverity) globalPatch.minSeverity = current.minSeverity
  const cd = parseInt(current.cooldownSeconds, 10)
  if (!isNaN(cd) && cd >= 0 && cd !== parseInt(initial.cooldownSeconds, 10)) globalPatch.cooldownSeconds = cd
  if (current.baseURL !== initial.baseURL) globalPatch.baseURL = current.baseURL
  if (current.includeResolved !== initial.includeResolved) globalPatch.includeResolved = current.includeResolved
  if (Object.keys(globalPatch).length > 0) patch.global = globalPatch

  if (current.slackEnabled !== initial.slackEnabled) {
    patch.slack = { enabled: current.slackEnabled }
  }
  if (current.discordEnabled !== initial.discordEnabled) {
    patch.discord = { enabled: current.discordEnabled }
  }

  const emailPatch: NonNullable<NotificationsSettingsPutRequest['patch']>['email'] = {}
  if (current.emailEnabled !== initial.emailEnabled) emailPatch.enabled = current.emailEnabled
  if (current.emailHost !== initial.emailHost) emailPatch.host = current.emailHost
  const port = parseInt(current.emailPort, 10)
  if (!isNaN(port) && port > 0 && port !== parseInt(initial.emailPort, 10)) emailPatch.port = port
  if (current.emailUsername !== initial.emailUsername) emailPatch.username = current.emailUsername
  if (current.emailFrom !== initial.emailFrom) emailPatch.from = current.emailFrom
  // Compare normalised forms so whitespace changes alone don't trigger a
  // wire-side update. The backend stores the cleaned array; we send only
  // when the parsed list differs from the parsed initial list.
  const currentTo = parseEmailTo(current.emailTo)
  const initialTo = parseEmailTo(initial.emailTo)
  if (currentTo.join('') !== initialTo.join('')) {
    emailPatch.to = currentTo
  }
  if (current.emailDigestMode !== initial.emailDigestMode) emailPatch.digestMode = current.emailDigestMode
  if (Object.keys(emailPatch).length > 0) patch.email = emailPatch

  const req: NotificationsSettingsPutRequest = {}
  if (Object.keys(patch).length > 0) req.patch = patch
  if (current.slackWebhookURL.trim() !== '') req.plaintextSlackWebhookURL = current.slackWebhookURL.trim()
  if (current.discordWebhookURL.trim() !== '') req.plaintextDiscordWebhookURL = current.discordWebhookURL.trim()
  if (current.emailPassword.trim() !== '') req.plaintextSMTPPassword = current.emailPassword
  return req
}

export function NotificationsSettingsTab() {
  const queryClient = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'settings', 'notifications'],
    queryFn: api.getSettingsNotifications,
  })

  if (isLoading) return <LoadingSpinner />
  if (error || !data) {
    return (
      <div className="rounded-lg border border-status-error-dim bg-status-error-dim/30 p-4 text-xs text-status-error">
        Failed to load Notifications settings. Refresh the page or check that the backend has BoltDB persistence enabled.
      </div>
    )
  }
  return (
    <NotificationsSettingsForm
      data={data}
      onSaved={() => {
        queryClient.invalidateQueries({ queryKey: ['admin', 'settings', 'notifications'] })
        // Keep the legacy /notifications/config query in sync — other
        // surfaces might still read it during the transition.
        queryClient.invalidateQueries({ queryKey: ['notifications-config'] })
      }}
    />
  )
}

function NotificationsSettingsForm({
  data,
  onSaved,
}: {
  data: NotificationsSettingsResponse
  onSaved: () => void
}) {
  const [initial, setInitial] = useState<FormState>(() => stateFromResponse(data))
  const [form, setForm] = useState<FormState>(() => stateFromResponse(data))
  const [revealSlack, setRevealSlack] = useState(false)
  const [revealDiscord, setRevealDiscord] = useState(false)
  const [revealSMTPPassword, setRevealSMTPPassword] = useState(false)
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false)

  const dirtyMap = {
    masterEnabled: form.masterEnabled !== initial.masterEnabled,
    minSeverity: form.minSeverity !== initial.minSeverity,
    cooldownSeconds: form.cooldownSeconds !== initial.cooldownSeconds,
    baseURL: form.baseURL !== initial.baseURL,
    includeResolved: form.includeResolved !== initial.includeResolved,
    slackEnabled: form.slackEnabled !== initial.slackEnabled,
    slackWebhookURL: form.slackWebhookURL.trim() !== '',
    discordEnabled: form.discordEnabled !== initial.discordEnabled,
    discordWebhookURL: form.discordWebhookURL.trim() !== '',
    emailEnabled: form.emailEnabled !== initial.emailEnabled,
    emailHost: form.emailHost !== initial.emailHost,
    emailPort: form.emailPort !== initial.emailPort,
    emailUsername: form.emailUsername !== initial.emailUsername,
    emailPassword: form.emailPassword.trim() !== '',
    emailFrom: form.emailFrom !== initial.emailFrom,
    emailTo: form.emailTo !== initial.emailTo,
    emailDigestMode: form.emailDigestMode !== initial.emailDigestMode,
  }
  const isDirty = Object.values(dirtyMap).some(Boolean)

  const queryClient = useQueryClient()
  const saveMutation = useMutation({
    mutationFn: () => api.putSettingsNotifications(buildPatch(initial, form)),
    onSuccess: (newData) => {
      const next = stateFromResponse(newData)
      setInitial(next)
      setForm(next)
      setSavedAt(Date.now())
      setRevealSlack(false)
      setRevealDiscord(false)
      setRevealSMTPPassword(false)
      // Bypass the invalidate→refetch round-trip: the PUT response IS
      // the new server state, so seed the cache directly. Without
      // this, `eff` in this component lags by one refetch — visible
      // as test-button gating that stays stale until the network
      // round-trip lands.
      queryClient.setQueryData(['admin', 'settings', 'notifications'], newData)
      onSaved()
    },
  })

  const resetMutation = useMutation({
    mutationFn: () => api.resetSettingsNotifications(),
    onSuccess: () => onSaved(),
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    saveMutation.mutate()
  }

  const eff = data.effective

  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      {!data.secretsReadable && (
        <div className="flex items-start gap-2 rounded-xl border border-status-warn-dim bg-status-warn-dim/30 p-4 text-xs text-status-warn">
          <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
          <div>
            <div className="font-semibold mb-0.5">Stored secret is unreadable</div>
            <div>
              The JWT secret was likely rotated since these credentials were saved. Re-enter the
              affected webhook URL or SMTP password below to restore delivery.
            </div>
          </div>
        </div>
      )}

      {/* Global */}
      <SectionCard
        icon={<Bell className="w-4 h-4 text-kb-accent" />}
        title="Global"
        subtitle="Master toggle, severity threshold, and dedup window applied to every channel."
      >
        <label className="flex items-start gap-2 text-xs text-kb-text-secondary cursor-pointer">
          <input
            type="checkbox"
            checked={form.masterEnabled}
            onChange={(e) => setForm({ ...form, masterEnabled: e.target.checked })}
            className="accent-kb-accent mt-0.5"
          />
          <div>
            <div className="flex items-center gap-2">
              <div className="text-kb-text-primary">Notifications enabled</div>
              {dirtyMap.masterEnabled && <UnsavedChip />}
            </div>
            <div className="text-kb-text-tertiary">
              Master kill switch. Off: no channel receives anything regardless of configuration — useful for maintenance windows.
            </div>
          </div>
        </label>

        <Field
          label="Minimum severity"
          dirty={dirtyMap.minSeverity}
          helper="Insights below this level are dropped before dispatch. 'Warning' is a sensible default for most clusters."
        >
          <select
            className="w-full max-w-md px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.minSeverity}
            onChange={(e) => setForm({ ...form, minSeverity: e.target.value })}
          >
            <option value="info">Info — every insight notifies</option>
            <option value="warning">Warning — and above (recommended)</option>
            <option value="critical">Critical — only critical insights</option>
          </select>
        </Field>

        <Field
          label="Cooldown (seconds)"
          dirty={dirtyMap.cooldownSeconds}
          helper="Same insight on the same resource won't notify again within this window. 3600 (1h) is the default."
        >
          <input
            type="number"
            min={0}
            className="w-40 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.cooldownSeconds}
            onChange={(e) => setForm({ ...form, cooldownSeconds: e.target.value })}
          />
        </Field>

        <Field
          label="Base URL"
          dirty={dirtyMap.baseURL}
          helper="Optional. Appears as a clickable link in every message — typically your KubeBolt UI's external URL."
        >
          <input
            type="text"
            placeholder="https://kubebolt.example.com"
            className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
            value={form.baseURL}
            onChange={(e) => setForm({ ...form, baseURL: e.target.value })}
          />
        </Field>

        <label className="flex items-start gap-2 text-xs text-kb-text-secondary cursor-pointer">
          <input
            type="checkbox"
            checked={form.includeResolved}
            onChange={(e) => setForm({ ...form, includeResolved: e.target.checked })}
            className="accent-kb-accent mt-0.5"
          />
          <div>
            <div className="flex items-center gap-2">
              <div className="text-kb-text-primary">Notify on resolved insights</div>
              {dirtyMap.includeResolved && <UnsavedChip />}
            </div>
            <div className="text-kb-text-tertiary">
              Also send a message when an active insight clears. Off: only new detections trigger.
            </div>
          </div>
        </label>
      </SectionCard>

      {/* Slack */}
      <ChannelCard
        title="Slack"
        icon={<SlackIcon />}
        configured={eff.slackConfigured}
        enabled={form.slackEnabled}
        enabledDirty={dirtyMap.slackEnabled}
        onToggleEnabled={(v) => setForm({ ...form, slackEnabled: v })}
        // Gate derives from FORM state so chip + button always agree.
        // !isDirty ensures the form matches the saved state — when
        // that holds, form.slackEnabled IS what the live notifier
        // respects. Reading server state (eff.slackActive) was racy:
        // it lags behind by one query refetch after save.
        canTest={form.slackEnabled && eff.slackConfigured && !isDirty}
        onTest={() => api.testNotification('slack').then(() => undefined)}
      >
        <Field
          label="Webhook URL"
          dirty={dirtyMap.slackWebhookURL}
          helper={
            eff.slackWebhookMasked
              ? `Currently set: ${eff.slackWebhookMasked}. Leave blank to keep, or paste a new URL to replace.`
              : 'Paste an incoming-webhook URL from your Slack app to enable delivery.'
          }
        >
          <SecretInput
            value={form.slackWebhookURL}
            onChange={(v) => setForm({ ...form, slackWebhookURL: v })}
            reveal={revealSlack}
            onToggleReveal={() => setRevealSlack((v) => !v)}
            placeholder={eff.slackWebhookMasked ? '••••••••' : 'https://hooks.slack.com/services/...'}
          />
        </Field>
      </ChannelCard>

      {/* Discord */}
      <ChannelCard
        title="Discord"
        icon={<DiscordIcon />}
        configured={eff.discordConfigured}
        enabled={form.discordEnabled}
        enabledDirty={dirtyMap.discordEnabled}
        onToggleEnabled={(v) => setForm({ ...form, discordEnabled: v })}
        canTest={form.discordEnabled && eff.discordConfigured && !isDirty}
        onTest={() => api.testNotification('discord').then(() => undefined)}
      >
        <Field
          label="Webhook URL"
          dirty={dirtyMap.discordWebhookURL}
          helper={
            eff.discordWebhookMasked
              ? `Currently set: ${eff.discordWebhookMasked}. Leave blank to keep, or paste a new URL to replace.`
              : 'Paste an incoming-webhook URL from your Discord channel integration to enable delivery.'
          }
        >
          <SecretInput
            value={form.discordWebhookURL}
            onChange={(v) => setForm({ ...form, discordWebhookURL: v })}
            reveal={revealDiscord}
            onToggleReveal={() => setRevealDiscord((v) => !v)}
            placeholder={eff.discordWebhookMasked ? '••••••••' : 'https://discord.com/api/webhooks/...'}
          />
        </Field>
      </ChannelCard>

      {/* Email */}
      <ChannelCard
        title="Email (SMTP)"
        icon={<Mail className="w-4 h-4 text-kb-text-primary" />}
        configured={eff.emailConfigured}
        enabled={form.emailEnabled}
        enabledDirty={dirtyMap.emailEnabled}
        onToggleEnabled={(v) => setForm({ ...form, emailEnabled: v })}
        canTest={form.emailEnabled && eff.emailConfigured && !isDirty}
        onTest={() => api.testNotification('email').then(() => undefined)}
      >
        <div className="grid grid-cols-2 gap-4">
          <Field label="Host" dirty={dirtyMap.emailHost} helper="SMTP server hostname.">
            <input
              type="text"
              placeholder="smtp.example.com"
              className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
              value={form.emailHost}
              onChange={(e) => setForm({ ...form, emailHost: e.target.value })}
            />
          </Field>
          <Field label="Port" dirty={dirtyMap.emailPort} helper="587 (STARTTLS) is the typical default.">
            <input
              type="number"
              min={1}
              max={65535}
              className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
              value={form.emailPort}
              onChange={(e) => setForm({ ...form, emailPort: e.target.value })}
            />
          </Field>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <Field label="Username" dirty={dirtyMap.emailUsername} helper="SMTP-AUTH username. Optional if your SMTP allows unauthenticated relay.">
            <input
              type="text"
              className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
              value={form.emailUsername}
              onChange={(e) => setForm({ ...form, emailUsername: e.target.value })}
            />
          </Field>
          <Field
            label="Password"
            dirty={dirtyMap.emailPassword}
            helper={
              eff.emailPasswordMasked
                ? `Currently set: ${eff.emailPasswordMasked}. Leave blank to keep, or type to replace.`
                : 'Optional. Leave blank for unauthenticated SMTP.'
            }
          >
            <SecretInput
              value={form.emailPassword}
              onChange={(v) => setForm({ ...form, emailPassword: v })}
              reveal={revealSMTPPassword}
              onToggleReveal={() => setRevealSMTPPassword((v) => !v)}
              placeholder={eff.emailPasswordMasked ? '••••••••' : ''}
            />
          </Field>
        </div>

        <Field
          label="From"
          dirty={dirtyMap.emailFrom}
          helper={
            // Two accepted shapes — plain address OR display-name form.
            // The display name is what recipients see as the sender in
            // their inbox; useful when alerts@ is a generic alias.
            'Accepts a plain address (alerts@example.com) or a display-name form (KubeBolt Alerts <alerts@example.com>).'
          }
        >
          {/* type="text" not type="email" — browser email validation
              rejects the display-name shape, but the backend's
              net/mail.ParseAddress accepts both per RFC 5322. */}
          <input
            type="text"
            placeholder="KubeBolt Alerts <alerts@example.com>"
            className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
            value={form.emailFrom}
            onChange={(e) => setForm({ ...form, emailFrom: e.target.value })}
          />
        </Field>

        <Field
          label="Recipients"
          dirty={dirtyMap.emailTo}
          helper="Comma-separated. Each entry can be a plain address or a display-name form (e.g. 'SRE Oncall <oncall@example.com>'). Every recipient gets every alert."
        >
          <input
            type="text"
            placeholder="oncall@example.com, sre@example.com"
            className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
            value={form.emailTo}
            onChange={(e) => setForm({ ...form, emailTo: e.target.value })}
          />
        </Field>

        <Field
          label="Digest mode"
          dirty={dirtyMap.emailDigestMode}
          helper="Instant: one email per insight. Hourly/daily: insights are buffered and sent as a single summary email."
        >
          <select
            className="w-full max-w-md px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.emailDigestMode}
            onChange={(e) => setForm({ ...form, emailDigestMode: e.target.value })}
          >
            <option value="instant">Instant</option>
            <option value="hourly">Hourly digest</option>
            <option value="daily">Daily digest</option>
          </select>
        </Field>
      </ChannelCard>

      {/* Action card with banners */}
      <div className="bg-kb-card border border-kb-border rounded-xl">
        <div className="p-3 flex items-center justify-between gap-3">
          <button
            type="button"
            onClick={() => setResetConfirmOpen(true)}
            disabled={resetMutation.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated disabled:opacity-50"
          >
            <RotateCcw className="w-3.5 h-3.5" />
            {resetMutation.isPending ? 'Resetting…' : 'Reset to env defaults'}
          </button>
          <div className="flex items-center gap-2">
            {isDirty && !saveMutation.isPending && (
              <button
                type="button"
                onClick={() => {
                  setForm(initial)
                  setRevealSlack(false)
                  setRevealDiscord(false)
                  setRevealSMTPPassword(false)
                }}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border"
              >
                <X className="w-3.5 h-3.5" />
                Cancel
              </button>
            )}
            <button
              type="submit"
              disabled={!isDirty || saveMutation.isPending}
              className="flex items-center gap-1.5 px-4 py-1.5 rounded-md text-xs font-medium bg-kb-accent text-kb-bg disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {saveMutation.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Save className="w-3.5 h-3.5" />}
              {saveMutation.isPending ? 'Saving…' : 'Save changes'}
            </button>
          </div>
        </div>

        {saveMutation.isError && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <div>{(saveMutation.error as Error)?.message || 'Failed to save.'}</div>
          </div>
        )}

        {savedAt && !isDirty && !saveMutation.isPending && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">
            <CheckCircle2 className="w-4 h-4 mt-0.5 shrink-0" />
            <div>Notifications saved. Channels and global thresholds picked up the new values without restart.</div>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={resetConfirmOpen}
        badge="Reset"
        variant="danger"
        title="Reset Notifications to env defaults?"
        description={
          <>Clears every UI-configured value, <strong className="text-kb-text-primary">including the Slack and Discord webhook URLs and the SMTP password</strong>. The next read falls back to env vars only. You'll need to re-paste secrets to restore delivery.</>
        }
        confirmLabel="Reset"
        onConfirm={() => {
          setResetConfirmOpen(false)
          resetMutation.mutate()
        }}
        onCancel={() => setResetConfirmOpen(false)}
        busy={resetMutation.isPending}
      />
    </form>
  )
}

// ─── Shared form primitives ────────────────────────────────────────────

function Field({
  label,
  helper,
  dirty,
  children,
}: {
  label: string
  helper?: string
  dirty?: boolean
  children: React.ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <label className="block text-[11px] font-semibold text-kb-text-primary uppercase tracking-wider">
          {label}
        </label>
        {dirty && <UnsavedChip />}
      </div>
      {children}
      {helper && <p className="text-[11px] text-kb-text-tertiary leading-relaxed">{helper}</p>}
    </div>
  )
}

function UnsavedChip() {
  return (
    <span className="text-[10px] font-mono font-medium uppercase tracking-wider text-status-warn">
      Unsaved
    </span>
  )
}

function SecretInput({
  value,
  onChange,
  reveal,
  onToggleReveal,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  reveal: boolean
  onToggleReveal: () => void
  placeholder?: string
}) {
  return (
    <div className="relative">
      <input
        type={reveal ? 'text' : 'password'}
        placeholder={placeholder}
        autoComplete="off"
        className="w-full pl-2 pr-9 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <button
        type="button"
        onClick={onToggleReveal}
        className="absolute right-1.5 top-1/2 -translate-y-1/2 p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary"
        aria-label={reveal ? 'Hide secret' : 'Show secret'}
      >
        {reveal ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
      </button>
    </div>
  )
}

function SectionCard({
  icon,
  title,
  subtitle,
  headerRight,
  children,
}: {
  icon?: React.ReactNode
  title: string
  subtitle?: string
  headerRight?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="bg-kb-card border border-kb-border rounded-xl">
      <header className="flex items-start justify-between gap-3 px-5 py-4 border-b border-kb-border">
        <div className="flex items-start gap-2 min-w-0">
          {icon && <div className="mt-0.5 shrink-0">{icon}</div>}
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-kb-text-primary">{title}</h2>
            {subtitle && (
              <p className="text-[11px] text-kb-text-tertiary mt-0.5 leading-snug">{subtitle}</p>
            )}
          </div>
        </div>
        {headerRight && <div className="shrink-0">{headerRight}</div>}
      </header>
      <div className="px-5 py-4 space-y-4">{children}</div>
    </section>
  )
}

// ChannelCard wraps SectionCard with the channel-specific bits:
// a tri-state status chip + an "Enable" toggle in the header and an
// inline "Send test" button at the bottom. Tri-state semantics:
//   - !configured       → "Not configured" (gray)
//   - configured && !enabled → "Paused" (gray, but toggle on can re-enable)
//   - configured && enabled  → "Enabled" (green)
//
// The test button is gated on canTest (= configured && enabled && !isDirty)
// because the live notifier (a) doesn't exist when paused, and (b) uses
// the last-saved credentials, not unsaved form values.
function ChannelCard({
  title,
  icon,
  configured,
  enabled,
  enabledDirty,
  onToggleEnabled,
  canTest,
  onTest,
  children,
}: {
  title: string
  icon: React.ReactNode
  configured: boolean
  enabled: boolean
  enabledDirty: boolean
  onToggleEnabled: (next: boolean) => void
  canTest: boolean
  onTest: () => Promise<void>
  children: React.ReactNode
}) {
  const [status, setStatus] = useState<'idle' | 'sending' | 'sent' | 'error'>('idle')
  const [error, setError] = useState<string | null>(null)

  async function handleTest() {
    setStatus('sending')
    setError(null)
    try {
      await onTest()
      setStatus('sent')
      setTimeout(() => setStatus('idle'), 3000)
    } catch (err) {
      setStatus('error')
      setError(err instanceof Error ? err.message : 'Test notification failed')
    }
  }

  // Tri-state chip
  let chipLabel = 'Not configured'
  let chipClass = 'bg-kb-elevated text-kb-text-tertiary'
  if (configured && enabled) {
    chipLabel = 'Enabled'
    chipClass = 'bg-status-ok-dim text-status-ok'
  } else if (configured && !enabled) {
    chipLabel = 'Paused'
    chipClass = 'bg-status-warn-dim text-status-warn'
  }

  return (
    <SectionCard
      icon={icon}
      title={title}
      headerRight={
        <div className="flex items-center gap-3 shrink-0">
          {/* Per-channel enable toggle. Always interactive — even when
              not configured the operator may want to pre-toggle off so
              that filling in the webhook URL later doesn't immediately
              start dispatching (a small "configure quietly first" win). */}
          <label className="flex items-center gap-1.5 text-[11px] text-kb-text-secondary cursor-pointer">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => onToggleEnabled(e.target.checked)}
              className="accent-kb-accent"
            />
            Enabled
            {enabledDirty && <UnsavedChip />}
          </label>
          <span
            className={`px-2 py-0.5 rounded-full text-[10px] font-mono font-semibold uppercase tracking-wider ${chipClass}`}
          >
            {chipLabel}
          </span>
        </div>
      }
    >
      {children}

      <div className="pt-2 border-t border-kb-border flex items-center gap-2">
        <button
          type="button"
          onClick={handleTest}
          disabled={!canTest || status === 'sending'}
          title={
            !canTest
              ? 'Save your changes first — testing uses the live notifier, not the unsaved form values.'
              : 'Send a synthetic test notification through this channel.'
          }
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {status === 'sending' ? (
            <>
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
              Sending…
            </>
          ) : status === 'sent' ? (
            <>
              <Check className="w-3.5 h-3.5 text-status-ok" />
              Sent
            </>
          ) : (
            <>
              <Send className="w-3.5 h-3.5" />
              Send test notification
            </>
          )}
        </button>
        {status === 'error' && error && (
          <span className="text-[11px] text-status-error">{error}</span>
        )}
      </div>
    </SectionCard>
  )
}

function SlackIcon() {
  // Brand-ish glyph, not the official trademark (same approach as the
  // retired NotificationsPage).
  return (
    <svg viewBox="0 0 24 24" className="w-4 h-4 text-kb-text-primary" fill="currentColor">
      <path d="M5.04 15.165c0 1.388-1.122 2.51-2.51 2.51a2.508 2.508 0 0 1-2.51-2.51c0-1.388 1.122-2.51 2.51-2.51h2.51v2.51zm1.266 0c0-1.388 1.122-2.51 2.51-2.51s2.51 1.122 2.51 2.51v6.284c0 1.388-1.122 2.51-2.51 2.51a2.508 2.508 0 0 1-2.51-2.51v-6.284zM8.816 5.063c-1.388 0-2.51-1.122-2.51-2.51S7.428.044 8.816.044s2.51 1.122 2.51 2.51v2.51h-2.51zm0 1.266c1.388 0 2.51 1.122 2.51 2.51s-1.122 2.51-2.51 2.51H2.531a2.508 2.508 0 0 1-2.51-2.51c0-1.388 1.122-2.51 2.51-2.51h6.284zm10.102 2.51c0-1.388 1.122-2.51 2.51-2.51s2.51 1.122 2.51 2.51-1.122 2.51-2.51 2.51h-2.51V8.839zm-1.266 0c0 1.388-1.122 2.51-2.51 2.51s-2.51-1.122-2.51-2.51V2.554c0-1.388 1.122-2.51 2.51-2.51s2.51 1.122 2.51 2.51v6.284zm-2.51 10.102c1.388 0 2.51 1.122 2.51 2.51s-1.122 2.51-2.51 2.51-2.51-1.122-2.51-2.51v-2.51h2.51zm0-1.266c-1.388 0-2.51-1.122-2.51-2.51s1.122-2.51 2.51-2.51h6.284c1.388 0 2.51 1.122 2.51 2.51s-1.122 2.51-2.51 2.51h-6.284z" />
    </svg>
  )
}

function DiscordIcon() {
  return (
    <svg viewBox="0 0 24 24" className="w-4 h-4 text-kb-text-primary" fill="currentColor">
      <path d="M20.317 4.37a19.791 19.791 0 0 0-4.885-1.515.074.074 0 0 0-.079.037c-.21.375-.444.864-.608 1.25a18.27 18.27 0 0 0-5.487 0 12.64 12.64 0 0 0-.617-1.25.077.077 0 0 0-.079-.037A19.736 19.736 0 0 0 3.677 4.37a.07.07 0 0 0-.032.027C.533 9.046-.32 13.58.099 18.057a.082.082 0 0 0 .031.057 19.9 19.9 0 0 0 5.993 3.03.078.078 0 0 0 .084-.028 14.09 14.09 0 0 0 1.226-1.994.076.076 0 0 0-.041-.106 13.107 13.107 0 0 1-1.872-.892.077.077 0 0 1-.008-.128 10.2 10.2 0 0 0 .372-.292.074.074 0 0 1 .077-.01c3.928 1.793 8.18 1.793 12.062 0a.074.074 0 0 1 .078.01c.12.098.246.198.373.292a.077.077 0 0 1-.006.127 12.299 12.299 0 0 1-1.873.892.077.077 0 0 0-.041.107c.36.698.772 1.362 1.225 1.993a.076.076 0 0 0 .084.028 19.839 19.839 0 0 0 6.002-3.03.077.077 0 0 0 .032-.054c.5-5.177-.838-9.674-3.549-13.66a.061.061 0 0 0-.031-.03zM8.02 15.33c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.956-2.419 2.157-2.419 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.956 2.418-2.157 2.418zm7.975 0c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.955-2.419 2.157-2.419 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.946 2.418-2.157 2.418z" />
    </svg>
  )
}
