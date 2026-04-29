import type { MouseEvent } from 'react'
import { useCopilot } from '@/contexts/CopilotContext'
import { KobiSigil } from '@/components/kobi'
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
 * (`config.enabled === false`) — dead-ends hurt trust.
 */
export function AskCopilotButton({
  payload,
  variant = 'icon',
  label = 'Ask Kobi',
  className = '',
  onAfterSend,
}: AskCopilotButtonProps) {
  const { config, openPanel, sendMessage } = useCopilot()

  if (!config?.enabled) return null

  function handleClick(e: MouseEvent<HTMLButtonElement>) {
    e.stopPropagation()
    const prompt = buildTriggerPrompt(payload)
    openPanel()
    void sendMessage(prompt, { trigger: payload.type })
    onAfterSend?.()
  }

  if (variant === 'text') {
    return (
      <button
        type="button"
        onClick={handleClick}
        title={label}
        className={`group relative inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[11px] font-medium text-kb-accent bg-gradient-to-r from-kb-accent-light via-kb-accent-light to-violet-500/10 ring-1 ring-kb-accent/25 shadow-[0_0_0_0_rgba(29,189,125,0)] hover:ring-kb-accent/50 hover:shadow-[0_0_14px_rgba(29,189,125,0.35)] hover:-translate-y-[0.5px] active:translate-y-0 transition-all duration-200 overflow-hidden ${className}`}
      >
        <span className="relative flex items-center">
          <span className="absolute inset-0 rounded-full bg-kb-accent/60 blur-[3px] animate-ping opacity-60" aria-hidden />
          <KobiSigil state="static" inheritColor size={14} className="relative" />
        </span>
        <span className="relative bg-gradient-to-r from-kb-accent to-violet-400 bg-clip-text text-transparent font-semibold">
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
      className={`group relative inline-flex items-center justify-center w-6 h-6 rounded-md text-kb-accent bg-gradient-to-br from-kb-accent-light to-violet-500/10 ring-1 ring-kb-accent/20 hover:ring-kb-accent/50 hover:scale-105 hover:shadow-[0_0_12px_rgba(29,189,125,0.35)] active:scale-95 transition-all duration-200 ${className}`}
    >
      <KobiSigil state="static" inheritColor size={14} className="transition-transform duration-200 group-hover:rotate-[8deg]" />
    </button>
  )
}
