import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  X,
  Plus,
  Search,
  Trash2,
  Pencil,
  Lightbulb,
  Loader2,
  MessageSquare,
  Check,
} from 'lucide-react'
import { api } from '@/services/api'
import { useCopilot, CONVERSATIONS_QUERY_KEY } from '@/contexts/CopilotContext'
import type { ConversationSummary } from '@/services/copilot/types'

// Relative "x ago" with a single coarse unit — enough for a history list.
function timeAgo(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return ''
  const s = Math.max(0, Math.floor((Date.now() - then) / 1000))
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  if (d < 30) return `${d}d ago`
  const mo = Math.floor(d / 30)
  if (mo < 12) return `${mo}mo ago`
  return `${Math.floor(mo / 12)}y ago`
}

interface ConversationListProps {
  onClose: () => void
}

/**
 * ConversationList is the Kobi history drawer: search + browse past
 * conversations (per user), resume one, rename, or delete. Overlays the
 * message area inside the panel.
 */
export function ConversationList({ onClose }: ConversationListProps) {
  const { resumeConversation, newConversation, conversationId } = useCopilot()
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editTitle, setEditTitle] = useState('')

  const { data: conversations = [], isLoading } = useQuery({
    queryKey: [...CONVERSATIONS_QUERY_KEY, { q: search }],
    queryFn: () => api.listConversations({ q: search || undefined, limit: 100 }),
    staleTime: 10_000,
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deleteConversation(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: CONVERSATIONS_QUERY_KEY }),
  })

  const renameMut = useMutation({
    mutationFn: ({ id, title }: { id: string; title: string }) =>
      api.patchConversation(id, { title }),
    onSuccess: () => {
      setEditingId(null)
      queryClient.invalidateQueries({ queryKey: CONVERSATIONS_QUERY_KEY })
    },
  })

  const startRename = (c: ConversationSummary) => {
    setEditingId(c.id)
    setEditTitle(c.title)
  }
  const commitRename = (id: string) => {
    const t = editTitle.trim()
    if (t) renameMut.mutate({ id, title: t })
    else setEditingId(null)
  }

  return (
    <div className="absolute inset-0 z-20 flex flex-col bg-kb-card">
      {/* Drawer header */}
      <div className="flex items-center justify-between px-4 py-2.5 border-b border-kb-border shrink-0">
        <span className="text-sm font-semibold text-kb-text-primary">Conversations</span>
        <button
          onClick={onClose}
          title="Back to chat"
          className="p-1.5 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      {/* New + search */}
      <div className="px-3 py-2.5 space-y-2 border-b border-kb-border shrink-0">
        <button
          onClick={() => {
            newConversation()
            onClose()
          }}
          className="w-full flex items-center gap-2 px-3 py-2 rounded-lg bg-kb-accent-light text-kb-accent text-sm font-medium hover:opacity-90 transition-opacity"
        >
          <Plus className="w-4 h-4" />
          New conversation
        </button>
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-kb-text-tertiary" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search conversations…"
            className="w-full pl-8 pr-3 py-1.5 rounded-lg bg-kb-elevated border border-kb-border text-sm text-kb-text-primary placeholder:text-kb-text-tertiary focus:outline-none focus:border-kb-accent"
          />
        </div>
      </div>

      {/* List */}
      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="flex items-center justify-center py-10 text-kb-text-tertiary">
            <Loader2 className="w-5 h-5 animate-spin" />
          </div>
        ) : conversations.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-12 px-6 text-center">
            <MessageSquare className="w-8 h-8 text-kb-text-tertiary mb-2" />
            <p className="text-sm text-kb-text-secondary">
              {search ? 'No matching conversations' : 'No past conversations yet'}
            </p>
          </div>
        ) : (
          <ul className="py-1">
            {conversations.map((c) => (
              <li
                key={c.id}
                className={`group px-3 py-2 cursor-pointer transition-colors ${
                  c.id === conversationId ? 'bg-kb-accent-light' : 'hover:bg-kb-elevated'
                }`}
                onClick={() => {
                  if (editingId === c.id) return
                  resumeConversation(c.id)
                  onClose()
                }}
              >
                <div className="flex items-start gap-2">
                  <div className="min-w-0 flex-1">
                    {editingId === c.id ? (
                      <div className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
                        <input
                          autoFocus
                          value={editTitle}
                          onChange={(e) => setEditTitle(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter') commitRename(c.id)
                            if (e.key === 'Escape') setEditingId(null)
                          }}
                          className="flex-1 min-w-0 px-1.5 py-0.5 rounded bg-kb-bg border border-kb-accent text-sm text-kb-text-primary focus:outline-none"
                        />
                        <button
                          onClick={() => commitRename(c.id)}
                          className="p-1 rounded hover:bg-kb-bg text-kb-accent"
                          title="Save"
                        >
                          <Check className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    ) : (
                      <div className="flex items-center gap-1.5 min-w-0">
                        {c.originatingInsightId && (
                          <Lightbulb
                            className="w-3 h-3 text-amber-500 shrink-0"
                            aria-label="Started from an insight"
                          />
                        )}
                        <span className="text-sm text-kb-text-primary truncate">{c.title || 'Untitled'}</span>
                      </div>
                    )}
                    {c.preview && editingId !== c.id && (
                      <p className="text-xs text-kb-text-tertiary truncate mt-0.5">{c.preview}</p>
                    )}
                    <div className="flex items-center gap-2 mt-1 text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wide">
                      <span>{timeAgo(c.updatedAt)}</span>
                      {c.clusterId && (
                        <>
                          <span>·</span>
                          <span className="truncate max-w-[120px]">{c.clusterId}</span>
                        </>
                      )}
                      {c.messageCount > 0 && (
                        <>
                          <span>·</span>
                          <span>{c.messageCount} msg</span>
                        </>
                      )}
                    </div>
                  </div>
                  {/* Row actions */}
                  {editingId !== c.id && (
                    <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          startRename(c)
                        }}
                        title="Rename"
                        className="p-1 rounded hover:bg-kb-bg text-kb-text-tertiary hover:text-kb-text-primary"
                      >
                        <Pencil className="w-3.5 h-3.5" />
                      </button>
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          deleteMut.mutate(c.id)
                        }}
                        title="Delete"
                        className="p-1 rounded hover:bg-kb-bg text-kb-text-tertiary hover:text-red-500"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
