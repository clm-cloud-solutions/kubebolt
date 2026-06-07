import { createContext, useContext, useState, useCallback, useEffect, useRef, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/services/api'
import { compactCopilotSession, sendCopilotChat, serializeMessages } from '@/services/copilot/chat'
import { useCopilotLayout } from '@/hooks/useCopilotLayout'
import { useAuth } from '@/contexts/AuthContext'
import type { CopilotMessage, CopilotConfig, CopilotUsage, ConversationDetail } from '@/services/copilot/types'

/** localStorage key holding the id of the conversation currently open, so a
 * browser refresh re-fetches and resumes it instead of starting blank. */
const ACTIVE_CONVERSATION_KEY = 'kubebolt-copilot-active-conversation'

/** Query key for the conversation history list — shared so the drawer and the
 * context's post-send invalidation stay in sync. */
export const CONVERSATIONS_QUERY_KEY = ['copilot-conversations'] as const

/** Metadata of a just-resumed conversation, used to drive the "stale context"
 * banner (age + cluster the chat ran against). */
export interface StaleResumeInfo {
  clusterId: string
  updatedAt: string
}

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
  /** Epoch ms of the last stream event for the in-flight turn, or null when
   * idle. The UI ticks against it to tell "actively streaming" from "stalled"
   * and to escalate a waiting hint. */
  lastActivityAt: number | null
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
  /** Abort the in-flight turn. Keeps whatever streamed so far; no error. */
  cancelMessage: () => void
  clearHistory: () => void
  /** Trigger manual compaction. resetAll=true starts a fresh session keeping only a summary. */
  compactSession: (resetAll?: boolean) => Promise<void>
  /** Record that the user acted on an action proposal card. Mutates the
   * tool result's content to include executionStatus/executionResult so
   * the LLM, on the next turn, sees the outcome and won't re-propose the
   * same action. Called from ActionProposalCard on success/error/dismissed. */
  recordProposalOutcome: (
    toolCallId: string,
    outcome: 'executed' | 'failed' | 'dismissed',
    resultSummary?: string,
  ) => void
  /** Record that the post-Execute progress poller has reached terminal
   * state. Persisted into the proposal so a re-mount (caused by a
   * follow-up message rebuilding messages[]) doesn't restart polling
   * against the cluster — the action already landed; the poller was UX. */
  recordProposalProgressSettled: (toolCallId: string) => void
  /** Record that the post-Execute poller hit the progress timeout with the
   * action applied-but-not-converged. Latches progressSettled (so re-mounts
   * don't re-poll) AND stamps progressOutcome='stalled' + the last-observed
   * progressDetail, so the card renders an honest terminal state instead of
   * an endless spinner. The auto-investigation is fired separately by the
   * caller via sendMessage(action_stalled). */
  recordProposalStalled: (toolCallId: string, detail: string) => void
  // ─── Conversation history (persist + resume) ───
  /** Id of the conversation currently open, or null for an unsaved fresh one.
   * The server mints it on the first turn; the client persists a pointer to it
   * so a refresh resumes the same conversation. */
  conversationId: string | null
  /** Title of the open conversation (auto-generated, renamable). */
  conversationTitle: string | null
  /** Set right after resuming a conversation so the panel can warn that the
   * cluster state may have moved on. Cleared on the next send or on dismiss. */
  staleResume: StaleResumeInfo | null
  /** Load a past conversation by id, rehydrate the transcript, and open the panel. */
  resumeConversation: (id: string) => Promise<void>
  /** Start a brand-new conversation (clears the transcript + the saved pointer).
   * The previous one is already persisted server-side. */
  newConversation: () => void
  /** Rename the open conversation (persists + updates the header). */
  renameActiveConversation: (title: string) => Promise<void>
  /** Dismiss the stale-context banner without sending a message. */
  dismissStaleResume: () => void
  /** Context name of the active cluster (matches a conversation's clusterId).
   * The history list filters to this by default — conversations are
   * cluster-bound. null while clusters are loading or none is active. */
  activeClusterContext: string | null
}

export interface SendMessageOptions {
  /** Origin of the message — propagated to backend logs for adoption analytics. */
  trigger?: string
  /** When the message originates from an insight, its stable fingerprint —
   * stored on the conversation so the insight detail can deep-link back. */
  originatingInsightId?: string
}

