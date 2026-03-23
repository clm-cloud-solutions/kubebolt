import { Inbox } from 'lucide-react'
import type { ReactNode } from 'react'

interface EmptyStateProps {
  icon?: ReactNode
  title: string
  message?: string
}

export function EmptyState({ icon, title, message }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center p-12 text-center">
      <div className="mb-4 text-kb-text-tertiary">
        {icon || <Inbox className="w-10 h-10" />}
      </div>
      <h3 className="text-sm font-medium text-kb-text-secondary mb-1">{title}</h3>
      {message && <p className="text-xs text-kb-text-tertiary">{message}</p>}
    </div>
  )
}
