import { Inbox } from 'lucide-react'
import type { ReactNode } from 'react'

interface EmptyStateProps {
  icon?: ReactNode
  title: string
  message?: string
  // Optional CTA rendered below the message. Used by filtered-list empty
  // states for a "Clear filters" recovery action so the operator can get
  // back to a populated list in one click instead of resetting each
  // control by hand.
  action?: ReactNode
}

export function EmptyState({ icon, title, message, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center p-12 text-center">
      <div className="mb-4 text-kb-text-tertiary">
        {icon || <Inbox className="w-10 h-10" />}
      </div>
      <h3 className="text-sm font-medium text-kb-text-secondary mb-1">{title}</h3>
      {message && <p className="text-xs text-kb-text-tertiary">{message}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}
