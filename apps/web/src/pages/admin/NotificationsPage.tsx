import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Bell, Send, Check, AlertTriangle, Info, Mail } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'

function ChannelIcon({ name }: { name: string }) {
  // Simple brand-ish glyphs — not the official logos to avoid trademark issues
  if (name === 'email') {
    return <Mail className="w-5 h-5" />
  }
  if (name === 'slack') {
    return (
      <svg viewBox="0 0 24 24" className="w-5 h-5" fill="currentColor">
        <path d="M5.04 15.165c0 1.388-1.122 2.51-2.51 2.51a2.508 2.508 0 0 1-2.51-2.51c0-1.388 1.122-2.51 2.51-2.51h2.51v2.51zm1.266 0c0-1.388 1.122-2.51 2.51-2.51s2.51 1.122 2.51 2.51v6.284c0 1.388-1.122 2.51-2.51 2.51a2.508 2.508 0 0 1-2.51-2.51v-6.284zM8.816 5.063c-1.388 0-2.51-1.122-2.51-2.51S7.428.044 8.816.044s2.51 1.122 2.51 2.51v2.51h-2.51zm0 1.266c1.388 0 2.51 1.122 2.51 2.51s-1.122 2.51-2.51 2.51H2.531a2.508 2.508 0 0 1-2.51-2.51c0-1.388 1.122-2.51 2.51-2.51h6.284zm10.102 2.51c0-1.388 1.122-2.51 2.51-2.51s2.51 1.122 2.51 2.51-1.122 2.51-2.51 2.51h-2.51V8.839zm-1.266 0c0 1.388-1.122 2.51-2.51 2.51s-2.51-1.122-2.51-2.51V2.554c0-1.388 1.122-2.51 2.51-2.51s2.51 1.122 2.51 2.51v6.284zm-2.51 10.102c1.388 0 2.51 1.122 2.51 2.51s-1.122 2.51-2.51 2.51-2.51-1.122-2.51-2.51v-2.51h2.51zm0-1.266c-1.388 0-2.51-1.122-2.51-2.51s1.122-2.51 2.51-2.51h6.284c1.388 0 2.51 1.122 2.51 2.51s-1.122 2.51-2.51 2.51h-6.284z" />
      </svg>
    )
  }
  if (name === 'discord') {
    return (
      <svg viewBox="0 0 24 24" className="w-5 h-5" fill="currentColor">
        <path d="M20.317 4.37a19.791 19.791 0 0 0-4.885-1.515.074.074 0 0 0-.079.037c-.21.375-.444.864-.608 1.25a18.27 18.27 0 0 0-5.487 0 12.64 12.64 0 0 0-.617-1.25.077.077 0 0 0-.079-.037A19.736 19.736 0 0 0 3.677 4.37a.07.07 0 0 0-.032.027C.533 9.046-.32 13.58.099 18.057a.082.082 0 0 0 .031.057 19.9 19.9 0 0 0 5.993 3.03.078.078 0 0 0 .084-.028 14.09 14.09 0 0 0 1.226-1.994.076.076 0 0 0-.041-.106 13.107 13.107 0 0 1-1.872-.892.077.077 0 0 1-.008-.128 10.2 10.2 0 0 0 .372-.292.074.074 0 0 1 .077-.01c3.928 1.793 8.18 1.793 12.062 0a.074.074 0 0 1 .078.01c.12.098.246.198.373.292a.077.077 0 0 1-.006.127 12.299 12.299 0 0 1-1.873.892.077.077 0 0 0-.041.107c.36.698.772 1.362 1.225 1.993a.076.076 0 0 0 .084.028 19.839 19.839 0 0 0 6.002-3.03.077.077 0 0 0 .032-.054c.5-5.177-.838-9.674-3.549-13.66a.061.061 0 0 0-.031-.03zM8.02 15.33c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.956-2.419 2.157-2.419 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.956 2.418-2.157 2.418zm7.975 0c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.955-2.419 2.157-2.419 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.946 2.418-2.157 2.418z" />
      </svg>
    )
  }
  return <Bell className="w-5 h-5" />
}

