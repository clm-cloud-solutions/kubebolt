import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ThumbsDown, ThumbsUp } from 'lucide-react'
import { api } from '@/services/api'
import type {
  CopilotFeedback,
  CopilotFeedbackRating,
  CopilotFeedbackReason,
} from '@/services/copilot/types'

// 👍/👎 under a finished Kobi answer (doc #67, phase 1c). The last rating given
// is the one kept: the same icon again withdraws it, the other one changes it.
// A 👎 asks why — an optional reason and comment — before it is sent, so it is
// counted once, with its reason.

export const FEEDBACK_REASONS: { value: CopilotFeedbackReason; label: string }[] = [
  { value: 'incorrect', label: 'Incorrect' },
  { value: 'incomplete', label: 'Incomplete' },
  { value: 'off_topic', label: "Didn't answer my question" },
  { value: 'slow', label: 'Too slow' },
]

const COMMENT_MAX = 2000
const ERROR_VISIBLE_MS = 6000

export const feedbackQueryKey = (conversationId: string) => ['copilot-feedback', conversationId] as const

// One request per conversation: every answer's bar reads the same cache entry,
// and a rating updates it in place.
function useConversationFeedback(conversationId: string) {
  return useQuery({
    queryKey: feedbackQueryKey(conversationId),
    queryFn: () => api.listConversationFeedback(conversationId),
    staleTime: Infinity,
    retry: false,
  })
}

interface RatingBody {
  rating: CopilotFeedbackRating | ''
  reason?: CopilotFeedbackReason
  comment?: string
}

export function AnswerFeedback({ conversationId, turnId }: { conversationId: string; turnId: string }) {
  const qc = useQueryClient()
  const { data } = useConversationFeedback(conversationId)
  const current = data?.find((f) => f.turnId === turnId)
  const [asking, setAsking] = useState(false)
  const [reason, setReason] = useState<CopilotFeedbackReason | undefined>()
  const [comment, setComment] = useState('')
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!failed) return
    const id = window.setTimeout(() => setFailed(false), ERROR_VISIBLE_MS)
    return () => window.clearTimeout(id)
  }, [failed])

  const rate = useMutation({
    mutationFn: (body: RatingBody) => api.rateCopilotAnswer({ conversationId, turnId, ...body }),
    onMutate: async (body) => {
      const key = feedbackQueryKey(conversationId)
      await qc.cancelQueries({ queryKey: key })
      const prev = qc.getQueryData<CopilotFeedback[]>(key)
      const others = (prev ?? []).filter((f) => f.turnId !== turnId)
      qc.setQueryData<CopilotFeedback[]>(
        key,
        body.rating
          ? [...others, { conversationId, turnId, rating: body.rating, reason: body.reason, comment: body.comment }]
          : others,
      )
      setFailed(false)
      return { prev }
    },
    onError: (_err, _body, ctx) => {
      qc.setQueryData(feedbackQueryKey(conversationId), ctx?.prev)
      setFailed(true)
    },
  })

  function clickUp() {
    setAsking(false)
    rate.mutate({ rating: current?.rating === 'up' ? '' : 'up' })
  }

  function clickDown() {
    if (current?.rating === 'down') {
      setAsking(false)
      rate.mutate({ rating: '' })
      return
    }
    setReason(undefined)
    setComment('')
    setAsking((open) => !open)
  }

  function sendDown() {
    setAsking(false)
    rate.mutate({ rating: 'down', reason, comment: comment.trim() || undefined })
  }

  const up = current?.rating === 'up'
  const down = current?.rating === 'down'
  const iconBtn =
    'p-1 rounded transition-colors hover:bg-kobi-elevated disabled:opacity-50 disabled:cursor-default'

  return (
    <div className="flex flex-col gap-1.5 min-w-0 flex-1">
      <div className="flex items-center gap-0.5">
        <button
          type="button"
          onClick={clickUp}
          aria-pressed={up}
          title={up ? 'Remove rating' : 'Good answer'}
          className={`${iconBtn} ${up ? 'text-kobi-accent' : 'text-kobi-text-tertiary hover:text-kobi-text-secondary'}`}
        >
          <ThumbsUp className={`w-3.5 h-3.5 ${up ? 'fill-current' : ''}`} />
        </button>
        <button
          type="button"
          onClick={clickDown}
          aria-pressed={down}
          aria-expanded={asking}
          title={down ? 'Remove rating' : 'Bad answer'}
          className={`${iconBtn} ${
            down || asking ? 'text-kobi-st-error' : 'text-kobi-text-tertiary hover:text-kobi-text-secondary'
          }`}
        >
          <ThumbsDown className={`w-3.5 h-3.5 ${down ? 'fill-current' : ''}`} />
        </button>
        {down && current?.reason && (
          <span className="ml-1 text-[11px] text-kobi-text-tertiary">
            {FEEDBACK_REASONS.find((r) => r.value === current.reason)?.label}
          </span>
        )}
        {failed && <span className="ml-1 text-[11px] text-kobi-st-error">Couldn't save your rating</span>}
      </div>

      {asking && (
        <div className="flex flex-col gap-2 p-2.5 rounded-lg bg-kobi-card border border-kobi-border max-w-[min(65ch,100%)]">
          <span className="text-xs text-kobi-text-secondary">What went wrong?</span>
          <div className="flex flex-wrap gap-1.5" role="radiogroup" aria-label="Reason">
            {FEEDBACK_REASONS.map((r) => {
              const on = reason === r.value
              return (
                <button
                  key={r.value}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  onClick={() => setReason(on ? undefined : r.value)}
                  className={`px-2 py-0.5 rounded-full border text-[11px] transition-colors ${
                    on
                      ? 'border-kobi-st-error text-kobi-st-error bg-kobi-st-error-dim'
                      : 'border-kobi-border text-kobi-text-secondary hover:text-kobi-text'
                  }`}
                >
                  {r.label}
                </button>
              )
            })}
          </div>
          <textarea
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            maxLength={COMMENT_MAX}
            rows={2}
            placeholder="Tell us more (optional)"
            className="w-full resize-y rounded-md bg-kobi-bg border border-kobi-border px-2 py-1.5 text-xs text-kobi-text placeholder:text-kobi-text-tertiary focus:outline-none focus:border-kobi-accent"
          />
          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={() => setAsking(false)}
              className="px-3 py-1.5 rounded-lg text-[11px] text-kobi-text-secondary hover:text-kobi-text transition-colors"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={sendDown}
              className="px-3 py-1.5 rounded-lg text-white text-[11px] font-medium bg-kobi-accent hover:bg-kobi-accent/90 transition-colors"
            >
              Send
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
