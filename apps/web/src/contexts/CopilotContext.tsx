import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { sendCopilotChat } from '@/services/copilot/chat'
import type { CopilotMessage, CopilotConfig } from '@/services/copilot/types'

interface CopilotContextValue {
  config?: CopilotConfig
  isOpen: boolean
  isLoading: boolean
  error: string | null
  messages: CopilotMessage[]
  pendingToolCalls: string[]
  usedFallback: boolean
  openPanel: () => void
  closePanel: () => void
  togglePanel: () => void
  sendMessage: (text: string) => Promise<void>
  clearHistory: () => void
}

const CopilotContext = createContext<CopilotContextValue | null>(null)

function generateId(): string {
  return `${Date.now()}-${Math.random().toString(36).slice(2, 9)}`
}

export function CopilotProvider({ children }: { children: ReactNode }) {
  const location = useLocation()
  const [isOpen, setIsOpen] = useState(false)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [messages, setMessages] = useState<CopilotMessage[]>([])
  const [pendingToolCalls, setPendingToolCalls] = useState<string[]>([])
  const [usedFallback, setUsedFallback] = useState(false)

  const { data: config } = useQuery({
    queryKey: ['copilot-config'],
    queryFn: api.getCopilotConfig,
    staleTime: 60_000,
    retry: false,
  })

  const sendMessage = useCallback(
    async (text: string) => {
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

      // Pre-create the assistant message that will accumulate streamed text
      const assistantId = generateId()
      let assistantText = ''
      setMessages((prev) => [
        ...prev,
        { id: assistantId, role: 'assistant', content: '', timestamp: new Date() },
      ])

      try {
        for await (const event of sendCopilotChat(newMessages, location.pathname)) {
          if (event.type === 'meta' && event.fallback) {
            setUsedFallback(true)
          } else if (event.type === 'tool_call' && event.toolName) {
            setPendingToolCalls((prev) => [...prev, event.toolName as string])
          } else if (event.type === 'text' && event.text) {
            assistantText += event.text
            setMessages((prev) =>
              prev.map((m) => (m.id === assistantId ? { ...m, content: assistantText } : m)),
            )
          } else if (event.type === 'error') {
            setError(event.error || 'Unknown error from copilot')
          } else if (event.type === 'done') {
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
    [messages, isLoading, location.pathname],
  )

  const openPanel = useCallback(() => setIsOpen(true), [])
  const closePanel = useCallback(() => setIsOpen(false), [])
  const togglePanel = useCallback(() => setIsOpen((v) => !v), [])
  const clearHistory = useCallback(() => {
    setMessages([])
    setError(null)
    setPendingToolCalls([])
    setUsedFallback(false)
  }, [])

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
        openPanel,
        closePanel,
        togglePanel,
        sendMessage,
        clearHistory,
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
