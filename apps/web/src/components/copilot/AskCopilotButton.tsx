import { Bot } from 'lucide-react'
import type { MouseEvent } from 'react'
import { useCopilot } from '@/contexts/CopilotContext'
import { buildTriggerPrompt, type CopilotTriggerPayload } from '@/services/copilot/triggers'

interface AskCopilotButtonProps {
  payload: CopilotTriggerPayload
  /** 'icon' renders a compact icon-only button with tooltip; 'text' adds a label. */
  variant?: 'icon' | 'text'
  /** Override label for the text variant and the tooltip. Defaults to "Ask Copilot". */
  label?: string
  className?: string
}

/**
 * AskCopilotButton launches the Copilot panel with a pre-loaded, context-aware
 * prompt built from the payload. Invisible when the Copilot is not configured
 * (`config.enabled === false`) — dead-ends hurt trust.
 */
export function AskCopilotButton({
  payload,
  variant = 'icon',
  label = 'Ask Copilot',
  className = '',
}: AskCopilotButtonProps) {
  const { config, openPanel, sendMessage } = useCopilot()

  if (!config?.enabled) return null

  function handleClick(e: MouseEvent<HTMLButtonElement>) {
    e.stopPropagation()
    const prompt = buildTriggerPrompt(payload)
    openPanel()
    void sendMessage(prompt, { trigger: payload.type })
  }

  if (variant === 'text') {
    return (
      <button
        type="button"
        onClick={handleClick}
        title={label}
        className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[11px] font-medium bg-kb-accent-light text-kb-accent hover:bg-kb-accent/20 transition-colors ${className}`}
      >
        <Bot className="w-3.5 h-3.5" />
        {label}
      </button>
    )
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      title={label}
      aria-label={label}
      className={`inline-flex items-center justify-center w-6 h-6 rounded-md text-kb-text-tertiary hover:text-kb-accent hover:bg-kb-accent-light transition-colors ${className}`}
    >
      <Bot className="w-3.5 h-3.5" />
    </button>
  )
}