const CopilotContext = createContext<CopilotContextValue | null>(null)

function generateId(): string {
  return `${Date.now()}-${Math.random().toString(36).slice(2, 9)}`
}

// Client-only fields written onto an action_proposal tool result AFTER it was
// emitted: the execution outcome (recordProposalOutcome) and the progress
// lifecycle (recordProposalProgressSettled / recordProposalStalled). These
// must survive the 'done' event's transcript rebuild, which replaces messages
// with the server's echo — and that echo predates any annotation applied
// during the SAME turn. The stall mutation is the prime victim: it's written
// right after the auto-investigation turn fires, so the server never saw it,
// and without this carry-over it was wiped on every 'done', re-arming the
// poller and re-triggering the root-cause investigation on a loop.
const PROPOSAL_ANNOTATION_FIELDS = [
  'executionStatus',
  'executionResult',
  'executedAt',
  'progressSettled',
  'progressOutcome',
  'progressDetail',
] as const

function carryProposalAnnotations(
  prev: CopilotMessage[],
  rebuilt: CopilotMessage[],
): CopilotMessage[] {
  const prevByCall = new Map<string, string>() // toolCallId → content
  for (const m of prev) {
    for (const tr of m.toolResults ?? []) prevByCall.set(tr.toolCallId, tr.content)
  }
  if (prevByCall.size === 0) return rebuilt
  return rebuilt.map((m) => {
    if (!m.toolResults || m.toolResults.length === 0) return m
    let mutated = false
    const merged = m.toolResults.map((tr) => {
      const prevContent = prevByCall.get(tr.toolCallId)
      if (!prevContent) return tr
      try {
        const cur = JSON.parse(tr.content)
        const old = JSON.parse(prevContent)
        if (cur?.kind !== 'action_proposal' || old?.kind !== 'action_proposal') return tr
        const carried: Record<string, unknown> = {}
        for (const k of PROPOSAL_ANNOTATION_FIELDS) {
          if (old[k] !== undefined && cur[k] === undefined) carried[k] = old[k]
        }
        if (Object.keys(carried).length === 0) return tr
        mutated = true
        return { ...tr, content: JSON.stringify({ ...cur, ...carried }) }
      } catch {
        return tr
      }
    })
    return mutated ? { ...m, toolResults: merged } : m
  })
}

