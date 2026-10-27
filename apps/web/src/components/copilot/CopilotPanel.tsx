import { Fragment, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import {
  X,
  Send,
  Square,
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
  History,
  Plus,
  Download,
  Clock,
  Hourglass,
  ArrowRight,
  ShieldCheck,
} from 'lucide-react'
import { ConversationList } from './ConversationList'
import { useCopilot } from '@/contexts/CopilotContext'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { useInsights } from '@/hooks/useInsights'
import { generateCopilotSuggestions } from '@/utils/copilotSuggestions'
import { KobiSigil, type KobiSigilState } from '@/components/kobi'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ActionProposalCard } from './ActionProposalCard'
import { ToolCallCard } from './ToolCallCard'
import { KobiMetricChartCard } from './KobiMetricChartCard'
import { AnswerFeedback } from './AnswerFeedback'
import { computeMetricChartAttachments, type MetricChartCard } from './metricChartAttachment'
import { computeExternalSources, externalSourcesLabel } from './externalSources'
import { externalTool } from '@/utils/toolName'
import { CloudProviderIcon } from '@/components/shared/CloudProviderIcon'
import { useAuth } from '@/contexts/AuthContext'
import {
  parseActionProposal,
  type CopilotMessage,
  type CopilotToolCall,
  type CopilotToolResult,
  type CopilotUsage,
} from '@/services/copilot/types'
import { livePhase } from '@/services/copilot/liveTurn'
import { useClusterLabel } from '@/hooks/useClusterLabel'
import {
  cloudBoundaries,
  formatClock,
  investigationRuns,
  isGuide,
  liveInvestigation,
  turnEndings,
} from './investigationRun'
import { CloudBoundary, GuideLine, InvestigationStrip, InvestigationSummary, TurnFailure } from './InvestigationMarks'

// modelLabel is how the header names a model: Claude ids by family and
// version ("claude-haiku-5-5" → "haiku 5.5", shown in caps), anything else as
// given.
export function modelLabel(id?: string): string {
  if (!id) return 'default'
  const m = /^claude-([a-z]+)-(\d+)-(\d+)/.exec(id)
  return m ? `${m[1]} ${m[2]}.${m[3]}` : id
}

// Past this share of the auto-compact trigger, the next question is likely to
// summarize the earlier turns.
const COMPACT_SOON_PCT = 90

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

// How old a resumed conversation must be before we warn that the cluster
// state may have moved on. 30 minutes balances "stale enough to matter"
// against not nagging on a conversation resumed minutes later.
const STALE_RESUME_MS = 30 * 60 * 1000

