import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { compactCopilotSession, sendCopilotChat } from '@/services/copilot/chat'
import { useCopilotLayout } from '@/hooks/useCopilotLayout'
import type { CopilotMessage, CopilotConfig, CopilotUsage } from '@/services/copilot/types'

/** Inline compaction notice rendered in the chat transcript. */
export interface CompactNotice {
  turnsFolded: number
  toolResultsStubbed: number
  tokensBefore: number
  tokensAfter: number
  model?: string
  auto: boolean // true = auto-compact mid-session, false = manual "new session"
}

interface CopilotContextValue {
  config?: CopilotConfig
  isOpen: boolean
  isLoading: boolean
  error: string | null
  messages: CopilotMessage[]
  pendingToolCalls: string[]
  usedFallback: boolean
  sessionUsage: CopilotUsage | null
  sessionRounds: number
  compactNotices: CompactNotice[]
  isCompacting: boolean
  /** Usage the provider reported for the most recent round. Reflects the
   * true context the LLM saw (including tool history, system prompt and
   * tool definitions), unlike a client-side approximation of messages[]. */
  lastRoundUsage: CopilotUsage | null
  /** Panel layout (docked vs floating + sizes). Lives on the context
   * — not a standalone hook — so multiple subscribers (CopilotPanel
   * itself, Layout's content-reservation logic, a future toolbar
   * dock indicator) see the same state without a separate store. */
  layout: ReturnType<typeof useCopilotLayout>
  openPanel: () => void
  closePanel: () => void
  togglePanel: () => void
  sendMessage: (text: string, options?: SendMessageOptions) => Promise<void>
  clearHistory: () => void
  /** Trigger manual compaction. resetAll=true starts a fresh session keeping only a summary. */
  compactSession: (resetAll?: boolean) => Promise<void>
}

export interface SendMessageOptions {
  /** Origin of the message — propagated to backend logs for adoption analytics. */
  trigger?: string
}

const CopilotContext = createContext<CopilotContextValue | null>(null)

function generateId(): string {
  return `${Date.now()}-${Math.random().toString(36).slice(2, 9)}`
}

