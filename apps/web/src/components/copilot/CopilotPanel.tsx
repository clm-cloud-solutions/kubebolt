import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import {
  Bot,
  X,
  Send,
  Trash2,
  Loader2,
  AlertCircle,
  Wrench,
  PanelRightClose,
  PanelRightOpen,
  Copy,
  Check,
  User,
  Scissors,
  Sparkles,
} from 'lucide-react'
import { useCopilot } from '@/contexts/CopilotContext'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { useInsights } from '@/hooks/useInsights'
import { generateCopilotSuggestions } from '@/utils/copilotSuggestions'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ActionProposalCard } from './ActionProposalCard'
import { parseActionProposal, type CopilotMessage, type CopilotUsage } from '@/services/copilot/types'

// Compact number formatter: 1234 → "1.2k", 15000000 → "15M"
function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${Math.round(n / 1_000)}k`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function formatUsageTooltip(u: CopilotUsage): string {
  const parts = [
    `Input: ${u.inputTokens.toLocaleString()}`,
    `Output: ${u.outputTokens.toLocaleString()}`,
  ]
  if (u.cacheReadTokens) parts.push(`Cache read: ${u.cacheReadTokens.toLocaleString()}`)
  if (u.cacheCreationTokens) parts.push(`Cache write: ${u.cacheCreationTokens.toLocaleString()}`)
  return parts.join('\n')
}

// Approximate current context size using a 4-chars-per-token heuristic
// (mirrors the backend's ApproxTokens). Excludes UI-only compact notices.
function approxContextTokens(messages: CopilotMessage[]): number {
  let chars = 0
  for (const m of messages) {
    if (m.kind === 'compact-notice') continue
    chars += (m.content ?? '').length
    if (m.toolCalls) {
      for (const tc of m.toolCalls) {
        chars += (tc.name ?? '').length + JSON.stringify(tc.input ?? {}).length
      }
    }
    if (m.toolResults) {
      for (const tr of m.toolResults) {
        chars += (tr.content ?? '').length
      }
    }
  }
  return Math.floor(chars / 4)
}

export function CopilotPanel() {
  const {
    config,
    isOpen,
    isLoading,
    error,
    messages,
    pendingToolCalls,
    usedFallback,
    sessionUsage,
    sessionRounds,
    closePanel,
    sendMessage,
    clearHistory,
    compactSession,
    isCompacting,
    lastRoundUsage,
    layout: layoutState,
  } = useCopilot()
  const { layout, toggleMode, setDockedWidth, setFloatingSize } = layoutState

  const [input, setInput] = useState('')
  const messagesContainerRef = useRef<HTMLDivElement>(null)
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  // Auto-scroll to bottom when new content arrives
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, pendingToolCalls])

  // Snap to bottom (no animation) when the panel opens
  useEffect(() => {
    if (isOpen) {
      // Wait for the panel to actually mount before scrolling
      requestAnimationFrame(() => {
        const el = messagesContainerRef.current
        if (el) el.scrollTop = el.scrollHeight
        setTimeout(() => inputRef.current?.focus(), 80)
      })
    }
  }, [isOpen])

  // Return focus to the input when a request completes so the user can
  // keep typing without reaching for the mouse.
  useEffect(() => {
    if (!isOpen || isLoading) return
    // Skip focus-steal if the user is interacting with something else
    // (e.g. clicked a link in the response, opened the compact button).
    if (document.activeElement && document.activeElement.tagName === 'BUTTON') return
    inputRef.current?.focus({ preventScroll: true })
  }, [isLoading, isOpen])

  // Context-size indicator: how full the conversation is relative to the
  // auto-compact trigger. Source of truth is the provider-reported input
  // (non-cached + cached) of the most recent round — this is exactly what
  // the LLM processed, including system prompt and tool definitions which
  // the client-side approximation misses. We fall back to the approximation
  // only before the first round has completed on a fresh session.
  const approxFromClient = useMemo(() => approxContextTokens(messages), [messages])
  const lastRoundFullInput = lastRoundUsage
    ? (lastRoundUsage.inputTokens ?? 0) +
      (lastRoundUsage.cacheReadTokens ?? 0) +
      (lastRoundUsage.cacheCreationTokens ?? 0)
    : 0
  const contextTokens =
    lastRoundFullInput > 0 ? lastRoundFullInput : approxFromClient
  const contextPct = useMemo(() => {
    if (!config?.compactTrigger || config.compactTrigger <= 0) return 0
    return Math.min(100, Math.round((contextTokens / config.compactTrigger) * 100))
  }, [contextTokens, config?.compactTrigger])
  const contextLineVisible = contextTokens > 0

  // ─── Resize handlers ────────────────────────────────────────
  function startDockedResize(e: React.MouseEvent) {
    e.preventDefault()
    const startX = e.clientX
    const startWidth = layout.dockedWidth

    function onMove(ev: MouseEvent) {
      // Dragging left from the left edge increases the width
      const dx = startX - ev.clientX
      setDockedWidth(startWidth + dx)
    }
    function onUp() {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.body.style.cursor = 'ew-resize'
    document.body.style.userSelect = 'none'
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
  }

  function startFloatingResize(e: React.MouseEvent, edge: 'left' | 'top' | 'corner') {
    e.preventDefault()
    const startX = e.clientX
    const startY = e.clientY
    const startW = layout.floatingWidth
    const startH = layout.floatingHeight

    function onMove(ev: MouseEvent) {
      let newW = startW
      let newH = startH
      if (edge === 'left' || edge === 'corner') {
        newW = startW + (startX - ev.clientX)
      }
      if (edge === 'top' || edge === 'corner') {
        newH = startH + (startY - ev.clientY)
      }
      setFloatingSize(newW, newH)
    }
    function onUp() {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.body.style.cursor = edge === 'corner' ? 'nwse-resize' : edge === 'left' ? 'ew-resize' : 'ns-resize'
    document.body.style.userSelect = 'none'
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
  }

  if (!config?.enabled || !isOpen) return null

  function handleSend() {
    if (!input.trim() || isLoading) return
    sendMessage(input)
    setInput('')
  }

  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  // Container styles depend on the mode.
  //
  // Docked mode sits below the Topbar (h-[52px]) instead of
  // covering it, and Layout adds a matching marginRight to the
  // main content column so the app content reflows to the left
  // rather than being hidden underneath the panel.
  const isDocked = layout.mode === 'docked'
  const containerStyle: React.CSSProperties = isDocked
    ? {
        position: 'fixed',
        top: 52,
        right: 0,
        bottom: 0,
        width: `${layout.dockedWidth}px`,
      }
    : {
        position: 'fixed',
        right: '20px',
        bottom: '20px',
        width: `${layout.floatingWidth}px`,
        height: `${layout.floatingHeight}px`,
        borderRadius: '12px',
      }

  return (
    <div
      style={containerStyle}
      className={`relative bg-kb-card border border-kb-border z-[300] flex flex-col shadow-2xl ${
        isDocked ? 'border-l' : ''
      }`}
    >
      {/* Resize handles — wide enough to grab easily, positioned slightly outside the panel
          edge so the cursor changes before reaching the visible border */}
      {isDocked ? (
        <div
          onMouseDown={startDockedResize}
          className="absolute -left-1 top-0 bottom-0 w-3 cursor-ew-resize hover:bg-kb-accent/30 transition-colors z-[400]"
          title="Drag to resize width"
        />
      ) : (
        <>
          {/* Left edge */}
          <div
            onMouseDown={(e) => startFloatingResize(e, 'left')}
            className="absolute -left-1 top-5 bottom-5 w-3 cursor-ew-resize hover:bg-kb-accent/30 transition-colors z-[400]"
            title="Drag to resize width"
          />
          {/* Top edge */}
          <div
            onMouseDown={(e) => startFloatingResize(e, 'top')}
            className="absolute -top-1 left-5 right-5 h-3 cursor-ns-resize hover:bg-kb-accent/30 transition-colors z-[400]"
            title="Drag to resize height"
          />
          {/* Top-left corner — diagonal resize, larger hit area */}
          <div
            onMouseDown={(e) => startFloatingResize(e, 'corner')}
            className="absolute -top-1 -left-1 w-6 h-6 cursor-nwse-resize hover:bg-kb-accent/40 transition-colors z-[401] rounded-tl-xl"
            title="Drag to resize"
          />
        </>
      )}

      {/* Header */}
      <div className="px-4 py-3 border-b border-kb-border flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <div className="w-7 h-7 rounded-lg bg-kb-accent-light flex items-center justify-center shrink-0">
            <Bot className="w-4 h-4 text-kb-accent" />
          </div>
          <div className="flex flex-col min-w-0">
            <span className="text-sm font-semibold text-kb-text-primary leading-tight truncate">
              KubeBolt Copilot AI
            </span>
            <span className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] truncate">
              {config.provider} · {config.model || 'default'}
            </span>
          </div>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          <button
            onClick={toggleMode}
            title={isDocked ? 'Switch to floating window' : 'Dock to right side'}
            className="p-1.5 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
          >
            {isDocked ? <PanelRightOpen className="w-3.5 h-3.5" /> : <PanelRightClose className="w-3.5 h-3.5" />}
          </button>
          {messages.filter((m) => m.kind !== 'compact-notice').length >= 2 && (
            <button
              onClick={() => void compactSession(true)}
              disabled={isCompacting || isLoading}
              title="New session with summary — compress conversation and start fresh"
              className="p-1.5 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-accent disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {isCompacting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Scissors className="w-3.5 h-3.5" />}
            </button>
          )}
          {messages.length > 0 && (
            <button
              onClick={clearHistory}
              title="Clear history"
              className="p-1.5 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          )}
          <button
            onClick={closePanel}
            title="Close (⌘J)"
            className="p-1.5 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* Messages */}
      <div ref={messagesContainerRef} className="flex-1 overflow-y-auto px-4 py-3 space-y-3">
        {messages.length === 0 && <EmptyState />}

        {messages
          .filter((m) => {
            // Skip tool-result-only user turns UNLESS they carry an action
            // proposal — those render as interactive ActionProposalCards.
            if (m.role === 'user' && !m.content && m.toolResults && m.toolResults.length > 0) {
              const hasProposal = m.toolResults.some(
                (tr) => parseActionProposal(tr.content) !== null,
              )
              if (!hasProposal) return false
            }
            // Skip assistant turns with only tool_calls (no content); the
            // actual tool call indicator is rendered via pendingToolCalls.
            if (m.role === 'assistant' && !m.content && m.toolCalls && m.toolCalls.length > 0) {
              return false
            }
            return true
          })
          .map((m) => {
            // User turns whose tool results include proposals render as
            // interactive cards, not chat bubbles. The LLM never executes —
            // the user clicks Execute on the card to trigger the mutation.
            if (
              m.role === 'user' &&
              !m.content &&
              m.toolResults &&
              m.toolResults.some((tr) => parseActionProposal(tr.content) !== null)
            ) {
              return (
                <div key={m.id} className="flex flex-col gap-1">
                  {m.toolResults.map((tr) => {
                    const proposal = parseActionProposal(tr.content)
                    if (!proposal) return null
                    return (
                      <ActionProposalCard
                        key={tr.toolCallId}
                        proposal={proposal}
                        toolCallId={tr.toolCallId}
                      />
                    )
                  })}
                </div>
              )
            }
            return <MessageBubble key={m.id} message={m} />
          })}

        {pendingToolCalls.length > 0 && (
          <div className="flex flex-col gap-1">
            {pendingToolCalls
              // Proposal tools are no-ops on the backend (they just build a
              // payload); showing a "loading propose_restart_workload" spinner
              // is misleading and slow-feeling, so hide them from the indicator.
              .filter((toolName) => !toolName.startsWith('propose_'))
              .map((toolName, i) => (
                <ToolCallIndicator key={i} toolName={toolName} />
              ))}
          </div>
        )}

        {isLoading &&
          messages[messages.length - 1]?.role === 'assistant' &&
          messages[messages.length - 1]?.content === '' && (
            <ThinkingIndicator />
          )}

        {error && (
          <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-[11px]">
            <AlertCircle className="w-3.5 h-3.5 shrink-0 mt-0.5" />
            <span className="break-words">{error}</span>
          </div>
        )}

        {usedFallback && (
          <div className="text-[10px] font-mono text-kb-text-tertiary text-center">
            via fallback model ({config.fallback?.provider} {config.fallback?.model})
          </div>
        )}

        <div ref={messagesEndRef} />
      </div>

      {/* Input */}
      <div className="border-t border-kb-border p-3 shrink-0">
        <div className="flex gap-2 items-end">
          {/* AI bezel — gradient border that rotates around the box
              (accent → blue → violet → back), paired with a slow
              hue-cycling outer glow. Both effects share the same
              palette as the Ask Copilot button so the affordance
              reads as "this is the AI surface" wherever it appears. */}
          <div className="kb-ai-bezel flex-1 bg-kb-bg">
            <textarea
              ref={inputRef}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Ask about your cluster..."
              rows={1}
              disabled={isLoading}
              className="relative z-[1] w-full px-3 py-2 rounded-lg bg-transparent border-0 text-xs text-kb-text-primary placeholder:text-kb-text-tertiary focus:outline-none resize-none max-h-32 disabled:opacity-50"
              style={{ minHeight: '36px' }}
            />
          </div>
          <button
            onClick={handleSend}
            disabled={!input.trim() || isLoading}
            className="w-9 h-9 rounded-lg bg-kb-accent hover:bg-kb-accent/90 text-white disabled:opacity-30 disabled:cursor-not-allowed flex items-center justify-center transition-colors shrink-0"
          >
            <Send className="w-4 h-4" />
          </button>
        </div>
        <div className="text-[9px] font-mono text-kb-text-tertiary mt-1.5 text-center leading-relaxed">
          AI can make mistakes. Verify important information before acting on it.
          <br />
          ⌘+Enter to send · ⌘J to toggle
          {sessionUsage && (
            <>
              <br />
              <span
                className="text-kb-text-secondary"
                title={formatUsageTooltip(sessionUsage)}
              >
                Question: {formatTokens(sessionUsage.inputTokens + sessionUsage.outputTokens)} billed
                {sessionRounds > 0 && ` · ${sessionRounds} round${sessionRounds === 1 ? '' : 's'}`}
                {sessionUsage.cacheReadTokens
                  ? ` · cache ${formatTokens(sessionUsage.cacheReadTokens)}`
                  : ''}
              </span>
            </>
          )}
          {contextLineVisible && (
            <>
              <br />
              <span
                className={`${contextPct >= 80 ? 'text-status-warn' : 'text-kb-text-secondary'}`}
                title={`Cumulative conversation size vs auto-compact trigger${
                  config.sessionBudget ? ` (budget ${config.sessionBudget.toLocaleString()})` : ''
                }`}
              >
                Session: {formatTokens(contextTokens)}
                {config.compactTrigger ? ` / ${formatTokens(config.compactTrigger)}` : ''}
                {config.compactTrigger ? ` · ${contextPct}%` : ''}
              </span>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

// ─── Empty state with smart suggestions ─────────────────────────

function EmptyState() {
  const { sendMessage } = useCopilot()
  const { data: overview } = useClusterOverview()
  const { data: insightsResp } = useInsights()

  const suggestions = useMemo(
    () => generateCopilotSuggestions(overview, insightsResp?.items),
    [overview, insightsResp?.items],
  )

  return (
    <div className="flex flex-col items-center justify-center h-full text-center px-4 py-8">
      {/* Hero icon — the empty state is a pause point, so the icon
          gets the full ambient-AI treatment: float on the wrapper,
          cycling glow on the body, two staggered halos pinging
          outward, a subtle breath on the icon itself, and corner
          sparkles twinkling on stagger. Each layer is reduced under
          motion-reduce. */}
      <div className="relative mb-4 animate-kb-ai-hero-float motion-reduce:animate-none">
        <div className="relative w-16 h-16">
          {/* Two staggered halos behind the body — bigger and slower
              than the toggle's, so the empty state reads as a
              presence, not a button. */}
          <span
            aria-hidden
            className="absolute inset-0 rounded-2xl bg-kb-accent/35 animate-kb-ai-hero-halo motion-reduce:hidden"
          />
          <span
            aria-hidden
            className="absolute inset-0 rounded-2xl bg-violet-500/30 animate-kb-ai-hero-halo motion-reduce:hidden"
            style={{ animationDelay: '1.6s' }}
          />

          {/* Gradient body carries the same green→violet identity as
              the toggle and Ask Copilot button, plus the cycling
              outer glow. */}
          <div className="relative w-16 h-16 rounded-2xl bg-gradient-to-br from-kb-accent via-kb-accent to-violet-500 flex items-center justify-center shadow-lg shadow-kb-accent/40 overflow-hidden animate-kb-ai-glow">
            <Bot className="relative w-8 h-8 text-white drop-shadow-[0_1px_2px_rgba(0,0,0,0.3)] animate-kb-ai-hero-breath motion-reduce:animate-none" />
          </div>

          {/* Sparkles at the corners twinkle on stagger so motion is
              always somewhere on the surface without any single
              point feeling busy. */}
          <Sparkles
            aria-hidden
            className="absolute -top-1.5 -right-1.5 w-3.5 h-3.5 text-kb-accent animate-kb-ai-hero-sparkle motion-reduce:hidden"
          />
          <Sparkles
            aria-hidden
            className="absolute -bottom-1.5 -left-1.5 w-3 h-3 text-violet-400 animate-kb-ai-hero-sparkle motion-reduce:hidden"
            style={{ animationDelay: '1.7s' }}
          />
        </div>
      </div>
      <h3 className="text-sm font-semibold text-kb-text-primary mb-1">KubeBolt Copilot AI</h3>
      <p className="text-xs text-kb-text-tertiary mb-4 max-w-xs">
        Ask questions about your cluster, troubleshoot issues, or learn about Kubernetes concepts.
      </p>
      <div className="space-y-1.5 w-full max-w-md">
        {suggestions.map((text) => (
          <button
            key={text}
            onClick={() => sendMessage(text)}
            className="w-full text-left px-3 py-2 rounded-lg bg-kb-bg hover:bg-kb-elevated border border-kb-border text-[11px] text-kb-text-secondary hover:text-kb-text-primary transition-colors"
          >
            {text}
          </button>
        ))}
      </div>
    </div>
  )
}

// ─── Message rendering ──────────────────────────────────────────

function MessageBubble({ message }: { message: CopilotMessage }) {
  const [copied, setCopied] = useState(false)

  if (message.kind === 'compact-notice' && message.compactMeta) {
    return <CompactNoticeBubble meta={message.compactMeta} />
  }

  if (message.role === 'user') {
    return (
      <div className="flex justify-end gap-2">
        <div className="max-w-[85%] px-3 py-2 rounded-lg bg-kb-elevated text-xs text-kb-text-primary whitespace-pre-wrap break-words">
          {message.content}
        </div>
        <div className="w-6 h-6 rounded-full bg-kb-elevated flex items-center justify-center shrink-0 mt-0.5">
          <User className="w-3.5 h-3.5 text-kb-text-secondary" />
        </div>
      </div>
    )
  }

  function handleCopyMessage() {
    navigator.clipboard.writeText(message.content).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }

  // assistant — render markdown with a copy action
  return (
    <div className="flex justify-start gap-2 group max-w-[95%] min-w-0">
      <div className="w-6 h-6 rounded-full bg-kb-accent-light flex items-center justify-center shrink-0 mt-0.5">
        <Bot className="w-3.5 h-3.5 text-kb-accent" />
      </div>
      <div className="flex flex-col items-start min-w-0 flex-1">
        <div className="px-3 py-2 rounded-lg bg-kb-bg text-xs text-kb-text-primary break-words min-w-0 max-w-full w-full overflow-hidden">
          {message.content ? (
            <MarkdownRenderer content={message.content} />
          ) : (
            <span className="text-kb-text-tertiary italic">...</span>
          )}
        </div>
        {message.content && (
          <button
            onClick={handleCopyMessage}
            title={copied ? 'Copied!' : 'Copy message'}
            className="flex items-center gap-1 ml-2 mt-1 px-1.5 py-0.5 rounded text-[9px] font-mono text-kb-text-tertiary hover:text-kb-accent hover:bg-kb-elevated/40 opacity-0 group-hover:opacity-100 transition-all"
          >
            {copied ? (
              <>
                <Check className="w-3 h-3" />
                Copied
              </>
            ) : (
              <>
                <Copy className="w-3 h-3" />
                Copy
              </>
            )}
          </button>
        )}
      </div>
    </div>
  )
}

function ToolCallIndicator({ toolName }: { toolName: string }) {
  const label = toolName.replace(/_/g, ' ')
  return (
    <div className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-kb-bg border border-kb-border text-[10px] font-mono text-kb-text-tertiary">
      <Wrench className="w-3 h-3 text-kb-accent" />
      <span>{label}</span>
      <Loader2 className="w-3 h-3 animate-spin ml-auto" />
    </div>
  )
}

// ThinkingIndicator — three staggered dots + a shimmering label,
// shown while the assistant has emitted no content yet (between
// the user submit and the first token / tool call). The single-
// spinner version it replaces read as "loading something" rather
// than "actively reasoning"; the dots + color flow communicate
// activity without overpromising progress.
function ThinkingIndicator() {
  return (
    <div className="flex items-center gap-2.5 px-3 py-2 rounded-lg bg-kb-bg border border-kb-border w-fit">
      <div className="flex items-center gap-1">
        <span
          className="w-1.5 h-1.5 rounded-full bg-kb-accent animate-kb-ai-thinking-dot"
          style={{ animationDelay: '0s' }}
        />
        <span
          className="w-1.5 h-1.5 rounded-full bg-kb-accent animate-kb-ai-thinking-dot"
          style={{ animationDelay: '0.18s' }}
        />
        <span
          className="w-1.5 h-1.5 rounded-full bg-kb-accent animate-kb-ai-thinking-dot"
          style={{ animationDelay: '0.36s' }}
        />
      </div>
      <span className="kb-ai-shimmer-text text-[11px] font-mono">Thinking</span>
    </div>
  )
}

function CompactNoticeBubble({
  meta,
}: {
  meta: NonNullable<CopilotMessage['compactMeta']>
}) {
  const saved = Math.max(0, meta.tokensBefore - meta.tokensAfter)
  const pct = meta.tokensBefore > 0 ? Math.round((saved / meta.tokensBefore) * 100) : 0
  const title = meta.auto ? 'Auto-compacted' : 'Session compacted'
  // Prefer the turns summary; fall back to "tool results stubbed" when the
  // compact only freed memory without folding (conversation still too short
  // for a summary fold but heavy enough to warrant trimming).
  const action =
    meta.turnsFolded > 0
      ? `${meta.turnsFolded} turn${meta.turnsFolded === 1 ? '' : 's'} folded`
      : meta.toolResultsStubbed > 0
        ? `${meta.toolResultsStubbed} tool result${
            meta.toolResultsStubbed === 1 ? '' : 's'
          } stubbed`
        : 'no-op'
  return (
    <div className="flex items-center gap-2 px-3 py-2 rounded-lg border border-dashed border-kb-accent/30 bg-gradient-to-r from-kb-accent-light via-kb-accent-light/40 to-violet-500/5 text-[10px] font-mono text-kb-text-secondary">
      <Sparkles className="w-3 h-3 text-kb-accent shrink-0" />
      <span className="text-kb-accent font-semibold uppercase tracking-wider">{title}</span>
      <span className="text-kb-text-tertiary">·</span>
      <span>{action}</span>
      {meta.turnsFolded > 0 && meta.toolResultsStubbed > 0 && (
        <>
          <span className="text-kb-text-tertiary">+</span>
          <span>
            {meta.toolResultsStubbed} tool result{meta.toolResultsStubbed === 1 ? '' : 's'} stubbed
          </span>
        </>
      )}
      {meta.tokensBefore > 0 && (
        <>
          <span className="text-kb-text-tertiary">·</span>
          <span>
            {formatTokens(meta.tokensBefore)} → {formatTokens(meta.tokensAfter)}
            {pct > 0 && <span className="text-kb-accent ml-1">(−{pct}%)</span>}
          </span>
        </>
      )}
      {meta.model && (
        <span className="ml-auto text-kb-text-tertiary truncate" title={meta.model}>
          {meta.model}
        </span>
      )}
    </div>
  )
}

