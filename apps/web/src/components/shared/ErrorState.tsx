import { AlertTriangle } from 'lucide-react'

interface ErrorStateProps {
  title?: string
  message?: string
  onRetry?: () => void
}

export function ErrorState({ title = 'Something went wrong', message, onRetry }: ErrorStateProps) {
  return (
    <div className="flex flex-col items-center justify-center p-12 text-center">
      <AlertTriangle className="w-10 h-10 text-status-error mb-4" />
      <h3 className="text-sm font-medium text-kb-text-primary mb-1">{title}</h3>
      {message && <p className="text-xs text-kb-text-secondary mb-4 max-w-md">{message}</p>}
      {onRetry && (
        <button
          onClick={onRetry}
          className="px-3 py-1.5 text-xs font-mono uppercase tracking-wider bg-kb-elevated text-kb-text-primary rounded-md border border-kb-border hover:border-kb-border-active transition-colors"
        >
          Retry
        </button>
      )}
    </div>
  )
}