function SeverityBadge({ severity }: { severity: string }) {
  const styles: Record<string, { bg: string; color: string; Icon: typeof AlertTriangle }> = {
    critical: { bg: 'bg-status-error-dim', color: 'text-status-error', Icon: AlertTriangle },
    warning:  { bg: 'bg-status-warn-dim',  color: 'text-status-warn',  Icon: AlertTriangle },
    info:     { bg: 'bg-status-info-dim',  color: 'text-status-info',  Icon: Info },
  }
  const style = styles[severity] || styles.warning
  const Icon = style.Icon
  return (
    <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full ${style.bg} ${style.color} text-[10px] font-mono font-semibold uppercase tracking-wider`}>
      <Icon className="w-3 h-3" />
      {severity}
    </span>
  )
}

// Per-channel env var hint shown when the channel is disabled.
// Email has many vars; we point users at the SMTP_* namespace instead.
function envHint(name: string): React.ReactNode {
  if (name === 'email') {
    return (
      <>Set <code className="text-kb-accent font-mono">KUBEBOLT_SMTP_HOST</code>, <code className="text-kb-accent font-mono">KUBEBOLT_SMTP_FROM</code>, and <code className="text-kb-accent font-mono">KUBEBOLT_SMTP_TO</code> in your environment (or <code className="text-kb-accent font-mono">.env</code> file) to enable email notifications.</>
    )
  }
  return (
    <>Set <code className="text-kb-accent font-mono">KUBEBOLT_{name.toUpperCase()}_WEBHOOK_URL</code> in your environment (or <code className="text-kb-accent font-mono">.env</code> file) to enable this channel.</>
  )
}

function configuredHint(name: string): React.ReactNode {
  if (name === 'email') {
    return <>Insight alerts are delivered by email. Change SMTP settings via the <code className="text-kb-accent font-mono">KUBEBOLT_SMTP_*</code> environment variables.</>
  }
  return <>Insight alerts are delivered to this channel. Set <code className="text-kb-accent font-mono">KUBEBOLT_{name.toUpperCase()}_WEBHOOK_URL</code> to change the target.</>
}

function ChannelCard({ name, enabled, digestMode, onTest }: { name: string; enabled: boolean; digestMode?: string; onTest: () => Promise<void> }) {
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

  const title = name.charAt(0).toUpperCase() + name.slice(1)

  return (
    <div className="bg-kb-card border border-kb-border rounded-xl p-5">
      <div className="flex items-start justify-between mb-4 gap-2">
        <div className="flex items-center gap-3 min-w-0">
          <div className={`w-10 h-10 rounded-lg flex items-center justify-center shrink-0 ${enabled ? 'bg-kb-accent-light text-kb-accent' : 'bg-kb-elevated text-kb-text-tertiary'}`}>
            <ChannelIcon name={name} />
          </div>
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="text-sm font-semibold text-kb-text-primary">{title}</span>
              {enabled && digestMode && (
                <span className="px-1.5 py-0.5 rounded-full bg-status-info-dim text-status-info text-[9px] font-mono font-semibold uppercase tracking-wider">
                  {digestMode}
                </span>
              )}
            </div>
            <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">
              {enabled ? 'Configured' : 'Not configured'}
            </div>
          </div>
        </div>
        <span className={`px-2 py-0.5 rounded-full text-[10px] font-mono font-semibold uppercase tracking-wider shrink-0 ${
          enabled ? 'bg-status-ok-dim text-status-ok' : 'bg-kb-elevated text-kb-text-tertiary'
        }`}>
          {enabled ? 'Enabled' : 'Disabled'}
        </span>
      </div>

      {enabled ? (
        <>
          <p className="text-[11px] text-kb-text-secondary leading-relaxed mb-4">
            {configuredHint(name)}
          </p>
          {name === 'email' && digestMode && digestMode !== 'instant' && (
            <div className="mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-info-dim">
              <Info className="w-3.5 h-3.5 text-status-info shrink-0 mt-0.5" />
              <span className="text-[11px] text-status-info">
                Digest mode <strong>{digestMode}</strong>: insights are buffered and sent as a single summary email every {digestMode === 'hourly' ? 'hour' : '24 hours'}.
              </span>
            </div>
          )}
          <div className="flex items-center gap-2">
            <button
              onClick={handleTest}
              disabled={status === 'sending'}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-xs text-kb-text-primary border border-kb-border transition-colors disabled:opacity-50"
            >
              {status === 'sending' ? (
                <>
                  <div className="w-3 h-3 border-2 border-kb-text-tertiary border-t-transparent rounded-full animate-spin" />
                  Sending...
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
          </div>
          {status === 'error' && error && (
            <div className="mt-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim">
              <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
              <span className="text-[11px] text-status-error">{error}</span>
            </div>
          )}
        </>
      ) : (
        <div className="text-[11px] text-kb-text-secondary leading-relaxed">
          {envHint(name)}
        </div>
      )}
    </div>
  )
}

export function NotificationsPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['notifications-config'],
    queryFn: api.getNotificationsConfig,
  })

  if (isLoading) {
    return (
      <div className="p-6 flex justify-center"><LoadingSpinner /></div>
    )
  }

  if (error || !data) {
    return (
      <div className="p-6 max-w-3xl">
        <div className="flex items-start gap-2 px-4 py-3 rounded-lg bg-status-error-dim">
          <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
          <span className="text-sm text-status-error">Failed to load notifications config</span>
        </div>
      </div>
    )
  }

  return (
    <div className="p-6 max-w-4xl">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <Bell className="w-5 h-5" />
            Notifications
          </h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5">
            Send insight alerts to external chat channels via webhooks.
          </p>
        </div>
      </div>

      {/* Global config */}
      <div className="bg-kb-surface border border-kb-border rounded-xl p-5 mb-6">
        <h2 className="text-xs font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider mb-3">Global Settings</h2>
        <div className="grid grid-cols-2 gap-6">
          <div>
            <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">Minimum Severity</div>
            <div className="flex items-center gap-2">
              <SeverityBadge severity={data.minSeverity} />
              <span className="text-[11px] text-kb-text-secondary">and above trigger notifications</span>
            </div>
          </div>
          <div>
            <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">Cooldown</div>
            <div className="flex items-center gap-2">
              <span className="text-xs font-mono text-kb-text-primary px-2 py-0.5 rounded-md bg-kb-elevated border border-kb-border">{data.cooldown}</span>
              <span className="text-[11px] text-kb-text-secondary">between same insights</span>
            </div>
          </div>
        </div>
        <p className="text-[10px] text-kb-text-tertiary mt-4 font-mono">
          Configure via <code className="text-kb-accent">KUBEBOLT_NOTIFICATIONS_MIN_SEVERITY</code> and <code className="text-kb-accent">KUBEBOLT_NOTIFICATIONS_COOLDOWN</code>
        </p>
      </div>

      {/* Channel cards */}
      <h2 className="text-xs font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider mb-3">Channels</h2>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        {data.channels.map((ch) => (
          <ChannelCard
            key={ch.name}
            name={ch.name}
            enabled={ch.enabled}
            digestMode={ch.digestMode}
            onTest={() => api.testNotification(ch.name as 'slack' | 'discord' | 'email').then(() => {})}
          />
        ))}
      </div>
    </div>
  )
}