export function CopilotProvider({ children }: { children: ReactNode }) {
  const location = useLocation()
  // Single source of truth for panel layout — consumers read via
  // useCopilot().layout so panel + content-reservation stay in sync.
  const layout = useCopilotLayout()
  const [isOpen, setIsOpen] = useState(false)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [messages, setMessages] = useState<CopilotMessage[]>([])
  const [pendingToolCalls, setPendingToolCalls] = useState<string[]>([])
  const [usedFallback, setUsedFallback] = useState(false)
  const [sessionUsage, setSessionUsage] = useState<CopilotUsage | null>(null)
  const [sessionRounds, setSessionRounds] = useState(0)
  const [compactNotices, setCompactNotices] = useState<CompactNotice[]>([])
  const [isCompacting, setIsCompacting] = useState(false)
  // Usage reported by the provider for the most recent round. This is the
  // real context size the LLM saw (user + assistant + tool history + system
  // prompt + tool definitions) — much more accurate than approximating from
  // client-side messages[]. Total context = inputTokens + cacheReadTokens +
  // cacheCreationTokens.
  const [lastRoundUsage, setLastRoundUsage] = useState<CopilotUsage | null>(null)

  const { data: config } = useQuery({
    queryKey: ['copilot-config'],
    queryFn: api.getCopilotConfig,
    staleTime: 60_000,
    retry: false,
  })

  const sendMessage = useCallback(
    async (text: string, options?: SendMessageOptions) => {
      const trimmed = text.trim()
      if (!trimmed || isLoading) return

      const userMsg: CopilotMessage = {
        id: generateId(),
        role: 'user',
        content: trimmed,
        timestamp: new Date(),
      }

      // Append the user message to history
      const newMessages = [...messages, userMsg]
      setMessages(newMessages)
      setIsLoading(true)
      setError(null)
      setPendingToolCalls([])
      setUsedFallback(false)
      setSessionUsage(null)
      setSessionRounds(0)

      // Pre-create the assistant message that will accumulate streamed text
      const assistantId = generateId()
      let assistantText = ''
      setMessages((prev) => [
        ...prev,
        { id: assistantId, role: 'assistant', content: '', timestamp: new Date() },
      ])

      // Snapshot the last round's provider-reported usage BEFORE we clear
      // it below. The backend uses it to seed its auto-compact check so
      // the trigger fires on round 0 of a follow-up question, matching
      // what the UI already shows.
      const carriedLastRoundUsage = lastRoundUsage

      try {
        for await (const event of sendCopilotChat(
          newMessages,
          location.pathname,
          undefined,
          options?.trigger,
          carriedLastRoundUsage,
        )) {
          if (event.type === 'meta' && event.fallback) {
            setUsedFallback(true)
          } else if (event.type === 'tool_call' && event.toolName) {
            setPendingToolCalls((prev) => [...prev, event.toolName as string])
          } else if (event.type === 'text' && event.text) {
            assistantText += event.text
            setMessages((prev) =>
              prev.map((m) => (m.id === assistantId ? { ...m, content: assistantText } : m)),
            )
          } else if (event.type === 'usage' && event.session) {
            setSessionUsage(event.session)
            if (typeof event.round === 'number') {
              setSessionRounds(event.round + 1)
            }
            if (event.turn) {
              setLastRoundUsage(event.turn)
            }
          } else if (event.type === 'compact' && typeof event.turnsFolded === 'number') {
            const notice: CompactNotice = {
              turnsFolded: event.turnsFolded,
              toolResultsStubbed: event.toolResultsStubbed ?? 0,
              tokensBefore: event.tokensBefore ?? 0,
              tokensAfter: event.tokensAfter ?? 0,
              model: event.model,
              auto: true,
            }
            setCompactNotices((prev) => [...prev, notice])
            setMessages((prev) => [
              ...prev,
              {
                id: generateId(),
                role: 'system',
                content: '',
                timestamp: new Date(),
                kind: 'compact-notice',
                compactMeta: notice,
              },
            ])
          } else if (event.type === 'error') {
            setError(event.error || 'Unknown error from copilot')
          } else if (event.type === 'done') {
            // Replace the frontend transcript with the server's full
            // messages array so tool_calls and tool_results from this
            // question persist into subsequent turns. This matches the
            // accumulative context model used by Anthropic/OpenAI and
            // allows the auto-compact trigger to see the real context
            // size on the next request.
            if (Array.isArray(event.messages)) {
              setMessages((prev) => {
                const notices = prev.filter((m) => m.kind === 'compact-notice')
                const rebuilt: CopilotMessage[] = event.messages!.map((m, idx) => ({
                  id: `srv-${Date.now()}-${idx}`,
                  role: m.role,
                  content: m.content ?? '',
                  toolCalls: m.toolCalls,
                  toolResults: m.toolResults,
                  timestamp: new Date(),
                }))
                // Keep compact notices visible at the end so the user
                // still sees that compaction happened.
                return [...rebuilt, ...notices]
              })
            }
            break
          }
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to reach copilot backend')
      } finally {
        setIsLoading(false)
        setPendingToolCalls([])
      }
    },
    [messages, isLoading, location.pathname, lastRoundUsage],
  )

  const openPanel = useCallback(() => setIsOpen(true), [])
  const closePanel = useCallback(() => setIsOpen(false), [])
  const togglePanel = useCallback(() => setIsOpen((v) => !v), [])
  const clearHistory = useCallback(() => {
    setMessages([])
    setError(null)
    setPendingToolCalls([])
    setUsedFallback(false)
    setSessionUsage(null)
    setSessionRounds(0)
    setCompactNotices([])
    setLastRoundUsage(null)
  }, [])

  const compactSession = useCallback(
    async (resetAll = true) => {
      if (isCompacting || isLoading) return
      const chatMessages = messages.filter((m) => m.kind !== 'compact-notice')
      if (chatMessages.length === 0) return
      setIsCompacting(true)
      setError(null)
      try {
        const resp = await compactCopilotSession(chatMessages, resetAll)
        const notice: CompactNotice = {
          turnsFolded: resp.turnsFolded,
          toolResultsStubbed: resp.toolResultsStubbed ?? 0,
          tokensBefore: resp.tokensBefore,
          tokensAfter: resp.tokensAfter,
          model: resp.model,
          auto: false,
        }
        setCompactNotices((prev) => [...prev, notice])
        // Replace the chat transcript with the compacted messages, then
        // append a visible notice so the user understands the reset.
        const rebuilt: CopilotMessage[] = resp.messages.map((m, idx) => ({
          id: `compact-${Date.now()}-${idx}`,
          role: m.role,
          content: m.content ?? '',
          toolCalls: m.toolCalls,
          toolResults: m.toolResults,
          timestamp: new Date(),
        }))
        rebuilt.push({
          id: generateId(),
          role: 'system',
          content: '',
          timestamp: new Date(),
          kind: 'compact-notice',
          compactMeta: notice,
        })
        setMessages(rebuilt)
        setSessionUsage(null)
        setSessionRounds(0)
        setLastRoundUsage(null)
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to compact session')
      } finally {
        setIsCompacting(false)
      }
    },
    [messages, isCompacting, isLoading],
  )

  // Cmd+J / Ctrl+J shortcut to toggle the panel (if enabled)
  useEffect(() => {
    if (!config?.enabled) return
    function handleKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === 'j') {
        e.preventDefault()
        setIsOpen((v) => !v)
      }
    }
    document.addEventListener('keydown', handleKey)
    return () => document.removeEventListener('keydown', handleKey)
  }, [config?.enabled])

  return (
    <CopilotContext.Provider
      value={{
        config,
        isOpen,
        isLoading,
        error,
        messages,
        pendingToolCalls,
        usedFallback,
        sessionUsage,
        sessionRounds,
        compactNotices,
        isCompacting,
        lastRoundUsage,
        layout,
        openPanel,
        closePanel,
        togglePanel,
        sendMessage,
        clearHistory,
        compactSession,
      }}
    >
      {children}
    </CopilotContext.Provider>
  )
}

export function useCopilot(): CopilotContextValue {
  const ctx = useContext(CopilotContext)
  if (!ctx) {
    throw new Error('useCopilot must be used within a CopilotProvider')
  }
  return ctx
}
