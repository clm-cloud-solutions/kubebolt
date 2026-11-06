import { useState, type MouseEvent } from 'react'
import { useCopilot } from '@/contexts/CopilotContext'
import { KobiFace } from '@/components/kobi'
import { buildTriggerPrompt, type CopilotTriggerPayload } from '@/services/copilot/triggers'

interface AskCopilotButtonProps {
  payload: CopilotTriggerPayload
  /** 'icon' renders a compact icon-only button with tooltip; 'text' adds a label. */
  variant?: 'icon' | 'text'
  /** Override label for the text variant and the tooltip. Defaults to "Ask Kobi". */
  label?: string
  className?: string
  /** Fired after the message is dispatched. Hosts use it to close
   *  the transient UI (hover tooltip, popover) that launched the
   *  button — the user's attention is on the Kobi panel now. */
  onAfterSend?: () => void
}

/**
 * AskCopilotButton launches the Kobi panel with a pre-loaded, context-aware
 * prompt built from the payload. Invisible when Kobi is not configured
 * (`config.enabled === false`) — dead-ends hurt trust, and it never needs to
 * show Unavailable.
 *
 * It wears Kobi's face, still: 24 px (S) as the icon button, 16 px (XS) in
 * the text button. While the cursor or the keyboard focus is on it the face
 * wakes up Watching — the dot pulses and the eyes blink — and never shows
 * the live state, which belongs to the floating head and the panel header.
 */
export function AskCopilotButton({
  payload,
  variant = 'icon',
  label = 'Ask Kobi',
  className = '',
  onAfterSend,
}: AskCopilotButtonProps) {
  const { config, openPanel, sendMessage } = useCopilot()
  const [awake, setAwake] = useState(false)

  if (!config?.enabled) return null

  const wake = {
    onMouseEnter: () => setAwake(true),
    onMouseLeave: () => setAwake(false),
    onFocus: () => setAwake(true),
    onBlur: () => setAwake(false),
  }

  function handleClick(e: MouseEvent<HTMLButtonElement>) {
    e.stopPropagation()
    const prompt = buildTriggerPrompt(payload)
    openPanel()
    // When launched from an insight, stamp the conversation with the insight's
    // stable fingerprint so it's attributable back (and the insight detail can
    // deep-link "Kobi analyzed this" to resume).
    const originatingInsightId = payload.type === 'insight' ? payload.insight.fingerprint : undefined
    void sendMessage(prompt, { trigger: payload.type, originatingInsightId })
    onAfterSend?.()
  }

  if (variant === 'text') {
    return (
      <button
        type="button"
        onClick={handleClick}
        title={label}
        {...wake}
        className={`group relative inline-flex items-center gap-1.5 pl-2 pr-2.5 py-1 rounded-md text-[11px] font-medium text-kobi-accent bg-kobi-accent-light ring-1 ring-kobi-accent/25 shadow-[0_0_0_0_rgba(0,224,122,0)] hover:ring-kobi-accent/50 hover:shadow-[0_0_14px_rgba(0,224,122,0.30)] hover:-translate-y-[0.5px] active:translate-y-0 transition-all duration-200 overflow-hidden ${className}`}
      >
        <KobiFace size={16} still={!awake} eager={awake} className="relative" />
        <span className="relative text-kobi-accent font-semibold">
          {label}
        </span>
        <span
          aria-hidden
          className="pointer-events-none absolute inset-0 -translate-x-full bg-gradient-to-r from-transparent via-white/25 to-transparent group-hover:translate-x-full transition-transform duration-700 ease-out"
        />
      </button>
    )
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      title={label}
      aria-label={label}
      {...wake}
      className={`relative inline-flex items-center justify-center w-6 h-6 rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-kobi-accent/60 active:scale-95 transition-transform duration-200 ${className}`}
    >
      {/* No tile: the face's shell is its own container. */}
      <KobiFace size={24} still={!awake} eager={awake} />
    </button>
  )
}