export function CopilotProvider({ children }: { children: ReactNode }) {
  const location = useLocation()
  // Auth state — used to gate the copilot config fetch until the auth
  // context has settled, so the request always carries a token (or
  // skips entirely when auth is still booting).
  const auth = useAuth()
  // Single source of truth for panel layout — consumers read via
  // useCopilot().layout so panel + content-reservation stay in sync.
  const layout = useCopilotLayout()
  const [isOpen, setIsOpen] = useState(false)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Cancel + latency feedback for the in-flight turn. abortRef holds the live
  // AbortController so cancelMessage() can stop the stream; lastActivityAt is
  // bumped on every stream event so the UI can tell "still flowing" from
  // "stalled" and surface a waiting/cancel affordance.
  const abortRef = useRef<AbortController | null>(null)
  const [lastActivityAt, setLastActivityAt] = useState<number | null>(null)
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

  // ─── Conversation persistence + resume ───
  const queryClient = useQueryClient()
  // Active cluster context. Conversations are scoped to the cluster they ran
  // against (Kobi's tool results are cluster-bound), so the resume pointer and
  // the history list are keyed/filtered by it. Shares the ['clusters'] query
  // with the Topbar (react-query dedupes), so switching clusters there flips
  // this here too. `.context` matches the backend's ConversationRecord.ClusterID.
  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    enabled: !auth.isLoading,
    staleTime: 30_000,
  })
  const activeClusterContext = clusters?.find((c) => c.active)?.context ?? null

  // The active user OWNS their conversations; each conversation also belongs to
  // a cluster. We key the localStorage resume pointer by (user, cluster) so two
  // users — or one user across two clusters — never resume each other's
  // conversation, and we reset in-memory state whenever that scope changes
  // (login / logout / account switch / cluster switch). "anon"/"none" cover the
  // auth-disabled and no-active-cluster cases (stable keys).
  const userKey = auth.user?.id ?? 'anon'
  const clusterKey = activeClusterContext ?? 'none'
  const scopeKey = `${userKey}::${clusterKey}`
  const pointerStorageKey = `${ACTIVE_CONVERSATION_KEY}:${scopeKey}`
  const [conversationId, setConversationId] = useState<string | null>(null)
  const [conversationTitle, setConversationTitle] = useState<string | null>(null)
  const [staleResume, setStaleResume] = useState<StaleResumeInfo | null>(null)
  // Guards the one-shot rehydrate so it doesn't re-run on every config/auth
  // state change. Reset to false on a user change so the new user rehydrates.
  const [hydrated, setHydrated] = useState(false)

  const persistActivePointer = useCallback(
    (id: string | null) => {
      try {
        if (id) localStorage.setItem(pointerStorageKey, id)
        else localStorage.removeItem(pointerStorageKey)
      } catch {
        // storage disabled / quota — pointer is a convenience, not load-bearing
      }
    },
    [pointerStorageKey],
  )

  // resetConversationState clears the in-memory transcript WITHOUT touching any
  // user's localStorage pointer — used on a user change so the next user never
  // sees the previous user's conversation, while each user's saved pointer
  // survives for when they return.
  const resetConversationState = useCallback(() => {
    setMessages([])
    setError(null)
    setPendingToolCalls([])
    setUsedFallback(false)
    setSessionUsage(null)
    setSessionRounds(0)
    setCompactNotices([])
    setLastRoundUsage(null)
    setConversationId(null)
    setConversationTitle(null)
    setStaleResume(null)
  }, [])

  // hydrateFromRecord replaces the in-memory transcript with a persisted
  // conversation. Used by both resume-from-list and rehydrate-on-mount. The
  // stored messages already carry tool calls/results (incl. the executed-state
  // mutations on action proposals), so the rich cards rehydrate verbatim and
  // terminal proposals render settled rather than re-polling the cluster.
  const hydrateFromRecord = useCallback(
    (rec: ConversationDetail) => {
      const rebuilt: CopilotMessage[] = (rec.messages ?? []).map((m, idx) => ({
        id: `srv-${rec.id}-${idx}`,
        role: m.role,
        content: m.content ?? '',
        toolCalls: m.toolCalls,
        toolResults: m.toolResults,
        timestamp: m.timestamp ? new Date(m.timestamp) : new Date(),
      }))
      setMessages(rebuilt)
      setConversationId(rec.id)
      setConversationTitle(rec.title || null)
      setLastRoundUsage(rec.lastRoundUsage ?? null)
      setError(null)
      setPendingToolCalls([])
      setUsedFallback(false)
      setSessionUsage(null)
      setSessionRounds(0)
      setCompactNotices([])
      setStaleResume(rec.updatedAt ? { clusterId: rec.clusterId, updatedAt: rec.updatedAt } : null)
      persistActivePointer(rec.id)
    },
    [persistActivePointer],
  )


  // Wait for auth init to settle before asking for the copilot config.
  // Earlier this query had `retry: false` AND fired immediately on mount,
  // so when CopilotProvider mounted before AuthProvider had finished its
  // silent-refresh (race during page load), the request went out without
  // a token, returned 401, and stayed in error state forever — the user
  // had to refresh the page for the copilot toggle to appear.
  //
  // Now: gate on `!auth.isLoading` so the fetch waits until auth is
  // settled (token attached if user is signed in, or auth confirmed
  // disabled). Drop the explicit `retry: false` so we inherit the
  // queryClient default (2 retries on transient network errors; auth
  // and cluster errors are still not retried).
  const { data: config } = useQuery({
    queryKey: ['copilot-config'],
    queryFn: api.getCopilotConfig,
    staleTime: 60_000,
    enabled: !auth.isLoading,
  })

  // Rehydrate the last-open conversation once, after auth + config settle, so
  // a browser refresh / re-login lands back in the same conversation instead
  // of a blank panel. A stale pointer (deleted/expired conversation → 404)
  // just clears silently.
  useEffect(() => {
    if (hydrated || auth.isLoading || !config?.enabled) return
    let cancelled = false
    let pointer: string | null = null
    try {
      pointer = localStorage.getItem(pointerStorageKey)
    } catch {
      pointer = null
    }
    if (!pointer) {
      setHydrated(true)
      return
    }
    api
      .getConversation(pointer)
      .then((rec) => {
        if (!cancelled) hydrateFromRecord(rec)
      })
      .catch(() => {
        if (!cancelled) persistActivePointer(null)
      })
      .finally(() => {
        if (!cancelled) setHydrated(true)
      })
    return () => {
      cancelled = true
    }
  }, [hydrated, auth.isLoading, config?.enabled, pointerStorageKey, hydrateFromRecord, persistActivePointer])

  // Reset Kobi when the conversation scope changes — a different user
  // (login / logout / account switch) OR a different cluster (cluster switch).
  // The CopilotProvider is mounted at the app root and does NOT remount on an
  // SPA login or a cluster switch, so without this the previous scope's
  // in-memory transcript would stay visible (e.g. cluster A's conversation
  // showing while viewing cluster B). Clearing state + flipping `hydrated`
  // makes the rehydrate effect above re-run against the NEW (user, cluster)
  // pointer.
  const prevScopeKeyRef = useRef(scopeKey)
  useEffect(() => {
    if (prevScopeKeyRef.current === scopeKey) return
    prevScopeKeyRef.current = scopeKey
    resetConversationState()
    setHydrated(false)
  }, [scopeKey, resetConversationState])

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

      // Fresh AbortController for this turn so the user can stop it.
      const ac = new AbortController()
      abortRef.current = ac

      // Append the user message to history
      const newMessages = [...messages, userMsg]
      setMessages(newMessages)
      setIsLoading(true)
      setLastActivityAt(Date.now())
      setError(null)
      setPendingToolCalls([])
      setUsedFallback(false)
      setSessionUsage(null)
      setSessionRounds(0)
      // Engaging the conversation clears the stale-resume warning.
      setStaleResume(null)
      // Provisional header title for a brand-new conversation so the panel
      // isn't blank until the server's auto-title lands (refined on the next
      // history-list refetch).
      if (!conversationId) {
        setConversationTitle(trimmed.length > 60 ? `${trimmed.slice(0, 60).trim()}…` : trimmed)
      }

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
          ac.signal,
          options?.trigger,
          carriedLastRoundUsage,
          conversationId,
          options?.originatingInsightId,
        )) {
          // Every event is a sign of life — bump so the UI can distinguish an
          // actively-streaming turn from a stalled one and escalate its hint.
          setLastActivityAt(Date.now())
          if (event.type === 'meta') {
            if (event.fallback) setUsedFallback(true)
            // The server hands back the conversation id up-front (new chats)
            // so a mid-stream refresh can already resume. Persist the pointer.
            if (event.conversationId) {
              setConversationId(event.conversationId)
              persistActivePointer(event.conversationId)
            }
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
                  timestamp: m.timestamp ? new Date(m.timestamp) : new Date(),
                }))
                // Carry client-only proposal annotations (execution outcome +
                // progress/stall lifecycle) from the pre-rebuild transcript so
                // the server echo doesn't wipe them — see carryProposalAnnotations.
                const preserved = carryProposalAnnotations(prev, rebuilt)
                // Keep compact notices visible at the end so the user
                // still sees that compaction happened.
                return [...preserved, ...notices]
              })
            }
            // Refresh the history drawer so the new/updated conversation (and
            // its server-refined title, on a slight delay) shows up.
            queryClient.invalidateQueries({ queryKey: CONVERSATIONS_QUERY_KEY })
            break
          }
        }
      } catch (err) {
        // User-initiated cancel (cancelMessage → ac.abort()) surfaces as an
        // AbortError. That's not a failure: keep whatever streamed so far
        // ("stop at the point it reached"), show no error banner, and tag the
        // assistant turn so the UI renders a "Stopped by you" marker (a partial
        // or empty answer must not read as completed or hung).
        if (ac.signal.aborted) {
          setMessages((prev) =>
            prev.map((m) => (m.id === assistantId ? { ...m, cancelled: true } : m)),
          )
        } else {
          setError(err instanceof Error ? err.message : 'Failed to reach copilot backend')
        }
      } finally {
        setIsLoading(false)
        setLastActivityAt(null)
        setPendingToolCalls([])
        // Only clear the controller if it's still ours (a newer turn may have
        // replaced it). Prevents a late finally from nulling an active stream.
        if (abortRef.current === ac) abortRef.current = null
      }
    },
    // location.pathname is read at call time inside sendCopilotChat, not
    // captured at definition — so it doesn't belong in the dep array (would
    // pointlessly recreate the callback on every route change).
    [messages, isLoading, lastRoundUsage, conversationId, persistActivePointer, queryClient],
  )

  // Stop the in-flight turn. The abort propagates to fetch → the stream throws
  // AbortError → sendMessage's catch ignores it and the finally resets loading.
  // Partial assistant text already streamed stays on screen.
  const cancelMessage = useCallback(() => {
    abortRef.current?.abort()
  }, [])

  const openPanel = useCallback(() => setIsOpen(true), [])
  const closePanel = useCallback(() => setIsOpen(false), [])
  const togglePanel = useCallback(() => setIsOpen((v) => !v), [])
  // newConversation starts a fresh chat: clear the transcript and forget the
  // saved pointer so a subsequent send mints a NEW conversation id. The prior
  // conversation is already persisted server-side and stays in the history.
  const newConversation = useCallback(() => {
    setMessages([])
    setError(null)
    setPendingToolCalls([])
    setUsedFallback(false)
    setSessionUsage(null)
    setSessionRounds(0)
    setCompactNotices([])
    setLastRoundUsage(null)
    setConversationId(null)
    setConversationTitle(null)
    setStaleResume(null)
    persistActivePointer(null)
  }, [persistActivePointer])

  // clearHistory is the legacy "start over" action; same semantics as starting
  // a new conversation now that transcripts persist.
  const clearHistory = newConversation

  // resumeConversation loads a past conversation by id and opens the panel.
  const resumeConversation = useCallback(
    async (id: string) => {
      try {
        setIsLoading(true)
        const rec = await api.getConversation(id)
        hydrateFromRecord(rec)
        setIsOpen(true)
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load conversation')
      } finally {
        setIsLoading(false)
      }
    },
    [hydrateFromRecord],
  )

  // renameActiveConversation persists a new title and updates the header.
  const renameActiveConversation = useCallback(
    async (title: string) => {
      const trimmed = title.trim()
      if (!conversationId || !trimmed) return
      const summary = await api.patchConversation(conversationId, { title: trimmed })
      setConversationTitle(summary.title || trimmed)
      queryClient.invalidateQueries({ queryKey: CONVERSATIONS_QUERY_KEY })
    },
    [conversationId, queryClient],
  )

  const dismissStaleResume = useCallback(() => setStaleResume(null), [])

  const compactSession = useCallback(
    async (resetAll = true) => {
      if (isCompacting || isLoading) return
      const chatMessages = messages.filter((m) => m.kind !== 'compact-notice')
      if (chatMessages.length === 0) return
      setIsCompacting(true)
      setError(null)
      try {
        const resp = await compactCopilotSession(chatMessages, resetAll, conversationId)
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
          timestamp: m.timestamp ? new Date(m.timestamp) : new Date(),
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
    [messages, isCompacting, isLoading, conversationId],
  )

  // Patch the tool result that emitted an action proposal with execution
  // metadata, so the LLM on the next turn sees the outcome (executed /
  // failed / dismissed) and won't propose the same action again. Without
  // this signal, the LLM has no way to know the user already acted on the
  // card — the proposal sits forever as "pending" in its mental model.
  // syncTranscript persists the current transcript outside a chat turn. It is
  // how an action-proposal outcome (Execute/Dismiss) reaches durable storage
  // BEFORE the next message — without it, a refresh after executing an action
  // would rehydrate the proposal without its executed state and re-offer the
  // Execute button against an already-run action. Best-effort + fire-and-forget;
  // the next chat turn persists the same transcript anyway.
  const syncTranscript = useCallback(
    (msgs: CopilotMessage[]) => {
      if (!conversationId) return
      api.patchConversation(conversationId, { messages: serializeMessages(msgs) }).catch(() => {
        /* best-effort */
      })
    },
    [conversationId],
  )

  const recordProposalOutcome = useCallback(
    (
      toolCallId: string,
      outcome: 'executed' | 'failed' | 'dismissed',
      resultSummary?: string,
    ) => {
      setMessages((prev) => {
        let didMutate = false
        const next = prev.map((m) => {
          if (!m.toolResults || m.toolResults.length === 0) return m
          let mutated = false
          const updatedResults = m.toolResults.map((tr) => {
            if (tr.toolCallId !== toolCallId) return tr
            try {
              const parsed = JSON.parse(tr.content)
              if (parsed && typeof parsed === 'object' && parsed.kind === 'action_proposal') {
                const augmented = {
                  ...parsed,
                  executionStatus: outcome,
                  executionResult: resultSummary ?? null,
                  executedAt: new Date().toISOString(),
                }
                mutated = true
                return { ...tr, content: JSON.stringify(augmented) }
              }
            } catch {
              // Not parseable JSON; leave as-is.
            }
            return tr
          })
          if (mutated) didMutate = true
          return mutated ? { ...m, toolResults: updatedResults } : m
        })
        if (didMutate) queueMicrotask(() => syncTranscript(next))
        return next
      })
    },
    [syncTranscript],
  )

  // Counterpart of recordProposalOutcome for the polling lifecycle. The
  // outcome ("executed") happens on Execute click; "settled" happens later
  // when the cluster actually reached the target state. We persist both
  // because they answer different questions on a re-mount: outcome tells
  // the LLM "the user decided"; settled tells the card "don't re-poll".
  const recordProposalProgressSettled = useCallback(
    (toolCallId: string) => {
      setMessages((prev) => {
        let didMutate = false
        const next = prev.map((m) => {
          if (!m.toolResults || m.toolResults.length === 0) return m
          let mutated = false
          const updatedResults = m.toolResults.map((tr) => {
            if (tr.toolCallId !== toolCallId) return tr
            try {
              const parsed = JSON.parse(tr.content)
              if (
                parsed &&
                typeof parsed === 'object' &&
                parsed.kind === 'action_proposal' &&
                !parsed.progressSettled
              ) {
                mutated = true
                return { ...tr, content: JSON.stringify({ ...parsed, progressSettled: true }) }
              }
            } catch {
              // not JSON — leave as-is
            }
            return tr
          })
          if (mutated) didMutate = true
          return mutated ? { ...m, toolResults: updatedResults } : m
        })
        if (didMutate) queueMicrotask(() => syncTranscript(next))
        return next
      })
    },
    [syncTranscript],
  )

  // Terminal counterpart for the non-convergence path. Latches the same
  // progressSettled bit recordProposalProgressSettled uses (so a re-mount
  // skips the poller) AND records progressOutcome='stalled' + the observed
  // detail so the card can render an honest "did not converge" state that
  // survives re-renders. Idempotent: skips if already stalled.
  const recordProposalStalled = useCallback(
    (toolCallId: string, detail: string) => {
      setMessages((prev) => {
        let didMutate = false
        const next = prev.map((m) => {
          if (!m.toolResults || m.toolResults.length === 0) return m
          let mutated = false
          const updatedResults = m.toolResults.map((tr) => {
            if (tr.toolCallId !== toolCallId) return tr
            try {
              const parsed = JSON.parse(tr.content)
              if (
                parsed &&
                typeof parsed === 'object' &&
                parsed.kind === 'action_proposal' &&
                parsed.progressOutcome !== 'stalled'
              ) {
                mutated = true
                return {
                  ...tr,
                  content: JSON.stringify({
                    ...parsed,
                    progressSettled: true,
                    progressOutcome: 'stalled',
                    progressDetail: detail,
                  }),
                }
              }
            } catch {
              // not JSON — leave as-is
            }
            return tr
          })
          if (mutated) didMutate = true
          return mutated ? { ...m, toolResults: updatedResults } : m
        })
        if (didMutate) queueMicrotask(() => syncTranscript(next))
        return next
      })
    },
    [syncTranscript],
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
        lastActivityAt,
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
        cancelMessage,
        clearHistory,
        compactSession,
        recordProposalOutcome,
        recordProposalProgressSettled,
        recordProposalStalled,
        conversationId,
        conversationTitle,
        staleResume,
        resumeConversation,
        newConversation,
        renameActiveConversation,
        dismissStaleResume,
        activeClusterContext,
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
