import { useState } from 'react'
import { Bot } from 'lucide-react'
import { useCopilot } from '@/contexts/CopilotContext'

export function CopilotToggle() {
  const { config, isOpen, togglePanel } = useCopilot()
  const [hovered, setHovered] = useState(false)

  // Hide entirely when copilot isn't enabled on the backend
  if (!config?.enabled) return null
  if (isOpen) return null

  return (
    <div
      className="fixed bottom-5 right-5 z-[250] flex items-center gap-2"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      {/* Tooltip — appears to the left of the button on hover */}
      <div
        className={`bg-kb-card border border-kb-border rounded-lg px-3 py-2 shadow-lg pointer-events-none transition-all duration-150 ${
          hovered ? 'opacity-100 translate-x-0' : 'opacity-0 translate-x-2'
        }`}
      >
        <div className="text-xs font-semibold text-kb-text-primary leading-tight whitespace-nowrap">
          KubeBolt Copilot AI
        </div>
        <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5 whitespace-nowrap">
          Ask anything about your cluster · ⌘J
        </div>
      </div>

      <button
        onClick={togglePanel}
        aria-label="Open KubeBolt Copilot AI"
        className="w-12 h-12 rounded-full bg-kb-accent hover:scale-110 active:scale-95 transition-transform shadow-lg shadow-kb-accent/30 flex items-center justify-center"
      >
        <Bot className="w-5 h-5 text-white" />
      </button>
    </div>
  )
}
