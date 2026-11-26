import { MessageSquarePlus } from 'lucide-react'
import { useAuth } from '@/contexts/AuthContext'

// Kobi Plans is a Beta: the badge says so wherever a plan shows, with the
// app's badge grammar (pill, accent-light fill, accent text, no border), and
// the feedback link goes to the same public form as the sidebar's.

export function BetaPill() {
  return (
    <span
      title="Kobi Plans is in beta: it works, and we want your feedback to shape it."
      className="inline-flex items-center px-2 py-0.5 rounded-full bg-kb-accent-light text-kb-accent text-[9px] font-mono font-semibold uppercase tracking-[0.08em] shrink-0"
    >
      Beta
    </span>
  )
}

export function PlansFeedbackLink() {
  const { user } = useAuth()
  return (
    <a
      href={`https://kubebolt.io/feedback${user?.email ? `?email=${encodeURIComponent(user.email)}` : ''}`}
      target="_blank"
      rel="noopener noreferrer"
      className="inline-flex items-center gap-1.5 text-[12px] text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
    >
      <MessageSquarePlus className="w-3.5 h-3.5" />
      Tell us how plans work for you
    </a>
  )
}