// Coarse single-unit relative time for the stale-resume banner.
function relativeTimeShort(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return ''
  const s = Math.max(0, Math.floor((Date.now() - then) / 1000))
  if (s < 90) return 'a moment ago'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} minutes ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} hour${h === 1 ? '' : 's'} ago`
  const d = Math.floor(h / 24)
  return `${d} day${d === 1 ? '' : 's'} ago`
}

// exportConversationMarkdown renders the visible transcript to a .md file and
// triggers a browser download — for pasting into postmortems / RCA docs.
function exportConversationMarkdown(messages: CopilotMessage[], title: string | null): void {
  const lines: string[] = [`# ${title || 'Kobi conversation'}`, '']
  // Per-message time turns the export into a real timeline (postmortems / RCA).
  const stamp = (m: CopilotMessage) =>
    m.timestamp instanceof Date && !Number.isNaN(m.timestamp.getTime())
      ? ` · ${m.timestamp.toLocaleString()}`
      : ''
  for (const m of messages) {
    if (m.kind === 'compact-notice') {
      lines.push('> _(conversation compacted here)_', '')
      continue
    }
    if (m.kind === 'maxrounds-notice') {
      lines.push(`> _(reached the tool-step limit${m.maxRoundsLimit ? ` of ${m.maxRoundsLimit}` : ''} here — investigation paused)_`, '')
      continue
    }
    if (m.role === 'user' && (m.content ?? '').trim()) {
      lines.push(`## You${stamp(m)}`, '', m.content.trim(), '')
    } else if (m.role === 'assistant' && (m.content ?? '').trim()) {
      lines.push(`## Kobi${stamp(m)}`, '', m.content.trim(), '')
    }
    if (m.toolCalls?.length) {
      for (const tc of m.toolCalls) {
        lines.push(`- 🔧 \`${tc.name}\``)
      }
      lines.push('')
    }
    if (m.failure) lines.push(`> _(Kobi couldn't finish: ${m.failure.message})_`, '')
  }
  const blob = new Blob([lines.join('\n')], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  const slug = (title || 'kobi-conversation').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
  a.href = url
  a.download = `${slug || 'kobi-conversation'}.md`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

// Approximate current context size using a 4-chars-per-token heuristic
// (mirrors the backend's ApproxTokens). Excludes UI-only compact notices.
function approxContextTokens(messages: CopilotMessage[]): number {
  let chars = 0
  for (const m of messages) {
    if (m.kind === 'compact-notice' || m.kind === 'maxrounds-notice') continue
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
    escalatedModel,
    sessionUsage,
    sessionRounds,
    closePanel,
    sendMessage,
    cancelMessage,
    clearHistory,
    compactSession,
    isCompacting,
    lastRoundUsage,
    layout: layoutState,
    conversationTitle,
    conversationId,
    staleResume,
    newConversation,
    dismissStaleResume,
  } = useCopilot()
  const { layout, toggleMode, setDockedWidth, setFloatingSize } = layoutState
  // Connector settings live in the admin-only AI hub; only admins get links there.
  const { hasRole } = useAuth()
  const canManageConnectors = hasRole('admin')

  const [input, setInput] = useState('')
  const [showHistory, setShowHistory] = useState(false)
  // The stale banner names the cluster, not its context id (agent:<uid>).
  const clusterLabel = useClusterLabel()
  // Finished investigations fold their steps under a summary; these are the
  // ones the operator opened (by the answer's id).
  const [openRuns, setOpenRuns] = useState<Set<string>>(() => new Set())
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

  // Focus the input when a fresh/blank conversation appears — clicking "New
  // conversation" (header or history drawer) clears the transcript but doesn't
  // change isOpen/isLoading, so the focus effects above don't fire. Keyed on
  // the transcript going empty; guarded on the drawer being closed so it never
  // steals focus from the history search box.
  useEffect(() => {
    if (!isOpen || isLoading || showHistory || messages.length > 0) return
    const t = setTimeout(() => inputRef.current?.focus({ preventScroll: true }), 60)
    return () => clearTimeout(t)
  }, [messages.length, isOpen, isLoading, showHistory])

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

  // A "pending proposal" is one where Kobi has emitted an action_proposal
  // tool result but the operator has not yet clicked Execute or Dismiss
  // (executionStatus is undefined). When at least one such card is
  // rendered, Kobi is effectively waiting for a decision — that's the
  // semantic the awaiting Sigil state communicates.
  const hasPendingProposal = useMemo(() => {
    for (const m of messages) {
      if (m.role !== 'user' || !m.toolResults) continue
      for (const tr of m.toolResults) {
        const proposal = parseActionProposal(tr.content)
        if (proposal && proposal.executionStatus === undefined) {
          return true
        }
      }
    }
    return false
  }, [messages])

  // Map runtime state to Kobi Sigil state, by precedence:
  //   investigating — model is generating or tool calls are in flight
  //   awaiting      — at least one proposal card is waiting for the
  //                   operator's Execute / Dismiss decision
  //   watching      — idle resting state
  // Investigating wins over awaiting because active work is the more
  // current signal (the operator may have just re-sent a follow-up
  // before clicking on the prior card).
  const kobiState: KobiSigilState =
    isLoading || pendingToolCalls.length > 0
      ? 'investigating'
      : hasPendingProposal
        ? 'awaiting'
        : 'watching'

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
      className={`relative bg-kobi-bg border border-kobi-border z-[300] flex flex-col ${
        isDocked ? 'border-l shadow-2xl' : 'kobi-floating-shadow'
      }`}
    >
      {/* Resize handles — wide enough to grab easily, positioned slightly outside the panel
          edge so the cursor changes before reaching the visible border. The GRAB area stays
          12px, but the visible hover indicator is a centered 3px hairline — tinting the whole
          hit zone read as a thick green slab down the panel edge. */}
      {isDocked ? (
        <div
          onMouseDown={startDockedResize}
          className="group absolute -left-1 top-0 bottom-0 w-3 cursor-ew-resize z-[400]"
          title="Drag to resize width"
        >
          <div className="absolute left-1/2 -translate-x-1/2 top-0 bottom-0 w-[3px] rounded-full group-hover:bg-kobi-accent/50 transition-colors" />
        </div>
      ) : (
        <>
          {/* Left edge */}
          <div
            onMouseDown={(e) => startFloatingResize(e, 'left')}
            className="group absolute -left-1 top-5 bottom-5 w-3 cursor-ew-resize z-[400]"
            title="Drag to resize width"
          >
            <div className="absolute left-1/2 -translate-x-1/2 top-0 bottom-0 w-[3px] rounded-full group-hover:bg-kobi-accent/50 transition-colors" />
          </div>
          {/* Top edge */}
          <div
            onMouseDown={(e) => startFloatingResize(e, 'top')}
            className="group absolute -top-1 left-5 right-5 h-3 cursor-ns-resize z-[400]"
            title="Drag to resize height"
          >
            <div className="absolute top-1/2 -translate-y-1/2 left-0 right-0 h-[3px] rounded-full group-hover:bg-kobi-accent/50 transition-colors" />
          </div>
          {/* Top-left corner — diagonal resize, larger hit area. Indicator is
              a 3px "L" hugging the panel's rounded corner (same hairline
              language as the edges), not a solid block. */}
          <div
            onMouseDown={(e) => startFloatingResize(e, 'corner')}
            className="group absolute -top-1 -left-1 w-6 h-6 cursor-nwse-resize z-[401]"
            title="Drag to resize"
          >
            <div className="absolute inset-1 rounded-tl-xl border-t-[3px] border-l-[3px] border-transparent group-hover:border-kobi-accent/50 transition-colors" />
          </div>
        </>
      )}

      {/* Header */}
      <div className="px-4 py-3 border-b border-kobi-border flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          {/* The avatar background tints with the state, in the same color
              family as the Sigil inside. At small sizes (18px) the inner
              SVG state changes alone are too subtle — tinting the whole
              circle makes the transition unmistakable without abandoning
              the calm-SRE register. */}
          <div
            className={`w-7 h-7 rounded-lg flex items-center justify-center shrink-0 transition-colors duration-300 ${
              kobiState === 'investigating'
                ? 'bg-amber-100 dark:bg-amber-950/40'
                : kobiState === 'awaiting'
                  ? 'bg-sky-100 dark:bg-sky-950/40'
                  : 'bg-kobi-accent-light'
            }`}
          >
            <KobiSigil state={kobiState} size={18} />
          </div>
          <div className="flex flex-col min-w-0">
            <span className="font-display text-[15px] font-semibold tracking-tight text-kobi-text leading-tight truncate">
              Kobi
            </span>
            <span
              className="text-[10px] font-mono text-kobi-text-tertiary uppercase tracking-[0.08em] truncate"
              title={[config.model, escalatedModel].filter(Boolean).join(' → ')}
            >
              {config.provider} · {modelLabel(config.model)}
              {/* Once the conversation left the cluster it runs on the
                  escalation model: the header says which. */}
              {escalatedModel && <span className="text-kobi-text-secondary"> → {modelLabel(escalatedModel)}</span>}
            </span>
          </div>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          <button
            onClick={() => setShowHistory(true)}
            title="Conversation history"
            className="p-1.5 rounded hover:bg-kobi-elevated text-kobi-text-tertiary hover:text-kobi-text transition-colors"
          >
            <History className="w-3.5 h-3.5" />
          </button>
          <button
            onClick={newConversation}
            title="New conversation"
            className="p-1.5 rounded hover:bg-kobi-elevated text-kobi-text-tertiary hover:text-kobi-text transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
          </button>
          <button
            onClick={toggleMode}
            title={isDocked ? 'Switch to floating window' : 'Dock to right side'}
            className="p-1.5 rounded hover:bg-kobi-elevated text-kobi-text-tertiary hover:text-kobi-text transition-colors"
          >
            {isDocked ? <PanelRightOpen className="w-3.5 h-3.5" /> : <PanelRightClose className="w-3.5 h-3.5" />}
          </button>
          {messages.filter((m) => m.kind !== 'compact-notice' && m.kind !== 'maxrounds-notice').length >= 2 && (
            <button
              onClick={() => void compactSession(true)}
              disabled={isCompacting || isLoading}
              title="New session with summary — compress conversation and start fresh"
              className="p-1.5 rounded hover:bg-kobi-elevated text-kobi-text-tertiary hover:text-kobi-accent disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {isCompacting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Scissors className="w-3.5 h-3.5" />}
            </button>
          )}
          {messages.filter((m) => m.kind !== 'compact-notice' && m.kind !== 'maxrounds-notice').length > 0 && (
            <button
              onClick={() => exportConversationMarkdown(messages, conversationTitle)}
              title="Export conversation (Markdown)"
              className="p-1.5 rounded hover:bg-kobi-elevated text-kobi-text-tertiary hover:text-kobi-text transition-colors"
            >
              <Download className="w-3.5 h-3.5" />
            </button>
          )}
          {messages.length > 0 && (
            <button
              onClick={clearHistory}
              title="Clear history"
              className="p-1.5 rounded hover:bg-kobi-elevated text-kobi-text-tertiary hover:text-kobi-text transition-colors"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          )}
          <button
            onClick={closePanel}
            title="Close (⌘J)"
            className="p-1.5 rounded hover:bg-kobi-elevated text-kobi-text-tertiary hover:text-kobi-text transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* Active conversation title */}
      {conversationTitle && messages.length > 0 && (
        <div className="px-4 py-1.5 border-b border-kobi-border flex items-center gap-1.5 shrink-0">
          <Sparkles className="w-3 h-3 text-kobi-text-tertiary shrink-0" />
          <span className="text-xs text-kobi-text-secondary truncate">{conversationTitle}</span>
        </div>
      )}

      {/* Stale-resume banner — shown when a conversation older than the
          threshold is resumed, so the operator knows the cluster state the
          transcript reflects may have moved on. */}
      {staleResume && Date.now() - new Date(staleResume.updatedAt).getTime() > STALE_RESUME_MS && (
        <div className="px-4 py-2 bg-amber-50 dark:bg-amber-950/30 border-b border-amber-200 dark:border-amber-900/40 flex items-start gap-2 shrink-0">
          <Clock className="w-3.5 h-3.5 text-amber-500 shrink-0 mt-0.5" />
          <p className="text-xs text-amber-700 dark:text-amber-300 leading-snug flex-1">
            This conversation is from {relativeTimeShort(staleResume.updatedAt)}
            {clusterLabel(staleResume.clusterId ?? '') ? ` · it ran against ${clusterLabel(staleResume.clusterId ?? '')}` : ''}. The cluster
            state may have changed — Kobi will re-check the live state when you continue.
          </p>
          <button
            onClick={dismissStaleResume}
            title="Dismiss"
            className="text-amber-500 hover:text-amber-700 dark:hover:text-amber-200 shrink-0"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      )}

      {/* Conversation history drawer — overlays the message area */}
      {showHistory && <ConversationList onClose={() => setShowHistory(false)} />}

      {/* Messages */}
      <div ref={messagesContainerRef} className="flex-1 overflow-y-auto px-4 py-3 space-y-3">
        {messages.length === 0 && <EmptyState />}

        {(() => {
          // Server-side flag (KUBEBOLT_AI_SHOW_TOOL_CALLS, default true).
          // ON: render persistent ToolCallCards inline + spinner cards for
          //     in-flight tools. OFF: keep the pre-2026-05-15 behavior —
          //     hide tool turns and show a transient ToolCallIndicator row.
          const showToolCalls = config?.showToolCalls !== false

          // Index tool results by their tool_call_id so the inline card
          // for an assistant tool_use can pull in its matching result
          // from the next user turn without an O(n²) lookup per render.
          const resultByCallId = new Map<string, CopilotToolResult>()
          for (const m of messages) {
            if (m.role === 'user' && m.toolResults) {
              for (const tr of m.toolResults) {
                resultByCallId.set(tr.toolCallId, tr)
              }
            }
          }

          // Spec #08 — compute chart attachments once per render. The helper
          // enforces "attach to the FIRST text-bearing assistant turn that
          // STRICTLY FOLLOWS the metric tool call" so the chart sits next to
          // the analysis prose, not above the "I'll check this" preamble that
          // typically ships with the tool_call in the same Claude message.
          // See `metricChartAttachment.ts` for the attachment rule details.
          const metricChartAttachments = computeMetricChartAttachments(messages)
          // Which cloud connectors each answer read from — derived from the
          // tool names, so it holds with tool cards hidden and after a reload.
          const externalSources = computeExternalSources(messages)
          // The first read of each cloud in a question gets a boundary mark;
          // a finished investigation folds its steps under a summary that
          // sits right after the question, above the answer.
          const boundaries = cloudBoundaries(messages)
          const runs = investigationRuns(messages)
          const endings = turnEndings(messages)
          const summaryBefore = new Map<string, string>()
          const folded = new Set<string>()
          // A finished investigation shows its charts right above the answer
          // and its proposals below it, outside the steps: folded or open,
          // they stay.
          const answerCharts = new Map<string, MetricChartCard[]>()
          const chartsMoved = new Set<string>()
          const proposalsAfter = new Map<string, string[]>()
          const proposalsMoved = new Set<string>()
          for (const run of runs.values()) {
            summaryBefore.set(run.stepIds[0] ?? run.answerId, run.answerId)
            if (!openRuns.has(run.answerId)) run.stepIds.forEach((id) => folded.add(id))
            const ids = [...run.stepIds, run.answerId]
            const cards = ids.flatMap((id) => metricChartAttachments.get(id) ?? [])
            if (cards.length > 0) {
              answerCharts.set(run.answerId, cards)
              ids.forEach((id) => chartsMoved.add(id))
            }
            if (run.proposalIds.length > 0) {
              proposalsAfter.set(run.answerId, run.proposalIds)
              run.proposalIds.forEach((id) => proposalsMoved.add(id))
            }
          }
          const toggleRun = (answerId: string) =>
            setOpenRuns((prev) => {
              const next = new Set(prev)
              if (next.has(answerId)) next.delete(answerId)
              else next.add(answerId)
              return next
            })

          const renderMessage = (m: CopilotMessage) => {
            if (folded.has(m.id)) return null
            // User turns whose tool results include proposals → interactive
            // ActionProposalCard (the LLM never executes; the operator clicks
            // Execute). This path is independent of showToolCalls.
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
            // Other tool-result-only user turns are consumed by the inline
            // ToolCallCards on the previous assistant turn (when ON) or
            // hidden entirely (when OFF) — either way, no bubble.
            if (m.role === 'user' && !m.content && m.toolResults && m.toolResults.length > 0) {
              return null
            }
            // Assistant turns with text + optional toolCalls. Render the
            // text bubble (if any) and the tool cards (when feature is on).
            if (m.role === 'assistant') {
              const hasText = !!m.content
              const toolCalls: CopilotToolCall[] = m.toolCalls ?? []
              const visibleCalls = showToolCalls
                ? toolCalls.filter((tc) => !tc.name.startsWith('propose_'))
                : []
              // Spec #08 — chart cards attach to the FIRST text-bearing
              // assistant turn after the tool call (not the tool-call turn
              // itself). Reading order: question → "I'll check this" →
              // tool chip → chart (evidence) → analysis text. This keeps
              // the chart visually next to the prose that interprets it.
              const metricCards = !hasText
                ? []
                : (answerCharts.get(m.id) ?? (chartsMoved.has(m.id) ? [] : (metricChartAttachments.get(m.id) ?? [])))
              if (
                !hasText &&
                visibleCalls.length === 0 &&
                metricCards.length === 0 &&
                !m.cancelled &&
                !m.failure
              ) {
                // Pre-2026-05-15 behavior — assistant-only-toolCalls turns
                // don't render at all; the bottom indicator covers them.
                // Exception: a cancelled or failed turn renders even when
                // empty so its marker shows (it ended before any token).
                return null
              }
              return (
                <div key={m.id} className="flex flex-col gap-2">
                  {metricCards.map((mc) => (
                    // Same outer lane as MessageBubble (max-w-[95%], avatar
                    // + gap-2 on the left). The avatar slot stays empty here
                    // so the chart's content aligns horizontally with the
                    // text bubble's content below, but only the text bubble
                    // shows the Kobi sigil — one avatar per response block.
                    <div
                      key={`chart-${mc.id}`}
                      className="flex justify-start gap-2 max-w-[95%] min-w-0"
                    >
                      <div className="w-6 shrink-0" aria-hidden />
                      <div className="flex-1 min-w-0">
                        <KobiMetricChartCard data={mc.data!} />
                      </div>
                    </div>
                  ))}
                  {hasText &&
                    // A round that goes on to call tools says what Kobi is
                    // about to check: a phrase, not an answer. The answer
                    // (the turn's last message) keeps the bubble.
                    ((m.toolCalls?.length ?? 0) > 0 && !m.turnId ? (
                      <RoundPhrase message={m} />
                    ) : (
                      <MessageBubble message={m} />
                    ))}
                  {visibleCalls.length > 0 && (
                    <ToolCardLane>
                      {visibleCalls.map((tc) => {
                        const tr = resultByCallId.get(tc.id)
                        const boundary = boundaries.get(tc.id)
                        // A skill or an Azure command list is a guide, not a
                        // read: one line, no card.
                        if (isGuide(tc)) {
                          return <GuideLine key={tc.id} call={tc} result={tr} running={!tr && isLoading} />
                        }
                        return (
                          <Fragment key={tc.id}>
                            {boundary && (
                              <CloudBoundary
                                connectorId={boundary}
                                input={tc.input}
                                canManageConnectors={canManageConnectors}
                              />
                            )}
                            <ToolCallCard
                              name={tc.name}
                              input={tc.input}
                              result={tr?.content}
                              isError={tr?.isError}
                              status={tr?.status}
                              durationMs={tr?.durationMs}
                              digest={tr?.digest}
                              reason={tr?.reason}
                              // No result and the turn is over: it was stopped
                              // or failed before this call came back.
                              stopped={!tr && !isLoading}
                              stoppedByYou={endings.get(m.id) === 'stopped'}
                              canManageConnectors={canManageConnectors}
                            />
                          </Fragment>
                        )
                      })}
                    </ToolCardLane>
                  )}
                  {hasText && externalSources.has(m.id) && (
                    <ExternalSourcesNote connectorIds={externalSources.get(m.id)!} />
                  )}
                  {m.failure && (
                    <ToolCardLane>
                      <TurnFailure message={m.failure.message} afterMs={m.failure.afterMs} />
                    </ToolCardLane>
                  )}
                  {m.cancelled && (
                    // "Stopped by you" — aligned to the assistant content lane
                    // (empty avatar slot + content), like the metric cards above.
                    <div className="flex justify-start gap-2 max-w-[95%]">
                      <div className="w-6 shrink-0" aria-hidden />
                      <div className="flex items-center gap-1.5 text-[11px] text-kobi-text-tertiary">
                        <Square className="w-2.5 h-2.5 fill-current shrink-0" />
                        <span className="font-mono">Stopped by you</span>
                      </div>
                    </div>
                  )}
                  {/* 👍/👎 — only on a finished turn's final answer, which the
                      server stamps with the turn id; needs the persisted
                      conversation the rating belongs to. */}
                  {m.turnId && conversationId && (
                    <div className="flex justify-start gap-2 max-w-[95%] min-w-0 -mt-1">
                      <div className="w-6 shrink-0" aria-hidden />
                      <AnswerFeedback conversationId={conversationId} turnId={m.turnId} />
                    </div>
                  )}
                </div>
              )
            }
            return <MessageBubble key={m.id} message={m} />
          }

          // Proposals of a finished investigation move under its answer. The
          // list stays flat, so a moved card keeps its state (keyed reorder).
          const byId = new Map(messages.map((m) => [m.id, m]))
          const order: CopilotMessage[] = []
          for (const m of messages) {
            if (proposalsMoved.has(m.id)) continue
            order.push(m)
            for (const id of proposalsAfter.get(m.id) ?? []) order.push(byId.get(id)!)
          }

          return order.map((m) => {
            const answerId = summaryBefore.get(m.id)
            if (!answerId) return renderMessage(m)
            return (
              <Fragment key={m.id}>
                <ToolCardLane>
                  <InvestigationSummary
                    run={runs.get(answerId)!}
                    open={openRuns.has(answerId)}
                    onToggle={() => toggleRun(answerId)}
                  />
                </ToolCardLane>
                {renderMessage(m)}
              </Fragment>
            )
          })
        })()}

        {/* With tool cards on, the turn in flight draws its cards from the
            live messages above, completing as each call returns. With them
            off, the transient indicator names the calls instead. */}
        {pendingToolCalls.length > 0 && config?.showToolCalls === false && (
          (() => {
            const visible = pendingToolCalls
              // Proposal tools are no-ops on the backend (they just build a
              // payload); showing a "loading propose_restart_workload" spinner
              // is misleading and slow-feeling, so hide them from the indicator.
              .filter((toolName) => !toolName.startsWith('propose_'))
            if (visible.length === 0) return null
            return (
              <div className="flex flex-col gap-1">
                {visible.map((toolName, i) => (
                  <ToolCallIndicator key={i} toolName={toolName} />
                ))}
              </div>
            )
          })()
        )}

        {isLoading && <StreamStatusHint />}

        {error && (
          <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-kobi-st-error-dim text-kobi-st-error text-[11px]">
            <AlertCircle className="w-3.5 h-3.5 shrink-0 mt-0.5" />
            <span className="break-words">{error}</span>
          </div>
        )}

        {usedFallback && (
          <div className="mx-auto w-fit flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-900/40 text-amber-700 dark:text-amber-300 text-[11px]">
            <AlertCircle className="w-3 h-3 shrink-0" />
            <span>
              Answered by the fallback model
              {config.fallback ? ` · ${config.fallback.provider} ${config.fallback.model}` : ''}
            </span>
          </div>
        )}

        <div ref={messagesEndRef} />
      </div>

      {/* Input */}
      <div className="border-t border-kobi-border p-3 shrink-0">
        {/* While a question runs, the strip says how it goes and holds Stop. */}
        {isLoading && <InvestigationStrip live={liveInvestigation(messages)} onStop={cancelMessage} />}
        <div className="flex gap-2 items-end">
          {/* AI bezel — gradient border that rotates around the box
              (accent → blue → violet → back), paired with a slow
              hue-cycling outer glow. Both effects share the same
              palette as the Ask Copilot button so the affordance
              reads as "this is the AI surface" wherever it appears. */}
          <div className="kb-ai-bezel flex-1 bg-kobi-card">
            <textarea
              ref={inputRef}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Ask about your cluster..."
              rows={1}
              disabled={isLoading}
              className="relative z-[1] w-full px-3 py-2 rounded-lg bg-transparent border-0 text-sm text-kobi-text placeholder:text-kobi-text-tertiary focus:outline-none resize-none max-h-32 overflow-y-auto overflow-x-hidden disabled:opacity-50"
              style={{ minHeight: '36px' }}
            />
          </div>
          {/* Stop lives in the strip above while a question runs. */}
          <button
            onClick={handleSend}
            disabled={isLoading || !input.trim()}
            aria-label="Send"
            className="w-9 h-9 rounded-lg bg-kobi-accent-fill hover:bg-kobi-accent-fill/90 text-kobi-ink disabled:opacity-30 disabled:cursor-not-allowed flex items-center justify-center transition-colors shrink-0"
          >
            <Send className="w-4 h-4" />
          </button>
        </div>
        <div className="text-[10.5px] font-mono text-kobi-text-tertiary mt-1.5 text-center leading-relaxed">
          AI can make mistakes. Verify important information before acting on it.
          <br />
          ⌘+Enter to send · ⌘J to toggle
          {sessionUsage && (
            <>
              <br />
              <span
                className="text-kobi-text-secondary"
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
                className={`${contextPct >= 80 ? 'text-kobi-st-warn' : 'text-kobi-text-secondary'}`}
                title={`Session: ${formatTokens(contextTokens)}${
                  config.compactTrigger ? ` / ${formatTokens(config.compactTrigger)} (auto-compact trigger)` : ''
                }${config.sessionBudget ? ` · budget ${config.sessionBudget.toLocaleString()}` : ''}`}
              >
                {/* Said as how full the conversation is; the numbers stay in
                    the tooltip. Near the trigger, what happens next. */}
                {config.compactTrigger ? (
                  <>
                    Conversation {contextPct}% full
                    {contextPct >= COMPACT_SOON_PCT && ' · earlier turns get summarized on the next question'}
                  </>
                ) : (
                  <>Conversation: {formatTokens(contextTokens)}</>
                )}
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
      {/* Hero — Kobi Sigil in its watching state. The empty state is
          a pause point; the gentle dot pulse communicates presence
          without performing enthusiasm. */}
      <div className="mb-4">
        <KobiSigil state="watching" size={56} />
      </div>
      <h3 className="font-display text-base font-semibold tracking-tight text-kobi-text mb-1">Kobi</h3>
      <p className="text-sm text-kobi-text-secondary mb-4 max-w-xs">
        Ask about your cluster, troubleshoot an issue, or learn about Kubernetes.
      </p>
      {/* Suggestion chips are the panel's most valuable surface — they get
          card treatment and a readable size instead of tertiary 12px. */}
      <div className="space-y-1.5 w-full max-w-md">
        {suggestions.map((text) => (
          <button
            key={text}
            onClick={() => sendMessage(text)}
            className="w-full text-left px-3 py-2 rounded-lg bg-kobi-card hover:bg-kobi-elevated border border-kobi-border hover:border-kobi-border-accent text-[13px] text-kobi-text-secondary hover:text-kobi-text transition-colors"
          >
            {text}
          </button>
        ))}
      </div>
    </div>
  )
}

// ─── Message rendering ──────────────────────────────────────────

// RoundPhrase is the text of a round that calls tools — "Checking the pods and
// the logs." — small and grey, without a bubble, in the lane the round's
// cards follow.
function RoundPhrase({ message }: { message: CopilotMessage }) {
  return (
    <div className="flex justify-start gap-2 max-w-[95%] min-w-0">
      <div className="w-6 shrink-0" aria-hidden />
      <div className="flex-1 min-w-0 max-w-[min(65ch,100%)] text-[12.5px] leading-snug text-kobi-text-secondary break-words">
        <MarkdownRenderer content={message.content} />
      </div>
    </div>
  )
}

function MessageBubble({ message }: { message: CopilotMessage }) {
  const [copied, setCopied] = useState(false)

  if (message.kind === 'compact-notice' && message.compactMeta) {
    return <CompactNoticeBubble meta={message.compactMeta} />
  }

  if (message.kind === 'maxrounds-notice') {
    return <MaxRoundsNoticeBubble limit={message.maxRoundsLimit} />
  }

  if (message.role === 'user') {
    return (
      <div className="flex justify-end gap-2">
        {/* User bubble — soft accent tint so the two voices are
            distinguishable at a glance (they were two near-identical
            grays in dark mode). bubble-user is the tint flattened to an
            opaque color so the tail diamond composes without a seam. */}
        <div className="relative max-w-[85%] px-3.5 py-2.5 rounded-xl rounded-tr-md bg-kobi-bubble-user border border-kobi-accent/20 text-sm text-kobi-text whitespace-pre-wrap break-words">
          {message.content}
          {/* WhatsApp-style tail toward the avatar: rotated square, borders
              only on the two protruding faces; the inner half covers the
              bubble's own border segment so the outline reads continuous. */}
          <span
            aria-hidden
            className="absolute -right-[5px] top-2 w-2.5 h-2.5 rotate-45 rounded-[1px] bg-kobi-bubble-user border-t border-r border-kobi-accent/20"
          />
        </div>
        <div className="w-6 h-6 rounded-full bg-kobi-elevated flex items-center justify-center shrink-0 mt-0.5">
          <User className="w-3.5 h-3.5 text-kobi-text-secondary" />
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

  // assistant — render markdown with a copy action.
  //
  // The Copy button is absolute-positioned in the top-right of the bubble
  // (rather than a sibling row below) so it doesn't reserve ~18px of
  // vertical space when hidden. Without this, every assistant message had
  // a phantom gap below it that became very visible once tool cards started
  // rendering immediately after the bubble.
  return (
    <div className="flex justify-start gap-2 group max-w-[95%] min-w-0">
      <div className="w-6 h-6 rounded-full bg-kobi-accent-light flex items-center justify-center shrink-0 mt-0.5">
        <KobiSigil state="static" size={14} />
      </div>
      {/* relative: hosts the tail. The tail can't live inside the bubble —
          its overflow-hidden (copy button / code blocks) would clip it. */}
      <div className="relative flex flex-col items-start min-w-0 flex-1">
        {/* Assistant bubble — card-v3 treatment: slightly ELEVATED over the
            panel bg (the old bg-kb-bg sat darker than the panel and sank),
            thin green-tinted bevel border, and a ~65ch measure so long RCA
            answers don't stretch into fatiguing 120-char lines on wide
            docked panels. */}
        <div className="relative px-3.5 py-2.5 rounded-xl rounded-tl-md bg-kobi-card border border-kobi-border-accent shadow-[inset_0_1px_0_rgba(255,255,255,0.04)] text-[15px] text-kobi-text break-words min-w-0 max-w-[min(65ch,100%)] overflow-hidden">
          {message.content ? (
            <MarkdownRenderer content={message.content} />
          ) : (
            <span className="text-kobi-text-tertiary italic">...</span>
          )}
          {message.content && (
            <button
              onClick={handleCopyMessage}
              title={copied ? 'Copied!' : 'Copy message'}
              className="absolute top-1.5 right-1.5 flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-mono text-kobi-text-tertiary bg-kobi-elevated/95 hover:text-kobi-accent hover:bg-kobi-elevated opacity-0 group-hover:opacity-100 transition-all"
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
        {/* Kobi-side tail — same diamond as the user bubble, card bg. */}
        <span
          aria-hidden
          className="absolute -left-[5px] top-2 w-2.5 h-2.5 rotate-45 rounded-[1px] bg-kobi-card border-b border-l border-kobi-border-accent"
        />
      </div>
    </div>
  )
}

// ToolCardLane wraps a column of ToolCallCards in the same horizontal lane
// the assistant MessageBubble uses: a 24px left gutter (where the assistant
// avatar would sit) and a max-w-[95%] right edge. Without this, the cards
// span the full panel width and break visual continuity with the bubble
// they belong to.
function ToolCardLane({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex justify-start gap-2 max-w-[95%] min-w-0">
      <div className="w-6 shrink-0" aria-hidden />
      <div className="flex flex-col gap-1.5 flex-1 min-w-0">{children}</div>
    </div>
  )
}

function ToolCallIndicator({ toolName }: { toolName: string }) {
  // Same labelling as ToolCallCard: a cloud connector read says whose cloud.
  const ext = externalTool(toolName)
  const label = ext ? ext.label : toolName.replace(/_/g, ' ')
  return (
    <div className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-kobi-bg border border-kobi-border text-[10px] font-mono text-kobi-text-tertiary">
      {ext ? (
        <span className="w-3 h-3 shrink-0 inline-flex items-center justify-center">
          <CloudProviderIcon provider={ext.connectorId} className="w-3 h-3" />
        </span>
      ) : (
        <Wrench className="w-3 h-3 text-kobi-accent" />
      )}
      <span>{label}</span>
      <Loader2 className="w-3 h-3 animate-spin ml-auto" />
    </div>
  )
}

// ExternalSourcesNote — the per-answer line that says Kobi left the cluster
// to reach this answer, and on what terms. Quiet by design (tertiary, mono,
// no border): it qualifies the answer, it is not part of it.
function ExternalSourcesNote({ connectorIds }: { connectorIds: string[] }) {
  return (
    <div className="flex justify-start gap-2 max-w-[95%] min-w-0">
      <div className="w-6 shrink-0" aria-hidden />
      <div className="flex items-center gap-1.5 min-w-0 text-[10px] font-mono text-kobi-text-tertiary">
        <ShieldCheck className="w-3 h-3 shrink-0" aria-hidden />
        <span className="flex items-center gap-1 shrink-0" aria-hidden>
          {connectorIds.map((id) => (
            <CloudProviderIcon key={id} provider={id} mono className="w-3 h-3" />
          ))}
        </span>
        <span className="min-w-0">
          Read from {externalSourcesLabel(connectorIds)} · read-only · credentials never shared with the model
        </span>
      </div>
    </div>
  )
}

// StreamStatusHint — adaptive in-flight indicator. It follows what the turn is
// waiting on (livePhase) and lastActivityAt (bumped on every stream event):
//   • the model writing a round → the "Thinking" dots before the first one,
//     "Analyzing N results" with its clock after a round's results: a round
//     that reads many results and writes the final answer takes tens of
//     seconds, and is not a stall
//   • a tool call running → nothing: its card spins and, past a few seconds,
//     counts its own time (ToolCallCard), so a slow read shows where it is
//   • anything else, recent activity → nothing (the text itself is the progress)
//   • quiet past those → a subtle "Waiting…" / "Taking longer than usual…"
//     line, so a slow network or a hung upstream doesn't read as a frozen
//     response. Cancelling is always one click away via the input's Stop
//     button. Ticks once a second so the thresholds advance on their own.
const IDLE_HINT_MS = 4000
const LONG_WAIT_MS = 15000
// A model round past this is unusual even for the final answer.
const MODEL_LONG_WAIT_MS = 60000

function StreamStatusHint() {
  const { isLoading, lastActivityAt, messages } = useCopilot()
  const [, setTick] = useState(0)
  useEffect(() => {
    if (!isLoading) return
    const id = window.setInterval(() => setTick((t) => t + 1), 1000)
    return () => window.clearInterval(id)
  }, [isLoading])

  if (!isLoading) return null
  const phase = livePhase(messages)
  const idleMs = lastActivityAt ? Date.now() - lastActivityAt : 0

  if (phase === 'model' && idleMs < MODEL_LONG_WAIT_MS) {
    // After a round's results, the model is reading them: say how many, and
    // for how long (since the last of them came back).
    const last = messages[messages.length - 1]
    const read = last?.role === 'user' ? (last.toolResults?.length ?? 0) : 0
    if (read === 0) return <ThinkingIndicator />
    return <ThinkingIndicator label={`Analyzing ${read} result${read === 1 ? '' : 's'}`} elapsedMs={idleMs} />
  }
  if (phase === 'tools') return null
  // Mid-stream and still flowing — the text is the signal, no extra chrome.
  if (phase === 'idle' && idleMs < IDLE_HINT_MS) return null

  const longWait = idleMs >= LONG_WAIT_MS
  return (
    <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-kobi-bg border border-kobi-border w-fit text-[11px] text-kobi-text-tertiary">
      <Loader2 className="w-3 h-3 animate-spin shrink-0" />
      <span className="font-mono">
        {longWait ? 'Taking longer than usual…' : 'Waiting for response…'}
      </span>
    </div>
  )
}

// ThinkingIndicator — three staggered dots + a shimmering label,
// shown while the assistant has emitted no content yet (between
// the user submit and the first token / tool call). The single-
// spinner version it replaces read as "loading something" rather
// than "actively reasoning"; the dots + color flow communicate
// activity without overpromising progress.
function ThinkingIndicator({ label = 'Thinking', elapsedMs }: { label?: string; elapsedMs?: number }) {
  return (
    <div className="flex items-center gap-2.5 px-3 py-2 rounded-lg bg-kobi-bg border border-kobi-border w-fit">
      <div className="flex items-center gap-1">
        <span
          className="w-1.5 h-1.5 rounded-full bg-kobi-accent animate-kb-ai-thinking-dot"
          style={{ animationDelay: '0s' }}
        />
        <span
          className="w-1.5 h-1.5 rounded-full bg-kobi-accent animate-kb-ai-thinking-dot"
          style={{ animationDelay: '0.18s' }}
        />
        <span
          className="w-1.5 h-1.5 rounded-full bg-kobi-accent animate-kb-ai-thinking-dot"
          style={{ animationDelay: '0.36s' }}
        />
      </div>
      <span className="kb-ai-shimmer-text text-[11px] font-mono">{label}</span>
      {elapsedMs !== undefined && (
        <span className="text-[11px] font-mono tabular-nums text-kobi-text-tertiary">{formatClock(elapsedMs)}</span>
      )}
    </div>
  )
}

// MaxRoundsNoticeBubble marks where a turn hit the tool-call step limit and
// offers a Continue control that resumes the investigation with a fresh round
// budget (a new user turn on the same conversation). Deterministic — it renders
// regardless of what the model's closing text said, so the operator always sees
// that Kobi paused on the limit rather than froze.
function MaxRoundsNoticeBubble({ limit }: { limit?: number }) {
  const { sendMessage, isLoading } = useCopilot()
  return (
    <div className="flex items-center gap-2 px-3 py-2 rounded-lg border border-dashed border-kobi-st-warn/40 bg-kobi-st-warn/5 text-[10px] font-mono text-kobi-text-secondary">
      <Hourglass className="w-3 h-3 text-kobi-st-warn shrink-0" />
      <span className="text-kobi-st-warn font-semibold uppercase tracking-wider">Step limit reached</span>
      <span className="text-kobi-text-tertiary">·</span>
      <span>
        Kobi paused after {limit ?? 'the'} tool step{limit === 1 ? '' : 's'} before finishing.
      </span>
      <button
        type="button"
        disabled={isLoading}
        onClick={() =>
          sendMessage('Continue the investigation from where you left off — run the next step you named.', {
            trigger: 'continue_after_max_rounds',
          })
        }
        className="ml-auto inline-flex items-center gap-1 text-kobi-st-warn hover:underline disabled:opacity-40 disabled:no-underline disabled:cursor-not-allowed"
      >
        Continue
        <ArrowRight className="w-3 h-3" />
      </button>
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
    <div className="flex items-center gap-2 px-3 py-2 rounded-lg border border-dashed border-kobi-accent/30 bg-gradient-to-r from-kb-accent-light via-kb-accent-light/40 to-violet-500/5 text-[10px] font-mono text-kobi-text-secondary">
      <Sparkles className="w-3 h-3 text-kobi-accent shrink-0" />
      <span className="text-kobi-accent font-semibold uppercase tracking-wider">{title}</span>
      <span className="text-kobi-text-tertiary">·</span>
      <span>{action}</span>
      {meta.turnsFolded > 0 && meta.toolResultsStubbed > 0 && (
        <>
          <span className="text-kobi-text-tertiary">+</span>
          <span>
            {meta.toolResultsStubbed} tool result{meta.toolResultsStubbed === 1 ? '' : 's'} stubbed
          </span>
        </>
      )}
      {meta.tokensBefore > 0 && (
        <>
          <span className="text-kobi-text-tertiary">·</span>
          <span>
            {formatTokens(meta.tokensBefore)} → {formatTokens(meta.tokensAfter)}
            {pct > 0 && <span className="text-kobi-accent ml-1">(−{pct}%)</span>}
          </span>
        </>
      )}
      {meta.model && (
        <span className="ml-auto text-kobi-text-tertiary truncate" title={meta.model}>
          {meta.model}
        </span>
      )}
    </div>
  )
}

